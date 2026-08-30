package formats

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// ── Parser Tests ─────────────────────────────────────────────────────

func TestParseSharepoint(t *testing.T) {
	data, err := os.ReadFile("../../samples/Sharepoint.txt")
	if err != nil {
		t.Fatal(err)
	}

	tools, err := ParseVirtualFile(data)
	if err != nil {
		t.Fatal(err)
	}

	if len(tools) == 0 {
		t.Fatal("expected tools to be parsed")
	}
	if len(tools) != 13 {
		t.Errorf("expected 13 tools, got %d", len(tools))
	}

	// ── [get-site] ───────────────────────────────────────────────────
	ts := findTool(tools, "get-site")
	if ts == nil {
		t.Fatal("get-site tool not found")
	}
	if ts.Description != "Resolve a SharePoint site URL to a site ID." {
		t.Errorf("get-site description = %q", ts.Description)
	}
	if len(ts.Steps) != 1 {
		t.Fatalf("get-site steps = %d, want 1", len(ts.Steps))
	}
	step := ts.Steps[0]

	// Assignments: $hostname = host($url), $pathname = path($url)
	if len(step.Assignments) != 2 {
		t.Fatalf("get-site assignments = %d, want 2", len(step.Assignments))
	}
	if step.Assignments[0].VarName != "hostname" {
		t.Errorf("assignment[0] var = %q, want hostname", step.Assignments[0].VarName)
	}
	if step.Assignments[0].Expr != "host($url)" {
		t.Errorf("assignment[0] expr = %q", step.Assignments[0].Expr)
	}
	if step.Assignments[1].VarName != "pathname" {
		t.Errorf("assignment[1] var = %q, want pathname", step.Assignments[1].VarName)
	}
	if step.Assignments[1].Expr != "path($url)" {
		t.Errorf("assignment[1] expr = %q", step.Assignments[1].Expr)
	}

	if step.Method != "GET" {
		t.Errorf("get-site method = %q, want GET", step.Method)
	}
	if step.URL != "https://graph.microsoft.com/v1.0/sites/$hostname:$pathname" {
		t.Errorf("get-site URL = %q", step.URL)
	}
	if len(step.Headers) != 1 || step.Headers["Authorization"] != "Bearer $token" {
		t.Errorf("get-site headers = %v", step.Headers)
	}
	if len(step.Responses) != 1 {
		t.Errorf("get-site responses = %d, want 1", len(step.Responses))
	}
	if _, ok := step.Responses[200]; !ok {
		t.Error("get-site missing HTTP 200 response")
	}

	// ── [create-list] — has a JSON body ──────────────────────────────
	cl := findTool(tools, "create-list")
	if cl == nil {
		t.Fatal("create-list tool not found")
	}
	clStep := cl.Steps[0]
	if clStep.Method != "POST" {
		t.Errorf("create-list method = %q, want POST", clStep.Method)
	}
	if clStep.Body == "" {
		t.Error("create-list missing body")
	}
	if clStep.Headers["Content-Type"] != "application/json" {
		t.Errorf("create-list Content-Type = %q", clStep.Headers["Content-Type"])
	}
	if _, ok := clStep.Responses[201]; !ok {
		t.Error("create-list missing HTTP 201 response")
	}

	// ── [get-list-items] — no response shaping (raw response) ────────
	gli := findTool(tools, "get-list-items")
	if gli == nil {
		t.Fatal("get-list-items tool not found")
	}
	gliStep := gli.Steps[0]
	if gliStep.Responses[200] == nil {
		t.Fatal("get-list-items missing HTTP 200")
	}
	if gliStep.Responses[200].Shaping != "" {
		t.Errorf("get-list-items unexpected shaping: %q", gliStep.Responses[200].Shaping)
	}

	// ── [get-list-next] — has assertion ──────────────────────────────
	gln := findTool(tools, "get-list-next")
	if gln == nil {
		t.Fatal("get-list-next tool not found")
	}
	glnStep := gln.Steps[0]
	if len(glnStep.Assertions) != 1 {
		t.Fatalf("get-list-next assertions = %d, want 1", len(glnStep.Assertions))
	}
	if glnStep.Assertions[0].Msg != "Invalid nextLink" {
		t.Errorf("assertion msg = %q", glnStep.Assertions[0].Msg)
	}
	if glnStep.Assertions[0].Expr != `host($next_link) == "graph.microsoft.com"` {
		t.Errorf("assertion expr = %q", glnStep.Assertions[0].Expr)
	}

	// ── [delete-list] — DELETE with HTTP 204 ─────────────────────────
	dl := findTool(tools, "delete-list")
	if dl == nil {
		t.Fatal("delete-list tool not found")
	}
	if _, ok := dl.Steps[0].Responses[204]; !ok {
		t.Error("delete-list missing HTTP 204")
	}
}

