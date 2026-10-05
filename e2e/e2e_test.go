//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"poggers.institute/freshbreath/internal/db"
)

// githubAuth registers fake GitHub as an oauth2 auth record, the way the
// real GitHub OAuth App is set up.
func githubAuth(fb *freshbreath, gh *fakeGitHub) int64 {
	return fb.createAuth("GitHub", db.AuthOAuth2, githubDescriptor(gh))
}

func githubDescriptor(gh *fakeGitHub) db.AuthDescriptor {
	return db.AuthDescriptor{
		AuthorizeURL:  gh.URL + "/login/oauth/authorize",
		TokenURL:      gh.URL + "/login/oauth/access_token",
		UserInfoURL:   gh.URL + "/user",
		UserEmailsURL: gh.URL + "/user/emails",
		ClientID:      githubClientID,
		ClientSecret:  githubClientSecret,
		Scopes:        "repo read:user",
		Provider:      "github",
	}
}

// githubVirtual registers a virtual service over fake GitHub, behind the
// GitHub login, passing each caller's own GitHub token upstream.
func githubVirtual(t *testing.T, fb *freshbreath, gh *fakeGitHub, gate int64) service {
	svc := fb.createService("github", "", db.ServiceDescriptor{Type: "virtual"}, &gate)
	fb.uploadServiceFile(svc, "github.txt", testdata(t, "github.txt", map[string]string{"GITHUB": gh.URL}))
	return svc
}

// The GitHub results every client should see, whichever door it uses. A
// virtual tool whose response isn't an object answers with it under
// "value", in the app and over MCP alike.
type githubResults struct {
	User   map[string]any `json:"user"`
	Repo   map[string]any `json:"repo"`
	Issues struct {
		Value []map[string]any `json:"value"`
	} `json:"issues"`
}

func checkGitHubResults(t *testing.T, got githubResults) {
	t.Helper()
	if got.User["login"] != githubUser["login"] || got.User["email"] != githubUser["email"] {
		t.Errorf("whoami = %v, want %v", got.User, githubUser)
	}
	if got.Repo["full_name"] != githubRepo["full_name"] || got.Repo["stargazers_count"] != float64(1337) {
		t.Errorf("get-repo = %v, want %v", got.Repo, githubRepo)
	}
	if len(got.Issues.Value) != len(githubIssues) || got.Issues.Value[0]["title"] != githubIssues[0]["title"] {
		t.Errorf("list-issues = %v, want %v", got.Issues, githubIssues)
	}
}

var githubTools = []string{"get-repo", "list-issues", "whoami"}

// An app behind a GitHub login calls a virtual GitHub service: one popup
// through GitHub, then each tool reaches GitHub with the user's own token.
func TestGitHubVirtualServiceFromApp(t *testing.T) {
	gh := newFakeGitHub(t)
	fb := startFreshbreath(t)
	gate := githubAuth(fb, gh)
	svc := githubVirtual(t, fb, gh, gate)
	a := fb.createApp("e2e-github", gate, testdata(t, "app.html", nil), svc)

	browser := newBrowser(t)
	raw := runScenario(t, browser, fb.URL+a.Route, "github", map[string]string{"service": svc.URL})

	var got struct {
		githubResults
		Tools   []string `json:"tools"`
		Session struct {
			Kind    string `json:"kind"`
			Subject string `json:"subject"`
		} `json:"session"`
	}
	json.Unmarshal(raw, &got)
	if !reflect.DeepEqual(got.Tools, githubTools) {
		t.Errorf("tools = %v, want %v", got.Tools, githubTools)
	}
	checkGitHubResults(t, got.githubResults)
	if got.Session.Kind != db.AuthOAuth2 || got.Session.Subject != "ext:github:4242" {
		t.Errorf("session = %+v, want an oauth2 session for ext:github:4242", got.Session)
	}
	if gh.Logins() != 1 {
		t.Errorf("GitHub logins = %d, want 1", gh.Logins())
	}
}

