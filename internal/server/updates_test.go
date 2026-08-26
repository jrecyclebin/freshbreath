package server

// Remote update feed tests — see design/remote-updates.md's Testing section.
// The round-trip test at the bottom is the load-bearing one: it proves build
// (publish mode) and apply (receive mode) are the same shape on the wire.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"poggers.institute/freshbreath/internal/db"
)

// ── helpers ─────────────────────────────────────────────────────────

type sseEvent struct {
	Event string
	Data  map[string]interface{}
}

// parseSSE splits an event-stream body into its events.
func parseSSE(body string) []sseEvent {
	var out []sseEvent
	var cur sseEvent
	for _, line := range strings.Split(body, "\n") {
		switch {
		case strings.HasPrefix(line, "event: "):
			cur.Event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			cur.Data = map[string]interface{}{}
			json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &cur.Data)
		case line == "":
			if cur.Event != "" {
				out = append(out, cur)
				cur = sseEvent{}
			}
		}
	}
	return out
}

// createUpdateFeed creates a feed through the admin API and returns
// (id, key). keyHex empty → server-generated.
func createUpdateFeed(t *testing.T, srv *Server, url, mode, name, keyHex string) (string, string) {
	t.Helper()
	body := map[string]string{"url": url, "mode": mode, "name": name}
	if keyHex != "" {
		body["key_hex"] = keyHex
	}
	b, _ := json.Marshal(body)
	rr := testRequest(t, srv, http.MethodPost, "/api/updates", bytes.NewReader(b), nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("create feed: status = %d, body = %q", rr.Code, rr.Body.String())
	}
	var res struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	json.Unmarshal(rr.Body.Bytes(), &res)
	return res.ID, res.Key
}

