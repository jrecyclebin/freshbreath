package formats

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/itchyny/gojq"
)

// ── jq integration ───────────────────────────────────────────────────
//
// Reads, mutations, assertions, shaping and request bodies are all jq
// expressions. jqRun evaluates one against a scope (jq's ".") with a set
// of $-variables (caller args + assigned vars + token built-ins). url(),
// assert() and decrypt() are registered as jq functions so they compose
// in pipelines like any builtin.
//
// We parse and compile per call rather than caching compiled codes: decrypt
// needs the per-call service secret key (VirtualHooks), which is a runtime
// value a cached closure could not capture freshly. jq evaluation is cheap
// relative to the HTTP request or SQL statement a tool step exists to make.

// jqTimeout caps a single jq evaluation. jq is expressive enough to loop
// (range, recurse), so a bounded deadline keeps a bad or hostile query
// from hanging a tool call.
const jqTimeout = 5 * time.Second

// jqVars is the $-variable environment for a jq evaluation: caller args,
// assigned vars, and token built-ins (when the caller is authenticated),
// with omitted optional params filled as "" so a $name never errors
// mid-tool. names are sorted for a stable variable supply order.
//
// names carry the leading "$" (gojq's WithVariables expects "$x", not "x");
// values keys keep the bare name for lookups.
type jqVars struct {
	names  []string       // sorted, deduped, each "$prefixed"
	values map[string]any // bare name -> value
}

