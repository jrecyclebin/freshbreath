package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"poggers.institute/freshbreath/internal/db"
)

// ── Deployment slots ──
//
// Each app has three slots: Development (the web upload folder), Staging, and
// Production, backed by sibling dirs under <DataDir>/apps/<nonce>/. A slot is
// routable iff its dir exists on disk. URL scheme:
//
//	/<slug>@dev      -> web/
//	/<slug>@staging  -> staging/
//	/<slug>@prod     -> production/
//	/<slug>          -> the slot named by the app's Environment (default Development)
//
// Deploy copies one slot dir over another via POST /api/apps/{nonce}/deploy.

func createSlotFile(t *testing.T, srv *Server, nonce, slotDir, path string, content []byte) {
	t.Helper()
	fullPath := filepath.Join(srv.config.DataDir, "apps", nonce, slotDir, path)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(fullPath, content, 0644); err != nil {
		t.Fatalf("write slot file: %v", err)
	}
}

// cleanupSlotApp removes the on-disk app dir when the test ends.
func cleanupSlotApp(t *testing.T, srv *Server, nonce string) {
	t.Helper()
	t.Cleanup(func() { os.RemoveAll(filepath.Join(srv.config.DataDir, "apps", nonce)) })
}

func deploySlot(t *testing.T, srv *Server, nonce, body string) *httptest.ResponseRecorder {
	t.Helper()
	return testRequest(t, srv, http.MethodPost, "/api/apps/"+nonce+"/deploy", strings.NewReader(body), nil)
}

func TestDeployToStaging(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "deploystg")
	cleanupSlotApp(t, srv, nonce)
	createSlotFile(t, srv, nonce, "web", "index.html", []byte("<h1>dev</h1>"))
	createSlotFile(t, srv, nonce, "web", "css/site.css", []byte("body{}"))

	rr := deploySlot(t, srv, nonce, `{"target":"staging"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rr.Code, rr.Body.String())
	}
	var res map[string]string
	json.Unmarshal(rr.Body.Bytes(), &res)
	if res["route"] != "/deploystg@staging" {
		t.Errorf("route = %q, want /deploystg@staging", res["route"])
	}

	// The full tree landed in the staging dir.
	for _, p := range []string{"index.html", "css/site.css"} {
		if _, err := os.Stat(filepath.Join(srv.config.DataDir, "apps", nonce, "staging", p)); err != nil {
			t.Errorf("staging/%s missing after deploy: %v", p, err)
		}
	}

	// Deploy timestamp recorded.
	app, err := srv.store.GetApp(nonce)
	if err != nil {
		t.Fatalf("GetApp: %v", err)
	}
	if app.Details == nil || app.Details.LastDeployedStaging == nil {
		t.Error("expected last_deployed_staging to be set")
	}
}

func TestDeployToProduction(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "deployprod")
	cleanupSlotApp(t, srv, nonce)
	createSlotFile(t, srv, nonce, "web", "index.html", []byte("<h1>dev</h1>"))

	rr := deploySlot(t, srv, nonce, `{"target":"prod"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rr.Code, rr.Body.String())
	}
	var res map[string]string
	json.Unmarshal(rr.Body.Bytes(), &res)
	if res["route"] != "/deployprod@prod" {
		t.Errorf("route = %q, want /deployprod@prod", res["route"])
	}
	app, _ := srv.store.GetApp(nonce)
	if app.Details == nil || app.Details.LastDeployedProduction == nil {
		t.Error("expected last_deployed_production to be set")
	}
}