// An MCP client connects straight to the virtual GitHub service, signing
// in to GitHub through the browser, and gets the same data.
func TestGitHubVirtualServiceOverMCP(t *testing.T) {
	gh := newFakeGitHub(t)
	fb := startFreshbreath(t)
	gate := githubAuth(fb, gh)
	svc := githubVirtual(t, fb, gh, gate)

	browser := newBrowser(t)
	session := connectMCP(t, browser, fb.URL+svc.URL)

	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	if !reflect.DeepEqual(names, githubTools) {
		t.Errorf("tools = %v, want %v", names, githubTools)
	}

	var got githubResults
	callTool(t, session, "whoami", nil, &got.User)
	callTool(t, session, "get-repo", map[string]any{"owner": "octo-fixture", "repo": "hello-fixture"}, &got.Repo)
	callTool(t, session, "list-issues", map[string]any{"owner": "octo-fixture", "repo": "hello-fixture"}, &got.Issues)
	checkGitHubResults(t, got)
	if gh.Logins() != 1 {
		t.Errorf("GitHub logins = %d, want 1", gh.Logins())
	}
}

// An app behind a GitHub login uses a Notion-shaped OAuth MCP server. One
// popup clears both legs — GitHub for the app, then Notion — and the
// proxy swaps the Fresh Breath token for Notion's on the way through.
func TestNotionMCPFromApp(t *testing.T) {
	gh := newFakeGitHub(t)
	notion := newFakeNotion(t)
	fb := startFreshbreath(t)
	gate := githubAuth(fb, gh)
	svc := fb.createService("notion", notion.URL+"/mcp", db.ServiceDescriptor{Type: "mcp", Proxied: true}, nil)
	a := fb.createApp("e2e-notion", gate, testdata(t, "app.html", nil), svc)

	browser := newBrowser(t)
	raw := runScenario(t, browser, fb.URL+a.Route, "notion", map[string]string{"service": svc.URL})

	var got struct {
		Tools  []string `json:"tools"`
		Search struct {
			Results []map[string]any `json:"results"`
		} `json:"search"`
	}
	json.Unmarshal(raw, &got)
	if !reflect.DeepEqual(got.Tools, []string{"notion-search"}) {
		t.Errorf("tools = %v, want [notion-search]", got.Tools)
	}
	if len(got.Search.Results) != len(notionPages) || got.Search.Results[0]["title"] != notionPages[0]["title"] {
		t.Errorf("search = %v, want %v", got.Search.Results, notionPages)
	}
	if gh.Logins() != 1 || notion.Logins() != 1 {
		t.Errorf("logins: GitHub %d, Notion %d; want 1 each", gh.Logins(), notion.Logins())
	}
}

// An open app runs a task service's scripts: callTool hands back a
// script's JSON output parsed, and throws with stderr when one fails.
func TestTaskServiceFromApp(t *testing.T) {
	fb := startFreshbreath(t)
	open := fb.builtinAuth(db.AuthAnonymous)
	svc := fb.createService("e2e-tasks", "", db.ServiceDescriptor{Type: "tasks"}, &open)
	fb.uploadServiceFile(svc, "tasks.txt", testdata(t, "tasks.txt", nil))
	a := fb.createApp("e2e-tasks", open, testdata(t, "app.html", nil), svc)

	browser := newBrowser(t)
	raw := runScenario(t, browser, fb.URL+a.Route, "tasks", map[string]string{"service": svc.URL})

	var got struct {
		Tools   []string          `json:"tools"`
		Echo    map[string]string `json:"echo"`
		Failure string            `json:"failure"`
	}
	json.Unmarshal(raw, &got)
	if !reflect.DeepEqual(got.Tools, taskTools) {
		t.Errorf("tools = %v, want %v", got.Tools, taskTools)
	}
	if got.Echo["echo"] != "hello from the app" || got.Echo["task"] != "echo" {
		t.Errorf("echo = %v", got.Echo)
	}
	if !strings.Contains(got.Failure, "it broke: on purpose") {
		t.Errorf("fail threw %q, want the script's stderr", got.Failure)
	}
}

var taskTools = []string{"echo", "fail", "token-kind"}

