package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// watchStream opens /service/watch/{id} on a live server and returns the
// call events as they arrive. It returns once the subscription is live.
func watchStream(t *testing.T, base, id, nonce, client string) <-chan serviceWatchEvent {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/service/watch/"+id, nil)
	req.Header.Set("X-App-Nonce", nonce)
	req.Header.Set("X-Frbr-Client", client)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("watch: status %d: %s", resp.StatusCode, b)
	}
	sc := bufio.NewScanner(resp.Body)
	if !sc.Scan() || sc.Text() != ": watch" {
		t.Fatalf("watch: first line %q", sc.Text())
	}
	out := make(chan serviceWatchEvent, 8)
	go func() {
		defer resp.Body.Close()
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				var ev serviceWatchEvent
				json.Unmarshal([]byte(data), &ev)
				out <- ev
			}
		}
	}()
	return out
}

func nextWatchEvent(t *testing.T, ch <-chan serviceWatchEvent) serviceWatchEvent {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("no watch event")
	}
	return serviceWatchEvent{}
}

// TestServiceWatch pins /service/watch for a virtual service and a proxied
// api service: successful calls arrive as events (tool names with their
// WATCH keys; fetch method and path, no query), failures and the watching
// page's own calls don't, and the endpoint answers only apps the service
// is approved for.
func TestServiceWatch(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/missing") {
			w.WriteHeader(404)
		}
		fmt.Fprint(w, `{"list": "groceries"}`)
	}))
	defer upstream.Close()

	toolFile := fmt.Sprintf(`[add-item] Add an item.

POST %[1]s/items
{name: $name}

HTTP 200
WATCH $name
---
[list-items] List items.

GET %[1]s/items
HTTP 200
`, upstream.URL)
	srv, svc, nonce := newVirtualSvcServer(t, toolFile, db.ServiceDescriptor{})
	live := httptest.NewServer(srv)
	t.Cleanup(live.Close) // registered first, so the watch streams are cancelled before it waits on them

	call := func(task, name, client string) {
		t.Helper()
		rr := testRequest(t, srv, http.MethodPost, "/service/call/keeper",
			strings.NewReader(`{"task": "`+task+`", "args": {"name": "`+name+`"}}`),
			map[string]string{"X-App-Nonce": nonce, "X-Frbr-Client": client})
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", task, rr.Code, rr.Body.String())
		}
	}

	// Events arrive in call order, so the first one must be the other
	// page's: the watcher's own call and the unwatched tool came earlier.
	events := watchStream(t, live.URL, fmt.Sprint(svc.ID), nonce, "page-a")
	call("list-items", "milk", "page-b") // unwatched: no event
	call("add-item", "mine", "page-a")   // the watcher's own: no event
	call("add-item", "theirs", "page-b")
	if ev := nextWatchEvent(t, events); ev.Tool != "add-item" || ev.Key != "theirs" {
		t.Fatalf("event = %+v", ev)
	}
	call("add-item", "agent", "") // no client (an MCP caller): everyone hears it
	if ev := nextWatchEvent(t, events); ev.Key != "agent" {
		t.Fatalf("event = %+v", ev)
	}

	// A proxied api service reports successful fetches by path.
	api, err := srv.coreCreateService(&db.User{ID: 1, Role: "Admin"}, "Shop", upstream.URL+"/v1", db.ServiceDescriptor{Type: "api", Proxied: true}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.store.LinkAppService(nonce, api.ID); err != nil {
		t.Fatal(err)
	}
	apiID := fmt.Sprint(api.ID)
	fetches := watchStream(t, live.URL, apiID, nonce, "page-a")
	for _, path := range []string{"/missing", "/users/42?secret=1"} {
		rr := testRequest(t, srv, http.MethodPost, "/service/"+apiID+path, strings.NewReader(`{}`),
			map[string]string{"X-App-Nonce": nonce})
		if path != "/missing" && rr.Code != http.StatusOK {
			t.Fatalf("fetch %s: status %d: %s", path, rr.Code, rr.Body.String())
		}
	}
	if ev := nextWatchEvent(t, fetches); ev.Method != "POST" || ev.Fetch != "/users/42" {
		t.Fatalf("fetch event = %+v", ev)
	}

	// An app the service isn't approved for can't watch it.
	other, err := srv.store.CreateApp("other", "Development", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rr := testRequest(t, srv, http.MethodGet, "/service/watch/"+apiID, nil, map[string]string{"X-App-Nonce": other})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("unapproved app: status %d", rr.Code)
	}
}
