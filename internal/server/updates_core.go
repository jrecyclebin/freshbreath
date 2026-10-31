package server

// Remote update feeds — see design/remote-updates.md. Two modes share one row
// shape and one per-feed AES key: "receive" pulls a key-authenticated archive
// from a remote URL and lands its manifest ops in staging; "publish" builds
// the same archive from selected apps/services and hands it back for the
// admin to self-host. The encryption is integrity, not confidentiality: the
// remote host never holds the key, so it can't forge a valid archive.

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"poggers.institute/freshbreath/internal/db"
	"poggers.institute/freshbreath/internal/utils"
)

// maxArchiveSize caps a fetched/built archive. Generous (apps can be big) but
// finite — an anonymous apply must not become an unbounded memory hose.
const maxArchiveSize = 256 << 20

// UpdateEvent is one progress event for the apply/build flows. It streams to
// the SSE handler through a channel, which keeps core testable without an
// HTTP response writer. ID namespaces every event to its feed.
type UpdateEvent struct {
	Event       string `json:"-"` // SSE event name; written by the handler
	ID          string `json:"id,omitempty"`
	Step        string `json:"step,omitempty"`
	Status      string `json:"status,omitempty"`
	Index       int    `json:"index"`
	Action      string `json:"action,omitempty"`
	Target      string `json:"target,omitempty"`
	Nonce       string `json:"nonce,omitempty"`
	Name        string `json:"name,omitempty"`
	SourceSlot  string `json:"source_slot,omitempty"`
	Version     string `json:"version,omitempty"`
	Apps        int    `json:"apps,omitempty"`
	Services    int    `json:"services,omitempty"`
	Ops         int    `json:"ops,omitempty"`
	Applied     int    `json:"applied"`
	Failed      int    `json:"failed"`
	Skipped     int    `json:"skipped"`
	Message     string `json:"message,omitempty"`
	DownloadURL string `json:"download_url,omitempty"`
}

// UpdateManifest is the descriptor at the heart of an update archive. Two op
// actions only (update_app_files, update_service_files) — the smallest
// vocabulary that's still useful, which is what makes the anonymous trigger
// surface tolerable: a feed's file ops can't change ownership or create
// anything. An import (a transfer from another server, applied by a signed-in
// Admin) matches targets the same way, and creates the ones it doesn't find.
type UpdateManifest struct {
	Version string     `json:"version"`
	Ops     []UpdateOp `json:"ops"`
}

type UpdateOp struct {
	Action string `json:"action"`
	// An app's nonce, or a service's URL. Archives built before services
	// matched by URL name the service instead; that still works.
	Target string `json:"target"`
	Source string `json:"source,omitempty"` // path inside the tarball; defaulted per action
	Slot   string `json:"slot,omitempty"`   // v1: staging only
	// What an import needs to create the target where it's missing. Feeds
	// never create, so their archives leave it out.
	Create *CreateSpec `json:"create,omitempty"`
}

// CreateSpec describes an app or service as it is on the server it came
// from. Auth records go by name and users by email, since ids mean nothing
// on another server; whatever has no match there is left empty — a gate
// then inherits the admin gate, the safe default.
type CreateSpec struct {
	Name        string                `json:"name"`
	URL         string                `json:"url,omitempty"`         // an app's own URL (a service's is its Target)
	Environment string                `json:"environment,omitempty"` // apps
	Descriptor  *db.ServiceDescriptor `json:"descriptor,omitempty"`  // services
	Gate        string                `json:"gate,omitempty"`        // protected_by, by auth record name
	ActsAs      string                `json:"acts_as,omitempty"`     // services: acts_as, by auth record name
	Owner       string                `json:"owner,omitempty"`       // apps: by email (kept only if an Admin+ there)
	Members     []string              `json:"members,omitempty"`     // by email
}

// applyPolicy is who applies an archive and how far it may go. A feed
// applies on its owner's authority, each target gated by the caller's
// token (claims, nil for anonymous), and only updates. An import applies
// as the signed-in caller, whose role (Admin+, checked at the door) stands
// in for the per-target gates, and creates what's missing.
type applyPolicy struct {
	owner     *db.User
	claims    *freshbreathClaims
	importing bool
}

// ── Archive crypto ──────────────────────────────────────────────────

// The wire format is raw bytes: nonce || ciphertext+tag, AES-256-GCM — the
// same construction as seal()/open() in services.go minus the base64, since
// the archive is binary end to end.

func encryptArchive(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("nonce: %w", err)
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func decryptArchive(key, data []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}
	ns := gcm.NonceSize()
	if len(data) < ns+gcm.Overhead() {
		return nil, fmt.Errorf("archive too short")
	}
	return gcm.Open(nil, data[:ns], data[ns:], nil)
}

// ── Manifest defaults & validation ──────────────────────────────────

// opSource returns the op's in-archive source path with its per-action
// default filled in: apps/<target> for app ops, services/<target>.txt for
// service ops.
func (op UpdateOp) opSource() string {
	if op.Source != "" {
		return op.Source
	}
	switch op.Action {
	case "update_app_files":
		return "apps/" + op.Target
	case "update_service_files":
		return "services/" + op.Target + ".txt"
	}
	return ""
}

// opService finds a service op's target: by URL, or by name for archives
// built before services matched by URL.
func (s *Server) opService(target string) (*db.Service, error) {
	if svc, err := s.store.GetServiceByURL(target); err == nil {
		return svc, nil
	}
	return s.store.GetServiceByName(target)
}

// archiveSourcePath resolves an op's in-archive source path against the
// extracted archive, refusing any path that leaves it — a manifest is
// remote input (it arrives encrypted, but encrypted by whoever holds the
// feed's key), so its paths get the same rule the tar entries get:
// cleaned, relative, inside archiveDir.
func archiveSourcePath(archiveDir string, op UpdateOp) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(op.opSource()))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("source %q escapes the archive", op.opSource())
	}
	return filepath.Join(archiveDir, clean), nil
}

