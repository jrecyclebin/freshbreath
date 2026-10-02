//go:build e2e

// Package e2e drives a real Fresh Breath server end to end: services and
// auth records set up over the admin API, an uploaded app exercised in
// headless Chromium, and MCP clients that log in through the browser the
// way Claude Desktop would. Upstream providers are in-process fakes (see
// fakes_test.go), so the suite needs no accounts and no network.
//
// Run with `mise run e2e`, or:
//
//	go test -tags e2e,sqlite_fts5 ./e2e/
//
// Chromium is found on PATH the way chromedp looks for it; set
// FRBR_E2E_CHROME to point at a specific binary.
package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	_ "github.com/mattn/go-sqlite3"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"poggers.institute/freshbreath/internal/db"
	"poggers.institute/freshbreath/internal/server"
	"poggers.institute/freshbreath/internal/sshkit"
)

// ── Fresh Breath ────────────────────────────────────────────────────

// freshbreath is one running server, fresh for each test: its own data
// directory, database and signing key, serving on a loopback port.
type freshbreath struct {
	URL string
	t   *testing.T
}

func startFreshbreath(t *testing.T) *freshbreath {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	baseURL := "http://" + ln.Addr().String()
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()

	sqlDB, err := sql.Open("sqlite3", filepath.Join(dataDir, "freshbreath.db")+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	store := db.NewStore(sqlDB)
	localKey := make([]byte, 32)
	rand.Read(localKey)
	store.SetSealKey(db.DeriveSubkey(localKey, db.SealSubkeyLabel))
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := store.EnsureSSHService(); err != nil {
		t.Fatalf("ssh service: %v", err)
	}

	agentMgr := sshkit.NewAgentManager()
	sessionMgr := sshkit.NewSessionManager(agentMgr, store, time.Hour)
	t.Cleanup(sessionMgr.Stop)
	srv := server.New(server.Config{
		Dir:           repoRoot,
		DataDir:       dataDir,
		DBPath:        filepath.Join(dataDir, "freshbreath.db"),
		PublicBaseURL: baseURL,
	}, store, localKey, agentMgr, sessionMgr, "e2e", "e2e")

	httpSrv := &http.Server{Handler: srv}
	go httpSrv.Serve(ln)
	t.Cleanup(func() { httpSrv.Close() })
	return &freshbreath{URL: baseURL, t: t}
}

// api calls the admin API. A fresh server has no admin gate yet (setup
// mode), so no credential is needed. out may be nil.
func (fb *freshbreath) api(method, path string, body, out any) {
	fb.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, fb.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	fb.do(req, out)
}

// upload posts one file as the multipart "file" field.
func (fb *freshbreath) upload(path, filename string, content []byte, out any) {
	fb.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", filename)
	fw.Write(content)
	mw.Close()
	req, _ := http.NewRequest("POST", fb.URL+path, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	fb.do(req, out)
}

func (fb *freshbreath) do(req *http.Request, out any) {
	fb.t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fb.t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		fb.t.Fatalf("%s %s: %d %s", req.Method, req.URL.Path, resp.StatusCode, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			fb.t.Fatalf("%s %s: decode %q: %v", req.Method, req.URL.Path, raw, err)
		}
	}
}

func (fb *freshbreath) createAuth(name, kind string, d db.AuthDescriptor) int64 {
	fb.t.Helper()
	var rec struct{ ID int64 }
	fb.api("POST", "/api/auth", map[string]any{"name": name, "kind": kind, "descriptor": d}, &rec)
	return rec.ID
}

// builtinAuth finds one of the seeded records (anonymous, ssh_key).
func (fb *freshbreath) builtinAuth(kind string) int64 {
	fb.t.Helper()
	var list struct {
		Auth []struct {
			ID      int64
			Kind    string
			Builtin bool
		}
	}
	fb.api("GET", "/api/auth", nil, &list)
	for _, r := range list.Auth {
		if r.Builtin && r.Kind == kind {
			return r.ID
		}
	}
	fb.t.Fatalf("no builtin %s record", kind)
	return 0
}

// service is a registered service as the admin API reports it.
type service struct {
	ID  int64  `json:"id"`
	URL string `json:"url"`
}

func (fb *freshbreath) createService(name, serviceURL string, d db.ServiceDescriptor, protectedBy *int64) service {
	fb.t.Helper()
	var created service
	fb.api("POST", "/api/services", map[string]any{
		"name": name, "url": serviceURL, "descriptor": d, "protected_by": protectedBy,
	}, &created)
	var list struct{ Services []service }
	fb.api("GET", "/api/services", nil, &list)
	for _, s := range list.Services {
		if s.ID == created.ID {
			return s
		}
	}
	fb.t.Fatalf("service %q not listed after create", name)
	return service{}
}

// uploadServiceFile uploads a virtual or task service's tool file.
func (fb *freshbreath) uploadServiceFile(svc service, filename string, content []byte) {
	fb.t.Helper()
	fb.upload(fmt.Sprintf("/api/services/%d/files", svc.ID), filename, content, nil)
}

// app is a registered, uploaded app.
type app struct {
	Nonce string
	Route string // hosted path, e.g. "/e2e-app"
}

// createApp registers an app behind gate, links the services, and uploads
// its one HTML page.
func (fb *freshbreath) createApp(name string, gate int64, page []byte, services ...service) app {
	fb.t.Helper()
	var a app
	var created struct{ Nonce string }
	fb.api("POST", "/api/apps", map[string]any{"name": name, "protected_by": gate}, &created)
	a.Nonce = created.Nonce
	ids := []int64{}
	for _, s := range services {
		ids = append(ids, s.ID)
	}
	fb.api("PUT", "/api/apps/"+a.Nonce+"/services", map[string]any{"services": ids}, nil)
	var uploaded struct{ Route string }
	fb.upload("/api/apps/"+a.Nonce+"/web", "index.html", page, &uploaded)
	a.Route = uploaded.Route
	return a
}

// ── Test data ───────────────────────────────────────────────────────

// testdata reads a file from testdata/, substituting {{KEY}} placeholders.
func testdata(t *testing.T, name string, subs map[string]string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for k, v := range subs {
		s = strings.ReplaceAll(s, "{{"+k+"}}", v)
	}
	return []byte(s)
}

// ── Browser ─────────────────────────────────────────────────────────

// newBrowser starts a headless Chromium with a clean profile. Popups are
// left blocked, as a real browser would have them: frbr.js has to open its
// login window inside the click's user activation or the test fails.
func newBrowser(t *testing.T) context.Context {
	t.Helper()
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.NoSandbox,
		chromedp.UserDataDir(t.TempDir()),
	)
	if path := os.Getenv("FRBR_E2E_CHROME"); path != "" {
		opts = append(opts, chromedp.ExecPath(path))
	}
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancel := chromedp.NewContext(allocCtx)
	// Close Chromium gracefully rather than killing it: a killed browser
	// leaves helpers still writing the profile while TempDir removes it.
	t.Cleanup(func() { chromedp.Cancel(ctx); cancel(); cancelAlloc() })
	if err := chromedp.Run(ctx); err != nil {
		t.Fatalf("start browser: %v", err)
	}
	return ctx
}

