package server

// Remote update feeds — see design/remote-updates.md. Two modes share one row
// shape and one per-feed AES key: "receive" pulls a key-authenticated archive
// from a remote URL and lands its manifest ops in staging; "publish" builds
// the same archive from selected apps/services and hands it back for the
// admin to self-host. The encryption is integrity, not confidentiality: the
// remote host never holds the key, so it can't forge a valid archive.

import (
	"archive/tar"
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
// surface tolerable: file ops can't change ownership or create anything.
type UpdateManifest struct {
	Version string     `json:"version"`
	Ops     []UpdateOp `json:"ops"`
}

type UpdateOp struct {
	Action string `json:"action"`
	Target string `json:"target"`
	Source string `json:"source,omitempty"` // path inside the tarball; defaulted per action
	Slot   string `json:"slot,omitempty"`   // v1: staging only
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

// validateUpdateOps pre-flights every op against the store and the extracted
// archive before the first one executes: targets must exist, sources must be
// in the tarball, slots default to staging. A validation failure touches
// nothing — "feed refused before it started" beats "half-applied feed."
func (s *Server) validateUpdateOps(ops []UpdateOp, archiveDir string) error {
	for i, op := range ops {
		switch op.Action {
		case "update_app_files":
			if _, err := s.store.GetApp(op.Target); err != nil {
				return fmt.Errorf("op %d: app not found: %s", i, op.Target)
			}
			slot := op.Slot
			if slot == "" {
				slot = "staging"
			}
			if slot != "staging" {
				return fmt.Errorf("op %d: slot %q not supported (v1: staging only)", i, slot)
			}
		case "update_service_files":
			svc, err := s.store.GetServiceByName(op.Target)
			if err != nil {
				return fmt.Errorf("op %d: service not found: %s", i, op.Target)
			}
			if svc.Descriptor.Type != "tasks" && svc.Descriptor.Type != "virtual" {
				return fmt.Errorf("op %d: service type %q does not support file publishing", i, svc.Descriptor.Type)
			}
		default:
			return fmt.Errorf("op %d: unknown action %q", i, op.Action)
		}
		src := filepath.Join(archiveDir, filepath.FromSlash(op.opSource()))
		if _, err := os.Stat(src); err != nil {
			return fmt.Errorf("op %d: source %q not found in archive", i, op.opSource())
		}
	}
	return nil
}

// executeUpdateOp runs one op against the extracted archive. Both actions
// overwrite, never delete — updates don't tear down.
func (s *Server) executeUpdateOp(op UpdateOp, archiveDir string) error {
	switch op.Action {
	case "update_app_files":
		src := filepath.Join(archiveDir, filepath.FromSlash(op.opSource()))
		dst := filepath.Join(s.config.DataDir, "apps", op.Target, "staging")
		if err := os.RemoveAll(dst); err != nil {
			return fmt.Errorf("failed to clear staging slot: %w", err)
		}
		if err := copyDir(src, dst); err != nil {
			return fmt.Errorf("copy failed: %w", err)
		}
		s.rebuildHostedRoutes()
		return nil
	case "update_service_files":
		svc, err := s.store.GetServiceByName(op.Target)
		if err != nil {
			return err
		}
		src := filepath.Join(archiveDir, filepath.FromSlash(op.opSource()))
		data, err := os.ReadFile(src)
		if err != nil {
			return fmt.Errorf("read source: %w", err)
		}
		path := serviceDefinitionPath(s.config.DataDir, svc)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return fmt.Errorf("failed to create service dir: %w", err)
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			return fmt.Errorf("write failed: %w", err)
		}
		if svc.Descriptor.Type == "virtual" {
			s.virtualMCPs.add(s, svc)
		}
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
// cleaned and traversal-checked — an archive is remote input.
func extractArchive(data []byte, destDir string) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
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
		if err := os.MkdirAll(filepath.Dir(out), 0755); err != nil {
			return err
		}
		f, err := os.OpenFile(out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(f, io.LimitReader(tr, maxArchiveSize))
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
	id := db.GenNonce()
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
		// ships again.
		if f.LastSeenVersion != f.LastAppliedVersion {
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
	if manifest.Version == f.LastAppliedVersion {
		return "up_to_date", manifest.Version, nil
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
func (s *Server) coreApplyUpdateFeeds(ctx context.Context, ids []string, progress chan<- UpdateEvent) error {
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
		if err := s.applyUpdateFeed(ctx, f, progress); err != nil {
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
// feed, streaming step events. The version stamps only on full success: the
// stamp is the only thing keeping an anonymous caller from re-running a feed
// forever, and stamping early would silently strand a half-applied feed.
func (s *Server) applyUpdateFeed(ctx context.Context, f *db.UpdateFeed, progress chan<- UpdateEvent) error {
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
	if err := s.validateUpdateOps(manifest.Ops, archiveDir); err != nil {
		return fail("validate", err)
	}
	if err := emit(UpdateEvent{Event: "validate", Step: "validate", Ops: len(manifest.Ops), Status: "ok"}); err != nil {
		return err
	}

	// ops — in manifest order; each overwrites, so a retry from op 0 is safe.
	for i, op := range manifest.Ops {
		if err := emit(UpdateEvent{Event: "op", Index: i, Action: op.Action, Target: op.Target, Status: "start"}); err != nil {
			return err
		}
		if err := s.executeUpdateOp(op, archiveDir); err != nil {
			return fail("op", fmt.Errorf("op %d (%s %s): %w", i, op.Action, op.Target, err))
		}
		if err := emit(UpdateEvent{Event: "op", Index: i, Action: op.Action, Target: op.Target, Status: "done"}); err != nil {
			return err
		}
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
	_ = emit(UpdateEvent{Event: "collect", Step: "collect", Apps: len(apps), Services: len(services), Status: "ok"})

	var manifest UpdateManifest
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	for _, nonce := range apps {
		if _, err := s.store.GetApp(nonce); err != nil {
			tw.Close()
			gzw.Close()
			return "", cerr(http.StatusNotFound, "app not found: %s", nonce)
		}
		sourceSlot := "staging"
		srcDir := filepath.Join(s.config.DataDir, "apps", nonce, "staging")
		if fi, err := os.Stat(srcDir); err != nil || !fi.IsDir() {
			sourceSlot = "dev"
			srcDir = filepath.Join(s.config.DataDir, "apps", nonce, "web")
		}
		arcRoot := "apps/" + nonce
		added := false
		err := filepath.Walk(srcDir, func(path string, fi os.FileInfo, err error) error {
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
			tw.Close()
			gzw.Close()
			return "", cerr(http.StatusInternalServerError, "collect app %s: %v", nonce, err)
		}
		if !added {
			tw.Close()
			gzw.Close()
			return "", cerr(http.StatusBadRequest, "app %s has no files to package (deploy to staging or upload to dev first)", nonce)
		}
		if err := emit(UpdateEvent{Event: "app", Nonce: nonce, SourceSlot: sourceSlot, Status: "added"}); err != nil {
			tw.Close()
			gzw.Close()
			return "", err
		}
		manifest.Ops = append(manifest.Ops, UpdateOp{
			Action: "update_app_files", Target: nonce, Source: arcRoot, Slot: "staging",
		})
	}

	for _, name := range services {
		svc, err := s.store.GetServiceByName(name)
		if err != nil {
			tw.Close()
			gzw.Close()
			return "", cerr(http.StatusNotFound, "service not found: %s", name)
		}
		if svc.Descriptor.Type != "tasks" && svc.Descriptor.Type != "virtual" {
			tw.Close()
			gzw.Close()
			return "", cerr(http.StatusBadRequest, "service type %q does not support file publishing", svc.Descriptor.Type)
		}
		data, err := os.ReadFile(serviceDefinitionPath(s.config.DataDir, svc))
		if err != nil {
			tw.Close()
			gzw.Close()
			return "", cerr(http.StatusBadRequest, "service %s has no definition file to package", name)
		}
		arcName := "services/" + name + ".txt"
		if err := tarWriteFile(tw, arcName, data); err != nil {
			tw.Close()
			gzw.Close()
			return "", cerr(http.StatusInternalServerError, "package service %s: %v", name, err)
		}
		if err := emit(UpdateEvent{Event: "service", Name: name, Status: "added"}); err != nil {
			tw.Close()
			gzw.Close()
			return "", err
		}
		manifest.Ops = append(manifest.Ops, UpdateOp{Action: "update_service_files", Target: name})
	}

	if len(manifest.Ops) == 0 {
		tw.Close()
		gzw.Close()
		return "", cerr(http.StatusBadRequest, "nothing selected: pass apps and/or services")
	}
	if version == "" {
		version = time.Now().UTC().Format("2006.01.02T15:04Z")
	}
	manifest.Version = version
	mb, _ := json.MarshalIndent(manifest, "", "  ")
	if err := tarWriteFile(tw, "manifest.json", mb); err != nil {
		tw.Close()
		gzw.Close()
		return "", cerr(http.StatusInternalServerError, "manifest: %v", err)
	}
	if err := tw.Close(); err != nil {
		return "", cerr(http.StatusInternalServerError, "tar close: %v", err)
	}
	if err := gzw.Close(); err != nil {
		return "", cerr(http.StatusInternalServerError, "gzip close: %v", err)
	}
	_ = emit(UpdateEvent{Event: "manifest", Step: "manifest", Version: version, Ops: len(manifest.Ops), Status: "ok"})
	archive, err := encryptArchive(key, buf.Bytes())
	if err != nil {
		return "", cerr(http.StatusInternalServerError, "encrypt: %v", err)
	}
	_ = emit(UpdateEvent{Event: "encrypt", Step: "encrypt", Status: "ok"})
	ref := db.GenNonce()
	s.putPendingArchive(feed.ID, ref, archive)
	// Plain admin-authenticated URL — no act-token. The consumer is the
	// control panel SPA, which already holds a bearer token; act-tokens are
	// for credential-less fetchers (see design/remote-updates.md Reuse Map).
	downloadURL := "/api/updates/" + feed.ID + "/archive?ref=" + ref
	_ = emit(UpdateEvent{Event: "done", Step: "done", Version: version, DownloadURL: downloadURL})
	return downloadURL, nil
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
