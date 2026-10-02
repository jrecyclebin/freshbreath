package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"poggers.institute/freshbreath/internal/sshkit"
)

// The client branches on these codes rather than on error text:
// invalid_token refreshes and retries, insufficient_user_authentication
// goes straight to a fresh login, after which the retry reopens the session.
func TestSessionAndLoginErrorCodes(t *testing.T) {
	noKey := fmt.Errorf("no SSH key available: %w", sshkit.ErrNoKey)
	cases := []struct {
		name   string
		write  func(http.ResponseWriter)
		status int
		code   string
	}{
		{"rejected token", func(w http.ResponseWriter) { httpInvalidToken(w, "Unauthorized") }, 401, "invalid_token"},
		{"reopen without agent key", func(w http.ResponseWriter) { writeSessionError(w, noKey) }, 401, "insufficient_user_authentication"},
		{"git without agent key", func(w http.ResponseWriter) { writeGitErr(w, noKey) }, 401, "insufficient_user_authentication"},
		{"not found", func(w http.ResponseWriter) { writeSessionError(w, sshkit.ErrSessionNotFound) }, 404, "session_not_found"},
		{"missing id", func(w http.ResponseWriter) { writeSessionError(w, errMissingSessionID) }, 400, "bad_request"},
		{"reopen dial failed", func(w http.ResponseWriter) { writeSessionError(w, errors.New("ssh dial x:22: refused")) }, 502, "reopen_failed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c.write(rec)
			var body struct{ Error string }
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
			}
			if rec.Code != c.status || body.Error != c.code {
				t.Fatalf("got %d %q, want %d %q", rec.Code, body.Error, c.status, c.code)
			}
		})
	}
}

// The step-up challenge says how recent a login must be, in both the
// header and the body, and keeps the header's description to the ASCII
// RFC 6750 allows (ErrNoKey's text carries an em dash).
func TestStepUpChallenge(t *testing.T) {
	rec := httptest.NewRecorder()
	httpStepUp(rec, sshkit.ErrNoKey.Error(), sshAgentTTL)

	var body struct {
		MaxAge int `json:"max_age"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body.MaxAge != 3600 {
		t.Fatalf("body max_age = %d, want 3600", body.MaxAge)
	}
	h := rec.Header().Get("WWW-Authenticate")
	if !strings.Contains(h, `error="insufficient_user_authentication"`) || !strings.Contains(h, `max_age="3600"`) {
		t.Fatalf("WWW-Authenticate = %q", h)
	}
	for _, r := range h {
		if r > 0x7e {
			t.Fatalf("WWW-Authenticate has non-ASCII %q: %q", r, h)
		}
	}
}
