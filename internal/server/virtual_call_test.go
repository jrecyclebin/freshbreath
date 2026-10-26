package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"poggers.institute/freshbreath/internal/db"
)

// TestVirtualCallUpstreamStatus pins the browser path's answer to an HTTP
// step whose status no `responses` block declared: the upstream status
// becomes the real HTTP status, the upstream body the response body — and
// an empty upstream body falls back to the "unexpected status N" message.
func TestVirtualCallUpstreamStatus(t *testing.T) {
	var status int
	var body string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	defer upstream.Close()

	toolFile := fmt.Sprintf(`[oops] Hit an upstream endpoint.

GET %s/api/oops
HTTP 200
`, upstream.URL)
	srv, _, nonce := newVirtualSvcServer(t, toolFile, db.ServiceDescriptor{})

	call := func() *httptest.ResponseRecorder {
		return testRequest(t, srv, http.MethodPost, "/service/call/keeper",
			strings.NewReader(`{"task": "oops", "args": {}}`),
			map[string]string{"X-App-Nonce": nonce})
	}

	// A JSON upstream body rides verbatim as the response body, JSON-typed.
	status, body = 503, `{"error": "boom"}`
	rr := call()
	if rr.Code != 503 {
		t.Fatalf("json body: status %d, want 503: %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("json body: content type %q, want application/json", ct)
	}
	if got := strings.TrimSpace(rr.Body.String()); got != `{"error": "boom"}` {
		t.Errorf("json body: body %q", got)
	}

	// A plain-text body passes through as text.
	status, body = 429, "slow down"
	rr = call()
	if rr.Code != 429 {
		t.Fatalf("text body: status %d, want 429: %s", rr.Code, rr.Body.String())
	}
	if got := strings.TrimSpace(rr.Body.String()); got != "slow down" {
		t.Errorf("text body: body %q", got)
	}

	// An empty upstream body falls back to the error message.
	status, body = 502, ""
	rr = call()
	if rr.Code != 502 {
		t.Fatalf("empty body: status %d, want 502: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "unexpected status 502") {
		t.Errorf("empty body: body %q", rr.Body.String())
	}
}