// makeUpdateArchive builds an encrypted in-memory archive: a tar.gz of files
// plus a manifest, sealed with key. This is the independent constructor the
// tests use — deliberately NOT coreBuildArchive, so apply is tested against
// an archive built by other means.
func makeUpdateArchive(t *testing.T, keyHex string, manifest *UpdateManifest, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)
	for name, data := range files {
		if err := tarWriteFile(tw, name, data); err != nil {
			t.Fatalf("tar %s: %v", name, err)
		}
	}
	mb, _ := json.Marshal(manifest)
	if err := tarWriteFile(tw, "manifest.json", mb); err != nil {
		t.Fatalf("tar manifest: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	key, _ := hexDecode(keyHex)
	enc, err := encryptArchive(key, buf.Bytes())
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	return enc
}

// archiveServer serves archive bytes with ETag/Last-Modified conditional
// support, the way a real update host would.
func archiveServer(t *testing.T, data []byte, etag, lastMod string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if etag != "" && r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if etag == "" && lastMod != "" && r.Header.Get("If-Modified-Since") == lastMod {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if etag != "" {
			w.Header().Set("ETag", etag)
		}
		if lastMod != "" {
			w.Header().Set("Last-Modified", lastMod)
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(data)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func applyUpdates(t *testing.T, srv *Server, body string) []sseEvent {
	t.Helper()
	rr := testRequest(t, srv, http.MethodPost, "/api/updates/apply", strings.NewReader(body), nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("apply: status = %d, body = %q", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("apply content-type = %q, want event-stream", ct)
	}
	return parseSSE(rr.Body.String())
}

func checkUpdates(t *testing.T, srv *Server) []UpdateAvailable {
	t.Helper()
	rr := testRequest(t, srv, http.MethodGet, "/api/updates/check", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("check: status = %d, body = %q", rr.Code, rr.Body.String())
	}
	var res struct {
		Updates []UpdateAvailable `json:"updates"`
	}
	json.Unmarshal(rr.Body.Bytes(), &res)
	return res.Updates
}

func feedState(t *testing.T, srv *Server, id string) *db.UpdateFeed {
	t.Helper()
	f, err := srv.store.GetUpdateFeed(id)
	if err != nil {
		t.Fatalf("GetUpdateFeed: %v", err)
	}
	return f
}

func hexDecode(s string) ([]byte, error) {
	return hex.DecodeString(s)
}

// ── CRUD + authz ────────────────────────────────────────────────────

func TestUpdateFeedCRUD(t *testing.T) {
	srv := newTestServer(t)

	id, key := createUpdateFeed(t, srv, "http://127.0.0.1:1/u.tar.gz.enc", "receive", "my feed", "")
	if len(key) != 64 {
		t.Fatalf("key = %d hex chars, want 64", len(key))
	}
	st := feedState(t, srv, id)
	if st.KeyHex != key {
		t.Error("stored key does not match the returned key")
	}

	// List never carries the key.
	rr := testRequest(t, srv, http.MethodGet, "/api/updates", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "key_hex") {
		t.Error("list response contains key_hex")
	}
	if strings.Contains(rr.Body.String(), key) {
		t.Error("list response leaks the key value")
	}
	var list struct {
		Feeds []*db.UpdateFeed `json:"feeds"`
	}
	json.Unmarshal(rr.Body.Bytes(), &list)
	if len(list.Feeds) != 1 || list.Feeds[0].ID != id {
		t.Fatalf("list = %+v, want one feed with id %s", list.Feeds, id)
	}
	// last_error fields are part of the list shape; empty on a fresh feed.
	if list.Feeds[0].LastError != "" || list.Feeds[0].LastErrorAt != nil {
		t.Errorf("fresh feed last_error = %q", list.Feeds[0].LastError)
	}

	// Supplied key is honored verbatim (cross-instance pairing path).
	supplied := strings.Repeat("ab", 32)
	id2, key2 := createUpdateFeed(t, srv, "http://127.0.0.1:1/two", "receive", "", supplied)
	if key2 != supplied {
		t.Errorf("supplied key not honored: got %q", key2)
	}
	// A malformed key is rejected.
	bad, _ := json.Marshal(map[string]string{"url": "http://127.0.0.1:1/x", "mode": "receive", "key_hex": "zz"})
	rr = testRequest(t, srv, http.MethodPost, "/api/updates", bytes.NewReader(bad), nil)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("bad key_hex: status = %d, want 400", rr.Code)
	}

	// Delete.
	rr = testRequest(t, srv, http.MethodDelete, "/api/updates/"+id2, nil, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rr.Code, rr.Body.String())
	}
	if _, err := srv.store.GetUpdateFeed(id2); err == nil {
		t.Error("feed survived delete")
	}

	// Edit URL clears the conditional-GET cache (the etag described the old
	// remote). Check first so the cache is populated.
	ts := archiveServer(t, []byte("x"), `"e1"`, "")
	id3, _ := createUpdateFeed(t, srv, ts.URL, "receive", "cache", "")
	checkUpdates(t, srv)
	if st := feedState(t, srv, id3); st.LastETag != `"e1"` {
		t.Fatalf("etag not cached after check: %q", st.LastETag)
	}
	url, _ := json.Marshal(map[string]string{"url": "https://example.com/other"})
	rr = testRequest(t, srv, http.MethodPut, "/api/updates/"+id3, bytes.NewReader(url), nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("put: %d %s", rr.Code, rr.Body.String())
	}
	st = feedState(t, srv, id3)
	if st.URL != "https://example.com/other" {
		t.Errorf("url not updated: %q", st.URL)
	}
	if st.LastETag != "" {
		t.Errorf("etag survived url edit: %q", st.LastETag)
	}
	if st.LastError != "" {
		t.Errorf("last_error survived url edit: %q", st.LastError)
	}
}

func TestUpdateFeedMemberDenied(t *testing.T) {
	srv := newTestServer(t)
	member := &db.User{ID: 1, Role: "Member"}
	if _, _, err := srv.coreCreateUpdateFeed(member, "https://x", "receive", "", ""); !forbidden(err) {
		t.Errorf("coreCreateUpdateFeed as Member: got %v, want 403", err)
	}
	if _, err := srv.coreListUpdateFeeds(member); !forbidden(err) {
		t.Errorf("coreListUpdateFeeds as Member: got %v, want 403", err)
	}
	if err := srv.coreUpdateUpdateFeed(member, "x", nil, nil); !forbidden(err) {
		t.Errorf("coreUpdateUpdateFeed as Member: got %v, want 403", err)
	}
	if err := srv.coreDeleteUpdateFeed(member, "x"); !forbidden(err) {
		t.Errorf("coreDeleteUpdateFeed as Member: got %v, want 403", err)
	}
	if _, err := srv.coreBuildArchive(member, "x", nil, nil, "v", nil, nil); !forbidden(err) {
		t.Errorf("coreBuildArchive as Member: got %v, want 403", err)
	}
}

func TestUpdateFeedCreateValidation(t *testing.T) {
	srv := newTestServer(t)
	bad, _ := json.Marshal(map[string]string{"mode": "receive"}) // no url
	rr := testRequest(t, srv, http.MethodPost, "/api/updates", bytes.NewReader(bad), nil)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("receive without url: %d, want 400", rr.Code)
	}
	bad, _ = json.Marshal(map[string]string{"mode": "banana"})
	rr = testRequest(t, srv, http.MethodPost, "/api/updates", bytes.NewReader(bad), nil)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("bad mode: %d, want 400", rr.Code)
	}
}

// ── Check: the two-layer version story ─────────────────────────────

func TestUpdateCheckLayers(t *testing.T) {
	srv := newTestServer(t)
	key := strings.Repeat("cd", 32)
	manifest := &UpdateManifest{Version: "v1", Ops: []UpdateOp{{Action: "update_service_files", Target: "nope"}}}
	archive := makeUpdateArchive(t, key, manifest, nil)

	// Layer 1: transport (ETag) → 304 → up_to_date → omitted. The 304
	// shortcut only applies once a version has been applied; stamp first.
	etagSrv := archiveServer(t, archive, `"v1"`, "")
	id, _ := createUpdateFeed(t, srv, etagSrv.URL, "receive", "etag feed", key)
	ups := checkUpdates(t, srv)
	if len(ups) != 1 || ups[0].ID != id || ups[0].Version != "v1" {
		t.Fatalf("first check = %+v, want one available (id %s, v1)", ups, id)
	}
	// Never-applied feed stays available on 304 (the check→apply sequence
	// depends on it).
	ups = checkUpdates(t, srv)
	if len(ups) != 1 || ups[0].ID != id {
		t.Fatalf("304-before-apply check = %+v, want still available", ups)
	}
	if err := srv.store.StampUpdateFeedApplied(id, "v1"); err != nil {
		t.Fatal(err)
	}
	ups = checkUpdates(t, srv)
	if len(ups) != 0 {
		t.Errorf("check after apply (etag 304) = %+v, want none", ups)
	}
	if feedState(t, srv, id).LastETag != `"v1"` {
		t.Error("etag not cached after first check")
	}

	// Layer 1 via Last-Modified fallback.
	modSrv := archiveServer(t, archive, "", "Wed, 21 Oct 2026 07:28:00 GMT")
	id2, _ := createUpdateFeed(t, srv, modSrv.URL, "receive", "lm feed", key)
	ups = checkUpdates(t, srv)
	if len(ups) != 1 || ups[0].ID != id2 {
		t.Fatalf("last-modified first check = %+v, want id2 available", ups)
	}
	if err := srv.store.StampUpdateFeedApplied(id2, "v1"); err != nil {
		t.Fatal(err)
	}
	ups = checkUpdates(t, srv)
	if len(ups) != 0 {
		t.Errorf("last-modified second check (304) = %+v, want none", ups)
	}

	// Layer 2: semantic. No conditional headers at all — the remote always
	// returns 200 — so only the version comparison can decide.
	noCondSrv := archiveServer(t, archive, "", "")
	id3, _ := createUpdateFeed(t, srv, noCondSrv.URL, "receive", "sem feed", key)
	ups = checkUpdates(t, srv)
	if len(ups) != 1 || ups[0].ID != id3 {
		t.Fatalf("semantic first check = %+v, want id3 available", ups)
	}
	if err := srv.store.StampUpdateFeedApplied(id3, "v1"); err != nil {
		t.Fatal(err)
	}
	ups = checkUpdates(t, srv)
	if len(ups) != 0 {
		t.Errorf("semantic second check (version equal) = %+v, want none", ups)
	}
}

// The vanishing-update hole: a version discovered but never applied must
// stay available across 304s, or /check goes quiet about it until the
// publisher ships again. Pending-ness is seen-vs-applied, not
// applied-vs-empty.
func TestUpdateCheckPendingSurvives304(t *testing.T) {
	srv := newTestServer(t)
	key := strings.Repeat("cd", 32)

	// A publisher that can ship a new version mid-test.
	data := []byte(nil)
	manifest := &UpdateManifest{Version: "v1"}
	data = makeUpdateArchive(t, key, manifest, nil)
	etag := "\"v1\""
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		w.Write(data)
	}))
	t.Cleanup(ts.Close)

	id, _ := createUpdateFeed(t, srv, ts.URL, "receive", "pend", key)

	// Discover v1 but never apply it. A later 304 must keep reporting it.
	ups := checkUpdates(t, srv)
	if len(ups) != 1 || ups[0].Version != "v1" {
		t.Fatalf("first check = %+v, want v1 available", ups)
	}
	ups = checkUpdates(t, srv) // 304 now
	if len(ups) != 1 || ups[0].Version != "v1" {
		t.Fatalf("304 before apply = %+v, want v1 still available", ups)
	}

	// Apply, then publisher ships v2. Check discovers it; nobody applies;
	// the next 304 must keep reporting v2 — this is the exact hole.
	if err := srv.store.StampUpdateFeedApplied(id, "v1"); err != nil {
		t.Fatal(err)
	}
	manifest.Version = "v2"
	data = makeUpdateArchive(t, key, manifest, nil)
	etag = "\"v2\""
	ups = checkUpdates(t, srv)
	if len(ups) != 1 || ups[0].Version != "v2" {
		t.Fatalf("check after v2 ships = %+v, want v2 available", ups)
	}
	ups = checkUpdates(t, srv) // 304, v2 seen but not applied
	if len(ups) != 1 || ups[0].Version != "v2" {
		t.Fatalf("304 with pending v2 = %+v, want v2 still available", ups)
	}

	// Once applied, the same 304 goes quiet.
	if err := srv.store.StampUpdateFeedApplied(id, "v2"); err != nil {
		t.Fatal(err)
	}
	ups = checkUpdates(t, srv)
	if len(ups) != 0 {
		t.Errorf("304 after apply = %+v, want none", ups)
	}
}

