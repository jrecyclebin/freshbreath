//go:build e2e

package e2e

import (
	"encoding/json"
	"reflect"
	"testing"

	"poggers.institute/freshbreath/internal/db"
)

// githubAuth registers fake GitHub as an oauth2 auth record, the way the
// real GitHub OAuth App is set up.
func githubAuth(fb *freshbreath, gh *fakeGitHub) int64 {
	return fb.createAuth("GitHub", db.AuthOAuth2, db.AuthDescriptor{
		AuthorizeURL:  gh.URL + "/login/oauth/authorize",
		TokenURL:      gh.URL + "/login/oauth/access_token",
		UserInfoURL:   gh.URL + "/user",
		UserEmailsURL: gh.URL + "/user/emails",
		ClientID:      githubClientID,
		ClientSecret:  githubClientSecret,
		Scopes:        "repo read:user",
		Provider:      "github",
	})
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

// An open app runs a task service's script and gets its output back.
func TestTaskServiceFromApp(t *testing.T) {
	fb := startFreshbreath(t)
	open := fb.builtinAuth(db.AuthAnonymous)
	svc := fb.createService("e2e-tasks", "", db.ServiceDescriptor{Type: "tasks"}, &open)
	fb.uploadServiceFile(svc, "tasks.txt", testdata(t, "tasks.txt", nil))
	a := fb.createApp("e2e-tasks", open, testdata(t, "app.html", nil), svc)

	browser := newBrowser(t)
	raw := runScenario(t, browser, fb.URL+a.Route, "tasks", map[string]string{"service": svc.URL})

	// callTool on a task hands the app the server's MCP-shaped envelope as
	// is: the script's stdout sits in content[0].text, unparsed. (The tasks
	// guide promises the parsed JSON instead — frbr.js doesn't do that yet.)
	var got struct {
		Tools []string `json:"tools"`
		Echo  struct {
			Content []struct{ Text string } `json:"content"`
			IsError bool                    `json:"isError"`
		} `json:"echo"`
	}
	json.Unmarshal(raw, &got)
	if !reflect.DeepEqual(got.Tools, []string{"echo"}) {
		t.Errorf("tools = %v, want [echo]", got.Tools)
	}
	if got.Echo.IsError || len(got.Echo.Content) != 1 {
		t.Fatalf("echo = %+v", got.Echo)
	}
	var echo map[string]string
	json.Unmarshal([]byte(got.Echo.Content[0].Text), &echo)
	if echo["echo"] != "hello from the app" || echo["task"] != "echo" {
		t.Errorf("echo output = %q", got.Echo.Content[0].Text)
	}
}