// TestDeployFromStaging pins the promote-what-you-tested flow: staging can be
// copied straight to prod without going back through the dev folder.
func TestDeployFromStaging(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "deploystg2prod")
	cleanupSlotApp(t, srv, nonce)
	createSlotFile(t, srv, nonce, "web", "index.html", []byte("dev"))
	createSlotFile(t, srv, nonce, "staging", "index.html", []byte("staged"))

	rr := deploySlot(t, srv, nonce, `{"source":"staging","target":"prod"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rr.Code, rr.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(srv.config.DataDir, "apps", nonce, "production", "index.html"))
	if err != nil {
		t.Fatalf("read production/index.html: %v", err)
	}
	if string(data) != "staged" {
		t.Errorf("production content = %q, want the staged copy", data)
	}
}

// TestDeployReplacesTarget pins that re-deploying wipes stale files from the
// target slot rather than merging over them.
func TestDeployReplacesTarget(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "deployreplace")
	cleanupSlotApp(t, srv, nonce)
	createSlotFile(t, srv, nonce, "staging", "old.html", []byte("old"))
	createSlotFile(t, srv, nonce, "web", "index.html", []byte("new"))

	rr := deploySlot(t, srv, nonce, `{"target":"staging"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(filepath.Join(srv.config.DataDir, "apps", nonce, "staging", "old.html")); !os.IsNotExist(err) {
		t.Error("stale file survived redeploy; target should be replaced, not merged")
	}
}

func TestDeployValidation(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "deployval")
	cleanupSlotApp(t, srv, nonce)
	createSlotFile(t, srv, nonce, "web", "index.html", []byte("dev"))

	cases := []struct {
		name string
		body string
		want int
	}{
		{"missing target", `{"source":"dev"}`, http.StatusBadRequest},
		{"bogus target", `{"target":"nightly"}`, http.StatusBadRequest},
		{"target dev", `{"target":"dev"}`, http.StatusBadRequest},
		{"bogus source", `{"source":"nightly","target":"prod"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := deploySlot(t, srv, nonce, tc.body)
			if rr.Code != tc.want {
				t.Errorf("status = %d, want %d; body=%q", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

func TestDeployEmptySource(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "deployempty")
	cleanupSlotApp(t, srv, nonce)

	rr := deploySlot(t, srv, nonce, `{"target":"staging"}`)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for empty dev slot; body=%q", rr.Code, rr.Body.String())
	}
}

func TestDeployMethodNotAllowed(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "deploymethod")
	rr := testRequest(t, srv, http.MethodGet, "/api/apps/"+nonce+"/deploy", nil, nil)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}

func TestHostedAppSlotRouting(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "slotapp")
	cleanupSlotApp(t, srv, nonce)
	createSlotFile(t, srv, nonce, "web", "index.html", []byte("dev"))
	createSlotFile(t, srv, nonce, "web", "app.js", []byte("dev-js"))
	createSlotFile(t, srv, nonce, "staging", "index.html", []byte("staged"))
	createSlotFile(t, srv, nonce, "production", "index.html", []byte("live"))
	srv.rebuildHostedRoutes()

	cases := []struct {
		path string
		want string
	}{
		{"/slotapp@dev/", "dev"},
		{"/slotapp@dev/app.js", "dev-js"},
		{"/slotapp@staging/", "staged"},
		{"/slotapp@prod/", "live"},
		{"/slotapp/", "dev"}, // Environment unset -> Development default
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			rr := testRequest(t, srv, http.MethodGet, tc.path, nil, nil)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rr.Code)
			}
			if rr.Body.String() != tc.want {
				t.Errorf("body = %q, want %q", rr.Body.String(), tc.want)
			}
		})
	}
}

// TestHostedAppSlotSlashRedirect pins that /slug@slot gets the same trailing-
// slash redirect as /slug so relative asset paths resolve.
func TestHostedAppSlotSlashRedirect(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "slotredir")
	cleanupSlotApp(t, srv, nonce)
	createSlotFile(t, srv, nonce, "staging", "index.html", []byte("staged"))
	srv.rebuildHostedRoutes()

	rr := testRequest(t, srv, http.MethodGet, "/slotredir@staging", nil, nil)
	if rr.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d, want 301", rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != "/slotredir@staging/" {
		t.Errorf("Location = %q, want /slotredir@staging/", loc)
	}
}

// TestHostedAppDefaultSlotFollowsEnvironment pins that the bare /<slug> path
// serves the slot named by the app's Environment setting, and that flipping
// the setting (via the app PUT) retargets it.
func TestHostedAppDefaultSlotFollowsEnvironment(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "envapp")
	cleanupSlotApp(t, srv, nonce)
	createSlotFile(t, srv, nonce, "web", "index.html", []byte("dev"))
	createSlotFile(t, srv, nonce, "staging", "index.html", []byte("staged"))
	createSlotFile(t, srv, nonce, "production", "index.html", []byte("live"))
	srv.rebuildHostedRoutes()

	setEnv := func(env string) {
		t.Helper()
		body := `{"name":"envapp","environment":"` + env + `","url":""}`
		rr := testRequest(t, srv, http.MethodPut, "/api/apps/"+nonce, strings.NewReader(body), nil)
		if rr.Code != http.StatusNoContent {
			t.Fatalf("PUT env=%s: status = %d, want 204; body=%q", env, rr.Code, rr.Body.String())
		}
	}

	cases := []struct {
		env  string
		want string
	}{
		{"Development", "dev"},
		{"Staging", "staged"},
		{"Production", "live"},
	}
	for _, tc := range cases {
		t.Run(tc.env, func(t *testing.T) {
			setEnv(tc.env)
			rr := testRequest(t, srv, http.MethodGet, "/envapp/", nil, nil)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rr.Code)
			}
			if rr.Body.String() != tc.want {
				t.Errorf("bare path with env=%s served %q, want %q", tc.env, rr.Body.String(), tc.want)
			}
		})
	}
}

func TestHostedAppUndeployedSlot404(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "undeployed")
	cleanupSlotApp(t, srv, nonce)
	createSlotFile(t, srv, nonce, "web", "index.html", []byte("dev"))
	srv.rebuildHostedRoutes()

	// Explicit slot that has no dir on disk.
	rr := testRequest(t, srv, http.MethodGet, "/undeployed@prod/", nil, nil)
	if rr.Code != http.StatusNotFound {
		t.Errorf("@prod status = %d, want 404 (no fallback to dev)", rr.Code)
	}

	// Bare path pointing at an undeployed default slot also 404s.
	if _, err := srv.store.GetApp(nonce); err != nil {
		t.Fatalf("GetApp: %v", err)
	}
	if err := srv.store.UpdateApp(nonce, "undeployed", "Production", "", nil); err != nil {
		t.Fatalf("UpdateApp: %v", err)
	}
	srv.rebuildHostedRoutes()
	rr = testRequest(t, srv, http.MethodGet, "/undeployed/", nil, nil)
	if rr.Code != http.StatusNotFound {
		t.Errorf("bare path status = %d, want 404 (env=Production, prod undeployed)", rr.Code)
	}
}

func TestHostedAppUnknownSlot404(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "unknownslot")
	cleanupSlotApp(t, srv, nonce)
	createSlotFile(t, srv, nonce, "web", "index.html", []byte("dev"))
	srv.rebuildHostedRoutes()

	rr := testRequest(t, srv, http.MethodGet, "/unknownslot@nightly/", nil, nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for unknown slot", rr.Code)
	}
}

// TestHostedAppRoutabilityByDisk pins that disk, not the details blob, decides
// routability: an app whose dirs were removed 404s even with a last_uploaded
// timestamp, and an app with files but empty details still serves.
func TestHostedAppRoutabilityByDisk(t *testing.T) {
	srv := newTestServer(t)

	// Details claim an upload but there is no dir on disk.
	ghost := createApp(t, srv, "ghostapp")
	cleanupSlotApp(t, srv, ghost)
	now := time.Now().UTC()
	if err := srv.store.UpdateAppDetails(ghost, &db.AppDetails{LastUploaded: &now}); err != nil {
		t.Fatalf("UpdateAppDetails: %v", err)
	}

	// Files on disk but details blob empty (never went through the upload API).
	bare := createApp(t, srv, "bareapp")
	cleanupSlotApp(t, srv, bare)
	createSlotFile(t, srv, bare, "web", "index.html", []byte("bare"))

	srv.rebuildHostedRoutes()

	rr := testRequest(t, srv, http.MethodGet, "/ghostapp/", nil, nil)
	if rr.Code != http.StatusNotFound {
		t.Errorf("ghostapp status = %d, want 404 (no slot dirs on disk)", rr.Code)
	}
	rr = testRequest(t, srv, http.MethodGet, "/bareapp/", nil, nil)
	if rr.Code != http.StatusOK || rr.Body.String() != "bare" {
		t.Errorf("bareapp status = %d body = %q, want 200 bare (disk is truth)", rr.Code, rr.Body.String())
	}
}