func TestUpdateCheckFailuresOmittedAndRecorded(t *testing.T) {
	srv := newTestServer(t)

	// Dead URL: omitted from results, error recorded on the row.
	id, _ := createUpdateFeed(t, srv, "http://127.0.0.1:1/dead.tar.gz.enc", "receive", "dead", "")
	ups := checkUpdates(t, srv)
	if len(ups) != 0 {
		t.Errorf("check with dead feed = %+v, want none", ups)
	}
	st := feedState(t, srv, id)
	if st.LastError == "" || st.LastErrorAt == nil {
		t.Errorf("dead feed last_error = %q, want recorded", st.LastError)
	}

	// Undecryptable bytes: same treatment.
	ts := archiveServer(t, []byte("garbage-not-an-archive"), "", "")
	id2, _ := createUpdateFeed(t, srv, ts.URL, "receive", "garbage", "")
	ups = checkUpdates(t, srv)
	if len(ups) != 0 {
		t.Errorf("check with garbage feed = %+v, want none", ups)
	}
	if feedState(t, srv, id2).LastError == "" {
		t.Error("garbage feed last_error not recorded")
	}
}

func TestUpdateCheckAnonymous(t *testing.T) {
	srv := newTestServer(t)
	// No Authorization header anywhere — the whole point of the flat mounts.
	rr := testRequest(t, srv, http.MethodGet, "/api/updates/check", nil, nil)
	if rr.Code == http.StatusUnauthorized || rr.Code == http.StatusForbidden {
		t.Fatalf("anonymous check denied: %d", rr.Code)
	}
}