// versionGreaterThan orders two manifest versions with "only move
// forward": runs of digits compare numerically, everything else byte by
// byte ("1.10" > "1.9", "2026.08.24-10" > "2026.08.24-2", "b" > "1.2").
// Equal versions — or a prior of "" against anything — never count as
// greater. The stamp is what a downgrade has to beat; this is the referee.
func versionGreaterThan(a, b string) bool {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		ca, cb := a[i], b[j]
		if isDigitByte(ca) && isDigitByte(cb) {
			si, sj := i, j
			for i < len(a) && isDigitByte(a[i]) {
				i++
			}
			for j < len(b) && isDigitByte(b[j]) {
				j++
			}
			na, nb := strings.TrimLeft(a[si:i], "0"), strings.TrimLeft(b[sj:j], "0")
			if len(na) != len(nb) {
				return len(na) > len(nb)
			}
			if na != nb {
				return na > nb
			}
			continue
		}
		if ca != cb {
			return ca > cb
		}
		i++
		j++
	}
	// A prefix loses to its extension ("1.2" < "1.2.1"); equal is equal.
	return len(a) > len(b)
}

func isDigitByte(c byte) bool { return c >= '0' && c <= '9' }

// gateAllowsApply reports whether this caller may apply an op that targets
// this gate: anyone who can access the target can update it, so an open
// gate needs no proof and a protected one wants the caller's token bound
// to it — minted under the record, or carrying it as a leg, the same
// boundTo the doors use. claims nil is the anonymous caller.
func gateAllowsApply(gate *db.AuthRecord, claims *freshbreathClaims) bool {
	if gateIsOpen(gate) {
		return true
	}
	return claims != nil && claims.boundTo(gate.ID)
}

// validateUpdateOps pre-flights every op against the store, the extracted
// archive and the caller's reach before the first one executes: targets
// must exist (or, importing, be creatable), sources must be in the
// tarball, slots default to staging, and a feed's caller must clear each
// target's gate (an open one needs no proof). A validation failure touches
// nothing — "feed refused before it started" beats "half-applied feed."
func (s *Server) validateUpdateOps(ops []UpdateOp, archiveDir string, p applyPolicy) error {
	for i, op := range ops {
		switch op.Action {
		case "update_app_files":
			app, err := s.store.GetApp(op.Target)
			if err != nil {
				if !p.importing || op.Create == nil {
					return fmt.Errorf("op %d: app not found: %s", i, op.Target)
				}
				if strings.TrimSpace(op.Create.Name) == "" {
					return fmt.Errorf("op %d: app %s has no name to create it with", i, op.Target)
				}
				if _, err := normalizeAppURL(op.Create.URL); err != nil {
					return fmt.Errorf("op %d: %v", i, err)
				}
			} else if gate, err := s.resolveAppGate(app); !p.importing && err == nil && !gateAllowsApply(gate, p.claims) {
				return fmt.Errorf("op %d: app %q sits behind gate %q — log in through it to apply its updates", i, op.Target, gate.Name)
			}
			slot := op.Slot
			if slot == "" {
				slot = "staging"
			}
			if slot != "staging" {
				return fmt.Errorf("op %d: slot %q not supported (v1: staging only)", i, slot)
			}
		case "update_service_files":
			svc, err := s.opService(op.Target)
			if err != nil {
				if !p.importing || op.Create == nil || op.Create.Descriptor == nil {
					return fmt.Errorf("op %d: service not found: %s", i, op.Target)
				}
				// Not found by URL, so the same name elsewhere is a different
				// service: refuse rather than overwrite it.
				if err := checkServiceName(op.Create.Name); err != nil {
					return fmt.Errorf("op %d: %v", i, err)
				}
				if _, err := s.store.GetServiceByName(op.Create.Name); err == nil {
					return fmt.Errorf("op %d: a service named %q already exists here at another URL", i, op.Create.Name)
				}
				svc = &db.Service{Name: op.Create.Name, URL: op.Target, Descriptor: *op.Create.Descriptor}
			}
			if svc.Descriptor.Type != "tasks" && svc.Descriptor.Type != "virtual" {
				return fmt.Errorf("op %d: service type %q does not support file publishing", i, svc.Descriptor.Type)
			}
			// Task scripts are written on the owner's authority (a feed's
			// owner, or whoever imports), so it must be someone allowed to.
			if err := s.gateTaskService(p.owner, svc.Descriptor.Type); err != nil {
				if p.importing {
					return fmt.Errorf("op %d: task service %s can only be imported by a Superuser", i, op.Target)
				}
				return fmt.Errorf("op %d: task service %s can only be updated by a feed a Superuser owns", i, op.Target)
			}
			if svc.ID != 0 && !p.importing {
				if gate, gerr := s.resolveServiceGate(svc); gerr == nil && !gateAllowsApply(gate, p.claims) {
					return fmt.Errorf("op %d: service %q sits behind gate %q — log in through it to apply its updates", i, op.Target, gate.Name)
				}
			}
		default:
			return fmt.Errorf("op %d: unknown action %q", i, op.Action)
		}
		src, err := archiveSourcePath(archiveDir, op)
		if err != nil {
			return fmt.Errorf("op %d: %w", i, err)
		}
		if _, err := os.Stat(src); err != nil {
			return fmt.Errorf("op %d: source %q not found in archive", i, op.opSource())
		}
	}
	return nil
}

