package server

import (
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/oauth2"

	"poggers.institute/freshbreath/internal/db"
)

func TestCallbackErrorEscapesAndHints(t *testing.T) {
	rerr := &oauth2.RetrieveError{
		ErrorCode:        "invalid_client",
		ErrorDescription: "Client authentication failed (e.g., unknown client)",
	}
	err := fmt.Errorf("token exchange: %w", rerr)

	s := &Server{}
	rec := &db.AuthRecord{Name: "clerk"}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/service/callback", nil)

	s.callbackError(w, r, rec, "Code exchange failed", err)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}

	body := w.Body.String()
	if !strings.Contains(body, "invalid_client") {
		t.Error("error detail missing from body")
	}
	if !strings.Contains(body, "client secret") {
		t.Error("operator hint missing from body")
	}
	if !strings.Contains(body, "Likely cause") {
		t.Error("hint box should be visible when a hint exists")
	}
}

func TestCallbackErrorEscapesDetail(t *testing.T) {
	// Provider-controlled error text must not be able to inject markup.
	rerr := &oauth2.RetrieveError{
		ErrorCode:        "evil",
		ErrorDescription: `<script>alert("xss")</script>`,
	}
	err := fmt.Errorf("token exchange: %w", rerr)

	s := &Server{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/service/callback", nil)
	s.callbackError(w, r, &db.AuthRecord{Name: "x"}, "Code exchange failed", err)

	body := w.Body.String()
	if strings.Contains(body, `<script>alert(`) {
		t.Error("unescaped <script> reached the response body")
	}
	if !strings.Contains(body, html.EscapeString("<script>")) {
		t.Error("detail should be HTML-escaped")
	}
	// No mapped hint for this code: the hint box must be hidden.
	if !strings.Contains(body, `<div class="auth-hint" hidden>`) {
		t.Error("hint box should be hidden when no hint")
	}
}

func TestOAuthHintCoversCommonCodes(t *testing.T) {
	for _, code := range []string{"invalid_client", "invalid_grant", "redirect_uri_mismatch", "unauthorized_client", "invalid_scope"} {
		if got := oauthHint(&oauth2.RetrieveError{ErrorCode: code}); got == "" {
			t.Errorf("oauthHint(%q) = empty", code)
		}
	}
	if got := oauthHint(&oauth2.RetrieveError{ErrorCode: "slow_down"}); got != "" {
		t.Errorf("unexpected hint for unmapped code: %q", got)
	}
}

func TestErrorsAsThroughWrap(t *testing.T) {
	rerr := &oauth2.RetrieveError{ErrorCode: "invalid_client"}
	err := fmt.Errorf("token exchange: %w", rerr)
	var out *oauth2.RetrieveError
	if !errors.As(err, &out) || out.ErrorCode != "invalid_client" {
		t.Fatal("errors.As failed through fmt.Errorf wrap")
	}
	_ = url.Values{}
}