// ── Apply ───────────────────────────────────────────────────────────

func TestUpdateApplyHappyPath(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "applyhappy")
	cleanupSlotApp(t, srv, nonce)

	manifest := &UpdateManifest{
		Version: "2026.08.24-1",
		Ops:     []UpdateOp{{Action: "update_app_files", Target: nonce}}, // source + slot defaulted
	}
	key := strings.Repeat("ef", 32)
	archive := makeUpdateArchive(t, key, manifest, map[string][]byte{
		"apps/" + nonce + "/index.html": []byte("<h1>updated</h1>"),
		"apps/" + nonce + "/css/x.css":  []byte("body{}"),
	})
	ts := archiveServer(t, archive, `"v1"`, "")
	id, _ := createUpdateFeed(t, srv, ts.URL, "receive", "happy", key)

	events := applyUpdates(t, srv, `{"ids":["`+id+`"]}`)

	// Expected sequence: fetch, decrypt, validate, op start/done, done.
	var names []string
	for _, e := range events {
		if e.Event == "op" {
			names = append(names, fmt.Sprintf("op:%v:%v", e.Data["index"], e.Data["status"]))
		} else {
			names = append(names, e.Event)
		}
	}
	want := []string{"fetch", "decrypt", "validate", "op:0:start", "op:0:done", "done", "summary"}
	if fmt.Sprint(names) != fmt.Sprint(want) {
		t.Errorf("event sequence = %v, want %v", names, want)
	}
	done := events[len(events)-2]
	if done.Data["version"] != "2026.08.24-1" {
		t.Errorf("done version = %v", done.Data["version"])
	}
	summary := events[len(events)-1]
	if summary.Data["applied"] != float64(1) {
		t.Errorf("summary = %v", summary.Data)
	}

	// Files landed in staging.
	for _, p := range []string{"index.html", "css/x.css"} {
		if _, err := os.Stat(filepath.Join(srv.config.DataDir, "apps", nonce, "staging", p)); err != nil {
			t.Errorf("staging/%s missing: %v", p, err)
		}
	}
	st := feedState(t, srv, id)
	if st.LastAppliedVersion != "2026.08.24-1" || st.LastAppliedAt == nil {
		t.Errorf("version not stamped: %+v", st)
	}
	if st.LastError != "" {
		t.Errorf("last_error not cleared on success: %q", st.LastError)
	}

	// Idempotent re-apply: the re-check says up-to-date → skip, no stamp churn.
	events = applyUpdates(t, srv, `{"ids":["`+id+`"]}`)
	if events[0].Event != "skip" {
		t.Errorf("re-apply first event = %q, want skip", events[0].Event)
	}
}