// runUpdateOps validates every op, then runs them in manifest order,
// creating a missing target first when importing. Each op overwrites, so a
// retry from op 0 is safe. It reports the step that failed (validate or
// op) with the error. Feeds and imports both go through here, which is
// what keeps their matching the same.
func (s *Server) runUpdateOps(ops []UpdateOp, archiveDir string, p applyPolicy, emit func(UpdateEvent) error) (string, error) {
	if err := s.validateUpdateOps(ops, archiveDir, p); err != nil {
		return "validate", err
	}
	if err := emit(UpdateEvent{Event: "validate", Step: "validate", Ops: len(ops), Status: "ok"}); err != nil {
		return "validate", err
	}
	for i, op := range ops {
		if err := emit(UpdateEvent{Event: "op", Index: i, Action: op.Action, Target: op.Target, Status: "start"}); err != nil {
			return "op", err
		}
		if p.importing {
			created, err := s.createOpTarget(op, p.owner)
			if err != nil {
				return "op", fmt.Errorf("op %d (create %s): %w", i, op.Target, err)
			}
			if created {
				if err := emit(UpdateEvent{Event: "op", Index: i, Action: op.Action, Target: op.Target, Name: op.Create.Name, Status: "created"}); err != nil {
					return "op", err
				}
			}
		}
		if err := s.executeUpdateOp(op, archiveDir); err != nil {
			return "op", fmt.Errorf("op %d (%s %s): %w", i, op.Action, op.Target, err)
		}
		if err := emit(UpdateEvent{Event: "op", Index: i, Action: op.Action, Target: op.Target, Status: "done"}); err != nil {
			return "op", err
		}
	}
	return "", nil
}

// createOpTarget creates an import's target if it isn't here yet, as actor,
// from the op's create spec. Auth records are matched by name and users by
// email; no match leaves the slot empty. Reports whether it created one.
func (s *Server) createOpTarget(op UpdateOp, actor *db.User) (bool, error) {
	c := op.Create
	switch op.Action {
	case "update_app_files":
		if _, err := s.store.GetApp(op.Target); err == nil || c == nil {
			return false, nil
		}
		var owner *int64
		if u, err := s.store.GetUserByEmail(c.Owner); c.Owner != "" && err == nil && roleIn(u.Role, rolesAdminPlus) {
			owner = &u.ID
		}
		if _, err := s.coreCreateAppWithNonce(actor, op.Target, c.Name, c.Environment, c.URL, owner, s.authIDByName(c.Gate)); err != nil {
			return false, err
		}
		if members := s.userIDsByEmail(c.Members); len(members) > 0 {
			if err := s.coreSetAppMembers(actor, op.Target, members); err != nil {
				return true, err
			}
		}
		return true, nil
	case "update_service_files":
		if _, err := s.opService(op.Target); err == nil || c == nil || c.Descriptor == nil {
			return false, nil
		}
		svc, err := s.coreCreateService(actor, c.Name, op.Target, *c.Descriptor, s.authIDByName(c.Gate), s.authIDByName(c.ActsAs))
		if err != nil {
			return false, err
		}
		if members := s.userIDsByEmail(c.Members); len(members) > 0 {
			if err := s.coreSetServiceMembers(actor, svc.ID, members); err != nil {
				return true, err
			}
		}
		return true, nil
	}
	return false, nil
}

// authIDByName is the id of the auth record with this name, or nil.
func (s *Server) authIDByName(name string) *int64 {
	if name == "" {
		return nil
	}
	rec, err := s.store.GetAuthRecordByName(name)
	if err != nil {
		return nil
	}
	return &rec.ID
}

// userIDsByEmail is the ids of the users with these emails, skipping any
// that have no account here.
func (s *Server) userIDsByEmail(emails []string) []int64 {
	var ids []int64
	for _, e := range emails {
		if u, err := s.store.GetUserByEmail(e); err == nil {
			ids = append(ids, u.ID)
		}
	}
	return ids
}

// executeUpdateOp runs one op against the extracted archive. Both actions
// overwrite, never delete — updates don't tear down.
func (s *Server) executeUpdateOp(op UpdateOp, archiveDir string) error {
	src, err := archiveSourcePath(archiveDir, op)
	if err != nil {
		return err
	}
	switch op.Action {
	case "update_app_files":
		dst := filepath.Join(s.config.DataDir, "apps", op.Target, "staging")
		// Rollback protection: the replaced slot stays one rename away at
		// staging.bak — recovery is a manual copy back, nothing fancier. It
		// fails the op rather than losing the only copy of the old files.
		if _, err := os.Stat(dst); err == nil {
			if err := os.Rename(dst, dst+".bak"); err != nil {
				return fmt.Errorf("failed to back up staging slot: %w", err)
			}
		}
		if err := copyDir(src, dst); err != nil {
			return fmt.Errorf("copy failed: %w", err)
		}
		s.rebuildHostedRoutes()
		return nil
	case "update_service_files":
		svc, err := s.opService(op.Target)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(src)
		if err != nil {
			return fmt.Errorf("read source: %w", err)
		}
		path := serviceDefinitionPath(s.config.DataDir, svc)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return fmt.Errorf("failed to create service dir: %w", err)
		}
		// Same rollback story as app slots: the old definition waits at
		// service.txt.bak.
		if _, err := os.Stat(path); err == nil {
			if err := os.Rename(path, path+".bak"); err != nil {
				return fmt.Errorf("failed to back up service file: %w", err)
			}
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			return fmt.Errorf("write failed: %w", err)
		}
		s.mcpMounts.add(s, svc)
		return nil
	}
	return fmt.Errorf("unknown action %q", op.Action)
}

// ── Tar helpers ─────────────────────────────────────────────────────

// tarEntry writes one file (or dir) entry from disk into the tarball.
func tarEntry(tw *tar.Writer, fullPath, arcName string) error {
	fi, err := os.Stat(fullPath)
	if err != nil {
		return err
	}
	hdr, err := tar.FileInfoHeader(fi, "")
	if err != nil {
		return err
	}
	hdr.Name = filepath.ToSlash(arcName)
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if fi.IsDir() {
		return nil
	}
	f, err := os.Open(fullPath)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(tw, f)
	return err
}