// scenarioResult is what the test app writes to #out when a scenario ends.
type scenarioResult struct {
	OK     bool            `json:"ok"`
	Error  string          `json:"error"`
	Result json.RawMessage `json:"result"`
}

// runScenario opens the app with ?scenario=<name>&<params>, clicks Run and
// waits for the app to report. Any login popups the scenario opens finish
// on their own: the fake providers approve without a prompt.
func runScenario(t *testing.T, browser context.Context, pageURL, name string, params map[string]string) json.RawMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(browser, 30*time.Second)
	defer cancel()

	q := url.Values{"scenario": {name}}
	for k, v := range params {
		q.Set(k, v)
	}
	var out string
	err := chromedp.Run(ctx,
		chromedp.Navigate(pageURL+"?"+q.Encode()),
		chromedp.WaitReady("#run", chromedp.ByID),
		chromedp.Click("#run", chromedp.ByID),
		chromedp.WaitVisible(`#out[data-done]`, chromedp.ByQuery),
		chromedp.Text("#out", &out, chromedp.ByID),
	)
	if err != nil {
		t.Fatalf("scenario %s: %v", name, err)
	}
	var res scenarioResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("scenario %s: bad output %q", name, out)
	}
	if !res.OK {
		t.Fatalf("scenario %s failed in the app: %s", name, res.Error)
	}
	return res.Result
}

// ── MCP client ──────────────────────────────────────────────────────

// connectMCP connects an MCP client to endpoint the way a desktop client does:
// the first request is refused, the SDK discovers Fresh Breath's
// authorization server, registers itself, and hands the authorize URL to
// a "browser" — here a new tab in the test's Chromium — then catches the
// redirect back on a loopback listener and exchanges the code.
func connectMCP(t *testing.T, browser context.Context, endpoint string) *mcp.ClientSession {
	t.Helper()
	codes := make(chan *auth.AuthorizationResult, 1)
	callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		select {
		case codes <- &auth.AuthorizationResult{Code: q.Get("code"), State: q.Get("state"), Iss: q.Get("iss")}:
		default:
		}
		io.WriteString(w, "<p>Signed in. You can close this window.</p>")
	}))
	t.Cleanup(callback.Close)
	redirectURL := callback.URL + "/callback"

	handler, err := auth.NewAuthorizationCodeHandler(&auth.AuthorizationCodeHandlerConfig{
		RedirectURL: redirectURL,
		DynamicClientRegistrationConfig: &auth.DynamicClientRegistrationConfig{
			Metadata: &oauthex.ClientRegistrationMetadata{
				ClientName:   "freshbreath-e2e",
				RedirectURIs: []string{redirectURL},
			},
		},
		AuthorizationCodeFetcher: func(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
			tab, closeTab := chromedp.NewContext(browser)
			defer closeTab()
			if err := chromedp.Run(tab, chromedp.Navigate(args.URL)); err != nil {
				return nil, fmt.Errorf("open authorize page: %w", err)
			}
			select {
			case res := <-codes:
				return res, nil
			case <-time.After(20 * time.Second):
				var html string
				chromedp.Run(tab, chromedp.OuterHTML("html", &html, chromedp.ByQuery))
				return nil, fmt.Errorf("no redirect back from the authorize page; it shows: %s", html)
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	})
	if err != nil {
		t.Fatalf("oauth handler: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "freshbreath-e2e", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint, OAuthHandler: handler}, nil)
	if err != nil {
		t.Fatalf("connect MCP %s: %v", endpoint, err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

// callTool calls an MCP tool and decodes its text result as JSON.
func callTool(t *testing.T, session *mcp.ClientSession, name string, args map[string]any, out any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	var text strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	if res.IsError {
		t.Fatalf("call %s: tool error: %s", name, text.String())
	}
	if err := json.Unmarshal([]byte(text.String()), out); err != nil {
		t.Fatalf("call %s: decode %q: %v", name, text.String(), err)
	}
}