func TestUpdateApplyAllAndSkip(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "applyall")
	cleanupSlotApp(t, srv, nonce)

	key := strings.Repeat("11", 32)
	mk := func(v string) *httptest.Server {
		m := &UpdateManifest{Version: v, Ops: []UpdateOp{{Action: "update_app_files", Target: nonce}}}
		a := makeUpdateArchive(t, key, m, map[string][]byte{
			"apps/" + nonce + "/index.html": []byte("v:" + v),
		})
		return archiveServer(t, a, `"`+v+`"`, "")
	}
	ts1, ts2 := mk("v1"), mk("v1")
	createUpdateFeed(t, srv, ts1.URL, "receive", "one", key)
	createUpdateFeed(t, srv, ts2.URL, "receive", "two", key)

	// Empty ids → applies all receive feeds with an update available.
	events := applyUpdates(t, srv, `{}`)
	summary := events[len(events)-1]
	if summary.Data["applied"] != float64(2) {
		t.Errorf("summary applied = %v, want 2; events = %+v", summary.Data["applied"], events)
	}
	// One done event per feed, each namespaced with its id.
	var doneIDs []string
	for _, e := range events {
		if e.Event == "done" {
			doneIDs = append(doneIDs, fmt.Sprint(e.Data["id"]))
		}
	}
	if len(doneIDs) != 2 {
		t.Errorf("done events = %v", doneIDs)
	}

	// Both up-to-date now: re-apply skips both.
	events = applyUpdates(t, srv, `{}`)
	summary = events[len(events)-1]
	if summary.Data["skipped"] != float64(2) || summary.Data["applied"] != float64(0) {
		t.Errorf("re-apply summary = %v, want 2 skipped 0 applied", summary.Data)
	}
}

func TestUpdateApplyRecheckGuard(t *testing.T) {
	srv := newTestServer(t)
	// Feed was up-to-date between /check and /apply → skip, no fetch.
	// A feed stamped with the current version simulates exactly that.
	nonce := createApp(t, srv, "applyguard")
	cleanupSlotApp(t, srv, nonce)
	m := &UpdateManifest{Version: "v1", Ops: []UpdateOp{{Action: "update_app_files", Target: nonce}}}
	archive := makeUpdateArchive(t, strings.Repeat("22", 32), m, map[string][]byte{
		"apps/" + nonce + "/index.html": []byte("x"),
	})
	ts := archiveServer(t, archive, "", "")
	id, _ := createUpdateFeed(t, srv, ts.URL, "receive", "", strings.Repeat("22", 32))
	if err := srv.store.StampUpdateFeedApplied(id, "v1"); err != nil {
		t.Fatal(err)
	}
	events := applyUpdates(t, srv, `{"ids":["`+id+`"]}`)
	if events[0].Event != "skip" {
		t.Errorf("first event = %q, want skip (already applied)", events[0].Event)
	}
	if st := feedState(t, srv, id); st.LastAppliedAt == nil {
		t.Error("skip should leave the old stamp alone")
	}
}

func TestUpdateApplyValidationFailures(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "applyval")
	cleanupSlotApp(t, srv, nonce)
	registerService(t, srv, "vals", "tasks://vals", db.ServiceDescriptor{Type: "tasks"})
	t.Cleanup(func() { os.Remove(filepath.Join(srv.config.DataDir, "tasks", "vals.txt")) })
	registerService(t, srv, "valsapi", "https://api.example.com", db.ServiceDescriptor{Type: "api"})

	key := strings.Repeat("33", 32)
	files := func() map[string][]byte {
		return map[string][]byte{
			"apps/" + nonce + "/index.html": []byte("x"),
			"services/vals.txt":             []byte("def"),
		}
	}
	cases := []struct {
		name string
		ops  []UpdateOp
		want string
	}{
		{"unknown app", []UpdateOp{{Action: "update_app_files", Target: "no-such-app"}}, "app not found"},
		{"unknown service", []UpdateOp{{Action: "update_service_files", Target: "no-such-svc"}}, "service not found"},
		{"wrong service type", []UpdateOp{{Action: "update_service_files", Target: "valsapi"}}, "does not support file publishing"},
		{"missing source", []UpdateOp{{Action: "update_app_files", Target: nonce, Source: "apps/ghost"}}, "not found in archive"},
		{"slot not staging", []UpdateOp{{Action: "update_app_files", Target: nonce, Slot: "production"}}, "staging only"},
		{"unknown action", []UpdateOp{{Action: "delete_everything"}}, "unknown action"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &UpdateManifest{Version: "v1", Ops: tc.ops}
			ts := archiveServer(t, makeUpdateArchive(t, key, m, files()), "", "")
			id, _ := createUpdateFeed(t, srv, ts.URL, "receive", "", key)
			events := applyUpdates(t, srv, `{"ids":["`+id+`"]}`)
			var ferr *sseEvent
			for i := range events {
				if events[i].Event == "feed_error" {
					ferr = &events[i]
				}
			}
			if ferr == nil {
				t.Fatalf("no feed_error event; events = %+v", events)
			}
			if ferr.Data["step"] != "validate" {
				t.Errorf("feed_error step = %v, want validate", ferr.Data["step"])
			}
			if !strings.Contains(fmt.Sprint(ferr.Data["message"]), tc.want) {
				t.Errorf("feed_error message = %v, want it to contain %q", ferr.Data["message"], tc.want)
			}
			st := feedState(t, srv, id)
			if st.LastAppliedVersion != "" {
				t.Error("version stamped on validation failure")
			}
			if st.LastError == "" {
				t.Error("last_error not recorded on validation failure")
			}
		})
	}
}