// tarWriteFile writes an in-memory file into the tarball.
func tarWriteFile(tw *tar.Writer, arcName string, data []byte) error {
	hdr := &tar.Header{Name: filepath.ToSlash(arcName), Mode: 0644, Size: int64(len(data))}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

// extractArchive unpacks an in-memory tar.gz into destDir. Entry names are
// cleaned and traversal-checked, and the total bytes written are capped:
// gzip can turn a 256MB archive into far more than that on disk, so the
// running total is checked from the headers before each file is written
// and anything past the cap is refused, never silently truncated.
func extractArchive(data []byte, destDir string) error {
	return extractArchiveBounded(data, destDir, uploadExtractMaxBytes)
}

func extractArchiveBounded(data []byte, destDir string, cap int64) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var total int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeDir {
			continue
		}
		clean := filepath.Clean(filepath.FromSlash(hdr.Name))
		if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
			return fmt.Errorf("archive entry escapes destination: %s", hdr.Name)
		}
		out := filepath.Join(destDir, clean)
		if hdr.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(out, 0755); err != nil {
				return err
			}
			continue
		}
		if total+hdr.Size > cap {
			return fmt.Errorf("archive extracts past the %dMB cap", cap>>20)
		}
		total += hdr.Size
		if err := os.MkdirAll(filepath.Dir(out), 0755); err != nil {
			return err
		}
		f, err := os.OpenFile(out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(f, tr)
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
}

// readArchiveManifest decrypts an archive and returns its manifest, without
// extracting to disk. This is the check path: read version, nothing else.
func readArchiveManifest(key, archive []byte) (*UpdateManifest, error) {
	plain, err := decryptArchive(key, archive)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(plain))
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("manifest.json not found in archive")
		}
		if err != nil {
			return nil, fmt.Errorf("tar: %w", err)
		}
		if hdr.Typeflag == tar.TypeReg && filepath.ToSlash(filepath.Clean(hdr.Name)) == "manifest.json" {
			data, err := io.ReadAll(io.LimitReader(tr, 1<<20))
			if err != nil {
				return nil, fmt.Errorf("read manifest: %w", err)
			}
			var m UpdateManifest
			if err := json.Unmarshal(data, &m); err != nil {
				return nil, fmt.Errorf("parse manifest: %w", err)
			}
			if m.Version == "" {
				return nil, fmt.Errorf("manifest has no version")
			}
			return &m, nil
		}
	}
}

// ── Feed CRUD ───────────────────────────────────────────────────────

func (s *Server) coreCreateUpdateFeed(actor *db.User, url, mode, name, keyHex string) (string, string, error) {
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return "", "", err
	}
	switch mode {
	case "receive":
		if url == "" {
			return "", "", cerr(http.StatusBadRequest, "url required for receive mode")
		}
	case "publish":
		// url is an optional label here
	default:
		return "", "", cerr(http.StatusBadRequest, "mode must be \"receive\" or \"publish\"")
	}
	// keyHex, when supplied, lets a receiver register a feed with the
	// publisher's known key — the cross-instance pairing path. Without it
	// every feed gets a fresh random key, shown once at creation.
	if keyHex == "" {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return "", "", cerr(http.StatusInternalServerError, "key generation failed")
		}
		keyHex = hex.EncodeToString(key)
	} else {
		if _, err := hex.DecodeString(keyHex); err != nil || len(keyHex) != 64 {
			return "", "", cerr(http.StatusBadRequest, "key_hex must be 64 hex characters (32 bytes)")
		}
	}
	id := utils.GenNonce()
	if err := s.store.CreateUpdateFeed(id, url, mode, name, keyHex, actor.ID); err != nil {
		return "", "", cerr(http.StatusInternalServerError, "%v", err)
	}
	s.audit(actor, "created update feed", mode+" "+nameOrURL(name, url))
	return id, keyHex, nil
}

func nameOrURL(name, url string) string {
	if name != "" {
		return name
	}
	return url
}

// coreListUpdateFeeds returns feed rows with the key already stripped —
// UpdateFeed's KeyHex carries json:"-", so the struct marshals safely.
func (s *Server) coreListUpdateFeeds(actor *db.User) ([]*db.UpdateFeed, error) {
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return nil, err
	}
	feeds, err := s.store.ListUpdateFeeds()
	if err != nil {
		return nil, cerr(http.StatusInternalServerError, "%v", err)
	}
	return feeds, nil
}

// coreUpdateUpdateFeed patches the mutable fields; the key is never editable.
func (s *Server) coreUpdateUpdateFeed(actor *db.User, id string, url, name *string) error {
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return err
	}
	feed, err := s.store.GetUpdateFeed(id)
	if err != nil {
		return cerr(http.StatusNotFound, "update feed not found")
	}
	if url != nil {
		if feed.Mode == "receive" && *url == "" {
			return cerr(http.StatusBadRequest, "url required for receive mode")
		}
	}
	if err := s.store.UpdateUpdateFeed(id, url, name); err != nil {
		return cerr(http.StatusInternalServerError, "%v", err)
	}
	s.audit(actor, "updated update feed", nameOrURL(feed.Name, feed.URL))
	return nil
}

func (s *Server) coreDeleteUpdateFeed(actor *db.User, id string) error {
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return err
	}
	feed, err := s.store.GetUpdateFeed(id)
	if err != nil {
		return cerr(http.StatusNotFound, "update feed not found")
	}
	if err := s.store.DeleteUpdateFeed(id); err != nil {
		return cerr(http.StatusInternalServerError, "%v", err)
	}
	s.dropPendingArchive(id)
	s.audit(actor, "deleted update feed", nameOrURL(feed.Name, feed.URL))
	return nil
}

// ── Check ───────────────────────────────────────────────────────────

