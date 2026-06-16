package main

import (
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
  data, err := os.ReadFile("samples/Sharepoint.txt")
  if err != nil {
    t.Fatal(err)
  }

  tools, err := parseVirtualFile(data)
  if err != nil {
    t.Fatal(err)
  }

  if len(tools) == 0 {
    t.Fatal("expected tools to be parsed")
  }
  if len(tools) != 11 {
    t.Errorf("expected 11 tools, got %d", len(tools))
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
  tools, err := parseVirtualFile([]byte(input))
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
  tools, err := parseVirtualFile([]byte(input))
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
  tools, err := parseVirtualFile([]byte(input))
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
  tools, err := parseVirtualFile([]byte(input))
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
    "name": "My List",
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

  tools, err := parseVirtualFile([]byte(fmt.Sprintf(`[greet]
GET %s/api/hello
Authorization: Bearer $token
HTTP 200
`, srv.URL)))
  if err != nil {
    t.Fatal(err)
  }

  result, err := executeVirtualTool(http.DefaultClient, tools, "greet", nil, "test-token")
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

  tools, err := parseVirtualFile([]byte(fmt.Sprintf(`[create]
POST %s/api/items
Content-Type: application/json

{"name": $name}
HTTP 200
`, srv.URL)))
  if err != nil {
    t.Fatal(err)
  }

  result, err := executeVirtualTool(http.DefaultClient, tools, "create", map[string]interface{}{"name": "Ada"}, "")
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

  tools, err := parseVirtualFile([]byte(fmt.Sprintf(`[get-site]
GET %s/api/site
HTTP 200
{"name": $.displayName, "url": $.webUrl}
`, srv.URL)))
  if err != nil {
    t.Fatal(err)
  }

  result, err := executeVirtualTool(http.DefaultClient, tools, "get-site", nil, "")
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

  tools, err := parseVirtualFile([]byte(fmt.Sprintf(`[oof]
GET %s/api/oops
HTTP 200
`, srv.URL)))
  if err != nil {
    t.Fatal(err)
  }

  result, err := executeVirtualTool(http.DefaultClient, tools, "oof", nil, "")
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

  tools, err := parseVirtualFile([]byte(fmt.Sprintf(`[get-lists]
GET %s/api/site
HTTP 200
$site_id = $.siteId

GET %s/api/sites/$site_id/lists
HTTP 200
`, srv.URL, srv.URL)))
  if err != nil {
    t.Fatal(err)
  }

  result, err := executeVirtualTool(http.DefaultClient, tools, "get-lists", nil, "")
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

  tools, err := parseVirtualFile([]byte(fmt.Sprintf(`[fetch]
GET %s/api/page
HTTP 200
assert(host($.nextLink) == "graph.microsoft.com", "Invalid nextLink host")
`, srv.URL)))
  if err != nil {
    t.Fatal(err)
  }

  _, err = executeVirtualTool(http.DefaultClient, tools, "fetch", nil, "")
  if err == nil {
    t.Fatal("assertion should have failed")
  }
  if !strings.Contains(err.Error(), "Invalid nextLink host") {
    t.Errorf("error = %v", err)
  }
}

func TestExecuteVirtualToolNotFound(t *testing.T) {
  tools := []VirtualTool{{Name: "greet"}}
  _, err := executeVirtualTool(http.DefaultClient, tools, "missing", nil, "")
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
  tools, err := parseVirtualFile(data)
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
  tools, err := parseVirtualFile(data)
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