func TestUpdateApplyDecryptFailure(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "applydecrypt")
	cleanupSlotApp(t, srv, nonce)

	// Archive encrypted with a DIFFERENT key than the feed's.
	m := &UpdateManifest{Version: "v1", Ops: []UpdateOp{{Action: "update_app_files", Target: nonce}}}
	archive := makeUpdateArchive(t, strings.Repeat("44", 32), m, map[string][]byte{
		"apps/" + nonce + "/index.html": []byte("x"),
	})
	ts := archiveServer(t, archive, "", "")
	id, _ := createUpdateFeed(t, srv, ts.URL, "receive", "", strings.Repeat("ef", 32))

	events := applyUpdates(t, srv, `{"ids":["`+id+`"]}`)
	// The re-check fails at decrypt (the archive is sealed with a different
	// key), so the error surfaces from the check step — before any fetch
	// event. A decrypt failure anywhere aborts safely with no stamp.
	if events[0].Event != "feed_error" {
		t.Fatalf("events = %v", events)
	}
	if !strings.Contains(fmt.Sprint(events[0].Data["message"]), "decrypt") {
		t.Errorf("feed_error message = %v, want decrypt failure", events[0].Data["message"])
	}
	st := feedState(t, srv, id)
	if st.LastAppliedVersion != "" {
		t.Error("version stamped on decrypt failure")
	}
	// A re-trigger is safe: still fails at decrypt, never silently skips.
	events = applyUpdates(t, srv, `{"ids":["`+id+`"]}`)
	if events[0].Event != "feed_error" {
		t.Errorf("re-trigger events = %v, want another feed_error", events)
	}
}

func TestUpdateApplyPartialFailureOtherFeedsContinue(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "applypartial")
	cleanupSlotApp(t, srv, nonce)
	registerService(t, srv, "ptsvc", "tasks://ptsvc", db.ServiceDescriptor{Type: "tasks"})
	t.Cleanup(func() { os.Remove(filepath.Join(srv.config.DataDir, "tasks", "ptsvc.txt")) })

	// Feed A: op 0 lands, op 1 fails mid-execution — its source is a
	// directory, which passes validation (stat ok) but fails at ReadFile.
	mBad := &UpdateManifest{
		Version: "v1",
		Ops: []UpdateOp{
			{Action: "update_app_files", Target: nonce},
			{Action: "update_service_files", Target: "ptsvc", Source: "apps/" + nonce},
		},
	}
	// Feed B: clean and applies fine.
	mGood := &UpdateManifest{
		Version: "v1",
		Ops:     []UpdateOp{{Action: "update_service_files", Target: "ptsvc"}},
	}
	files := map[string][]byte{
		"apps/" + nonce + "/index.html": []byte("partial"),
		"services/ptsvc.txt":            []byte("good def"),
	}
	tsBad := archiveServer(t, makeUpdateArchive(t, strings.Repeat("55", 32), mBad, files), "", "")
	tsGood := archiveServer(t, makeUpdateArchive(t, strings.Repeat("66", 32), mGood, files), "", "")
	idBad, _ := createUpdateFeed(t, srv, tsBad.URL, "receive", "bad", strings.Repeat("55", 32))
	idGood, _ := createUpdateFeed(t, srv, tsGood.URL, "receive", "good", strings.Repeat("66", 32))

	events := applyUpdates(t, srv, `{}`)
	summary := events[len(events)-1]
	if summary.Data["applied"] != float64(1) || summary.Data["failed"] != float64(1) {
		t.Errorf("summary = %v, want applied=1 failed=1", summary.Data)
	}

	// Bad feed: op 0 is on disk (partial apply), version NOT stamped,
	// last_error recorded — retriable from op 0, idempotent.
	if _, err := os.Stat(filepath.Join(srv.config.DataDir, "apps", nonce, "staging", "index.html")); err != nil {
		t.Errorf("partial apply: op 0 should be on disk: %v", err)
	}
	stBad := feedState(t, srv, idBad)
	if stBad.LastAppliedVersion != "" {
		t.Error("partial failure stamped the version")
	}
	if stBad.LastError == "" {
		t.Error("partial failure did not record last_error")
	}
	// Good feed applied fully.
	if got := readFileOr(t, filepath.Join(srv.config.DataDir, "tasks", "ptsvc.txt")); got != "good def" {
		t.Errorf("ptsvc.txt = %q, want the good feed's definition", got)
	}
	stGood := feedState(t, srv, idGood)
	if stGood.LastAppliedVersion != "v1" {
		t.Errorf("good feed not stamped: %+v", stGood)
	}
}