// checkUpdateFeed runs the two-layer check for one receive feed: a cheap
// conditional GET (etag, then last-modified) that short-circuits a 304, then
// the semantic layer — decrypt and compare the manifest version against the
// last applied one. A successful 200 records the seen version together with
// the etag/last-modified cache, so a later 304 can decide pending-ness by
// seen-vs-applied. Errors are recorded on the row (last_error) — the caller
// sees only actionable results.
func (s *Server) checkUpdateFeed(f *db.UpdateFeed) (status, version string, err error) {
	fail := func(e error) (string, string, error) {
		_ = s.store.SetUpdateFeedError(f.ID, e.Error())
		return "", "", e
	}
	req, err := http.NewRequest(http.MethodGet, f.URL, nil)
	if err != nil {
		return fail(fmt.Errorf("bad url: %w", err))
	}
	// Conditional GET only when the cached validators are interpretable: a
	// 304 says "unchanged" about a response whose version we recorded in
	// last_seen_version. Rows migrated in before that column existed (and
	// 200s whose manifest failed to parse) have validators but no seen
	// version — for those, force a full fetch until seen is known. One
	// unconditional GET, then cheap 304s resume.
	if f.LastSeenVersion != "" {
		if f.LastETag != "" {
			req.Header.Set("If-None-Match", f.LastETag)
		} else if f.LastModified != "" {
			req.Header.Set("If-Modified-Since", f.LastModified)
		}
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fail(fmt.Errorf("fetch: %w", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		// A 304 means "unchanged since we last looked". Whether that's
		// pending or current is seen-vs-applied, not applied-vs-empty: a
		// version that was discovered but never applied must stay
		// available, or /check goes quiet about it until the publisher
		// ships again. Only a genuinely newer one does — a downgrade the
		// publisher shipped later reads as up-to-date, not as an update.
		if versionGreaterThan(f.LastSeenVersion, f.LastAppliedVersion) {
			return "update_available", f.LastSeenVersion, nil
		}
		return "up_to_date", f.LastAppliedVersion, nil
	}
	if resp.StatusCode != http.StatusOK {
		return fail(fmt.Errorf("fetch: status %d", resp.StatusCode))
	}
	if resp.ContentLength > maxArchiveSize {
		return fail(fmt.Errorf("archive exceeds size limit (%d bytes)", maxArchiveSize))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxArchiveSize+1))
	if err != nil {
		return fail(fmt.Errorf("download: %w", err))
	}
	if len(body) > maxArchiveSize {
		return fail(fmt.Errorf("archive exceeds size limit (%d bytes)", maxArchiveSize))
	}
	// Cache the validators on any 200 (even one that later fails to decrypt):
	// they describe this response, and the design promises the next check can
	// be a cheap 304.
	etag, lastMod := resp.Header.Get("ETag"), resp.Header.Get("Last-Modified")
	if err := s.store.UpdateFeedCache(f.ID, etag, lastMod); err != nil {
		return fail(err)
	}
	f.LastETag, f.LastModified = etag, lastMod
	key, err := hex.DecodeString(f.KeyHex)
	if err != nil {
		return fail(fmt.Errorf("stored key invalid: %w", err))
	}
	manifest, err := readArchiveManifest(key, body)
	if err != nil {
		return fail(err)
	}
	if err := s.store.UpdateFeedSeen(f.ID, manifest.Version); err != nil {
		return fail(err)
	}
	f.LastSeenVersion = manifest.Version
	// Only a newer version applies — equal reads up-to-date, and an older
	// one (a publisher rollback, or a replay of an old archive) reads
	// up-to-date too: the stamp is a floor, and apply can't go under it.
	if !versionGreaterThan(manifest.Version, f.LastAppliedVersion) {
		return "up_to_date", f.LastAppliedVersion, nil
	}
	return "update_available", manifest.Version, nil
}

// UpdateAvailable is one actionable row from the flat anonymous /check.
type UpdateAvailable struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Name    string `json:"name,omitempty"`
}

// coreCheckAllUpdateFeeds checks every receive feed, sequentially (v1; N is
// small), returning only feeds with an update available. Up-to-date and
// failed feeds are omitted — the caller sees actionable results only.
func (s *Server) coreCheckAllUpdateFeeds() []UpdateAvailable {
	feeds, err := s.store.ListUpdateFeedsByMode("receive")
	if err != nil {
		return nil
	}
	var out []UpdateAvailable
	for _, f := range feeds {
		status, version, err := s.checkUpdateFeed(f)
		if err != nil || status != "update_available" {
			continue
		}
		out = append(out, UpdateAvailable{ID: f.ID, Version: version, Name: f.Name})
	}
	return out
}

// ── Apply ───────────────────────────────────────────────────────────

