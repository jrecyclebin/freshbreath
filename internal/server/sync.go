package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/pkg/sftp"

	"poggers.institute/freshbreath/internal/db"
	"poggers.institute/freshbreath/internal/sshkit"
)

// ── File listing ──

// handleSyncList handles GET /sync/files?sessionId=...&path=...[&recursive=1]
// Returns directory listing with path, size, and modification time. With
// recursive, every entry below path, files and directories alike.
func (s *Server) handleSyncList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	session, err := s.resolveSession(r)
	if err != nil {
		writeSessionError(w, err)
		return
	}

	dirPath := sanitizePath(r.URL.Query().Get("path"))
	if dirPath == "" {
		dirPath = "/"
	}
	recursive := r.URL.Query().Get("recursive")
	files, err := listRemote(session.SFTPClient, dirPath, recursive == "1" || recursive == "true")
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to read directory: %v", err), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"files": files,
	})
}

// listRemote lists dirPath, or everything below it when recursive, sorted
// by path. Hidden files are left out, and hidden directories aren't entered.
func listRemote(client *sftp.Client, dirPath string, recursive bool) ([]map[string]interface{}, error) {
	entry := func(fp string, e os.FileInfo) map[string]interface{} {
		return map[string]interface{}{
			"path":      fp,
			"name":      e.Name(),
			"size":      e.Size(),
			"isDir":     e.IsDir(),
			"updatedAt": e.ModTime().UTC().Format(time.RFC3339),
		}
	}
	files := []map[string]interface{}{}

	if !recursive {
		entries, err := client.ReadDir(dirPath)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			files = append(files, entry(path.Join(dirPath, e.Name()), e))
		}
		return files, nil
	}

	// The root itself must be readable; below it, entries we can't stat are
	// skipped, as the diff's walk does.
	if _, err := client.ReadDir(dirPath); err != nil {
		return nil, err
	}
	walker := client.Walk(dirPath)
	for walker.Step() {
		if walker.Err() != nil || walker.Path() == dirPath {
			continue
		}
		e := walker.Stat()
		if strings.HasPrefix(e.Name(), ".") {
			if e.IsDir() {
				walker.SkipDir()
			}
			continue
		}
		files = append(files, entry(walker.Path(), e))
	}
	sort.Slice(files, func(i, j int) bool { return files[i]["path"].(string) < files[j]["path"].(string) })
	return files, nil
}

// ── Diff ──

type diffRequest struct {
	SessionID string            `json:"sessionId"`
	BasePath  string            `json:"basePath"`
	Files     []diffRequestFile `json:"files"`
}

type diffRequestFile struct {
	Path string `json:"path"`
	Hash string `json:"hash"`
}

// handleSyncDiff handles POST /sync/files/diff
// Compares client's file list against remote, returning what needs
// uploading (missing or changed) and what should be deleted (remote
// files the client doesn't have).
func (s *Server) handleSyncDiff(w http.ResponseWriter, r *http.Request) {
	var req diffRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	session, err := s.sessionMgr.Get(req.SessionID, sessionUserID(r))
	if err != nil {
		writeSessionError(w, err)
		return
	}

	basePath := sanitizePath(req.BasePath)
	if basePath == "" {
		basePath = "/"
	}

	// Build a set of the client's known file paths
	clientFiles := make(map[string]string, len(req.Files)) // path → hash
	for _, f := range req.Files {
		clientFiles[sanitizePath(f.Path)] = strings.ToLower(f.Hash)
	}

	// Walk the remote directory
	remoteFiles, err := walkRemote(session.SFTPClient, basePath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to walk remote: %v", err), http.StatusBadGateway)
		return
	}

	var upload []string
	var deletePaths []string

	// Remote files that the client doesn't have, or has a different hash → client needs to upload
	for rPath, rHash := range remoteFiles {
		cHash, exists := clientFiles[rPath]
		if !exists {
			deletePaths = append(deletePaths, rPath)
		} else if cHash != rHash {
			upload = append(upload, rPath)
		}
		// Same hash — nothing to do
	}

	// Client files that don't exist on remote → client should upload them
	for cPath := range clientFiles {
		if _, exists := remoteFiles[cPath]; !exists {
			upload = append(upload, cPath)
		}
	}

	sort.Strings(upload)
	sort.Strings(deletePaths)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"upload": upload,
		"delete": deletePaths,
	})
}

// ── Upload ──

// handleSyncUpload handles PUT /sync/files/{path...}?sessionId=...
// Writes the request body as a file at the given path. Verifies SHA256
// after write using the X-Hash header.
func (s *Server) handleSyncUpload(w http.ResponseWriter, r *http.Request) {
	session, err := s.resolveSession(r)
	if err != nil {
		writeSessionError(w, err)
		return
	}

	filePath := sanitizePath(r.PathValue("path"))
	if filePath == "" {
		http.Error(w, "Missing file path", http.StatusBadRequest)
		return
	}

	expectedHash := strings.ToLower(r.Header.Get("X-Hash"))

	// Ensure parent directory exists
	dir := path.Dir(filePath)
	if dir != "/" && dir != "." {
		session.SFTPClient.MkdirAll(dir)
	}

	// Open file for writing on the remote
	f, err := session.SFTPClient.Create(filePath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to create file: %v", err), http.StatusBadGateway)
		return
	}
	defer f.Close()

	// Write and hash simultaneously
	h := sha256.New()
	mw := io.MultiWriter(f, h)

	if _, err := io.Copy(mw, r.Body); err != nil {
		http.Error(w, fmt.Sprintf("Write failed: %v", err), http.StatusBadGateway)
		return
	}

	actualHash := hex.EncodeToString(h.Sum(nil))

	if expectedHash != "" && expectedHash != actualHash {
		// Hash mismatch — clean up the bad file
		session.SFTPClient.Remove(filePath)
		http.Error(w, fmt.Sprintf("Hash mismatch: expected %s, got %s", expectedHash, actualHash), http.StatusConflict)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"path": filePath,
		"hash": actualHash,
	})
}