func TestUpdateApplyFetchFailureAndSelfHealing(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "applyheal")
	cleanupSlotApp(t, srv, nonce)

	m := &UpdateManifest{Version: "v1", Ops: []UpdateOp{{Action: "update_app_files", Target: nonce}}}
	files := map[string][]byte{"apps/" + nonce + "/index.html": []byte("healed")}
	goodArchive := makeUpdateArchive(t, strings.Repeat("77", 32), m, files)

	// A host that starts broken and gets fixed — the self-healing story.
	var serveGood bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !serveGood {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write(goodArchive)
	}))
	t.Cleanup(ts.Close)

	id, _ := createUpdateFeed(t, srv, ts.URL, "receive", "", strings.Repeat("77", 32))
	events := applyUpdates(t, srv, `{"ids":["`+id+`"]}`)
	if events[0].Event != "feed_error" {
		t.Fatalf("events = %+v, want fetch feed_error", events)
	}
	st := feedState(t, srv, id)
	if st.LastError == "" {
		t.Error("fetch failure did not record last_error")
	}

	// Host fixed → apply succeeds → last_error clears.
	serveGood = true
	events = applyUpdates(t, srv, `{"ids":["`+id+`"]}`)
	if events[len(events)-2].Event != "done" {
		t.Fatalf("healed apply events = %+v", events)
	}
	st = feedState(t, srv, id)
	if st.LastError != "" || st.LastErrorAt != nil {
		t.Errorf("last_error not cleared after success: %q", st.LastError)
	}
	if got := readFileOr(t, filepath.Join(srv.config.DataDir, "apps", nonce, "staging", "index.html")); got != "healed" {
		t.Errorf("staging content = %q, want healed", got)
	}
}

func TestUpdateApplyUnknownIDsSkippedSilently(t *testing.T) {
	srv := newTestServer(t)
	events := applyUpdates(t, srv, `{"ids":["totally-bogus"]}`)
	summary := events[len(events)-1]
	if summary.Data["applied"] != float64(0) || summary.Data["failed"] != float64(0) || summary.Data["skipped"] != float64(0) {
		t.Errorf("summary = %v, want all-zero for unknown ids", summary.Data)
	}
}

func TestUpdateRateLimit(t *testing.T) {
	srv := newTestServer(t)
	for i := 0; i < updateCheckRate; i++ {
		rr := testRequest(t, srv, http.MethodGet, "/api/updates/check", nil, nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("check %d: %d", i+1, rr.Code)
		}
	}
	rr := testRequest(t, srv, http.MethodGet, "/api/updates/check", nil, nil)
	if rr.Code != http.StatusTooManyRequests {
		t.Errorf("check %d: %d, want 429", updateCheckRate+1, rr.Code)
	}
}

func readFileOr(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// ── Build + round-trip ──────────────────────────────────────────────

func TestUpdateBuild(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "buildapp")
	cleanupSlotApp(t, srv, nonce)
	registerService(t, srv, "buildsvc", "tasks://buildsvc", db.ServiceDescriptor{Type: "tasks"})
	t.Cleanup(func() { os.Remove(filepath.Join(srv.config.DataDir, "tasks", "buildsvc.txt")) })
	if err := os.MkdirAll(filepath.Join(srv.config.DataDir, "tasks"), 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(srv.config.DataDir, "tasks", "buildsvc.txt"), []byte("svc def"), 0644)

	// No staging yet → build falls back to dev and says so.
	createSlotFile(t, srv, nonce, "web", "index.html", []byte("built from dev"))

	id, key := createUpdateFeed(t, srv, "https://selfhost.example/feed.enc", "publish", "my feed", "")
	body := map[string]interface{}{"apps": []string{nonce}, "services": []string{"buildsvc"}, "version": "2026.09.01"}
	b, _ := json.Marshal(body)
	rr := testRequest(t, srv, http.MethodPost, "/api/updates/"+id+"/build", bytes.NewReader(b), nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("build: %d %s", rr.Code, rr.Body.String())
	}
	events := parseSSE(rr.Body.String())
	var seq []string
	var downloadURL string
	for _, e := range events {
		if e.Event == "app" || e.Event == "service" {
			seq = append(seq, e.Event)
		} else {
			seq = append(seq, e.Event)
		}
		if e.Event == "app" && e.Data["source_slot"] != "dev" {
			t.Errorf("app event source_slot = %v, want dev (staging absent)", e.Data["source_slot"])
		}
		if e.Event == "done" {
			downloadURL = fmt.Sprint(e.Data["download_url"])
		}
	}
	want := []string{"collect", "app", "service", "manifest", "encrypt", "done"}
	if fmt.Sprint(seq) != fmt.Sprint(want) {
		t.Errorf("build sequence = %v, want %v", seq, want)
	}
	if downloadURL == "" {
		t.Fatal("done event carried no download_url")
	}

	// Download is one-shot and admin-gated by mount; grab the bytes.
	rr = testRequest(t, srv, http.MethodGet, downloadURL, nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("archive download: %d %s", rr.Code, rr.Body.String())
	}
	archive := rr.Body.Bytes()
	rr = testRequest(t, srv, http.MethodGet, downloadURL, nil, nil)
	if rr.Code != http.StatusGone {
		t.Errorf("second download: %d, want 410 (one-shot)", rr.Code)
	}

	// The archive is decryptable with the feed key and its manifest matches.
	keyBytes, _ := hexDecode(key)
	manifest, err := readArchiveManifest(keyBytes, archive)
	if err != nil {
		t.Fatalf("readArchiveManifest: %v", err)
	}
	if manifest.Version != "2026.09.01" || len(manifest.Ops) != 2 {
		t.Errorf("manifest = %+v", manifest)
	}

	// Build on a receive feed is rejected.
	rxID, _ := createUpdateFeed(t, srv, "https://x", "receive", "", "")
	rr = testRequest(t, srv, http.MethodPost, "/api/updates/"+rxID+"/build", strings.NewReader(`{}`), nil)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("build on receive feed: %d, want 400", rr.Code)
	}
}

