package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/pkg/sftp"

	"poggers.institute/freshbreath/internal/sshkit"
)

// ── File listing ──

// handleSyncList handles GET /sync/files?sessionId=...&path=...
// Returns directory listing with path, size, and modification time.
func (s *Server) handleSyncList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	session, err := s.resolveSession(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	dirPath := sanitizePath(r.URL.Query().Get("path"))
	if dirPath == "" {
		dirPath = "/"
	}

	entries, err := session.SFTPClient.ReadDir(dirPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to read directory: %v", err), http.StatusBadGateway)
		return
	}

	files := make([]map[string]interface{}, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		// Skip hidden files and . / ..
		if strings.HasPrefix(name, ".") {
			continue
		}
		fp := path.Join(dirPath, name)
		files = append(files, map[string]interface{}{
			"path":      fp,
			"name":      name,
			"size":      e.Size(),
			"isDir":     e.IsDir(),
			"updatedAt": e.ModTime().UTC().Format(time.RFC3339),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"files": files,
	})
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

	session, err := s.sessionMgr.Get(req.SessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
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
		http.Error(w, err.Error(), http.StatusBadRequest)
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
		http.Error(w, err.Error(), http.StatusBadRequest)
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
		http.Error(w, err.Error(), http.StatusBadRequest)
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

// resolveSession extracts the sessionId query param and looks up the session.
func (s *Server) resolveSession(r *http.Request) (*sshkit.Session, error) {
	sid := r.URL.Query().Get("sessionId")
	if sid == "" {
		return nil, fmt.Errorf("missing sessionId parameter")
	}
	return s.sessionMgr.Get(sid)
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
