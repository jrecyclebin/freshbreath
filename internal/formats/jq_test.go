package formats

import (
	"reflect"
	"strings"
	"testing"
)

func TestJQRunBasic(t *testing.T) {
	scope := map[string]any{"name": map[string]any{"first": "Tom"}, "age": 37.0}
	got, err := jqRun(`{first: .name.first, n: .age}`, scope, &jqVars{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("got %T: %v", got, got)
	}
	if m["first"] != "Tom" {
		t.Errorf("first = %v", m["first"])
	}
	if m["n"] != 37.0 {
		t.Errorf("n = %v", m["n"])
	}
}

func TestJQVars(t *testing.T) {
	vars := &jqVars{
		names:  []string{"$host", "$path"},
		values: map[string]any{"host": "h", "path": "/p"},
	}
	got, err := jqRun(`$host + $path`, nil, vars, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "h/p" {
		t.Errorf("got %v", got)
	}
}

func TestJQURL(t *testing.T) {
	// Full parse: scheme, authority parts, path, query, fragment.
	scope := map[string]any{"u": "https://contoso.sharepoint.com:8443/sites/x?filter=a#sec"}
	got, err := jqRun(`url(.u)`, scope, &jqVars{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("expected object, got %T", got)
	}
	want := map[string]any{
		"scheme":   "https",
		"host":     "contoso.sharepoint.com:8443",
		"hostname": "contoso.sharepoint.com",
		"port":     "8443",
		"path":     "/sites/x",
		"query":    "filter=a",
		"fragment": "sec",
	}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("url() = %v, want %v", m, want)
	}

	// Field traversal — the old host()/urlpath() drop-ins.
	h, err := jqRun(`url(.u).host`, scope, &jqVars{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if h != "contoso.sharepoint.com:8443" {
		t.Errorf("url(.).host = %v", h)
	}
	p, err := jqRun(`url(.u).path`, scope, &jqVars{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p != "/sites/x" {
		t.Errorf("url(.).path = %v", p)
	}

	// Absent parts are "" — the shape stays stable (no userinfo keys when
	// the URL carries none, no port, no query, no fragment).
	got, err = jqRun(`url("https://h.example/a")`, scope, &jqVars{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	m = got.(map[string]any)
	for _, k := range []string{"scheme", "host", "hostname", "port", "path", "query", "fragment"} {
		if _, has := m[k]; !has {
			t.Errorf("missing key %s in %v", k, m)
		}
	}
	if m["port"] != "" || m["query"] != "" || m["fragment"] != "" {
		t.Errorf("absent parts should be empty: %v", m)
	}
	if _, has := m["user"]; has {
		t.Errorf("userinfo key present without userinfo: %v", m)
	}

	// Userinfo appears only when the URL carries it.
	got, err = jqRun(`url("https://alice:secret@h.example/a")`, scope, &jqVars{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	m = got.(map[string]any)
	if m["user"] != "alice" || m["password"] != "secret" {
		t.Errorf("userinfo = %v", m)
	}

	// Composition: pipeline + assert, the get-list-next guard shape.
	okv, err := jqRun(`.u | url(.).host | assert(. == "contoso.sharepoint.com:8443"; "bad host")`, scope, &jqVars{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if okv != "contoso.sharepoint.com:8443" {
		t.Errorf("pipeline = %v", okv)
	}

	// Lenient parse: not-a-URL is a relative path, same as the old pair.
	got, err = jqRun(`url("foo").path`, scope, &jqVars{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "foo" {
		t.Errorf("lenient parse path = %v", got)
	}
}

func TestJQAssertPasses(t *testing.T) {
	// assert returns its input on success, so the pipeline carries on.
	got, err := jqRun(`. | assert(.x == 1; "nope") | .x`, map[string]any{"x": 1.0}, &jqVars{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != 1.0 {
		t.Errorf("got %v", got)
	}
}

func TestJQAssertFails(t *testing.T) {
	_, err := jqRun(`. | assert(.x == 2; "x must be 2")`, map[string]any{"x": 1.0}, &jqVars{}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "x must be 2") {
		t.Errorf("err = %v", err)
	}
}

func TestJQAssertBareStatement(t *testing.T) {
	// A bare assert(...) statement: input is scope, returns scope unchanged.
	scope := map[string]any{"nextLink": "https://graph.microsoft.com/x"}
	got, err := jqRun(`assert(url(.nextLink).host == "graph.microsoft.com"; "bad host")`, scope, &jqVars{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("expected scope returned unchanged, got %T", got)
	}
	if m["nextLink"] != "https://graph.microsoft.com/x" {
		t.Errorf("assert mutated scope: %v", got)
	}
}

func TestJQAssertBareFails(t *testing.T) {
	_, err := jqRun(`assert(url(.nextLink).host == "graph.microsoft.com"; "Invalid nextLink host")`,
		map[string]any{"nextLink": "https://evil.com/x"}, &jqVars{}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "Invalid nextLink host") {
		t.Errorf("err = %v", err)
	}
}

func TestJQDecryptNoHooks(t *testing.T) {
	_, err := jqRun(`decrypt("xyz")`, nil, &jqVars{}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "no secret key") {
		t.Errorf("err = %v", err)
	}
}

func TestJQDecryptOpens(t *testing.T) {
	hooks := &VirtualHooks{Open: func(s string) (string, error) {
		if s == "sealed" {
			return "plaintext", nil
		}
		return "", nil
	}}
	got, err := jqRun(`decrypt("sealed")`, nil, &jqVars{}, hooks)
	if err != nil {
		t.Fatal(err)
	}
	if got != "plaintext" {
		t.Errorf("got %v", got)
	}
}

func TestJQMutation(t *testing.T) {
	scope := map[string]any{"rows": []any{
		map[string]any{"id": 1.0, "password": "p1"},
		map[string]any{"id": 2.0, "password": "p2"},
	}}
	got, err := jqRun(`.rows |= map(del(.password))`, scope, &jqVars{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rows := got.(map[string]any)["rows"].([]any)
	for _, r := range rows {
		if _, has := r.(map[string]any)["password"]; has {
			t.Errorf("password survived: %v", r)
		}
	}
}

func TestJQMultipleOutputsFails(t *testing.T) {
	// .[] fans out; in our single-value contexts that's a mistake.
	_, err := jqRun(`.rows[]`, map[string]any{"rows": []any{1.0, 2.0}}, &jqVars{}, nil)
	if err == nil {
		t.Fatal("expected error for multiple outputs")
	}
}

func TestJQTimeoutCatchesRunaway(t *testing.T) {
	// range(1e9) would run effectively forever; the deadline must catch it.
	_, err := jqRun(`[range(1e9)] | length`, nil, &jqVars{}, nil)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestJQBuildVars(t *testing.T) {
	args := map[string]any{"title": "T"}
	vars := map[string]any{"site": "S"}
	auth := VirtualAuth{Token: "tok", Email: "e", Sub: "s", UserID: int64(7)}
	omitted := map[string]bool{"note": true}
	jv := buildJQVars(args, vars, auth, omitted)
	// omitted optional filled as ""
	if jv.values["note"] != "" {
		t.Errorf("note = %v", jv.values["note"])
	}
	// names sorted
	for i := 1; i < len(jv.names); i++ {
		if jv.names[i-1] > jv.names[i] {
			t.Errorf("names not sorted: %v", jv.names)
		}
	}
	// round-trip a query using all of them
	got, err := jqRun(`$title + "|" + $site + "|" + $token + "|" + ($token_id|tostring) + "|" + $note`,
		nil, jv, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "T|S|tok|7|" {
		t.Errorf("got %v", got)
	}
}

func TestJQTokenAbsentFails(t *testing.T) {
	// Unauthenticated: $token not declared → jq compile error.
	_, err := jqRun(`$token`, nil, &jqVars{}, nil)
	if err == nil {
		t.Fatal("expected error for $token when unauthenticated")
	}
}