// TestUpdateRoundTrip is the load-bearing one: build on a publish feed,
// serve the bytes over HTTP, point a receive feed at them (registered with
// the publisher's key), check, apply, assert disk state. Proves the two
// modes are the same shape on the wire.
func TestUpdateRoundTrip(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "rtapp")
	cleanupSlotApp(t, srv, nonce)
	registerService(t, srv, "rtsvc", "tasks://rtsvc", db.ServiceDescriptor{Type: "tasks"})
	t.Cleanup(func() { os.Remove(filepath.Join(srv.config.DataDir, "tasks", "rtsvc.txt")) })

	// Publisher's app lives in dev (no staging yet → build falls back),
	// service definition on disk.
	createSlotFile(t, srv, nonce, "web", "index.html", []byte("<h1>round trip</h1>"))
	createSlotFile(t, srv, nonce, "web", "js/app.js", []byte("console.log(1)"))
	if err := os.MkdirAll(filepath.Join(srv.config.DataDir, "tasks"), 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(srv.config.DataDir, "tasks", "rtsvc.txt"), []byte("old def"), 0644)

	// 1. Build on a publish feed.
	pubID, pubKey := createUpdateFeed(t, srv, "https://selfhost.example/rt.enc", "publish", "rt publisher", "")
	b, _ := json.Marshal(map[string]interface{}{"apps": []string{nonce}, "services": []string{"rtsvc"}, "version": "rt-1"})
	rr := testRequest(t, srv, http.MethodPost, "/api/updates/"+pubID+"/build", bytes.NewReader(b), nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("build: %d %s", rr.Code, rr.Body.String())
	}
	var downloadURL string
	for _, e := range parseSSE(rr.Body.String()) {
		if e.Event == "done" {
			downloadURL = fmt.Sprint(e.Data["download_url"])
		}
	}
	rr = testRequest(t, srv, http.MethodGet, downloadURL, nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("download: %d", rr.Code)
	}
	archive := rr.Body.Bytes()

	// 2. Self-host the bytes.
	ts := archiveServer(t, archive, `"rt-1"`, "")

	// 3. Point a receive feed at it, registered with the publisher's key.
	rxID, _ := createUpdateFeed(t, srv, ts.URL, "receive", "rt receiver", pubKey)

	// 4. Check reports it.
	ups := checkUpdates(t, srv)
	found := false
	for _, u := range ups {
		if u.ID == rxID {
			found = true
			if u.Version != "rt-1" {
				t.Errorf("check version = %q, want rt-1", u.Version)
			}
		}
	}
	if !found {
		t.Fatalf("check = %+v, want rx feed available", ups)
	}

	// 5. Apply → staging slot + service definition updated, version stamped.
	// Tamper with the service definition first so the restore is observable.
	os.WriteFile(filepath.Join(srv.config.DataDir, "tasks", "rtsvc.txt"), []byte("tampered"), 0644)
	events := applyUpdates(t, srv, `{"ids":["`+rxID+`"]}`)
	if events[len(events)-2].Event != "done" {
		t.Fatalf("apply events = %+v", events)
	}
	if got := readFileOr(t, filepath.Join(srv.config.DataDir, "apps", nonce, "staging", "index.html")); got != "<h1>round trip</h1>" {
		t.Errorf("staging index.html = %q", got)
	}
	if got := readFileOr(t, filepath.Join(srv.config.DataDir, "apps", nonce, "staging", "js/app.js")); got != "console.log(1)" {
		t.Errorf("staging js/app.js = %q", got)
	}
	if got := readFileOr(t, filepath.Join(srv.config.DataDir, "tasks", "rtsvc.txt")); got != "old def" {
		t.Errorf("tasks/rtsvc.txt = %q", got)
	}
	st := feedState(t, srv, rxID)
	if st.LastAppliedVersion != "rt-1" || st.LastError != "" {
		t.Errorf("receiver state = %+v", st)
	}
}
