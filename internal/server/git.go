package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"

	"poggers.institute/freshbreath/internal/db"
	"poggers.institute/freshbreath/internal/sshkit"
)

// handleGitSign handles POST /ssh/git/sign — sign commit payloads with the
// user's SSH key (no clone, no network). Returns the armored SSH SIGNATURE
// block plus a verbatim body for GitHub's POST /repos/{o}/{r}/git/commits.
func (s *Server) handleGitSign(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	au, ok := resolveGitUser(w, r)
	if !ok {
		return
	}

	var req struct {
		Commits []sshkit.CommitToSign `json:"commits"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeGitErr(w, fmt.Errorf("%w: invalid JSON: %v", sshkit.ErrInvalidInput, err))
		return
	}
	if len(req.Commits) == 0 {
		writeGitErr(w, fmt.Errorf("%w: empty commits", sshkit.ErrInvalidInput))
		return
	}

	signed, err := s.gitGw.SignCommits(au.ID, req.Commits)
	if err != nil {
		writeGitErr(w, err)
		return
	}
	_ = s.store.LogAudit(au.Email, "git_sign", fmt.Sprintf("%d commit(s)", len(signed)))
	writeJSON(w, map[string]interface{}{"commits": signed})
}

// handleGitBranches handles POST /ssh/git/branches — list remote refs via
// ls-remote (no clone, no packfile).
func (s *Server) handleGitBranches(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	au, ok := resolveGitUser(w, r)
	if !ok {
		return
	}
	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeGitErr(w, fmt.Errorf("%w: invalid JSON: %v", sshkit.ErrInvalidInput, err))
		return
	}
	if req.URL == "" {
		writeGitErr(w, fmt.Errorf("%w: url is required", sshkit.ErrInvalidInput))
		return
	}

	head, branches, err := s.gitGw.Branches(au.ID, req.URL)
	if err != nil {
		writeGitErr(w, err)
		return
	}
	writeJSON(w, map[string]interface{}{"head": head, "branches": branches})
}

// handleGitPull handles POST /ssh/git/pull — selective read side. No paths →
// tree listing; paths → file contents (a directory path includes its subtree).
func (s *Server) handleGitPull(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	au, ok := resolveGitUser(w, r)
	if !ok {
		return
	}
	var req struct {
		URL    string   `json:"url"`
		Branch string   `json:"branch"`
		Paths  []string `json:"paths"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeGitErr(w, fmt.Errorf("%w: invalid JSON: %v", sshkit.ErrInvalidInput, err))
		return
	}
	if req.URL == "" {
		writeGitErr(w, fmt.Errorf("%w: url is required", sshkit.ErrInvalidInput))
		return
	}

	snap, err := s.gitGw.Pull(au.ID, req.URL, req.Branch, req.Paths)
	if err != nil {
		writeGitErr(w, err)
		return
	}
	writeJSON(w, snap)
}

// handleGitCommit handles POST /ssh/git/commit — fused signed-commit-and-push.
func (s *Server) handleGitCommit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	au, ok := resolveGitUser(w, r)
	if !ok {
		return
	}
	var req sshkit.GitCommitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeGitErr(w, fmt.Errorf("%w: invalid JSON: %v", sshkit.ErrInvalidInput, err))
		return
	}
	if req.URL == "" {
		writeGitErr(w, fmt.Errorf("%w: url is required", sshkit.ErrInvalidInput))
		return
	}
	if req.Message == "" {
		writeGitErr(w, fmt.Errorf("%w: commit message is required", sshkit.ErrInvalidInput))
		return
	}
	// Author defaults from the user record when not supplied.
	if req.AuthorName == "" {
		req.AuthorName = au.Name
	}
	if req.AuthorEmail == "" {
		req.AuthorEmail = au.Email
	}

	sha, err := s.gitGw.CommitPush(au.ID, req)
	if err != nil {
		writeGitErr(w, err)
		return
	}
	_ = s.store.LogAudit(au.Email, "git_commit", fmt.Sprintf("%s @ %s", sha, req.URL))
	writeJSON(w, map[string]interface{}{
		"commit": sha,
		"status": "pushed",
		"signed": true,
	})
}

// resolveGitUser extracts the authenticated user from the request context and
// rejects unauthenticated requests (matching the handleSSHSessions guard).
// Returns ok=false if the response has already been written.
func resolveGitUser(w http.ResponseWriter, r *http.Request) (*db.User, bool) {
	user, _ := r.Context().Value(userKey).(*db.User)
	if user == nil || user.ID < 0 {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	return user, true
}

// writeGitErr maps a gateway error to the right HTTP status:
//   - ErrNoKey                → 401 (client re-prompts for the SSH passphrase)
//   - ErrInvalidInput         → 400 (bad JSON, missing url/message, bad sha/path)
//   - os.ErrNotExist          → 404 (unknown branch, missing pull path)
//   - ErrStaleBase, ErrNothingToCommit → 409 (client pulls and retries)
//   - anything else           → 502 (clone/push/transport failure)
func writeGitErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sshkit.ErrNoKey):
		http.Error(w, err.Error(), http.StatusUnauthorized)
	case errors.Is(err, sshkit.ErrInvalidInput):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, os.ErrNotExist):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, sshkit.ErrStaleBase), errors.Is(err, sshkit.ErrNothingToCommit):
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		http.Error(w, err.Error(), http.StatusBadGateway)
	}
}

// writeJSON encodes v as JSON with the application/json content type.
func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