// jqRun evaluates a jq query against scope (.) with the given $-vars and
// returns its single result. An emitted jq error, or a second result
// (jq expressions that fan out — e.g. `.[]` without collecting — are a
// mistake in our single-value contexts), become a Go error. hooks
// supplies decrypt's per-service secret key.
func jqRun(query string, scope any, vars *jqVars, hooks *VirtualHooks) (any, error) {
	q, err := gojq.Parse(query)
	if err != nil {
		return nil, fmt.Errorf("jq: %w", err)
	}
	code, err := gojq.Compile(q,
		gojq.WithVariables(vars.names),
		gojq.WithFunction("url", 1, 1, jqURL),
		gojq.WithFunction("assert", 1, 2, jqAssert),
		gojq.WithFunction("decrypt", 1, 1, func(v any, args []any) any {
			return jqDecrypt(hooks, args)
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("jq: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), jqTimeout)
	defer cancel()
	vals := make([]any, len(vars.names))
	for i, n := range vars.names {
		vals[i] = vars.values[strings.TrimPrefix(n, "$")]
	}
	iter := code.RunWithContext(ctx, scope, vals...)

	v, ok := iter.Next()
	if !ok {
		return nil, fmt.Errorf("jq produced no output: %s", query)
	}
	if e, isErr := v.(error); isErr {
		return nil, jqErr(e)
	}
	if v2, ok2 := iter.Next(); ok2 {
		if e, isErr := v2.(error); isErr {
			return nil, jqErr(e)
		}
		return nil, fmt.Errorf("jq produced multiple outputs (wrap with [...] to collect): %s", query)
	}
	return v, nil
}

// jqErr trims gojq's framing for a cleaner, single "jq:" prefix.
func jqErr(e error) error {
	return fmt.Errorf("jq: %s", strings.TrimSpace(e.Error()))
}

// buildJQVars assembles the $-variable environment for a step: caller args
// (persisting across steps, unlike "." which each response replaces),
// assigned vars, and the authenticated token built-ins. Omitted optional
// params resolve to "" — the honest "not given" string for a template.
//
// token is present only when auth.Token is set, so a tool referencing
// $token without a logged-in caller fails with jq's "undefined $token"
// at compile time — the same gate the old resolver enforced.
func buildJQVars(args map[string]any, vars map[string]any, auth VirtualAuth, omittedOptionals map[string]bool) *jqVars {
	values := make(map[string]any, len(args)+len(vars)+4)
	for k, v := range args {
		values[k] = v
	}
	for k, v := range vars {
		values[k] = v
	}
	for name := range omittedOptionals {
		if _, ok := values[name]; !ok {
			values[name] = ""
		}
	}
	if auth.Token != "" {
		values["token"] = auth.Token
	}
	if auth.Email != "" {
		values["token_email"] = auth.Email
		values["token_sub"] = auth.Sub
		values["token_id"] = jqNormalize(auth.UserID)
	}
	bare := make([]string, 0, len(values))
	for n := range values {
		bare = append(bare, n)
	}
	sort.Strings(bare)
	names := make([]string, len(bare))
	for i, n := range bare {
		names[i] = "$" + n
	}
	return &jqVars{names: names, values: values}
}

// ── jq functions ─────────────────────────────────────────────────────

// jqURL is url(<url>): a parsed URL as a jq object. All fields are
// strings; absent parts are "" so the shape is stable:
//
//	scheme, host, hostname, port, path, query, fragment
//	user, password   (only when the URL carries userinfo)
//
// host is the authority — hostname plus :port when one is present — so
// url($x).host is the drop-in for the old host() and url($x).path for the
// old urlpath(). query is the raw query string ("a=1&b=2"), not a parsed
// object — it splices into derived URLs, and parsing it in jq is possible
// when a tool needs to. A string that isn't a URL parses leniently as a
// relative path (Go's url.Parse), same as the old pair: url("foo").path
// is "foo".
func jqURL(v any, args []any) any {
	u, err := url.Parse(jqToString(args[0]))
	if err != nil {
		return fmt.Errorf("url(): %w", err)
	}
	obj := map[string]any{
		"scheme":   u.Scheme,
		"host":     u.Host,
		"hostname": u.Hostname(),
		"port":     u.Port(),
		"path":     u.Path,
		"query":    u.RawQuery,
		"fragment": u.Fragment,
	}
	if u.User != nil {
		obj["user"] = u.User.Username()
		if pw, ok := u.User.Password(); ok {
			obj["password"] = pw
		}
	}
	return obj
}

// jqAssert is assert(cond; msg). It returns its input unchanged on
// success so a pipeline carries on (`. | assert(...) | ...`), and
// returns a Go error on failure — gojq surfaces that as an iteration
// error which jqRun reports. cond uses jq falsiness (only false and
// null fail); msg is optional and defaults to "assertion failed".
func jqAssert(v any, args []any) any {
	if jqTruthy(args[0]) {
		return v
	}
	msg := "assertion failed"
	if len(args) > 1 {
		if s, ok := args[1].(string); ok && s != "" {
			msg = s
		}
	}
	return fmt.Errorf("%s", msg)
}

// jqTruthy reports whether v is truthy in jq: everything except false
// and null is truthy (0 and "" are truthy, unlike most languages).
func jqTruthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	default:
		return true
	}
}

// jqDecrypt is decrypt(<sealed>): opens a value this service sealed.
// The secret key is per-service and per-call (VirtualHooks.Open); a nil
// hook means this server can't open anything, which the old resolver
// reported with the same message.
func jqDecrypt(hooks *VirtualHooks, args []any) any {
	s, ok := args[0].(string)
	if !ok {
		return fmt.Errorf("decrypt(): requires a string, got %T", args[0])
	}
	if hooks == nil || hooks.Open == nil {
		return fmt.Errorf("decrypt(): this server has no secret key for the service")
	}
	plain, err := hooks.Open(s)
	if err != nil {
		// Never echo the argument: it's either ciphertext or something
		// that was meant to be.
		return fmt.Errorf("decrypt(): value was not encrypted by this service")
	}
	return plain
}

// jqNormalize converts integer types gojq's encoder rejects (int64,
// uint64, and the sized variants) into int or float64, recursively through
// maps and slices. gojq accepts int64 as a *passthrough* input but panics
// the moment one reaches an encoder (tostring, @base64, @json, sorting,
// arithmetic that re-emits). SQL results and token_id carry int64, so
// anything bound for jq is normalized first. json.Unmarshal's float64
// and gojq's own int/float64/*big.Int/json.Number pass through untouched.
// Maps and slices are mutated in place: callers only pass trees they own
// (a freshly-built SQL result, a freshly-parsed HTTP body, the var set).
func jqNormalize(v any) any {
	switch x := v.(type) {
	case int64:
		return int(x)
	case uint64:
		if x <= uint64(math.MaxInt64) {
			return int(x)
		}
		return float64(x)
	case int32:
		return int(x)
	case uint32:
		return int(x)
	case int:
		return v
	case []any:
		for i, e := range x {
			x[i] = jqNormalize(e)
		}
		return x
	case map[string]any:
		for k, e := range x {
			x[k] = jqNormalize(e)
		}
		return x
	default:
		return v
	}
}

// jqToString coerces a jq value to a string for URL parsing. nil → "",
// bool → its word, numbers/objects/arrays use fmt's %v rendering (good
// enough for a URL that was stored as a string; a non-string input to
// url() is almost certainly a mistake, but we don't hard-fail
// to stay lenient like the old helpers).
func jqToString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	case bool:
		if x {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprintf("%v", v)
	}
}