// An admin moves an app behind a different gate while its page is open.
// The page's old login can't be refreshed for this app any more, so the
// next call prompts for the new gate, retries, and later calls ride the
// new login without prompting again.
func TestRegatedAppPromptsForNewGate(t *testing.T) {
	gh := newFakeGitHub(t)
	fb := startFreshbreath(t)
	oldGate := githubAuth(fb, gh)
	newGate := fb.createAuth("GitHub (new gate)", db.AuthOAuth2, githubDescriptor(gh))
	open := fb.builtinAuth(db.AuthAnonymous)
	svc := fb.createService("e2e-tasks", "", db.ServiceDescriptor{Type: "tasks"}, &open)
	fb.uploadServiceFile(svc, "tasks.txt", testdata(t, "tasks.txt", nil))
	a := fb.createApp("e2e-regate", oldGate, testdata(t, "app.html", nil), svc)

	browser := newBrowser(t)
	ctx, cancel := context.WithTimeout(browser, 45*time.Second)
	defer cancel()
	q := url.Values{"scenario": {"regate"}, "service": {svc.URL}}
	err := chromedp.Run(ctx,
		chromedp.Navigate(fb.URL+a.Route+"?"+q.Encode()),
		chromedp.WaitReady("#run", chromedp.ByID),
		chromedp.Click("#run", chromedp.ByID),
		chromedp.WaitVisible(`#out[data-ready]`, chromedp.ByQuery),
	)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}

	fb.api("PUT", "/api/apps/"+a.Nonce, map[string]any{"name": "e2e-regate", "protected_by": newGate}, nil)

	var out string
	err = chromedp.Run(ctx,
		chromedp.Click("#again", chromedp.ByID),
		chromedp.WaitVisible(`#out[data-done]`, chromedp.ByQuery),
		chromedp.Text("#out", &out, chromedp.ByID),
	)
	if err != nil {
		var html string
		chromedp.Run(browser, chromedp.OuterHTML("body", &html, chromedp.ByQuery))
		t.Fatalf("calls after the gate moved: %v\n%s", err, html)
	}
	var res struct {
		scenarioResult
		Result struct {
			Before, After, Later map[string]string
			AuthID               int64
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("bad output %q", out)
	}
	if !res.OK {
		t.Fatalf("scenario failed in the app: %s", res.Error)
	}
	for name, echo := range map[string]map[string]string{"before": res.Result.Before, "after": res.Result.After, "later": res.Result.Later} {
		if echo["echo"] != name {
			t.Errorf("%s call = %v", name, echo)
		}
	}
	if res.Result.AuthID != newGate {
		t.Errorf("proxy session = record %d, want the new gate %d", res.Result.AuthID, newGate)
	}
	if gh.Logins() != 2 {
		t.Errorf("GitHub logins = %d, want 2 (one per gate)", gh.Logins())
	}
}

// An MCP client connects to a task service behind the GitHub login. Each
// task's arguments show up as strings in its schema, the script gets the
// caller's GitHub token as TASK_TOKEN, and a failing script is a tool
// error carrying its stderr.
func TestTaskServiceOverMCP(t *testing.T) {
	gh := newFakeGitHub(t)
	fb := startFreshbreath(t)
	gate := githubAuth(fb, gh)
	svc := fb.createService("e2e-tasks", "", db.ServiceDescriptor{Type: "tasks"}, &gate)
	fb.uploadServiceFile(svc, "tasks.txt", testdata(t, "tasks.txt", nil))

	browser := newBrowser(t)
	session := connectMCP(t, browser, fb.URL+"/mcp/e2e-tasks")

	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	schemas := map[string]string{}
	for _, tool := range tools.Tools {
		b, _ := json.Marshal(tool.InputSchema.(map[string]any)["properties"])
		schemas[tool.Name] = string(b)
	}
	want := map[string]string{
		"echo":       `{"message":{"type":"string"}}`,
		"fail":       `{"reason":{"type":"string"}}`,
		"token-kind": `{}`,
	}
	if !reflect.DeepEqual(schemas, want) {
		t.Errorf("tool properties = %v, want %v", schemas, want)
	}

	var echo map[string]string
	callTool(t, session, "echo", map[string]any{"message": "hello over MCP"}, &echo)
	if echo["echo"] != "hello over MCP" || echo["task"] != "echo" {
		t.Errorf("echo = %v", echo)
	}
	var tokenKind map[string]string
	callTool(t, session, "token-kind", nil, &tokenKind)
	if tokenKind["token_kind"] != "gho_access" {
		t.Errorf("token-kind = %v, want the caller's GitHub token (gho_access)", tokenKind)
	}

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "fail", Arguments: map[string]any{"reason": "on purpose"}})
	if err != nil {
		t.Fatalf("call fail: %v", err)
	}
	var text strings.Builder
	for _, c := range res.Content {
		text.WriteString(c.(*mcp.TextContent).Text)
	}
	if !res.IsError || !strings.Contains(text.String(), "it broke: on purpose") {
		t.Errorf("fail = %+v (%q), want a tool error with the script's stderr", res, text.String())
	}
	if gh.Logins() != 1 {
		t.Errorf("GitHub logins = %d, want 1", gh.Logins())
	}
}