// coreApplyUpdateFeeds applies the named feeds — or, when ids is empty, every
// receive feed — after re-checking each one. The re-check is not optional: it
// guards against forcing a version that was already applied between /check
// and this call. Events stream per feed; a feed_error on one feed never stops
// the others. The returned counts feed the summary event.
//
// claims is the caller's verified token (nil = anonymous), checked against
// each op's target gate in the validation: anyone who can access the
// target can apply its updates, and an open gate needs no proof — so an
// anonymous caller still gets feeds whose every target is open.
func (s *Server) coreApplyUpdateFeeds(ctx context.Context, claims *freshbreathClaims, ids []string, progress chan<- UpdateEvent) error {
	emit := func(e UpdateEvent) error {
		select {
		case progress <- e:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	var feeds []*db.UpdateFeed
	if len(ids) > 0 {
		for _, id := range ids {
			f, err := s.store.GetUpdateFeed(id)
			// Unknown or non-receive ids skip silently — the caller sees only
			// actionable results, matching /check's contract.
			if err != nil || f.Mode != "receive" {
				continue
			}
			feeds = append(feeds, f)
		}
	} else {
		feeds, _ = s.store.ListUpdateFeedsByMode("receive")
	}

	applied, failed, skipped := 0, 0, 0
	for _, f := range feeds {
		status, _, err := s.checkUpdateFeed(f)
		if err != nil {
			failed++
			if eerr := emit(UpdateEvent{Event: "feed_error", ID: f.ID, Step: "check", Message: err.Error()}); eerr != nil {
				return eerr
			}
			continue
		}
		if status != "update_available" {
			skipped++
			if eerr := emit(UpdateEvent{Event: "skip", ID: f.ID, Version: f.LastAppliedVersion}); eerr != nil {
				return eerr
			}
			continue
		}
		if err := s.applyUpdateFeed(ctx, f, claims, progress); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			failed++
			continue
		}
		applied++
	}
	return emit(UpdateEvent{Event: "summary", Applied: applied, Failed: failed, Skipped: skipped})
}

// applyUpdateFeed runs the full fetch→decrypt→validate→ops→stamp flow for one
// feed, streaming step events. The version stamps only on full success, and
// only moves forward: the stamp is a floor — re-runs and publisher
// downgrades read up-to-date — and stamping early would silently strand a
// half-applied feed.
func (s *Server) applyUpdateFeed(ctx context.Context, f *db.UpdateFeed, claims *freshbreathClaims, progress chan<- UpdateEvent) error {
	emit := func(e UpdateEvent) error {
		e.ID = f.ID
		select {
		case progress <- e:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	fail := func(step string, err error) error {
		_ = s.store.SetUpdateFeedError(f.ID, step+": "+err.Error())
		_ = emit(UpdateEvent{Event: "feed_error", ID: f.ID, Step: step, Message: err.Error()})
		return err
	}

	// fetch — non-conditional; we want the bytes.
	resp, err := s.httpClient.Get(f.URL)
	if err != nil {
		return fail("fetch", err)
	}
	if resp.ContentLength > maxArchiveSize {
		resp.Body.Close()
		return fail("fetch", fmt.Errorf("archive exceeds size limit (%d bytes)", maxArchiveSize))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxArchiveSize+1))
	resp.Body.Close()
	if err != nil {
		return fail("fetch", fmt.Errorf("download: %w", err))
	}
	if len(body) > maxArchiveSize {
		return fail("fetch", fmt.Errorf("archive exceeds size limit (%d bytes)", maxArchiveSize))
	}
	if resp.StatusCode != http.StatusOK {
		return fail("fetch", fmt.Errorf("status %d", resp.StatusCode))
	}
	// Cache etag/last-modified regardless of apply success so the next check
	// can be a conditional GET.
	etag, lastMod := resp.Header.Get("ETag"), resp.Header.Get("Last-Modified")
	if err := s.store.UpdateFeedCache(f.ID, etag, lastMod); err != nil {
		return fail("fetch", err)
	}
	if err := emit(UpdateEvent{Event: "fetch", Step: "fetch", Status: "ok"}); err != nil {
		return err
	}

	// decrypt + extract.
	key, err := hex.DecodeString(f.KeyHex)
	if err != nil {
		return fail("decrypt", err)
	}
	plain, err := decryptArchive(key, body)
	if err != nil {
		return fail("decrypt", err)
	}
	archiveDir, err := os.MkdirTemp("", "fb-update-*")
	if err != nil {
		return fail("decrypt", err)
	}
	defer os.RemoveAll(archiveDir)
	if err := extractArchive(plain, archiveDir); err != nil {
		return fail("decrypt", err)
	}
	manifest, err := readArchiveManifest(key, body)
	if err != nil {
		return fail("decrypt", err)
	}
	if err := emit(UpdateEvent{Event: "decrypt", Step: "decrypt", Status: "ok"}); err != nil {
		return err
	}

	// validate — every op, before the first one runs.
	// The feed acts on its owner's authority, re-read now: a deleted or
	// inactive owner has none. Setup mode has no users yet and is open to
	// a synthetic Superuser everywhere, so it is here too.
	var owner *db.User
	if rec, err := s.adminAuthRecord(); err == nil && rec == nil {
		owner = &db.User{ID: -1, Name: "Setup Account", Role: "Superuser", Status: "Active"}
	} else if u, err := s.userFromSubject(subjectForUser(&db.User{ID: f.CreatedBy})); err == nil {
		owner = u
	}
	if step, err := s.runUpdateOps(manifest.Ops, archiveDir, applyPolicy{owner: owner, claims: claims}, emit); err != nil {
		if ctx.Err() != nil {
			return err
		}
		return fail(step, err)
	}

	// stamp — full success only.
	if err := s.store.StampUpdateFeedApplied(f.ID, manifest.Version); err != nil {
		return fail("stamp", err)
	}
	// Audit attributes to the assigner admin — a label, not a live credential.
	actor, err := s.store.GetUser(f.CreatedBy)
	if err != nil {
		actor = nil
	}
	s.audit(actor, "applied update feed", fmt.Sprintf("%s → %s", nameOrURL(f.Name, f.URL), manifest.Version))
	return emit(UpdateEvent{Event: "done", ID: f.ID, Version: manifest.Version, Applied: len(manifest.Ops)})
}

// ── Build (publish mode) ────────────────────────────────────────────

// pendingArchive is a built-but-undownloaded archive. One-shot: it's dropped
// on first download or on expiry, whichever comes first.
type pendingArchive struct {
	data    []byte
	expires time.Time
}

// coreBuildArchive assembles an encrypted tar.gz from the selected apps
// (current staging slot, dev fallback) and services (definition files),
// mints a short-lived act-token download URL, and streams progress. The
// archive is never retained after download in v1.
func (s *Server) coreBuildArchive(actor *db.User, id string, apps, services []string, version string, ctx context.Context, progress chan<- UpdateEvent) (string, error) {
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return "", err
	}
	feed, err := s.store.GetUpdateFeed(id)
	if err != nil {
		return "", cerr(http.StatusNotFound, "update feed not found")
	}
	if feed.Mode != "publish" {
		return "", cerr(http.StatusBadRequest, "feed is not in publish mode")
	}
	key, err := hex.DecodeString(feed.KeyHex)
	if err != nil {
		return "", cerr(http.StatusInternalServerError, "stored key invalid: %v", err)
	}
	emit := func(e UpdateEvent) error {
		select {
		case progress <- e:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	plain, version, err := s.packArchive(actor, apps, services, version, false, emit)
	if err != nil {
		return "", err
	}
	archive, err := encryptArchive(key, plain)
	if err != nil {
		return "", cerr(http.StatusInternalServerError, "encrypt: %v", err)
	}
	_ = emit(UpdateEvent{Event: "encrypt", Step: "encrypt", Status: "ok"})
	ref := utils.GenNonce()
	s.putPendingArchive(feed.ID, ref, archive)
	// Plain admin-authenticated URL — no act-token. The consumer is the
	// control panel SPA, which already holds a bearer token; act-tokens are
	// for credential-less fetchers (see design/remote-updates.md Reuse Map).
	downloadURL := "/api/updates/" + feed.ID + "/archive?ref=" + ref
	_ = emit(UpdateEvent{Event: "done", Step: "done", Version: version, DownloadURL: downloadURL})
	return downloadURL, nil
}

// packArchive assembles a tar.gz (unencrypted) of the selected apps
// (current staging slot, dev fallback) and services (definition files),
// with its manifest, streaming progress. forImport adds what an import
// needs to create each target it doesn't have: names, gates by auth
// record name, owner and members by email. A feed's archive leaves that
// out — feeds never create, and it would put those emails in a file
// hosted out in the world.
func (s *Server) packArchive(actor *db.User, apps, services []string, version string, forImport bool, emit func(UpdateEvent) error) (data []byte, _ string, err error) {
	_ = emit(UpdateEvent{Event: "collect", Step: "collect", Apps: len(apps), Services: len(services), Status: "ok"})

	var manifest UpdateManifest
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)
	defer func() {
		if err != nil {
			tw.Close()
			gzw.Close()
		}
	}()

	for _, nonce := range apps {
		app, err := s.store.GetApp(nonce)
		if err != nil {
			return nil, "", cerr(http.StatusNotFound, "app not found: %s", nonce)
		}
		sourceSlot := "staging"
		srcDir := filepath.Join(s.config.DataDir, "apps", nonce, "staging")
		if fi, err := os.Stat(srcDir); err != nil || !fi.IsDir() {
			sourceSlot = "dev"
			srcDir = filepath.Join(s.config.DataDir, "apps", nonce, "web")
		}
		arcRoot := "apps/" + nonce
		added := false
		err = filepath.Walk(srcDir, func(path string, fi os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(srcDir, path)
			if rel == "." {
				return nil
			}
			if err := tarEntry(tw, path, filepath.Join(filepath.FromSlash(arcRoot), rel)); err != nil {
				return err
			}
			added = true
			return nil
		})
		if err != nil {
			return nil, "", cerr(http.StatusInternalServerError, "collect app %s: %v", nonce, err)
		}
		if !added {
			return nil, "", cerr(http.StatusBadRequest, "app %s has no files to package (deploy to staging or upload to dev first)", nonce)
		}
		if err := emit(UpdateEvent{Event: "app", Nonce: nonce, SourceSlot: sourceSlot, Status: "added"}); err != nil {
			return nil, "", err
		}
		op := UpdateOp{Action: "update_app_files", Target: nonce, Source: arcRoot, Slot: "staging"}
		if forImport {
			op.Create = &CreateSpec{
				Name:        app.Name,
				URL:         app.URL,
				Environment: app.Environment,
				Gate:        s.authNameByID(app.ProtectedBy),
				Owner:       s.userEmail(app.OwnerID),
			}
			if ids, err := s.store.ListAppMembers(nonce); err == nil {
				op.Create.Members = s.userEmails(ids)
			}
		}
		manifest.Ops = append(manifest.Ops, op)
	}

	for _, name := range services {
		svc, err := s.store.GetServiceByName(name)
		if err != nil {
			return nil, "", cerr(http.StatusNotFound, "service not found: %s", name)
		}
		if svc.Descriptor.Type != "tasks" && svc.Descriptor.Type != "virtual" {
			return nil, "", cerr(http.StatusBadRequest, "service type %q does not support file publishing", svc.Descriptor.Type)
		}
		if err := s.gateTaskService(actor, svc.Descriptor.Type); err != nil {
			return nil, "", err
		}
		data, err := os.ReadFile(serviceDefinitionPath(s.config.DataDir, svc))
		if err != nil {
			return nil, "", cerr(http.StatusBadRequest, "service %s has no definition file to package", name)
		}
		arcName := "services/" + name + ".txt"
		if err := tarWriteFile(tw, arcName, data); err != nil {
			return nil, "", cerr(http.StatusInternalServerError, "package service %s: %v", name, err)
		}
		if err := emit(UpdateEvent{Event: "service", Name: name, Status: "added"}); err != nil {
			return nil, "", err
		}
		op := UpdateOp{Action: "update_service_files", Target: svc.URL, Source: arcName}
		if forImport {
			d := svc.Descriptor
			op.Create = &CreateSpec{
				Name:       svc.Name,
				Descriptor: &d,
				Gate:       s.authNameByID(svc.ProtectedBy),
				ActsAs:     s.authNameByID(svc.ActsAs),
			}
			if ids, err := s.store.ListServiceMembers(svc.ID); err == nil {
				op.Create.Members = s.userEmails(ids)
			}
		}
		manifest.Ops = append(manifest.Ops, op)
	}

	if len(manifest.Ops) == 0 {
		return nil, "", cerr(http.StatusBadRequest, "nothing selected: pass apps and/or services")
	}
	if version == "" {
		version = time.Now().UTC().Format("2006.01.02T15:04Z")
	}
	manifest.Version = version
	mb, _ := json.MarshalIndent(manifest, "", "  ")
	if err := tarWriteFile(tw, "manifest.json", mb); err != nil {
		return nil, "", cerr(http.StatusInternalServerError, "manifest: %v", err)
	}
	if err := tw.Close(); err != nil {
		return nil, "", cerr(http.StatusInternalServerError, "tar close: %v", err)
	}
	if err := gzw.Close(); err != nil {
		return nil, "", cerr(http.StatusInternalServerError, "gzip close: %v", err)
	}
	_ = emit(UpdateEvent{Event: "manifest", Step: "manifest", Version: version, Ops: len(manifest.Ops), Status: "ok"})
	return buf.Bytes(), version, nil
}

// authNameByID is the name of the auth record with this id, or "".
func (s *Server) authNameByID(id *int64) string {
	if id == nil {
		return ""
	}
	if rec, err := s.store.GetAuthRecord(*id); err == nil {
		return rec.Name
	}
	return ""
}

// userEmail is the email of the user with this id, or "".
func (s *Server) userEmail(id *int64) string {
	if id == nil {
		return ""
	}
	if u, err := s.store.GetUser(*id); err == nil {
		return u.Email
	}
	return ""
}

func (s *Server) userEmails(ids []int64) []string {
	var out []string
	for _, id := range ids {
		if e := s.userEmail(&id); e != "" {
			out = append(out, e)
		}
	}
	return out
}

// ── Import & transfer ───────────────────────────────────────────────

// coreImportArchive applies a transfer from another server: a plain tar.gz
// (it came through the tunnel, sent by a signed-in Admin, so there's no
// feed key to check) run through the same validation and ops as a feed.
// It acts as actor, and creates the apps and services it doesn't find.
func (s *Server) coreImportArchive(ctx context.Context, actor *db.User, data []byte, progress chan<- UpdateEvent) error {
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return err
	}
	emit := func(e UpdateEvent) error {
		select {
		case progress <- e:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	fail := func(step string, err error) error {
		_ = emit(UpdateEvent{Event: "feed_error", Step: step, Message: err.Error()})
		return err
	}

	dir, err := os.MkdirTemp("", "fb-import-*")
	if err != nil {
		return fail("extract", err)
	}
	defer os.RemoveAll(dir)
	if err := extractArchive(data, dir); err != nil {
		return fail("extract", err)
	}
	mb, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return fail("extract", fmt.Errorf("manifest.json not found in archive"))
	}
	var manifest UpdateManifest
	if err := json.Unmarshal(mb, &manifest); err != nil {
		return fail("extract", fmt.Errorf("parse manifest: %w", err))
	}
	if err := emit(UpdateEvent{Event: "extract", Step: "extract", Status: "ok"}); err != nil {
		return err
	}

	if step, err := s.runUpdateOps(manifest.Ops, dir, applyPolicy{owner: actor, importing: true}, emit); err != nil {
		if ctx.Err() != nil {
			return err
		}
		return fail(step, err)
	}
	s.audit(actor, "imported a transfer", fmt.Sprintf("%d app(s)/service(s)", len(manifest.Ops)))
	return emit(UpdateEvent{Event: "done", Version: manifest.Version, Applied: len(manifest.Ops)})
}

// coreTransfer packs the selected apps and services and sends them to a
// remote's import, through its tunnel, as the remote user whose token the
// browser holds (this server never keeps it). The remote's progress events
// come back into this stream after this server's own.
func (s *Server) coreTransfer(ctx context.Context, actor *db.User, remoteID, token string, apps, services []string, progress chan<- UpdateEvent) error {
	emit := func(e UpdateEvent) error {
		select {
		case progress <- e:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	fail := func(step string, err error) error {
		_ = emit(UpdateEvent{Event: "feed_error", Step: step, Message: err.Error()})
		return err
	}
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return fail("collect", err)
	}
	remote, err := s.store.GetRemote(remoteID)
	if err != nil {
		return fail("collect", cerr(http.StatusNotFound, "remote not found"))
	}
	plain, _, err := s.packArchive(actor, apps, services, "", true, emit)
	if err != nil {
		return fail("collect", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost/api/updates/import", bytes.NewReader(plain))
	if err != nil {
		return fail("send", err)
	}
	req.Header.Set("Content-Type", "application/gzip")
	req.Header.Set("Authorization", "Bearer "+strings.TrimPrefix(token, "Bearer "))
	if err := emit(UpdateEvent{Event: "send", Step: "send", Name: remote.Name, Status: "ok"}); err != nil {
		return err
	}
	resp, err := s.remoteConns.get(remote).client.Do(req)
	if err != nil {
		return fail("send", fmt.Errorf("could not reach %q: %w", remote.Name, err))
	}
	defer resp.Body.Close()
	s.audit(actor, "transferred to remote", fmt.Sprintf("%s: %d app(s), %d service(s) (%d)", remote.Name, len(apps), len(services), resp.StatusCode))
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if resp.StatusCode == http.StatusUnauthorized {
			return fail("send", fmt.Errorf("%q turned down your sign-in there; sign in to it again", remote.Name))
		}
		return fail("send", fmt.Errorf("%q answered %d: %s", remote.Name, resp.StatusCode, strings.TrimSpace(string(msg))))
	}
	return relayUpdateEvents(resp.Body, emit)
}

// relayUpdateEvents reads another server's update event stream and emits
// each event into this one.
func relayUpdateEvents(r io.Reader, emit func(UpdateEvent) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	var name, data string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data += strings.TrimPrefix(line, "data: ")
		case line == "" && name != "":
			var e UpdateEvent
			if err := json.Unmarshal([]byte(data), &e); err == nil {
				e.Event = name
				if err := emit(e); err != nil {
					return err
				}
			}
			name, data = "", ""
		}
	}
	return sc.Err()
}

// ── Pending archive store ───────────────────────────────────────────

// putPendingArchive holds a built archive for one-shot download. Entries
// expire with (roughly) the act token that points at them.
func (s *Server) putPendingArchive(feedID, ref string, data []byte) {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	if s.pendingArchives == nil {
		s.pendingArchives = make(map[string]*pendingArchive)
	}
	s.sweepPendingArchivesLocked(time.Now())
	s.pendingArchives[feedID+"/"+ref] = &pendingArchive{data: data, expires: time.Now().Add(actTokenTTL)}
}

// takePendingArchive removes and returns a pending archive (one-shot).
func (s *Server) takePendingArchive(feedID, ref string) ([]byte, bool) {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	k := feedID + "/" + ref
	a, ok := s.pendingArchives[k]
	if !ok {
		return nil, false
	}
	delete(s.pendingArchives, k)
	if time.Now().After(a.expires) {
		return nil, false
	}
	return a.data, true
}

// dropPendingArchive discards a feed's undownloaded archives (feed deleted).
func (s *Server) dropPendingArchive(feedID string) {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	prefix := feedID + "/"
	for k := range s.pendingArchives {
		if strings.HasPrefix(k, prefix) {
			delete(s.pendingArchives, k)
		}
	}
}

func (s *Server) sweepPendingArchivesLocked(now time.Time) {
	for k, a := range s.pendingArchives {
		if now.After(a.expires) {
			delete(s.pendingArchives, k)
		}
	}
}