// ── Download ──

// handleSyncDownload handles GET /sync/files/{path...}?sessionId=...
// Streams the file contents as a binary response.
func (s *Server) handleSyncDownload(w http.ResponseWriter, r *http.Request) {
	session, err := s.resolveSession(r)
	if err != nil {
		writeSessionError(w, err)
		return
	}

	filePath := sanitizePath(r.PathValue("path"))
	if filePath == "" {
		http.Error(w, "Missing file path", http.StatusBadRequest)
		return
	}

	f, err := session.SFTPClient.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) || strings.Contains(err.Error(), "not found") {
			http.Error(w, "File not found", http.StatusNotFound)
		} else {
			http.Error(w, fmt.Sprintf("Failed to open file: %v", err), http.StatusBadGateway)
		}
		return
	}
	defer f.Close()

	stat, err := f.Stat()
	if err == nil && stat.IsDir() {
		http.Error(w, "Path is a directory, not a file", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	io.Copy(w, f)
}

// ── Delete ──

// handleSyncDelete handles DELETE /sync/files/{path...}?sessionId=...
func (s *Server) handleSyncDelete(w http.ResponseWriter, r *http.Request) {
	session, err := s.resolveSession(r)
	if err != nil {
		writeSessionError(w, err)
		return
	}

	filePath := sanitizePath(r.PathValue("path"))
	if filePath == "" {
		http.Error(w, "Missing file path", http.StatusBadRequest)
		return
	}

	if err := session.SFTPClient.Remove(filePath); err != nil {
		http.Error(w, fmt.Sprintf("Failed to delete: %v", err), http.StatusBadGateway)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ── File ops dispatcher ──

// handleSyncFileOps routes GET/PUT/DELETE on /sync/files/{path*}
// to the appropriate handler.
func (s *Server) handleSyncFileOps(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleSyncDownload(w, r)
	case http.MethodPut:
		s.handleSyncUpload(w, r)
	case http.MethodDelete:
		s.handleSyncDelete(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// ── Helpers ──

// writeSessionError maps session lookup failures to machine-readable
// responses, so clients react to codes instead of error text:
//
//	401 Unauthorized  {"error":"insufficient_user_authentication"} → the expired session needs a reopen and the agent key has lapsed
//	404 Not Found     {"error":"session_not_found"} → unknown, someone else's, or past its reopen window
//	400 Bad Request   {"error":"bad_request"}       → missing sessionId
//	502 Bad Gateway   {"error":"reopen_failed"}     → the reopen's dial failed
func writeSessionError(w http.ResponseWriter, err error) {
	var code string
	var status int
	switch {
	case errors.Is(err, sshkit.ErrNoKey):
		httpStepUp(w, err.Error(), sshAgentTTL)
		return
	case errors.Is(err, sshkit.ErrSessionNotFound):
		status, code = http.StatusNotFound, "session_not_found"
	case errors.Is(err, errMissingSessionID):
		status, code = http.StatusBadRequest, "bad_request"
	default:
		status, code = http.StatusBadGateway, "reopen_failed"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": err.Error()})
}

var errMissingSessionID = errors.New("missing sessionId parameter")

// sessionUserID is the requesting user's id — sessions answer only to the
// user who opened them. Zero (no user) matches no session.
func sessionUserID(r *http.Request) int64 {
	if u, _ := r.Context().Value(userKey).(*db.User); u != nil {
		return u.ID
	}
	return 0
}

// resolveSession extracts the sessionId query param and looks up the
// requesting user's session, reopening it if it expired.
func (s *Server) resolveSession(r *http.Request) (*sshkit.Session, error) {
	sid := r.URL.Query().Get("sessionId")
	if sid == "" {
		return nil, errMissingSessionID
	}
	return s.sessionMgr.Get(sid, sessionUserID(r))
}

// sanitizePath prevents path traversal attacks.
func sanitizePath(p string) string {
	if p == "" {
		return ""
	}
	// Clean the path and strip any leading ..
	cleaned := path.Clean("/" + p)
	// Remove leading slash for consistent handling
	if cleaned == "/" {
		return "/"
	}
	return cleaned
}

// walkRemote recursively walks a directory on the remote host,
// returning a map of file path → SHA256 hash. Directories are skipped.
func walkRemote(client *sftp.Client, root string) (map[string]string, error) {
	result := make(map[string]string)

	walker := client.Walk(root)
	for walker.Step() {
		if err := walker.Err(); err != nil {
			// Skip files we can't stat — permissions, broken symlinks, etc.
			continue
		}
		entry := walker.Stat()
		if entry == nil || entry.IsDir() {
			continue
		}

		fullPath := walker.Path()

		// Hash the file contents
		f, err := client.Open(fullPath)
		if err != nil {
			continue
		}
		h := sha256.New()
		io.Copy(h, f)
		f.Close()

		result[fullPath] = hex.EncodeToString(h.Sum(nil))
	}

	return result, nil
}