func TestParseSimpleTool(t *testing.T) {
	input := `[hello] Say hello
GET https://example.com/greet/$name
Authorization: Bearer $token
---
[echo] Echo back
POST https://example.com/echo
Content-Type: application/json
{
  "message": $msg
}

HTTP 200
{
  "reply": $.reply
}
`
	tools, err := ParseVirtualFile([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}

	if tools[0].Name != "hello" {
		t.Errorf("tool[0] name = %q", tools[0].Name)
	}
	if tools[0].Steps[0].Method != "GET" {
		t.Errorf("tool[0] method = %q", tools[0].Steps[0].Method)
	}

	if tools[1].Name != "echo" {
		t.Errorf("tool[1] name = %q", tools[1].Name)
	}
	echoStep := tools[1].Steps[0]
	if echoStep.Method != "POST" {
		t.Errorf("echo method = %q", echoStep.Method)
	}
	if echoStep.Body == "" {
		t.Error("echo missing body")
	}
	if echoStep.Responses[200] == nil || echoStep.Responses[200].Shaping == "" {
		t.Error("echo missing response shaping")
	}
}

func TestParseMultiStep(t *testing.T) {
	input := `[fetch-and-update] Fetch then update
$hostname = host($url)
GET https://api.example.com/data/$id
Authorization: Bearer $token

HTTP 200
$next_url = $['links']['next']
GET $next_url
Authorization: Bearer $token

HTTP 200
`
	tools, err := ParseVirtualFile([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if len(tools[0].Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(tools[0].Steps))
	}
	if tools[0].Steps[0].Method != "GET" {
		t.Errorf("step[0] method = %q", tools[0].Steps[0].Method)
	}
	if tools[0].Steps[1].Method != "GET" {
		t.Errorf("step[1] method = %q", tools[0].Steps[1].Method)
	}
	if len(tools[0].Steps[1].Assignments) != 1 || tools[0].Steps[1].Assignments[0].VarName != "next_url" {
		t.Errorf("step[1] assignments = %v", tools[0].Steps[1].Assignments)
	}
}

func TestParseBadButWorkable(t *testing.T) {
	input := `---
[fetch-and-update] --- Fetch then update
$hostname = unknown($url)
  GET https://api.example.com/data/$id
Authorization: Bearer $token

HTTP 200
$next_url = $['links']['next']
GET $next_url
Authorization: Bearer $token

HTTP 200
---
---
[nawwwwww Broken test
# Will need to fix this
ldskjasdjflksda
sddslkfj;;;
`
	tools, err := ParseVirtualFile([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if len(tools[0].Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(tools[0].Steps))
	}
	if tools[0].Steps[0].Method != "GET" {
		t.Errorf("step[0] method = %q", tools[0].Steps[0].Method)
	}
	if tools[0].Steps[1].Method != "GET" {
		t.Errorf("step[1] method = %q", tools[0].Steps[1].Method)
	}
	if len(tools[0].Steps[1].Assignments) != 1 || tools[0].Steps[1].Assignments[0].VarName != "next_url" {
		t.Errorf("step[1] assignments = %v", tools[0].Steps[1].Assignments)
	}
}

func TestParseDollarDollarInURL(t *testing.T) {
	input := `[list-items] List items
GET https://graph.microsoft.com/v1.0/items?$$select=id,name&$$top=10
Authorization: Bearer $token

HTTP 200
`
	tools, err := ParseVirtualFile([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	url := tools[0].Steps[0].URL
	if url != "https://graph.microsoft.com/v1.0/items?$$select=id,name&$$top=10" {
		t.Errorf("URL = %q", url)
	}
}

// ── Line Classifier Tests ────────────────────────────────────────────

func TestTryParseAssignment(t *testing.T) {
	ok, name, expr := tryParseAssignment("$hostname = host($url)")
	if !ok || name != "hostname" || expr != "host($url)" {
		t.Errorf("got (%v, %q, %q)", ok, name, expr)
	}

	ok, name, expr = tryParseAssignment("$next_link = $['@odata']['nextLink']")
	if !ok || name != "next_link" || expr != "$['@odata']['nextLink']" {
		t.Errorf("got (%v, %q, %q)", ok, name, expr)
	}

	// Not an assignment
	ok, _, _ = tryParseAssignment("GET /foo")
	if ok {
		t.Error("should not parse as assignment")
	}
	ok, _, _ = tryParseAssignment("$invalid-name = value")
	if ok {
		t.Error("hyphens not allowed in variable names")
	}
}

func TestTryParseAssertion(t *testing.T) {
	ok, expr, msg := tryParseAssertion(`assert(host($next_link) == "graph.microsoft.com", "Invalid nextLink")`)
	if !ok || msg != "Invalid nextLink" {
		t.Errorf("got (%v, %q, %q)", ok, expr, msg)
	}
	if expr != `host($next_link) == "graph.microsoft.com"` {
		t.Errorf("expr = %q", expr)
	}

	// Assertion without message
	ok, expr, msg = tryParseAssertion(`assert($x == $y)`)
	if !ok || msg != "" {
		t.Errorf("got (%v, %q, %q)", ok, expr, msg)
	}

	// Not an assertion
	ok, _, _ = tryParseAssertion("$x = 5")
	if ok {
		t.Error("should not parse as assertion")
	}
}

func TestTryParseRequest(t *testing.T) {
	for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
		ok, method, u := tryParseRequest(m + " https://example.com/api")
		if !ok || method != m || u != "https://example.com/api" {
			t.Errorf("%s: got (%v, %q, %q)", m, ok, method, u)
		}
	}

	ok, _, _ := tryParseRequest("GETS /foo")
	if ok {
		t.Error("GETS should not match")
	}
}

func TestTryParseHTTPStatus(t *testing.T) {
	ok, code := tryParseHTTPStatus("HTTP 200")
	if !ok || code != 200 {
		t.Errorf("got (%v, %d)", ok, code)
	}

	ok, code = tryParseHTTPStatus("HTTP 201 Created")
	if !ok || code != 201 {
		t.Errorf("got (%v, %d)", ok, code)
	}

	ok, _ = tryParseHTTPStatus("HTTPS 200")
	if ok {
		t.Error("HTTPS should not match")
	}
}

func TestParseHeaderLine(t *testing.T) {
	k, v, ok := parseHeaderLine("Authorization: Bearer $token")
	if !ok || k != "Authorization" || v != "Bearer $token" {
		t.Errorf("got (%v, %q, %q)", ok, k, v)
	}

	k, v, ok = parseHeaderLine("Content-Type: application/json")
	if !ok || k != "Content-Type" || v != "application/json" {
		t.Errorf("got (%v, %q, %q)", ok, k, v)
	}

	// Not a header
	_, _, ok = parseHeaderLine("$var = value")
	if ok {
		t.Error("assignment should not parse as header")
	}
	_, _, ok = parseHeaderLine("[not-a-header]")
	if ok {
		t.Error("bracket expression should not parse as header")
	}
}

func TestParseVirtualToolHeader(t *testing.T) {
	// Valid tool headers
	_, _, ok := parseVirtualToolHeader("[get-site] Resolve a site")
	if !ok {
		t.Error("expected [get-site] to parse")
	}

	// Invalid: name contains spaces or special chars
	_, _, ok = parseVirtualToolHeader(`["item1", "item2"]`)
	if ok {
		t.Error("JSON array should not parse as tool header")
	}

	// Invalid: empty name
	_, _, ok = parseVirtualToolHeader("[] Description")
	if ok {
		t.Error("empty name should not parse")
	}
}

// ── Variable Resolution Tests ────────────────────────────────────────

func TestResolveURLSimple(t *testing.T) {
	vars := map[string]interface{}{"site_id": "abc-123"}
	result, err := resolveTemplate(
		"https://graph.microsoft.com/v1.0/sites/$site_id/lists",
		vars, nil, "", ResolveURL,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result != "https://graph.microsoft.com/v1.0/sites/abc-123/lists" {
		t.Errorf("result = %q", result)
	}
}

func TestResolveDollarDollar(t *testing.T) {
	result, err := resolveTemplate(
		"https://example.com?$$select=id,name&$$top=10",
		nil, nil, "", ResolveURL,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result != "https://example.com?$select=id,name&$top=10" {
		t.Errorf("result = %q", result)
	}
}

func TestResolveDollarDollarWithVars(t *testing.T) {
	vars := map[string]interface{}{"site_id": "abc"}
	result, err := resolveTemplate(
		"https://graph.microsoft.com/v1.0/sites/$site_id/lists?$$select=id,name",
		vars, nil, "", ResolveURL,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result != "https://graph.microsoft.com/v1.0/sites/abc/lists?$select=id,name" {
		t.Errorf("result = %q", result)
	}
}

func TestResolveURLQueryEscape(t *testing.T) {
	// Variables in the query string should be escaped.
	vars := map[string]interface{}{"folder": "My Documents", "site_id": "abc-123"}
	result, err := resolveTemplate(
		"https://graph.microsoft.com/v1.0/sites/$site_id/drive/root/children?path=$folder",
		vars, nil, "", ResolveURL,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result != "https://graph.microsoft.com/v1.0/sites/abc-123/drive/root/children?path=My+Documents" {
		t.Errorf("result = %q", result)
	}
}

func TestResolveURLQueryEscapeSpecialChars(t *testing.T) {
	// Ampersands, equals, and spaces in query values must be escaped.
	vars := map[string]interface{}{"q": "hello & goodbye", "site_id": "abc"}
	result, err := resolveTemplate(
		"https://graph.microsoft.com/v1.0/sites/$site_id/search?query=$q",
		vars, nil, "", ResolveURL,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result != "https://graph.microsoft.com/v1.0/sites/abc/search?query=hello+%26+goodbye" {
		t.Errorf("result = %q", result)
	}
}

func TestResolveURLQueryNoEscapePath(t *testing.T) {
	// Path variables stay raw even when the URL also has a query string.
	vars := map[string]interface{}{"site_id": "contoso.sharepoint.com:/sites/My Site", "top": "10"}
	result, err := resolveTemplate(
		"https://graph.microsoft.com/v1.0/sites/$site_id/lists?$$top=$top",
		vars, nil, "", ResolveURL,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result != "https://graph.microsoft.com/v1.0/sites/contoso.sharepoint.com:/sites/My Site/lists?$top=10" {
		t.Errorf("result = %q", result)
	}
}

func TestResolveURLNoQuery(t *testing.T) {
	// URLs without a query string should still resolve path variables.
	vars := map[string]interface{}{"site_id": "abc-123"}
	result, err := resolveTemplate(
		"https://graph.microsoft.com/v1.0/sites/$site_id",
		vars, nil, "", ResolveURL,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result != "https://graph.microsoft.com/v1.0/sites/abc-123" {
		t.Errorf("result = %q", result)
	}
}

func TestResolveBodyStrings(t *testing.T) {
	vars := map[string]interface{}{
		"name":        "My List",
		"description": "A test list",
	}
	result, err := resolveTemplate(
		`{"displayName": $name, "description": $description}`,
		vars, nil, "", ResolveBody,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result != `{"displayName": "My List", "description": "A test list"}` {
		t.Errorf("result = %q", result)
	}
}

func TestResolveBodyNumber(t *testing.T) {
	vars := map[string]interface{}{"count": int64(42)}
	result, err := resolveTemplate(
		`{"count": $count}`,
		vars, nil, "", ResolveBody,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result != `{"count": 42}` {
		t.Errorf("result = %q", result)
	}
}

func TestResolveBodySkipDollarInStrings(t *testing.T) {
	vars := map[string]interface{}{"name": "test"}
	result, err := resolveTemplate(
		`{"$schema": "http://example.com", "name": $name}`,
		vars, nil, "", ResolveBody,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result != `{"$schema": "http://example.com", "name": "test"}` {
		t.Errorf("result = %q", result)
	}
}

func TestResolveBodySpreadOnly(t *testing.T) {
	vars := map[string]interface{}{
		"fields": map[string]interface{}{"Title": "Hello", "Color": "Red"},
	}
	result, err := resolveTemplate(
		`{...$fields}`,
		vars, nil, "", ResolveBody,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result != `{"Color":"Red","Title":"Hello"}` {
		t.Errorf("result = %q", result)
	}
}

func TestResolveBodySpreadWithOtherFields(t *testing.T) {
	vars := map[string]interface{}{
		"name":   "test",
		"fields": map[string]interface{}{"Title": "Hello", "Color": "Red"},
	}
	result, err := resolveTemplate(
		`{"name": $name, ...$fields}`,
		vars, nil, "", ResolveBody,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result != `{"name": "test", "Color":"Red","Title":"Hello"}` {
		t.Errorf("result = %q", result)
	}
}

func TestResolveBodySpreadNonObjectFails(t *testing.T) {
	vars := map[string]interface{}{
		"val": "not an object",
	}
	_, err := resolveTemplate(
		`{...$val}`,
		vars, nil, "", ResolveBody,
	)
	if err == nil {
		t.Fatal("expected error for spreading non-object")
	}
	if !strings.Contains(err.Error(), "spread requires an object") {
		t.Errorf("error = %v", err)
	}
}

func TestResolveBodyLiteralDots(t *testing.T) {
	// ... not followed by $ should be literal dots
	result, err := resolveTemplate(
		`{"msg": "loading..."}`,
		nil, nil, "", ResolveBody,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result != `{"msg": "loading..."}` {
		t.Errorf("result = %q", result)
	}
}

func TestResolveHeaderToken(t *testing.T) {
	vars := map[string]interface{}{}
	result, err := resolveTemplate(
		"Bearer $token",
		vars, nil, "my-secret-token", ResolveHeader,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result != "Bearer my-secret-token" {
		t.Errorf("result = %q", result)
	}
}

func TestResolveDotPath(t *testing.T) {
	scope := map[string]interface{}{
		"value": []interface{}{"a", "b"},
	}
	result, err := resolveTemplate(
		`{"items": $.value}`,
		nil, scope, "", ResolveBody,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result != `{"items": ["a","b"]}` {
		t.Errorf("result = %q", result)
	}
}

func TestResolveBracketPath(t *testing.T) {
	scope := map[string]interface{}{
		"@odata": map[string]interface{}{
			"nextLink": "https://graph.microsoft.com/v1.0/next",
		},
	}
	result, err := resolveTemplate(
		`{"next_link": $['@odata']['nextLink']}`,
		nil, scope, "", ResolveBody,
	)
	if err != nil {
		t.Fatal(err)
	}
	expected := `{"next_link": "https://graph.microsoft.com/v1.0/next"}`
	if result != expected {
		t.Errorf("result = %q", result)
	}
}

func TestResolveUndefinedVariable(t *testing.T) {
	_, err := resolveTemplate("Hello $unknown", nil, nil, "", ResolveURL)
	if err == nil {
		t.Error("expected error for undefined variable")
	}
}

func TestResolveMultipleVarsInURL(t *testing.T) {
	vars := map[string]interface{}{
		"hostname": "contoso.sharepoint.com",
		"pathname": "/sites/my-site",
	}
	result, err := resolveTemplate(
		"https://graph.microsoft.com/v1.0/sites/$hostname:$pathname",
		vars, nil, "", ResolveURL,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result != "https://graph.microsoft.com/v1.0/sites/contoso.sharepoint.com:/sites/my-site" {
		t.Errorf("result = %q", result)
	}
}

// ── Expression Evaluation Tests ──────────────────────────────────────

func TestEvalExprHost(t *testing.T) {
	scope := map[string]interface{}{
		"url": "https://contoso.sharepoint.com/sites/my-site",
	}
	val, err := evalExpr("host($url)", nil, scope, "")
	if err != nil {
		t.Fatal(err)
	}
	if val != "contoso.sharepoint.com" {
		t.Errorf("host() = %v", val)
	}
}

func TestEvalExprPath(t *testing.T) {
	scope := map[string]interface{}{
		"url": "https://contoso.sharepoint.com/sites/my-site",
	}
	val, err := evalExpr("path($url)", nil, scope, "")
	if err != nil {
		t.Fatal(err)
	}
	if val != "/sites/my-site" {
		t.Errorf("path() = %v", val)
	}
}

func TestEvalExprJSONPath(t *testing.T) {
	scope := map[string]interface{}{
		"value": []interface{}{1, 2, 3},
	}
	val, err := evalExpr("$.value", nil, scope, "")
	if err != nil {
		t.Fatal(err)
	}
	arr, ok := val.([]interface{})
	if !ok || len(arr) != 3 {
		t.Errorf("$.value = %v", val)
	}
}

func TestEvalExprStringLiteral(t *testing.T) {
	val, err := evalExpr(`"hello world"`, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if val != "hello world" {
		t.Errorf("got %v", val)
	}
}

func TestEvalExprNumberLiteral(t *testing.T) {
	val, err := evalExpr("42", nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if val != int64(42) {
		t.Errorf("got %v", val)
	}
}

func TestEvalExprVariable(t *testing.T) {
	vars := map[string]interface{}{"name": "Alice"}
	val, err := evalExpr("$name", vars, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if val != "Alice" {
		t.Errorf("got %v", val)
	}
}

func TestEvalExprUnknownFunction(t *testing.T) {
	_, err := evalExpr("unknown($x)", nil, nil, "")
	if err == nil {
		t.Error("expected error for unknown function")
	}
}

// ── Assertion Evaluation Tests ───────────────────────────────────────

func TestEvalAssertionEqual(t *testing.T) {
	scope := map[string]interface{}{
		"next_link": "https://graph.microsoft.com/v1.0/next",
	}
	err := evalAssertion(`host($next_link) == "graph.microsoft.com"`, "Invalid nextLink", nil, scope, "")
	if err != nil {
		t.Errorf("assertion should pass: %v", err)
	}
}

func TestEvalAssertionNotEqual(t *testing.T) {
	scope := map[string]interface{}{
		"next_link": "https://evil.com/next",
	}
	err := evalAssertion(`host($next_link) == "graph.microsoft.com"`, "Invalid nextLink", nil, scope, "")
	if err == nil {
		t.Error("assertion should fail for evil.com")
	}
}

func TestEvalAssertionNE(t *testing.T) {
	vars := map[string]interface{}{"status": "ok"}
	err := evalAssertion(`$status != "error"`, "", vars, nil, "")
	if err != nil {
		t.Errorf("!= assertion should pass: %v", err)
	}
}

// ── Helpers ──────────────────────────────────────────────────────────

func findTool(tools []VirtualTool, name string) *VirtualTool {
	for i := range tools {
		if tools[i].Name == name {
			return &tools[i]
		}
	}
	return nil
}

// ── Executor tests ───────────────────────────────────────────────────

func TestExecuteVirtualToolBasicGET(t *testing.T) {
	// Spin up a test HTTP server.
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.URL.Path != "/api/hello" {
			t.Errorf("path = %q, want /api/hello", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("auth header = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"message": "hello"}`)
	}))
	defer srv.Close()

	tools, err := ParseVirtualFile([]byte(fmt.Sprintf(`[greet]
GET %s/api/hello
Authorization: Bearer $token
HTTP 200
`, srv.URL)))
	if err != nil {
		t.Fatal(err)
	}

	result, err := ExecuteVirtualTool(http.DefaultClient, tools, "greet", nil, VirtualAuth{Token: "test-token"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("server was never called")
	}
	m, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("result type = %T, want map", result)
	}
	if m["message"] != "hello" {
		t.Errorf("message = %v, want hello", m["message"])
	}
}

func TestExecuteVirtualToolPOSTWithBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("method = %q", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"name": "Ada"`) {
			t.Errorf("body = %q", body)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"id": 42, "name": "Ada"}`)
	}))
	defer srv.Close()

	tools, err := ParseVirtualFile([]byte(fmt.Sprintf(`[create]
POST %s/api/items
Content-Type: application/json

{"name": $name}
HTTP 200
`, srv.URL)))
	if err != nil {
		t.Fatal(err)
	}

	result, err := ExecuteVirtualTool(http.DefaultClient, tools, "create", map[string]interface{}{"name": "Ada"}, VirtualAuth{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := result.(map[string]interface{})
	if m["id"] != float64(42) {
		t.Errorf("id = %v, want 42", m["id"])
	}
}

func TestExecuteVirtualToolShaping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"id": 1, "displayName": "My Site", "webUrl": "https://example.com"}`)
	}))
	defer srv.Close()

	tools, err := ParseVirtualFile([]byte(fmt.Sprintf(`[get-site]
GET %s/api/site
HTTP 200
{"name": $.displayName, "url": $.webUrl}
`, srv.URL)))
	if err != nil {
		t.Fatal(err)
	}

	result, err := ExecuteVirtualTool(http.DefaultClient, tools, "get-site", nil, VirtualAuth{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := result.(map[string]interface{})
	if m["name"] != "My Site" {
		t.Errorf("name = %v", m["name"])
	}
	if m["url"] != "https://example.com" {
		t.Errorf("url = %v", m["url"])
	}
}

func TestExecuteVirtualToolUnexpectedStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		fmt.Fprintln(w, `{"error": "boom"}`)
	}))
	defer srv.Close()

	tools, err := ParseVirtualFile([]byte(fmt.Sprintf(`[oof]
GET %s/api/oops
HTTP 200
`, srv.URL)))
	if err != nil {
		t.Fatal(err)
	}

	result, err := ExecuteVirtualTool(http.DefaultClient, tools, "oof", nil, VirtualAuth{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map result, got %T", result)
	}
	if m["status"] != 500 {
		t.Errorf("status = %v, want 500", m["status"])
	}
	if !strings.Contains(fmt.Sprint(m["error"]), "unexpected status 500") {
		t.Errorf("error = %v", m["error"])
	}
	body, _ := m["body"].(map[string]interface{})
	if body["error"] != "boom" {
		t.Errorf("body.error = %v", body)
	}
}

func TestExecuteVirtualToolAssignment(t *testing.T) {
	reqNum := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqNum++
		if reqNum == 1 {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintln(w, `{"siteId": "abc-123"}`)
		} else {
			if r.URL.Path != "/api/sites/abc-123/lists" {
				t.Errorf("path = %q", r.URL.Path)
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintln(w, `{"lists": []}`)
		}
	}))
	defer srv.Close()

	tools, err := ParseVirtualFile([]byte(fmt.Sprintf(`[get-lists]
GET %s/api/site
HTTP 200
$site_id = $.siteId

GET %s/api/sites/$site_id/lists
HTTP 200
`, srv.URL, srv.URL)))
	if err != nil {
		t.Fatal(err)
	}

	result, err := ExecuteVirtualTool(http.DefaultClient, tools, "get-lists", nil, VirtualAuth{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reqNum != 2 {
		t.Errorf("reqNum = %d, want 2", reqNum)
	}
	m := result.(map[string]interface{})
	if m["lists"] == nil {
		t.Error("expected lists key in result")
	}
}

func TestExecuteVirtualToolAssertionFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"nextLink": "https://evil.com/next"}`)
	}))
	defer srv.Close()

	tools, err := ParseVirtualFile([]byte(fmt.Sprintf(`[fetch]
GET %s/api/page
HTTP 200
assert(host($.nextLink) == "graph.microsoft.com", "Invalid nextLink host")
`, srv.URL)))
	if err != nil {
		t.Fatal(err)
	}

	_, err = ExecuteVirtualTool(http.DefaultClient, tools, "fetch", nil, VirtualAuth{}, nil)
	if err == nil {
		t.Fatal("assertion should have failed")
	}
	if !strings.Contains(err.Error(), "Invalid nextLink host") {
		t.Errorf("error = %v", err)
	}
}

func TestExecuteVirtualToolNotFound(t *testing.T) {
	tools := []VirtualTool{{Name: "greet"}}
	_, err := ExecuteVirtualTool(http.DefaultClient, tools, "missing", nil, VirtualAuth{}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), `tool "missing" not found`) {
		t.Errorf("error = %v", err)
	}
}

func TestToolParamsTypeAnnotation(t *testing.T) {
	data := []byte(`[add-item] Add an item.
$fields is object
$name is string
POST /items
Authorization: Bearer $token
Content-Type: application/json
{"name": $name, ...$fields}

HTTP 201
---
[search] Search items.
GET /items?q=$query
Authorization: Bearer $token

HTTP 200
`)
	tools, err := ParseVirtualFile(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 {
		t.Fatalf("got %d tools", len(tools))
	}

	// add-item: $fields is object (from annotation), $name is string (from annotation),
	// spread also infers object for $fields but annotation already covers it
	addParams := tools[0].Params
	paramMap := map[string]ParamType{}
	for _, p := range addParams {
		paramMap[p.Name] = p.Type
	}
	if paramMap["fields"] != ParamObject {
		t.Errorf("fields type = %v, want object", paramMap["fields"])
	}
	if paramMap["name"] != ParamString {
		t.Errorf("name type = %v, want string", paramMap["name"])
	}

	// search: $query defaults to string
	searchParams := tools[1].Params
	if len(searchParams) != 1 || searchParams[0].Name != "query" {
		t.Fatalf("search params = %v", searchParams)
	}
	if searchParams[0].Type != ParamString {
		t.Errorf("query type = %v, want string", searchParams[0].Type)
	}
}

func TestToolParamsSpreadInference(t *testing.T) {
	data := []byte(`[update] Update an item.
PATCH /items/$id
Authorization: Bearer $token
Content-Type: application/json
{...$fields}

HTTP 200
`)
	tools, err := ParseVirtualFile(data)
	if err != nil {
		t.Fatal(err)
	}
	paramMap := map[string]ParamType{}
	for _, p := range tools[0].Params {
		paramMap[p.Name] = p.Type
	}
	// $fields used with spread → inferred as object
	if paramMap["fields"] != ParamObject {
		t.Errorf("fields type = %v, want object", paramMap["fields"])
	}
	// $id is just a plain reference → default string
	if paramMap["id"] != ParamString {
		t.Errorf("id type = %v, want string", paramMap["id"])
	}
}

// ── String-Spread Body Tests ─────────────────────────────────────────

func TestParseRawBody(t *testing.T) {
	input := `[upload] Upload a file's contents.
PUT https://example.com/files/$path
Authorization: Bearer $token
Content-Type: text/plain
...$content
HTTP 200
`
	tools, err := ParseVirtualFile([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	step := tools[0].Steps[0]
	if step.BodyRaw != "$content" {
		t.Errorf("BodyRaw = %q, want $content", step.BodyRaw)
	}
	if step.Body != "" {
		t.Errorf("Body should be empty, got %q", step.Body)
	}
	if len(step.Headers) != 2 {
		t.Errorf("headers = %v", step.Headers)
	}
	if _, ok := step.Responses[200]; !ok {
		t.Error("missing HTTP 200 response")
	}
}

func TestParseRawBodyBase64Expr(t *testing.T) {
	input := `[upload-image] Upload an image.
PUT https://example.com/images/$path
Content-Type: image/png
...base64dec($content)
HTTP 200
`
	tools, err := ParseVirtualFile([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	step := tools[0].Steps[0]
	if step.BodyRaw != "base64dec($content)" {
		t.Errorf("BodyRaw = %q, want base64dec($content)", step.BodyRaw)
	}
}

func TestToolParamsRawBodyString(t *testing.T) {
	data := []byte(`[upload] Upload a file.
PUT /files/$path
Content-Type: text/plain
...$content
HTTP 200
`)
	tools, err := ParseVirtualFile(data)
	if err != nil {
		t.Fatal(err)
	}
	paramMap := map[string]ParamType{}
	for _, p := range tools[0].Params {
		paramMap[p.Name] = p.Type
	}
	// A string-spread body advertises its var as a string, NOT an object
	// (unlike the in-JSON {...$fields} spread).
	if paramMap["content"] != ParamString {
		t.Errorf("content type = %v, want string", paramMap["content"])
	}
	if paramMap["path"] != ParamString {
		t.Errorf("path type = %v, want string", paramMap["path"])
	}
}

func TestEvalExprBase64Dec(t *testing.T) {
	val, err := evalExpr(`base64dec("aGVsbG8=")`, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if val != "hello" {
		t.Errorf("base64dec() = %v, want hello", val)
	}
}

func TestEvalExprBase64Enc(t *testing.T) {
	val, err := evalExpr(`base64enc("hello")`, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if val != "aGVsbG8=" {
		t.Errorf("base64enc() = %v, want aGVsbG8=", val)
	}
}

func TestEvalExprBase64DecInvalid(t *testing.T) {
	_, err := evalExpr(`base64dec("not valid b64!!!")`, nil, nil, "")
	if err == nil {
		t.Fatal("expected error for invalid base64")
	}
	if !strings.Contains(err.Error(), "base64dec()") {
		t.Errorf("error = %v", err)
	}
}

func TestExecuteVirtualToolRawBody(t *testing.T) {
	var gotBody string
	var gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		gotCT = r.Header.Get("Content-Type")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	tools, err := ParseVirtualFile([]byte(fmt.Sprintf(`[upload] Upload.
PUT %s/files
Content-Type: text/plain
...$content
HTTP 200
`, srv.URL)))
	if err != nil {
		t.Fatal(err)
	}

	// "hello $world" proves the body goes through verbatim: a $ in the
	// file data must NOT be interpreted as a variable reference.
	_, err = ExecuteVirtualTool(http.DefaultClient, tools, "upload", map[string]interface{}{"content": "hello $world"}, VirtualAuth{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotBody != "hello $world" {
		t.Errorf("body = %q, want verbatim \"hello $world\"", gotBody)
	}
	if gotCT != "text/plain" {
		t.Errorf("Content-Type = %q, want text/plain", gotCT)
	}
}

func TestExecuteVirtualToolRawBodyBase64(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	tools, err := ParseVirtualFile([]byte(fmt.Sprintf(`[upload-image] Upload image.
PUT %s/files
Content-Type: image/png
...base64dec($content)
HTTP 200
`, srv.URL)))
	if err != nil {
		t.Fatal(err)
	}

	// A PNG signature — bytes that aren't legal inside a JSON string.
	want := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	encoded := base64.StdEncoding.EncodeToString(want)
	_, err = ExecuteVirtualTool(http.DefaultClient, tools, "upload-image", map[string]interface{}{"content": encoded}, VirtualAuth{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotBody, want) {
		t.Errorf("body = %v, want %v", gotBody, want)
	}
}

func TestExecuteVirtualToolRawBodyAssignment(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	tools, err := ParseVirtualFile([]byte(fmt.Sprintf(`[upload] Upload.
$payload = $content
PUT %s/files
Content-Type: text/plain
...$payload
HTTP 200
`, srv.URL)))
	if err != nil {
		t.Fatal(err)
	}

	_, err = ExecuteVirtualTool(http.DefaultClient, tools, "upload", map[string]interface{}{"content": "raw bytes here"}, VirtualAuth{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotBody != "raw bytes here" {
		t.Errorf("body = %q, want \"raw bytes here\"", gotBody)
	}
}

func TestExecuteVirtualToolRawBodyMissingContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("server should not be called when Content-Type is missing")
	}))
	defer srv.Close()

	tools, err := ParseVirtualFile([]byte(fmt.Sprintf(`[upload] Upload.
PUT %s/files
...$content
HTTP 200
`, srv.URL)))
	if err != nil {
		t.Fatal(err)
	}

	_, err = ExecuteVirtualTool(http.DefaultClient, tools, "upload", map[string]interface{}{"content": "hi"}, VirtualAuth{}, nil)
	if err == nil {
		t.Fatal("expected error for missing Content-Type")
	}
	if !strings.Contains(err.Error(), "Content-Type") {
		t.Errorf("error = %v, want mention of Content-Type", err)
	}
}

func TestExecuteVirtualToolRawBodyNonStringFails(t *testing.T) {
	tools, err := ParseVirtualFile([]byte(`[upload] Upload.
PUT https://example.invalid/files
Content-Type: text/plain
...$fields
HTTP 200
`))
	if err != nil {
		t.Fatal(err)
	}

	_, err = ExecuteVirtualTool(http.DefaultClient, tools, "upload", map[string]interface{}{"fields": map[string]interface{}{"a": 1}}, VirtualAuth{}, nil)
	if err == nil {
		t.Fatal("expected error for non-string raw body")
	}
	if !strings.Contains(err.Error(), "requires a string") {
		t.Errorf("error = %v, want 'requires a string'", err)
	}
}

// ── SQL steps ────────────────────────────────────────────────────────

func TestParseSQLStep(t *testing.T) {
	tools, err := ParseVirtualFile([]byte(`[recent-tasks] The ten most recent tasks.

SELECT id, title, done
  FROM tasks
  WHERE done = $done
  ORDER BY created_at DESC
  LIMIT 10

{
  "tasks": $.rows
}
---
[add-task] Add a task to the list.

INSERT INTO tasks (title, done)
  VALUES ($title, 0)
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 {
		t.Fatalf("tools = %d, want 2", len(tools))
	}

	first := tools[0]
	if len(first.Steps) != 1 {
		t.Fatalf("steps = %d, want 1", len(first.Steps))
	}
	st := first.Steps[0]
	if st.Method != "" || st.URL != "" {
		t.Errorf("HTTP fields set on SQL step: method=%q url=%q", st.Method, st.URL)
	}
	wantSQL := "SELECT id, title, done\nFROM tasks\nWHERE done = :done\nORDER BY created_at DESC\nLIMIT 10"
	if st.SQL != wantSQL {
		t.Errorf("SQL = %q\nwant %q", st.SQL, wantSQL)
	}
	if len(st.SQLNames) != 1 || st.SQLNames[0] != "done" {
		t.Errorf("SQLNames = %v, want [done]", st.SQLNames)
	}
	// Shaping block attaches to the SQL step (anchor status 0).
	if sh := st.Responses[0]; sh == nil || sh.Shaping == "" {
		t.Errorf("shaping missing on SQL step: %+v", st.Responses)
	}
	// $done is a tool parameter.
	params := map[string]bool{}
	for _, p := range first.Params {
		params[p.Name] = true
	}
	if !params["done"] {
		t.Errorf("params missing done: %+v", first.Params)
	}

	second := tools[1]
	if len(second.Steps) != 1 || second.Steps[0].SQLNames[0] != "title" {
		t.Errorf("second tool steps = %+v", second.Steps)
	}
	if second.Steps[0].Responses[0] == nil {
		t.Error("second tool SQL step missing shaping anchor")
	}
}

func TestParseSQLVersusHTTPDelete(t *testing.T) {
	tools, err := ParseVirtualFile([]byte(`[drop-task]
DELETE FROM tasks WHERE id = $id
---
[delete-remote]
DELETE https://api.example.com/things/$id
`))
	if err != nil {
		t.Fatal(err)
	}
	if st := tools[0].Steps[0]; st.SQL == "" || st.Method != "" {
		t.Errorf("DELETE FROM: sql=%q method=%q — want a SQL step", st.SQL, st.Method)
	}
	if st := tools[1].Steps[0]; st.Method != "DELETE" || st.URL != "https://api.example.com/things/$id" {
		t.Errorf("DELETE url: method=%q url=%q", st.Method, st.URL)
	}
}

func TestParseSQLStringLiteralError(t *testing.T) {
	_, err := ParseVirtualFile([]byte(`[search]
SELECT id FROM tasks WHERE name LIKE '%$term%'
`))
	if err == nil || !strings.Contains(err.Error(), "string literal") {
		t.Errorf("want string-literal error, got %v", err)
	}
	// Correct form: bind the whole pattern.
	_, err = ParseVirtualFile([]byte(`[search]
SELECT id FROM tasks WHERE name LIKE $pattern
`))
	if err != nil {
		t.Errorf("bound pattern: %v", err)
	}
}

func TestParseSQLReservedAppNonce(t *testing.T) {
	_, err := ParseVirtualFile([]byte(`[bad]
SELECT * FROM t WHERE x = $app_nonce
`))
	if err == nil || !strings.Contains(err.Error(), "app_nonce") {
		t.Errorf("want app_nonce reserved error, got %v", err)
	}
	// Also refused in HTTP templates, not just SQL.
	_, err = ParseVirtualFile([]byte(`[bad]
GET https://api.example.com/$app_nonce
`))
	if err == nil || !strings.Contains(err.Error(), "app_nonce") {
		t.Errorf("want app_nonce reserved error, got %v", err)
	}
}

func TestParseSQLCompileDetails(t *testing.T) {
	// $$ escape, duplicate names deduped, quoted literals untouched
	// ($5 has no name so it stays literal; $name in a literal errors).
	tools, err := ParseVirtualFile([]byte(`[q]
SELECT 'it''s $5, cheap' AS note, x
  FROM t WHERE a = $a AND b = $a AND c = $$
`))
	if err != nil {
		t.Fatal(err)
	}
	st := tools[0].Steps[0]
	want := "SELECT 'it''s $5, cheap' AS note, x\nFROM t WHERE a = :a AND b = :a AND c = $"
	if st.SQL != want {
		t.Errorf("SQL = %q\nwant %q", st.SQL, want)
	}
	if len(st.SQLNames) != 1 || st.SQLNames[0] != "a" {
		t.Errorf("SQLNames = %v, want [a]", st.SQLNames)
	}
}

func TestExecuteSQLStepShapingAndAssignment(t *testing.T) {
	var gotSQL string
	var gotParams map[string]interface{}
	runner := func(sqlText string, params map[string]interface{}) (map[string]interface{}, error) {
		gotSQL = sqlText
		gotParams = params
		return map[string]interface{}{
			"columns": []interface{}{"id", "title"},
			"rows":    []interface{}{[]interface{}{float64(1), "one"}, []interface{}{float64(2), "two"}},
		}, nil
	}
	tools, err := ParseVirtualFile([]byte(`[recent]
SELECT id, title FROM tasks
  WHERE done = $done

{
  "firstId": $.rows.0.0,
  "firstTitle": $.rows.0.1
}
`))
	if err != nil {
		t.Fatal(err)
	}
	result, err := ExecuteVirtualTool(http.DefaultClient, tools, "recent",
		map[string]interface{}{"done": float64(0)}, VirtualAuth{}, runner)
	if err != nil {
		t.Fatal(err)
	}
	if gotSQL != "SELECT id, title FROM tasks\nWHERE done = :done" {
		t.Errorf("runner sql = %q", gotSQL)
	}
	if gotParams["done"] != float64(0) {
		t.Errorf("runner params = %v", gotParams)
	}
	m := result.(map[string]interface{})
	if m["firstId"] != float64(1) {
		t.Errorf("firstId = %v", m["firstId"])
	}
	if m["firstTitle"] != "one" {
		t.Errorf("firstTitle = %v", m["firstTitle"])
	}
}

func TestExecuteSQLStepUnresolvedVar(t *testing.T) {
	tools, err := ParseVirtualFile([]byte(`[q]
SELECT * FROM t WHERE x = $nope
`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = ExecuteVirtualTool(http.DefaultClient, tools, "q", nil, VirtualAuth{},
		func(string, map[string]interface{}) (map[string]interface{}, error) {
			return map[string]interface{}{}, nil
		})
	if err == nil || !strings.Contains(err.Error(), "unresolved $nope") {
		t.Errorf("want unresolved error, got %v", err)
	}

	// No runner wired at all.
	_, err = ExecuteVirtualTool(http.DefaultClient, tools, "q", nil, VirtualAuth{}, nil)
	if err == nil || !strings.Contains(err.Error(), "no database target") {
		t.Errorf("want no-runner error, got %v", err)
	}
}

func TestExecuteMixedHTTPAndSQL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"title": "Fetched", "body": "Content"}`)
	}))
	defer srv.Close()

	var insertSQL string
	var insertParams map[string]interface{}
	runner := func(sqlText string, params map[string]interface{}) (map[string]interface{}, error) {
		insertSQL = sqlText
		insertParams = params
		return map[string]interface{}{"rowsAffected": float64(1), "lastInsertId": float64(7)}, nil
	}

	tools, err := ParseVirtualFile([]byte(fmt.Sprintf(`[import-issue] Fetch and file.

GET %s/issues/1

HTTP 200

$title = $.title
$body = $.body

INSERT INTO issues (title, body, source)
  VALUES ($title, $body, 'github')
`, srv.URL)))
	if err != nil {
		t.Fatal(err)
	}
	result, err := ExecuteVirtualTool(http.DefaultClient, tools, "import-issue", nil, VirtualAuth{}, runner)
	if err != nil {
		t.Fatal(err)
	}
	if insertSQL != "INSERT INTO issues (title, body, source)\nVALUES (:title, :body, 'github')" {
		t.Errorf("insert sql = %q", insertSQL)
	}
	if insertParams["title"] != "Fetched" || insertParams["body"] != "Content" {
		t.Errorf("insert params = %v", insertParams)
	}
	// No shaping: the tool returns the SQL step's scope.
	m := result.(map[string]interface{})
	if m["lastInsertId"] != float64(7) {
		t.Errorf("result = %v", m)
	}
}

func TestTypeAnnotationsOptionalAndMultiName(t *testing.T) {
	tools, err := ParseVirtualFile([]byte(`[list] List issues.

$state is string?
$owner is string
$host, $search is string?

GET https://api.example.com/issues?state=$state&owner=$owner&host=$host&q=$search
`))
	if err != nil {
		t.Fatal(err)
	}
	params := map[string]struct {
		typ      ParamType
		optional bool
	}{}
	for _, p := range tools[0].Params {
		params[p.Name] = struct {
			typ      ParamType
			optional bool
		}{p.Type, p.Optional}
	}
	wantOptional := map[string]bool{"state": true, "host": true, "search": true, "owner": false}
	for name, w := range wantOptional {
		p, ok := params[name]
		if !ok {
			t.Fatalf("param %s missing: %+v", name, params)
		}
		if p.optional != w {
			t.Errorf("%s optional = %v, want %v", name, p.optional, w)
		}
		if p.typ != ParamString {
			t.Errorf("%s type = %v", name, p.typ)
		}
	}

	// The `?` binds to the declaration: one line, both names optional.
	if !params["host"].optional || !params["search"].optional {
		t.Errorf("multi-name optionality: %+v", params)
	}
}

func TestTypeAnnotationGrammar(t *testing.T) {
	// All the shapes that must parse…
	for _, line := range []string{
		"$state is string?",
		"$owner is string",
		"$host, $search is string?",
		"$a,$b,$c is number",
		"$x is object",
	} {
		if !typeAnnotationRe.MatchString(line) {
			t.Errorf("%q should match", line)
		}
	}
	// …and all the shapes that must not.
	for _, line := range []string{
		"$x is widget",
		"$x is string??",
		"$x, is string",
		"$x is string extra",
		"host is string",
		"$x are string",
	} {
		if typeAnnotationRe.MatchString(line) {
			t.Errorf("%q should NOT match", line)
		}
	}
}

func TestOptionalParamExecution(t *testing.T) {
	var gotURI string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURI = r.URL.RequestURI()
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	src := `[list] List things.

$state is string?
$owner, $path is string?

GET ` + srv.URL + `/things?state=$state&owner=$owner&path=$path&page=1
`
	tools, err := ParseVirtualFile([]byte(src))
	if err != nil {
		t.Fatal(err)
	}

	// Omitted optionals vanish from the query; literals survive.
	if _, err := ExecuteVirtualTool(http.DefaultClient, tools, "list", map[string]interface{}{}, VirtualAuth{}, nil); err != nil {
		t.Fatalf("omit optionals: %v", err)
	}
	if gotURI != "/things?page=1" {
		t.Errorf("omit optionals: uri = %q, want /things?page=1", gotURI)
	}

	// An explicit empty string is the same as omitted: no filter.
	if _, err := ExecuteVirtualTool(http.DefaultClient, tools, "list", map[string]interface{}{"state": ""}, VirtualAuth{}, nil); err != nil {
		t.Fatalf("explicit empty: %v", err)
	}
	if gotURI != "/things?page=1" {
		t.Errorf("explicit empty: uri = %q, want /things?page=1", gotURI)
	}

	// Supplied optionals stay, in position.
	if _, err := ExecuteVirtualTool(http.DefaultClient, tools, "list", map[string]interface{}{"state": "open", "path": "cmd/x"}, VirtualAuth{}, nil); err != nil {
		t.Fatalf("supplied: %v", err)
	}
	if gotURI != "/things?state=open&path=cmd%2Fx&page=1" {
		t.Errorf("supplied: uri = %q", gotURI)
	}

	// Required params stay required: absent is an error.
	reqSrc := `[get] Get one.

GET ` + srv.URL + `/things/$name
`
	reqTools, err := ParseVirtualFile([]byte(reqSrc))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteVirtualTool(http.DefaultClient, reqTools, "get", map[string]interface{}{}, VirtualAuth{}, nil); err == nil {
		t.Error("missing required param should error")
	}
}

func TestDropEmptyOptionalPairs(t *testing.T) {
	cases := []struct {
		url  string
		want string
	}{
		{"http://x/t?state=&page=1", "http://x/t?page=1"},
		{"http://x/t?page=1&state=", "http://x/t?page=1"},
		{"http://x/t?state=", "http://x/t"},
		{"http://x/t?state=open&page=1", "http://x/t?state=open&page=1"},
		{"http://x/t?other=&page=1", "http://x/t?other=&page=1"}, // not optional
		{"http://x/t", "http://x/t"},
		{"http://x/t?state=%20", "http://x/t?state=%20"}, // escaped space, not empty
	}
	opt := map[string]bool{"state": true}
	for _, c := range cases {
		if got := dropEmptyOptionalPairs(c.url, opt); got != c.want {
			t.Errorf("dropEmptyOptionalPairs(%q) = %q, want %q", c.url, got, c.want)
		}
	}
}

func TestOptionalParamSQLBindsNull(t *testing.T) {
	src := `[search] Search tasks.

$done is number?
$owner is string?

SELECT id
  FROM tasks
  WHERE done = $done AND owner = $owner
`
	tools, err := ParseVirtualFile([]byte(src))
	if err != nil {
		t.Fatal(err)
	}

	var bound map[string]interface{}
	runner := func(sqlText string, params map[string]interface{}) (map[string]interface{}, error) {
		bound = params
		return map[string]interface{}{"rows": []interface{}{}}, nil
	}

	// Omitted optional binds NULL; required param binds its value.
	if _, err := ExecuteVirtualTool(http.DefaultClient, tools, "search", map[string]interface{}{"owner": "j"}, VirtualAuth{}, runner); err != nil {
		t.Fatal(err)
	}
	if bound["done"] != nil {
		t.Errorf("omitted optional should bind NULL, got %v (%T)", bound["done"], bound["done"])
	}
	if bound["owner"] != "j" {
		t.Errorf("owner binding = %v", bound["owner"])
	}

	// Explicitly supplied optional binds the value, even "".
	if _, err := ExecuteVirtualTool(http.DefaultClient, tools, "search", map[string]interface{}{"owner": "j", "done": ""}, VirtualAuth{}, runner); err != nil {
		t.Fatal(err)
	}
	if v, ok := bound["done"]; !ok || v != "" {
		t.Errorf("explicit empty optional should bind \"\", got %v", bound["done"])
	}
}

func TestIdentityBuiltins(t *testing.T) {
	var gotAuth, gotUserHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotUserHeader = r.Header.Get("X-User")
		fmt.Fprintln(w, `{"ok": true}`)
	}))
	defer srv.Close()

	tools, err := ParseVirtualFile([]byte(fmt.Sprintf(`[whoami] Report the caller upstream.

GET %s/api/whoami
Authorization: Bearer $token
X-User: $token_email

HTTP 200

{
  "email": $token_email,
  "sub": $token_sub,
  "id": $token_id
}
`, srv.URL)))
	if err != nil {
		t.Fatal(err)
	}

	auth := VirtualAuth{Token: "tok", Email: "j@poggers.institute", Sub: "abc-123", UserID: int64(7)}
	// The caller tries to spoof every identity built-in by argument name.
	result, err := ExecuteVirtualTool(http.DefaultClient, tools, "whoami",
		map[string]interface{}{"token_email": "spoof@evil.example", "token_sub": "spoof", "token_id": float64(999)},
		auth, nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotUserHeader != "j@poggers.institute" {
		t.Errorf("X-User = %q, want the injected email", gotUserHeader)
	}
	m := result.(map[string]interface{})
	if m["email"] != "j@poggers.institute" {
		t.Errorf("email = %v, want injected value", m["email"])
	}
	if m["sub"] != "abc-123" {
		t.Errorf("sub = %v, want injected value", m["sub"])
	}
	if m["id"] != float64(7) {
		t.Errorf("id = %v (%T), want 7", m["id"], m["id"])
	}

	// Anonymous caller: identity built-ins are required — referencing one
	// without a verified identity fails, the same contract as $token.
	tools2, err := ParseVirtualFile([]byte(fmt.Sprintf(`[whoami-anon] Report the caller upstream.

GET %s/api/whoami
X-User: $token_email

HTTP 200

{
  "email": $token_email,
  "sub": $token_sub,
  "id": $token_id
}
`, srv.URL)))
	if err != nil {
		t.Fatal(err)
	}
	_, err = ExecuteVirtualTool(http.DefaultClient, tools2, "whoami-anon", nil, VirtualAuth{}, nil)
	if err == nil || !strings.Contains(err.Error(), "undefined variable: $token_email") {
		t.Errorf("anonymous caller: want undefined-variable error, got %v", err)
	}

	// Logged in upstream but no Fresh Breath account: $token_id is null,
	// the email still stamps.
	result, err = ExecuteVirtualTool(http.DefaultClient, tools2, "whoami-anon", nil,
		VirtualAuth{Email: "ghost@example.com", Sub: "ghost@example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	m = result.(map[string]interface{})
	if m["email"] != "ghost@example.com" {
		t.Errorf("ghost email = %v", m["email"])
	}
	if m["id"] != nil {
		t.Errorf("ghost id = %v, want nil", m["id"])
	}
}

func TestIdentityBuiltinsSQL(t *testing.T) {
	tools, err := ParseVirtualFile([]byte(`[stamp] Record who ran this.

INSERT INTO stamps (email, uid, sub)
  VALUES ($token_email, $token_id, $token_sub)
`))
	if err != nil {
		t.Fatal(err)
	}

	var bound map[string]interface{}
	runner := func(sqlText string, params map[string]interface{}) (map[string]interface{}, error) {
		bound = params
		return map[string]interface{}{"rows": []interface{}{}}, nil
	}

	auth := VirtualAuth{Email: "kay@example.com", Sub: "kay@example.com", UserID: int64(3)}
	if _, err := ExecuteVirtualTool(http.DefaultClient, tools, "stamp",
		map[string]interface{}{"token_email": "spoof@evil.example", "token_id": float64(999)},
		auth, runner); err != nil {
		t.Fatal(err)
	}
	if bound["token_email"] != "kay@example.com" {
		t.Errorf("email binding = %v, want injected value", bound["token_email"])
	}
	if bound["token_id"] != int64(3) {
		t.Errorf("id binding = %v (%T), want 3", bound["token_id"], bound["token_id"])
	}
	if bound["token_sub"] != "kay@example.com" {
		t.Errorf("sub binding = %v", bound["token_sub"])
	}
}
