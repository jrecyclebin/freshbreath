package formats

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"poggers.institute/freshbreath/internal/utils"
)

// ── Data Structures ──────────────────────────────────────────────────

// ParamType describes the JSON Schema type of a tool parameter.
type ParamType string

const (
	ParamString ParamType = "string"
	ParamObject ParamType = "object"
	ParamNumber ParamType = "number"
	ParamBool   ParamType = "boolean"
	ParamArray  ParamType = "array"

	// ParamEncrypted is a string the caller sends in plaintext and the tool
	// only ever sees sealed: the executor encrypts it on the way in, so it
	// can be stored as-is and opened later with decrypt(). Non-string values
	// are JSON-stringified first. It's a plain string on the wire.
	ParamEncrypted ParamType = "encrypted"

	// String-constrained types: variables typed with these carry a JSON
	// Schema `format` on the wire (tool input schemas and elicitation form
	// schemas), which helps models and host form UIs; they resolve as plain
	// strings in templates and SQL.
	ParamEmail    ParamType = "email"
	ParamURI      ParamType = "uri"
	ParamDate     ParamType = "date"
	ParamDateTime ParamType = "date-time"

	// ParamPath is a string that resolves raw in a URL *path*: its slashes
	// are structure, not content. It is the opt-out for URL path escaping —
	// by default a substituted value is percent-escaped as a single path
	// segment (url.PathEscape), so a $var can't walk the path or smuggle a
	// `?` into the query. Declare `$route is path` for a variable that
	// intentionally carries whole segments.
	ParamPath ParamType = "path"
)

// stringFormats maps the format-typed ParamTypes to their JSON Schema
// `format` value. Everything else resolves by type name alone.
var stringFormats = map[ParamType]string{
	ParamEmail:    "email",
	ParamURI:      "uri",
	ParamDate:     "date",
	ParamDateTime: "date-time",
}

// ToolParam describes an input parameter for a virtual tool.
type ToolParam struct {
	Name     string
	Type     ParamType
	Format   string   // JSON Schema format for the *-typed string params
	Optional bool     // from a `?` annotation; parameters are required by default
	Values   []string // declared enum values; non-nil when the param is an enum
}

// VirtualTool defines a single tool in a virtual service description.
type VirtualTool struct {
	Name            string
	Description     string
	AppOnly         bool        // declared with a trailing "!" — app-only visibility
	Params          []ToolParam // input parameters inferred from template references
	Steps           []VirtualStep
	typeAnnotations []typeAnnotation // parsed from "$name is type" lines, unexported
}

type typeAnnotation struct {
	names    []string // one or more: "$a, $b is number"
	typ      ParamType
	format   string // "email"/"uri"/... for the format-typed string params
	optional bool
	values   []string // non-nil for enum annotations: $x is "a" | "b"
}

// FormField is one input of a FORM elicitation step. Fields use the same
// annotation syntax as tool parameters ("$x is type", enums, optional "?")
// but bind runtime scope from the user's answers instead of caller arguments.
type FormField struct {
	Name     string
	Type     ParamType
	Format   string
	Optional bool
	Values   []string // enum choices
}

// VirtualForm is a FORM directive: a blocking form elicitation whose result
// replaces the scope (like every step output).
type VirtualForm struct {
	Message string      // $-interpolated message shown above the form
	Fields  []FormField // declared on indented lines under the directive
}

// VirtualElicit is an elicitation directive step: FORM (blocking form) or URL
// (URL-mode elicitation handing the user a link out-of-band).
type VirtualElicit struct {
	Form    *VirtualForm // non-nil for FORM steps
	URL     string       // URL template for URL steps
	Message string       // message template (may be empty for URL steps)
}

// VirtualStep is one step within a tool's script — an HTTP request, a SQL
// statement, an elicitation, or a pre/post-processing block of jq statements.
type VirtualStep struct {
	// Pre is the ordered list of jq statements that run before this step's
	// request, against the scope left by the previous step (caller args for
	// the first step). Assignments ($x = expr) bind a $-variable; bare
	// expressions transform "." (e.g. ".rows |= map(del(.password))") or check
	// it (assert(...; ...)). Source order is preserved so an assert can follow
	// a mutation in the same block.
	Pre       []VirtualStmt
	Method    string // GET, POST, PUT, PATCH, DELETE
	URL       string // URL template with $variable interpolation
	Headers   map[string]string
	Body      string                   // jq expression; its result is the request body (string → raw bytes, else JSON)
	SQL       string                   // Compiled SQL: $var → :var bindings
	SQLNames  []string                 // Unique bind names, first-appearance order
	Responses map[int]*VirtualResponse // Expected status → response handling (0 = SQL step shaping)
	Elicit    *VirtualElicit           // FORM or URL directive: elicitation step
}

// VirtualStmt is one line of a step's pre-block: either an assignment
// (Assign non-nil, the new $-variable's bare name) or a bare jq
// expression whose result replaces "." (a mutation) or is discarded
// after a check (an assert, which passes its input through).
type VirtualStmt struct {
	Assign *string // bare variable name for "$x = expr"; nil for a bare expression
	Expr   string  // jq expression
}

// VirtualResponse describes what to do after receiving a particular status code.
type VirtualResponse struct {
	Shaping string // jq expression; its result is the tool's return value
}

// ── Parser ───────────────────────────────────────────────────────────

// A trailing "!" marks a tool app-only: it is listed with
// _meta.ui.visibility: ["app"] (MCP Apps), meaning hosts hide it from the
// model and only the app UI may call it. The mark is authoring syntax — the
// exposed tool name is the part before it.
var toolNameRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*!?$`)
var plainVarRe = regexp.MustCompile(`\$([a-zA-Z_]\w*)`)

// ParseVirtualFile parses a virtual service description file into tools.
// Tools are separated by --- delimiters and begin with [name] headers.
func ParseVirtualFile(data []byte) ([]VirtualTool, error) {
	var tools []VirtualTool
	var cur *VirtualTool
	var curLines []string

	seenNames := map[string]bool{}
	flush := func() error {
		if cur != nil {
			if err := parseVirtualToolBody(cur, curLines); err != nil {
				return fmt.Errorf("tool %s: %w", cur.Name, err)
			}
			if err := rejectReservedParams(*cur); err != nil {
				return fmt.Errorf("tool %s: %w", cur.Name, err)
			}
			cur.Params = toolParams(*cur)
			key := strings.ToLower(cur.Name)
			if seenNames[key] {
				return fmt.Errorf("duplicate tool name %q (tool names must be unique; [foo] and [foo!] collide)", cur.Name)
			}
			seenNames[key] = true
			tools = append(tools, *cur)
		}
		cur = nil
		curLines = nil
		return nil
	}

	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)

		if trimmed == "---" {
			if err := flush(); err != nil {
				return nil, err
			}
			continue
		}

		if name, desc, appOnly, ok := parseVirtualToolHeader(trimmed); ok {
			if err := flush(); err != nil {
				return nil, err
			}
			cur = &VirtualTool{Name: strings.TrimSuffix(name, "!"), Description: desc, AppOnly: appOnly}
			curLines = nil
			continue
		}

		if cur != nil {
			curLines = append(curLines, line)
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}

	return tools, nil
}

// toolParams scans all template strings and expressions in a tool's steps and
// returns the variable names that are referenced but not locally assigned — i.e.
// the parameters the caller must supply. $token is excluded (it's the auth token).
//
// Types are inferred:
//   - Explicit type annotations (e.g. "$fields is object") override inference.
//   - All other parameters default to "string".
var typeAnnotationRe = regexp.MustCompile(
	`^\$([a-zA-Z_]\w*(?:\s*,\s*\$[a-zA-Z_]\w*)*)\s+is\s+(string|object|number|boolean|array|encrypted|email|uri|date-time|date|path)(\?)?$`)

// enumAnnotationRe matches an enum declaration: "$name is "a" | 'b' | ..." with
// an optional trailing `?`. The values are quote-delimited (double or single,
// no escapes — a value cannot contain its own quote character), one or more,
// separated by `|`. Like type annotations, multiple names may share one
// declaration ("$a, $b is "x" | "y""). The first token after `is ` MUST be a
// quoted value, so this regex is disjoint from typeAnnotationRe (which
// requires a bare type word there) — a line can match at most one.
var enumAnnotationRe = regexp.MustCompile(
	`^\$([a-zA-Z_]\w*(?:\s*,\s*\$[a-zA-Z_]\w*)*)\s+is\s+((?:"[^"]*"|'[^']*')(?:\s*\|\s*(?:"[^"]*"|'[^']*'))*)(\?)?$`)

// enumValueRe finds one quoted token (double- or single-quoted) in an enum
// body. Used after enumAnnotationRe has already validated the overall
// shape, so every match is a value.
var enumValueRe = regexp.MustCompile(`"[^"]*"|'[^']*'`)

// parseEnumValues splits an enumAnnotationRe body (group 2) into its bare
// values, stripping the surrounding quotes. An empty quoted token ("" or ”)
// is a legitimate value meaning the empty string.
func parseEnumValues(s string) []string {
	tokens := enumValueRe.FindAllString(s, -1)
	values := make([]string, len(tokens))
	for i, t := range tokens {
		values[i] = t[1 : len(t)-1]
	}
	return values
}

func toolParams(tool VirtualTool) []ToolParam {
	// token* are server-injected (auth token + identity claims), never caller params.
	defined := map[string]bool{"token": true, "token_email": true, "token_sub": true, "token_id": true, "elicitation_url": true, "handoff_url": true}
	for _, step := range tool.Steps {
		// An assignment's LHS binds a $-variable locally; its RHS is scanned
		// below. FORM fields bind runtime scope from the user's answers, not
		// caller arguments — they must never surface as caller params.
		for _, st := range step.Pre {
			if st.Assign != nil {
				defined[*st.Assign] = true
			}
		}
		if step.Elicit != nil && step.Elicit.Form != nil {
			for _, f := range step.Elicit.Form.Fields {
				defined[f.Name] = true
			}
		}
	}

	seen := map[string]bool{}
	types := map[string]ParamType{} // name → inferred/annotated type
	formats := map[string]string{}  // name → JSON Schema format (email/uri/…)

	// scan finds $name references a template or jq expression introduces.
	// $name in a jq expression is a jq variable backed by a caller arg.
	scan := func(s string) {
		for _, m := range plainVarRe.FindAllStringSubmatch(s, -1) {
			if name := m[1]; !defined[name] {
				seen[name] = true
			}
		}
	}

	for _, step := range tool.Steps {
		scan(step.URL)
		for _, v := range step.Headers {
			scan(v)
		}
		scan(step.Body)
		if step.Elicit != nil {
			if step.Elicit.Form != nil {
				scan(step.Elicit.Form.Message)
			}
			scan(step.Elicit.Message)
			scan(step.Elicit.URL)
		}
		// SQL steps keep their bind names from compilation — scan those.
		for _, name := range step.SQLNames {
			if !defined[name] {
				seen[name] = true
			}
		}
		for _, st := range step.Pre {
			scan(st.Expr)
		}
		for _, resp := range step.Responses {
			scan(resp.Shaping)
		}
	}

	// Apply explicit type annotations. The `?` binds to the declaration,
	// so "$host, $search is string?" makes both optional; mixed optionality
	// is two lines.
	optional := map[string]bool{}
	enumValues := map[string][]string{}
	for _, ann := range tool.typeAnnotations {
		for _, name := range ann.names {
			if _, ok := seen[name]; ok {
				types[name] = ann.typ
				if ann.format != "" {
					formats[name] = ann.format
				}
				if ann.optional {
					optional[name] = true
				}
				if ann.values != nil {
					enumValues[name] = ann.values
				}
			}
		}
	}

	params := make([]ToolParam, 0, len(seen))
	for name := range seen {
		t := ParamString
		if pt, ok := types[name]; ok {
			t = pt
		}
		params = append(params, ToolParam{Name: name, Type: t, Format: formats[name], Optional: optional[name], Values: enumValues[name]})
	}
	sort.Slice(params, func(i, j int) bool { return params[i].Name < params[j].Name })
	return params
}

// parseVirtualToolHeader validates a [name] header for virtual tools.
// Names must be valid identifiers (letters, digits, underscores, hyphens),
// optionally followed by "!" to mark the tool app-only.
func parseVirtualToolHeader(line string) (name, desc string, appOnly, ok bool) {
	name, desc, ok = parseHeader(line)
	if !ok || !toolNameRe.MatchString(name) {
		return "", "", false, false
	}
	return name, desc, strings.HasSuffix(name, "!"), true
}

// parseVirtualToolBody parses the lines of a tool definition into steps.
//
// A step is a pre-block of jq statements (assignments and bare mutations/
// asserts) followed by at most one request (HTTP, SQL, FORM or URL). After a
// request, a shaping block ({...}) terminates the tool with a jq result;
// otherwise the next step's pre-block begins. Lines are dispatched by a
// small state machine (sPre, sHeaders, sResp). Multiline jq expressions —
// bodies, shaping blocks and bare statements — are consumed whole by
// collectJQ, which tracks bracket depth so there is no separate body or
// shaping state to carry across iterations.
func parseVirtualToolBody(tool *VirtualTool, lines []string) error {
	var step *VirtualStep
	var lastHTTPStatus int

	const (
		sPre = iota
		sHeaders
		sResp
	)
	state := sPre

	newStep := func() {
		if step != nil && (step.Method != "" || step.SQL != "" || step.Elicit != nil || len(step.Pre) > 0 || shapingOf(step) != "") {
			tool.Steps = append(tool.Steps, *step)
		}
		step = &VirtualStep{Responses: make(map[int]*VirtualResponse)}
		state = sPre
	}
	newStep()

	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// In the response state, anything that isn't a shaping block ("{")
		// or another status ("HTTP ") begins a new step: an assignment, a
		// bare jq mutation/assert, or a request/SQL/FORM/URL. collectJQ and
		// the request parsers consume their own continuation lines, so there
		// is no shaping/body state to finalize before switching.
		if state == sResp && startsNewStep(line) {
			newStep()
		}

		switch state {
		case sPre:
			if ok, vn, ex := tryParseAssignment(line); ok {
				name := vn
				step.Pre = append(step.Pre, VirtualStmt{Assign: &name, Expr: ex})
			} else if ann, ok := parseAnnotation(line); ok {
				tool.typeAnnotations = append(tool.typeAnnotations, ann)
			} else if ok, form := tryParseForm(line); ok {
				// FORM directive: a blocking form elicitation whose answers
				// replace the scope. Field declarations use the same annotation
				// syntax as tool parameters on indented continuation lines
				// (SQL-style): ends at the first non-indented line; blanks and
				// indented # comments are skipped.
				j := i + 1
				for j < len(lines) {
					raw := lines[j]
					t := strings.TrimSpace(raw)
					if t == "" {
						j++
						continue
					}
					if raw[0] != ' ' && raw[0] != '\t' {
						break
					}
					if !strings.HasPrefix(t, "#") {
						field, err := parseFormField(t)
						if err != nil {
							return err
						}
						form.Fields = append(form.Fields, field)
					}
					j++
				}
				i = j - 1
				step.Elicit = &VirtualElicit{Form: form}
				// Forms have no status line; anchor any shaping block to 0.
				step.Responses[0] = &VirtualResponse{}
				lastHTTPStatus = 0
				state = sResp
			} else if ok, urlTmpl, msg := tryParseElicitURL(line); ok {
				// URL directive: URL-mode elicitation. One line — the resolved
				// link is handed to the user through the host's elicitation UI;
				// there are no headers or bodies. Shaping may follow, anchored
				// to 0 like SQL shaping.
				step.Elicit = &VirtualElicit{URL: urlTmpl, Message: msg}
				step.Responses[0] = &VirtualResponse{}
				lastHTTPStatus = 0
				state = sResp
			} else if isSQLVerbLine(line) {
				// SQL step: the verb line plus every indented continuation,
				// against the RAW line (the loop's trim already happened).
				// Ends at the first non-indented line. Blank lines and
				// indented # comments are skipped, not collected. Checked
				// before tryParseRequest: "DELETE FROM" is SQL, not a
				// DELETE to the URL "FROM …".
				sqlLines := []string{line}
				j := i + 1
				for j < len(lines) {
					raw := lines[j]
					t := strings.TrimSpace(raw)
					if t == "" {
						j++
						continue
					}
					if raw[0] != ' ' && raw[0] != '\t' {
						break
					}
					if !strings.HasPrefix(t, "#") {
						sqlLines = append(sqlLines, t)
					}
					j++
				}
				i = j - 1
				compiled, names, err := compileSQL(strings.Join(sqlLines, "\n"))
				if err != nil {
					return err
				}
				step.SQL = compiled
				step.SQLNames = names
				// SQL has no status line; anchor any shaping block to 0.
				step.Responses[0] = &VirtualResponse{}
				lastHTTPStatus = 0
				state = sResp
			} else if ok, m, u := tryParseRequest(line); ok {
				step.Method = m
				step.URL = u
				state = sHeaders
			} else if strings.HasPrefix(line, "{") {
				// A shaping block with no request of its own — the tool's
				// return value, assembled from variables gathered across
				// earlier steps. Anchored at 0 like SQL shaping: there is no
				// status line here to hang it on.
				step.Responses[0] = &VirtualResponse{}
				lastHTTPStatus = 0
				expr, k := collectJQ(lines, i)
				step.Responses[0].Shaping = expr
				i = k
				state = sResp
			} else {
				// Bare jq statement: a mutation (".rows |= map(del(.password))")
				// or an assert ("assert(host(.x) == \"y\"; \"msg\")") that runs
				// against the current scope before the next request.
				expr, k := collectJQ(lines, i)
				step.Pre = append(step.Pre, VirtualStmt{Expr: expr})
				i = k
			}

		case sHeaders:
			if ok, code := tryParseHTTPStatus(line); ok {
				step.Responses[code] = &VirtualResponse{}
				lastHTTPStatus = code
				state = sResp
			} else if k, v, ok := parseHeaderLine(line); ok {
				if step.Headers == nil {
					step.Headers = make(map[string]string)
				}
				step.Headers[k] = v
			} else {
				// The request body is a jq expression. Its result is sent
				// verbatim when it's a string (with a required Content-Type),
				// or JSON-encoded otherwise — handled at execution. Anything
				// that isn't a header or an HTTP status begins the body.
				expr, k := collectJQ(lines, i)
				step.Body = expr
				i = k
				state = sResp
			}

		case sResp:
			if strings.HasPrefix(line, "{") {
				// Shaping: a jq object expression whose result is the tool's
				// return value. It terminates the tool at execution.
				expr, k := collectJQ(lines, i)
				if block := step.Responses[lastHTTPStatus]; block != nil {
					block.Shaping = expr
				}
				i = k
			} else if ok, code := tryParseHTTPStatus(line); ok {
				step.Responses[code] = &VirtualResponse{}
				lastHTTPStatus = code
			}
		}
	}

	// Finalize last step
	if step != nil && (step.Method != "" || step.SQL != "" || step.Elicit != nil || len(step.Pre) > 0 || shapingOf(step) != "") {
		tool.Steps = append(tool.Steps, *step)
	}
	return nil
}

// ── Line Classifiers ─────────────────────────────────────────────────

var identRe = regexp.MustCompile(`^[a-zA-Z_]\w*$`)

func tryParseAssignment(line string) (ok bool, varName, expr string) {
	if !strings.HasPrefix(line, "$") {
		return false, "", ""
	}
	rest := line[1:]
	eqIdx := strings.Index(rest, " = ")
	if eqIdx < 0 {
		return false, "", ""
	}
	name := rest[:eqIdx]
	if !identRe.MatchString(name) {
		return false, "", ""
	}
	return true, name, strings.TrimSpace(rest[eqIdx+3:])
}

func tryParseRequest(line string) (ok bool, method, reqURL string) {
	for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
		if strings.HasPrefix(line, m+" ") {
			return true, m, strings.TrimSpace(line[len(m)+1:])
		}
	}
	return false, "", ""
}

// sqlVerbs are the statement openers that make a line a SQL step. SQL's
// DELETE is always followed by FROM; HTTP's DELETE is always followed by
// something with a scheme — that one word is the whole disambiguation.
var sqlVerbs = []string{
	"SELECT ", "INSERT ", "UPDATE ", "DELETE FROM ", "REPLACE ",
	"WITH ", "CREATE ", "DROP ", "ALTER ",
}

func isSQLVerbLine(line string) bool {
	u := strings.ToUpper(line)
	for _, v := range sqlVerbs {
		if strings.HasPrefix(u, v) {
			return true
		}
	}
	return false
}

var sqlNameRe = regexp.MustCompile(`^[a-zA-Z_]\w*`)

// compileSQL turns $var references into :var named-parameter bindings and
// returns the bind names in first-appearance order. A $var inside a single-
// quoted SQL string literal is an error, not a best-effort splice: binding
// can't reach inside a literal, so `LIKE '%$term%'` must become
// `LIKE $pattern` with the caller supplying the wildcards.
func compileSQL(raw string) (string, []string, error) {
	var b strings.Builder
	var names []string
	seen := map[string]bool{}
	inStr := false
	for i := 0; i < len(raw); {
		ch := raw[i]
		if inStr {
			if ch == '\'' {
				if i+1 < len(raw) && raw[i+1] == '\'' { // '' escape
					b.WriteString("''")
					i += 2
					continue
				}
				inStr = false
				b.WriteByte(ch)
				i++
				continue
			}
			if ch == '$' {
				if m := sqlNameRe.FindString(raw[i+1:]); m != "" {
					return "", nil, fmt.Errorf("$%s is inside a SQL string literal; bind it outside instead (e.g. WHERE name LIKE $pattern, caller supplies the wildcards)", m)
				}
			}
			b.WriteByte(ch)
			i++
			continue
		}
		switch ch {
		case '\'':
			inStr = true
			b.WriteByte(ch)
			i++
		case '$':
			if i+1 < len(raw) && raw[i+1] == '$' { // $$ → literal $
				b.WriteByte('$')
				i += 2
				continue
			}
			m := sqlNameRe.FindString(raw[i+1:])
			if m == "" {
				return "", nil, fmt.Errorf("stray $ in SQL step")
			}
			b.WriteByte(':')
			b.WriteString(m)
			if !seen[m] {
				seen[m] = true
				names = append(names, m)
			}
			i += 1 + len(m)
		default:
			b.WriteByte(ch)
			i++
		}
	}
	return b.String(), names, nil
}

// rejectReservedParams refuses tools that reference $app_nonce themselves.
// app_nonce is the parameter the SERVER synthesizes for default-target SQL
// tools; a template's own use of the name would be silently shadowed, so
// it's a load-time error instead.
func rejectReservedParams(tool VirtualTool) error {
	check := func(s string) error {
		for _, m := range plainVarRe.FindAllStringSubmatch(s, -1) {
			if m[1] == "app_nonce" {
				return fmt.Errorf("$app_nonce is reserved (the server supplies it for database tools); rename the parameter")
			}
		}
		return nil
	}
	for _, st := range tool.Steps {
		for _, s := range []string{st.URL, st.Body} {
			if err := check(s); err != nil {
				return err
			}
		}
		if st.Elicit != nil {
			msgs := []string{st.Elicit.Message, st.Elicit.URL}
			if st.Elicit.Form != nil {
				msgs = append(msgs, st.Elicit.Form.Message)
			}
			for _, s := range msgs {
				if err := check(s); err != nil {
					return err
				}
			}
			if st.Elicit.Form != nil {
				for _, f := range st.Elicit.Form.Fields {
					if f.Name == "app_nonce" {
						return fmt.Errorf("$app_nonce is reserved (the server supplies it for database tools); rename the FORM field")
					}
				}
			}
		}
		for _, v := range st.Headers {
			if err := check(v); err != nil {
				return err
			}
		}
		for _, name := range st.SQLNames {
			if name == "app_nonce" {
				return fmt.Errorf("$app_nonce is reserved (the server supplies it for database tools); rename the parameter")
			}
		}
		for _, st2 := range st.Pre {
			if err := check(st2.Expr); err != nil {
				return err
			}
		}
		for _, resp := range st.Responses {
			if err := check(resp.Shaping); err != nil {
				return err
			}
		}
	}
	return nil
}

// parseAnnotation parses one "$names is spec" line — a type annotation or an
// enum declaration — into a typeAnnotation. Returns ok=false when the line
// isn't an annotation.
func parseAnnotation(line string) (typeAnnotation, bool) {
	if m := enumAnnotationRe.FindStringSubmatch(line); m != nil {
		var names []string
		for _, part := range strings.Split(m[1], ",") {
			names = append(names, strings.TrimPrefix(strings.TrimSpace(part), "$"))
		}
		return typeAnnotation{names: names, typ: ParamString, optional: m[3] == "?", values: parseEnumValues(m[2])}, true
	}
	if m := typeAnnotationRe.FindStringSubmatch(line); m != nil {
		var names []string
		for _, part := range strings.Split(m[1], ",") {
			names = append(names, strings.TrimPrefix(strings.TrimSpace(part), "$"))
		}
		ann := typeAnnotation{names: names, optional: m[3] == "?"}
		if f, ok := stringFormats[ParamType(m[2])]; ok {
			ann.typ = ParamString
			ann.format = f
		} else {
			ann.typ = ParamType(m[2])
		}
		return ann, true
	}
	return typeAnnotation{}, false
}

// parseFormField parses one FORM field declaration. Fields reuse the tool
// parameter annotation syntax minus object/array: elicitation schemas are
// flat primitives (string, number, boolean, enum, formatted string), and
// arrays exist in the MCP form subset only for multi-select enums, which the
// single-field annotation can't express.
func parseFormField(line string) (FormField, error) {
	ann, ok := parseAnnotation(line)
	if !ok {
		return FormField{}, fmt.Errorf("invalid FORM field %q: expected an annotation like $name is string", line)
	}
	if len(ann.names) != 1 {
		return FormField{}, fmt.Errorf("FORM fields are declared one per line: %q", line)
	}
	if ann.typ == ParamObject || ann.typ == ParamArray || ann.typ == ParamEncrypted || ann.typ == ParamPath {
		return FormField{}, fmt.Errorf("FORM field $%s: %s fields are not supported (forms take string, number, boolean, enum, or a formatted string)", ann.names[0], ann.typ)
	}
	return FormField{Name: ann.names[0], Type: ann.typ, Format: ann.format, Optional: ann.optional, Values: ann.values}, nil
}

// tryParseForm matches a FORM directive: "FORM <message>". The message is
// plain text or a quoted string (quotes stripped); either way it stays
// $-interpolated at execution time.
func tryParseForm(line string) (ok bool, form *VirtualForm) {
	if !strings.HasPrefix(line, "FORM ") {
		return false, nil
	}
	return true, &VirtualForm{Message: unquoteText(strings.TrimSpace(line[len("FORM "):]))}
}

// tryParseElicitURL matches a URL directive: "URL <template> [message]".
// The template is the first whitespace-delimited token (URL templates never
// contain spaces); the remainder, if present, is the descriptive text shown
// with the elicitation, quoted or plain.
func tryParseElicitURL(line string) (ok bool, urlTmpl, msg string) {
	if !strings.HasPrefix(line, "URL ") {
		return false, "", ""
	}
	rest := strings.TrimSpace(line[len("URL "):])
	if rest == "" {
		return false, "", ""
	}
	urlTmpl = rest
	if idx := strings.IndexAny(rest, " \t"); idx >= 0 {
		urlTmpl = strings.TrimSpace(rest[:idx])
		msg = unquoteText(strings.TrimSpace(rest[idx+1:]))
	}
	return true, urlTmpl, msg
}

// unquoteText strips one pair of surrounding double quotes from a directive
// message, if present. No escape processing (consistent with enum values).
func unquoteText(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

func tryParseHTTPStatus(line string) (ok bool, code int) {
	if !strings.HasPrefix(line, "HTTP ") {
		return false, 0
	}
	rest := strings.TrimSpace(line[5:])
	parts := strings.SplitN(rest, " ", 2)
	n, err := strconv.Atoi(parts[0])
	if err != nil {
		return false, 0
	}
	return true, n
}

func parseHeaderLine(line string) (key, value string, ok bool) {
	idx := strings.Index(line, ":")
	if idx < 1 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:idx])
	value = strings.TrimSpace(line[idx+1:])
	// Header keys start with a letter (Authorization, Content-Type, etc.)
	if len(key) == 0 || !unicode.IsLetter(rune(key[0])) {
		return "", "", false
	}
	return key, value, true
}

// startsNewStep reports whether a line in the response state begins a
// new step rather than continuing the current one. A shaping block ("{")
// and another status ("HTTP ") stay with the current step; everything
// else — an assignment, a bare jq statement, or a request/SQL/FORM/URL —
// starts the next step's pre-block. Bare jq never begins with "{" (a
// leading brace is shaping), so the two never collide.
func startsNewStep(line string) bool {
	return !strings.HasPrefix(line, "{") && !strings.HasPrefix(line, "HTTP ")
}

// jqAccumDepth scans one line of a jq expression, updating the running
// bracket depth and string-literal state. It counts { } [ ] ( ) outside
// string literals and toggles inStr on unescaped double quotes (jq
// string literals are double-quoted; single quotes are not used).
func jqAccumDepth(line string, depth int, inStr bool) (int, bool) {
	for i := 0; i < len(line); i++ {
		ch := line[i]
		if inStr {
			if ch == '\\' && i+1 < len(line) {
				i++ // skip the escaped character
				continue
			}
			if ch == '"' {
				inStr = false
			}
			continue
		}
		switch ch {
		case '"':
			inStr = true
		case '{', '[', '(':
			depth++
		case '}', ']', ')':
			depth--
		}
	}
	return depth, inStr
}

// collectJQ consumes a jq expression starting at lines[start] (included)
// and returns the joined expression and the index of the last consumed
// line. A single-line expression whose brackets balance ends on its
// line; an unbalanced line continues until it closes, spanning newlines.
// Blank lines and #-comment lines between (while depth > 0) are skipped:
// jq has no comments inside expressions, so they're authoring notes the
// way they were for JSON bodies, not content jq would parse.
func collectJQ(lines []string, start int) (string, int) {
	var out []string
	depth, inStr := 0, false
	for i := start; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if i > start && (t == "" || strings.HasPrefix(t, "#")) {
			continue
		}
		out = append(out, t)
		depth, inStr = jqAccumDepth(t, depth, inStr)
		if depth <= 0 && !inStr {
			return strings.Join(out, "\n"), i
		}
	}
	return strings.Join(out, "\n"), len(lines) - 1
}

// ── Variable Resolution (URL and header templates) ───────────────────
//
// URLs and headers interpolate $-variables by string substitution (with
// query escaping in URL query strings). They are NOT jq — scope reads go
// through jq (assignments read ".field", then "$var feeds the URL). The
// lookup is the same $-variable set jq sees: caller args + assigned vars +
// token built-ins, so a $name means the same thing in a URL and in a jq
// expression.

// ResolveMode controls how a variable value is encoded when substituted.
type ResolveMode int

const (
	ResolveURL    ResolveMode = iota // Split path/query; query values escaped, path values escaped as segments unless declared `path`
	ResolveHeader                    // Raw string substitution

	resolvePath        // Internal: raw substitution, $$ → $
	resolvePathEscaped // Internal: per-value url.PathEscape, $$ → $ — a name declared `path` falls back to raw
	resolveQuery       // Internal: QueryEscape values, $$ → $
)

// buildLookup assembles the $-variable map URL/header templates resolve
// against: caller args (persisting across steps), assigned vars, omitted
// optional params (as ""), and the token built-ins when authenticated.
// It mirrors buildJQVars' value set so $name means the same value in a
// URL and in a jq expression at the same point.
func buildLookup(args map[string]interface{}, vars map[string]interface{}, auth VirtualAuth, omitted map[string]bool) map[string]interface{} {
	m := make(map[string]interface{}, len(args)+len(vars)+4)
	for k, v := range args {
		m[k] = v
	}
	for k, v := range vars {
		m[k] = v
	}
	for name := range omitted {
		if _, ok := m[name]; !ok {
			m[name] = ""
		}
	}
	if auth.Token != "" {
		m["token"] = auth.Token
	}
	if auth.Email != "" {
		m["token_email"] = auth.Email
		m["token_sub"] = auth.Sub
		m["token_id"] = auth.UserID
	}
	return m
}

// resolveTemplate replaces $variable references in a template string.
// rawPathNames lists variables declared `is path` — the ones whose values
// substitute raw even in a URL's path (their slashes are structure); every
// other URL path value is percent-escaped as a single segment.
func resolveTemplate(tmpl string, lookup map[string]interface{}, mode ResolveMode, rawPathNames map[string]bool) (string, error) {
	// For URL mode, split path and query so each half gets its own escaping.
	if mode == ResolveURL {
		qIdx := strings.Index(tmpl, "?")
		if qIdx == -1 {
			return resolveTemplateInner(tmpl, lookup, resolvePathEscaped, rawPathNames)
		}
		path, err := resolveTemplateInner(tmpl[:qIdx], lookup, resolvePathEscaped, rawPathNames)
		if err != nil {
			return "", err
		}
		query, err := resolveTemplateInner(tmpl[qIdx+1:], lookup, resolveQuery, nil)
		if err != nil {
			return "", err
		}
		return path + "?" + query, nil
	}
	return resolveTemplateInner(tmpl, lookup, mode, nil)
}

// dropEmptyOptionalPairs removes `name=` pairs (empty value) for the
// optional names from a resolved URL's query string. Pairs with values are
// untouched, as are empty pairs for names not listed — a required param the
// caller passed as "" stays visible, and literal pairs (page=1) never match.
func dropEmptyOptionalPairs(rawURL string, optional map[string]bool) string {
	qIdx := strings.Index(rawURL, "?")
	if qIdx == -1 {
		return rawURL
	}
	path, query := rawURL[:qIdx], rawURL[qIdx+1:]
	var b strings.Builder
	for _, pair := range strings.Split(query, "&") {
		if i := strings.Index(pair, "="); i >= 0 && optional[pair[:i]] && pair[i+1:] == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('&')
		}
		b.WriteString(pair)
	}
	if b.Len() == 0 {
		return path
	}
	return path + "?" + b.String()
}

// resolveTemplateInner does the actual variable replacement. $name is
// looked up in the map; $$ → $ in path/query modes (a literal dollar in a
// URL). There is no body mode: request bodies and shaping are jq.
func resolveTemplateInner(tmpl string, lookup map[string]interface{}, mode ResolveMode, rawPathNames map[string]bool) (string, error) {
	var b strings.Builder
	i := 0
	for i < len(tmpl) {
		ch := tmpl[i]
		if ch != '$' {
			b.WriteByte(ch)
			i++
			continue
		}
		// $$ → literal $ (path and query modes)
		if (mode == resolvePath || mode == resolvePathEscaped || mode == resolveQuery) && i+1 < len(tmpl) && tmpl[i+1] == '$' {
			b.WriteByte('$')
			i += 2
			continue
		}
		val, name, consumed, err := resolveVarRef(tmpl[i:], lookup)
		if err != nil {
			return "", fmt.Errorf("position %d: %w", i, err)
		}
		b.WriteString(encodeValue(val, mode, name, rawPathNames))
		i += consumed
	}
	return b.String(), nil
}

// resolveVarRef parses a $name reference and returns its value, the bare
// name (for the `path` opt-out in encodeValue), and the number of characters
// consumed. $name is looked up in the merged variable map; an absent name
// is an error (so $token without a logged-in caller fails, the same gate the
// old resolver enforced).
func resolveVarRef(s string, lookup map[string]interface{}) (interface{}, string, int, error) {
	if len(s) < 2 {
		return nil, "", 0, fmt.Errorf("lonely $ at end of string")
	}
	name, consumed := parseIdent(s[1:])
	if name == "" {
		return nil, "", 0, fmt.Errorf("invalid variable reference after $")
	}
	if val, ok := lookup[name]; ok {
		return val, name, consumed + 1, nil
	}
	return nil, "", 0, fmt.Errorf("undefined variable: $%s", name)
}

func parseIdent(s string) (string, int) {
	for i, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
			return s[:i], i
		}
	}
	return s, len(s)
}

func encodeValue(val interface{}, mode ResolveMode, name string, rawPathNames map[string]bool) string {
	// A JSON null (or an unset $token_id) renders as nothing in URLs and
	// headers — "<nil>" is never what anyone wants.
	if val == nil {
		return ""
	}
	// A variable declared `is path` carries whole path segments: its
	// slashes are structure, so it substitutes raw even in an escaped path.
	if mode == resolvePathEscaped && rawPathNames[name] {
		mode = resolvePath
	}
	switch mode {
	case resolveQuery:
		return url.QueryEscape(fmt.Sprintf("%v", val))
	case resolvePathEscaped:
		// One value, one segment: slashes, question marks and friends are
		// percent-escaped, so a $var can't walk the path or fake a query.
		return url.PathEscape(fmt.Sprintf("%v", val))
	default: // resolvePath, ResolveURL, ResolveHeader
		return fmt.Sprintf("%v", val)
	}
}

// ── Expression Evaluation (jq) ──────────────────────────────────────────
// Assignments, assertions, mutations, shaping and request bodies are all
// jq expressions evaluated by jqRun (see jq.go) against the current scope
// (".") with the $-variable set from buildJQVars. url(), assert() and
// decrypt() are registered as jq functions so they compose in
// pipelines. There is no hand-rolled evaluator here.

// jqEval binds the per-run state a jq expression needs: the caller args
// and assigned vars (live — assignments mutate vars and the next jq
// evaluation sees them), the authenticated identity (token built-ins),
// and the hooks that back decrypt(). scope is passed per call because it
// changes every step (each response replaces it).
type jqEval struct {
	args    map[string]interface{}
	vars    map[string]interface{}
	auth    VirtualAuth
	omitted map[string]bool
	hooks   *VirtualHooks
}

// run evaluates a jq query against scope and returns its single result.
func (e *jqEval) run(query string, scope interface{}) (interface{}, error) {
	return jqRun(query, scope, buildJQVars(e.args, e.vars, e.auth, e.omitted), e.hooks)
}

// ── Executor ─────────────────────────────────────────────────────────
// hasContentType reports whether the headers contain a Content-Type entry
// (case-insensitive). Used to enforce that string (raw) bodies declare their
// content type explicitly.
func hasContentType(headers map[string]string) bool {
	for k := range headers {
		if strings.EqualFold(k, "Content-Type") {
			return true
		}
	}
	return false
}

// findVirtualTool looks up a tool by name (case-insensitive).
func findVirtualTool(tools []VirtualTool, name string) *VirtualTool {
	for i := range tools {
		if strings.EqualFold(tools[i].Name, name) {
			return &tools[i]
		}
	}
	return nil
}

// VirtualToolSummaries returns lightweight tool descriptions for listing.
// AppOnly flags tools declared with "!" so app UIs can tell which tools are
// theirs alone.
func VirtualToolSummaries(tools []VirtualTool) []map[string]any {
	out := make([]map[string]any, len(tools))
	for i, t := range tools {
		m := map[string]any{"name": t.Name, "description": t.Description}
		if t.AppOnly {
			m["appOnly"] = true
		}
		out[i] = m
	}
	return out
}

// SQLRunner executes one compiled SQL statement (with :name bindings) and
// its named parameters on behalf of a virtual tool, returning the result as
// a scope-shaped map ("rows", "rowsAffected", …). It is a callback because
// the formats package cannot know about databases; the server wires it to
// the app-database core, deciding the target from the service's config.
type SQLRunner func(sqlText string, params map[string]interface{}) (map[string]interface{}, error)

// VirtualAuth carries the verified caller identity into tool execution.
// Token backs the $token built-in; Email, Sub and UserID back $token_email,
// $token_sub and $token_id. The server populates them from verified token
// claims — they are injected ahead of caller arguments, so a caller can
// never supply or shadow them.
type VirtualAuth struct {
	Token  string
	Email  string
	Sub    string
	UserID interface{} // int64 when the caller maps to a Fresh Breath user; nil otherwise
}

// ── Elicitation (FORM / URL steps) ───────────────────────────────────

// ElicitRequest is the neutral description of one elicitation the executor
// needs answered. The server adapter turns it into protocol-specific
// (mcp.ElicitParams) messages.
type ElicitRequest struct {
	Mode    string                 // "form" or "url"
	Message string                 // resolved message shown to the user
	URL     string                 // resolved link (url mode)
	Schema  map[string]interface{} // flat JSON Schema (form mode)
}

// ElicitResponse is one answered elicitation.
type ElicitResponse struct {
	Action  string                 // "accept", "decline", "cancel"
	Content map[string]interface{} // submitted values (form) / completion payload (url)
}

// Suspension is the executor's state at an unanswered elicitation step. The
// server stores it and resumes the tool when the answer arrives.
type Suspension struct {
	ToolName      string
	Args          map[string]interface{}
	Auth          VirtualAuth
	Vars          map[string]interface{}
	Scope         map[string]interface{}
	StepIdx       int
	ElicitationID string // crypto-random; doubles as the MCP RequestState / ElicitationID
	Elicit        ElicitRequest
	Handoff       bool // URL step as the final step with no shaping: no response is needed
}

// ResumeState converts a suspension into a resumable state (response to be
// attached by the server when the host answers).
func (susp *Suspension) ResumeState() *ResumeState {
	return &ResumeState{
		ToolName: susp.ToolName,
		Args:     susp.Args,
		Auth:     susp.Auth,
		Vars:     susp.Vars,
		Scope:    susp.Scope,
		StepIdx:  susp.StepIdx,
	}
}

// ErrSuspend signals that execution paused at an elicitation step; the
// Suspension carries everything needed to resume.
type ErrSuspend struct {
	Susp *Suspension
}

func (e *ErrSuspend) Error() string { return "suspended awaiting elicitation" }

// ResumeState carries a suspension plus the elicitation's answer back into
// the executor. Execution restarts AT the saved step with Response injected
// (assignments/assertions on the step re-run harmlessly — they are pure
// evaluations), and continues from there.
type ResumeState struct {
	ToolName string
	Args     map[string]interface{}
	Auth     VirtualAuth
	Vars     map[string]interface{}
	Scope    map[string]interface{}
	StepIdx  int
	Response *ElicitResponse
}

// VirtualHooks supplies server-provided values the executor can't know.
type VirtualHooks struct {
	// ElicitationURL builds the public completion-callback URL for an
	// elicitation ID; it backs the $elicitation_url built-in in URL step
	// templates. Nil when the server has no public completion route.
	ElicitationURL func(elicitationID string) string

	// Seal and Open back the encrypted parameter type and decrypt(), under a
	// key scoped to the service — ciphertext one service seals, no other
	// service opens. Nil means neither is available and both fail the tool.
	Seal func(plain string) (string, error)
	Open func(sealed string) (string, error)
}

// ExecContext is the optional server-provided context for a run: the hooks
// and, when resuming, the stored suspension with its answer attached.
type ExecContext struct {
	Hooks  *VirtualHooks
	Resume *ResumeState
}

// ExecuteVirtualTool runs a virtual tool's steps and returns the result.
// auth carries the upstream token and the caller's verified identity
// (empty when unauthenticated); sqlRunner may be nil for tools that never
// touch a database. exctx (optional) continues a run that was suspended at
// an elicitation step; its Resume must name the same tool.
func ExecuteVirtualTool(httpClient *http.Client, tools []VirtualTool, toolName string, args map[string]interface{}, auth VirtualAuth, sqlRunner SQLRunner, exctx ...*ExecContext) (interface{}, error) {
	tool := findVirtualTool(tools, toolName)
	if tool == nil {
		return nil, fmt.Errorf("tool %q not found", toolName)
	}
	var rs *ResumeState
	var hooks *VirtualHooks
	for _, x := range exctx {
		if x == nil {
			continue
		}
		if x.Resume != nil {
			rs = x.Resume
		}
		if x.Hooks != nil {
			hooks = x.Hooks
		}
	}
	if rs != nil && rs.ToolName != "" && !strings.EqualFold(rs.ToolName, tool.Name) {
		return nil, fmt.Errorf("resume state is for tool %q, not %q", rs.ToolName, toolName)
	}
	return runVirtualTool(tool, httpClient, args, auth, sqlRunner, hooks, rs)
}

// runVirtualTool is the executor core.
func runVirtualTool(tool *VirtualTool, httpClient *http.Client, args map[string]interface{}, auth VirtualAuth, sqlRunner SQLRunner, hooks *VirtualHooks, rs *ResumeState) (interface{}, error) {
	vars := make(map[string]interface{})
	scope := interface{}(map[string]interface{}{})
	startIdx := 0
	if rs != nil {
		// Resuming: the stored scope already carries caller args, optional
		// placeholders and everything gathered before the suspension.
		vars = rs.Vars
		scope = rs.Scope
		startIdx = rs.StepIdx
		if rs.Args != nil {
			args = rs.Args
		}
	} else {
		// Encrypted params are sealed before anything else sees them, and
		// the sealed value replaces the argument outright — SQL binds fall
		// back to args, and a suspension stores them, so the plaintext must
		// not survive there either. Omitted and null values stay as they
		// are: there's nothing to hide.
		sealed := make(map[string]interface{}, len(args))
		for k, v := range args {
			sealed[k] = v
		}
		for _, p := range tool.Params {
			v, ok := sealed[p.Name]
			if p.Type != ParamEncrypted || !ok || v == nil {
				continue
			}
			if hooks == nil || hooks.Seal == nil {
				return nil, fmt.Errorf("parameter %q is encrypted, but this server has no secret key for the service", p.Name)
			}
			plain, isString := v.(string)
			if !isString {
				j, err := json.Marshal(v)
				if err != nil {
					return nil, fmt.Errorf("parameter %q: %w", p.Name, err)
				}
				plain = string(j)
			}
			c, err := hooks.Seal(plain)
			if err != nil {
				return nil, fmt.Errorf("parameter %q: %w", p.Name, err)
			}
			sealed[p.Name] = c
		}
		args = sealed
		// jq reads caller args as $name variables (buildJQVars); the initial
		// scope (jq's ".") is empty, since no response has arrived yet. A
		// shaping-only first step with "." is empty by design.
		scope = map[string]interface{}{}
	}

	// Identity built-ins are server-injected claims, not inputs: namesake
	// caller arguments are dropped outright. They're injected only when the
	// caller has a verified identity — a tool referencing one without a
	// logged-in caller fails with the same "undefined variable" error as
	// $token, rather than stamping empty values into anything. They ride as
	// jq $-variables (buildJQVars adds them), not on scope.
	delete(args, "token_email")
	delete(args, "token_sub")
	delete(args, "token_id")
	delete(args, "elicitation_url") // reserved: injected per elicitation step
	delete(args, "handoff_url")     // reserved: hand-off URL step output
	if auth.Email != "" {
		vars["token_email"] = auth.Email
		vars["token_sub"] = auth.Sub
		vars["token_id"] = auth.UserID
	}

	// Optional params the caller didn't supply resolve as empty strings
	// (so a $name in a URL doesn't error, and jq sees "" not undefined),
	// and their empty query pairs are dropped from resolved URLs: an
	// optional filter the caller passed on should vanish from the query,
	// not ride along as `state=`. In SQL steps the same omission binds as
	// NULL — the honest value for "not given" in a database.
	optionalNames := make(map[string]bool)
	omittedOptionals := make(map[string]bool)
	for _, p := range tool.Params {
		if p.Optional {
			optionalNames[p.Name] = true
			if _, ok := args[p.Name]; !ok {
				omittedOptionals[p.Name] = true
			}
		}
	}

	// Enum validation: a parameter with declared values must be one of them
	// when supplied. Optional enums the caller omitted are skipped — the
	// empty placeholder above would otherwise trip the check. This runs on
	// every call path; the MCP SDK also enforces the schema enum before the
	// handler, but the HTTP/task path bypasses that, so the executor is the
	// single chokepoint that covers both.
	for _, p := range tool.Params {
		if len(p.Values) == 0 {
			continue
		}
		if omittedOptionals[p.Name] {
			continue
		}
		val, ok := args[p.Name]
		if !ok {
			// A required enum the caller didn't send: the MCP SDK's
			// required-check rejects this on the MCP path; elsewhere it
			// surfaces later as an undefined-variable error. Nothing to
			// validate against here.
			continue
		}
		s := fmt.Sprintf("%v", val)
		allowed := false
		for _, v := range p.Values {
			if s == v {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, fmt.Errorf("parameter %q must be one of %v, got %q", p.Name, p.Values, s)
		}
	}

	// jq evaluation context: scope changes every step (passed per call);
	// the rest is live across the tool. buildJQVars rebuilds the $-variable
	// set from these each call so assignments are visible to the next.
	eval := &jqEval{
		args:    args,
		vars:    vars,
		auth:    auth,
		omitted: omittedOptionals,
		hooks:   hooks,
	}
	// Variables declared `is path` are the whole-path opt-out: URL path
	// values are percent-escaped as single segments unless declared here.
	rawPathNames := make(map[string]bool)
	for _, p := range tool.Params {
		if p.Type == ParamPath {
			rawPathNames[p.Name] = true
		}
	}
	lookup := func() map[string]interface{} {
		// URL/header $-interpolation reaches the $-variable set (caller args,
		// assigned vars, token built-ins) AND the current scope (the
		// response the previous step returned): a URL like /next/$id reads
		// the id field of the last response, an assigned $next_url shadows a
		// same-named scope field. Same precedence the old resolver enforced.
		m := buildLookup(args, vars, auth, omittedOptionals)
		if sc, ok := scope.(map[string]interface{}); ok {
			for k, v := range sc {
				if _, has := m[k]; !has {
					m[k] = v
				}
			}
		}
		return m
	}

	for stepIdx := startIdx; stepIdx < len(tool.Steps); stepIdx++ {
		step := tool.Steps[stepIdx]

		// Run the pre-block: assignments and bare jq statements, in source
		// order, against the scope the previous step left. An assignment
		// binds a $-variable (visible to the rest of the tool); a bare
		// expression's result replaces scope (a mutation like
		// ".rows |= map(del(.password))") or is discarded after a check
		// (an assert, which passes its input through). Source order matters:
		// an assert can gate a later mutation in the same block.
		for _, st := range step.Pre {
			val, err := eval.run(st.Expr, scope)
			if err != nil {
				if st.Assign != nil {
					return nil, fmt.Errorf("step %d, assignment $%s: %w", stepIdx, *st.Assign, err)
				}
				return nil, fmt.Errorf("step %d: %w", stepIdx, err)
			}
			if st.Assign != nil {
				vars[*st.Assign] = val
			} else {
				scope = val
			}
		}

		// Elicitation step: FORM (blocking form whose answers replace the
		// scope) or URL (URL-mode elicitation handing the user a link).
		// Executing one without a stored answer suspends the tool — the
		// server surfaces that as an input-required result and resumes with
		// ResumeState once the answer arrives (multi-round-trip, SEP-2322).
		if step.Elicit != nil {
			// URL step as the final step with no shaping: a pure hand-off. No
			// elicitation — there is nothing to wait for — and the resolved
			// link lands in scope as $.handoff_url so the tool result carries
			// it. (Eliciting here would need an eager completion notification
			// that races the input-required result on the wire; hosts that
			// wait for it would hang. The elicitation UI is for steps that
			// gate further work.)
			if step.Elicit.Form == nil && stepIdx == len(tool.Steps)-1 && shapingOf(&step) == "" {
				el, err := buildElicitRequest(step.Elicit, lookup(), rawPathNames)
				if err != nil {
					return nil, fmt.Errorf("step %d, url: %w", stepIdx, err)
				}
				// Land the resolved link in scope (so it carries through to the
				// tool result) and in vars (so a later step's jq sees $handoff_url).
				if sc, ok := scope.(map[string]interface{}); ok {
					sc["handoff_url"] = el.URL
				} else {
					scope = map[string]interface{}{"handoff_url": el.URL}
				}
				vars["handoff_url"] = el.URL
				continue
			}
			elicitationID := elicitationID()
			if hooks != nil && hooks.ElicitationURL != nil {
				// $elicitation_url backs out-of-band flows that redirect or
				// call back: the author interpolates the public completion
				// URL into their target link. Re-injected per elicitation —
				// a later step gets a fresh ID.
				vars["elicitation_url"] = hooks.ElicitationURL(elicitationID)
			}
			el, err := buildElicitRequest(step.Elicit, lookup(), rawPathNames)
			if err != nil {
				return nil, fmt.Errorf("step %d: %w", stepIdx, err)
			}
			if rs != nil && rs.StepIdx == stepIdx {
				resp := rs.Response
				if resp == nil {
					return nil, fmt.Errorf("step %d: resume state has no elicitation response", stepIdx)
				}
				if resp.Action != "accept" {
					verb := "declined"
					if resp.Action == "cancel" {
						verb = "cancelled"
					}
					return nil, fmt.Errorf("step %d: user %s the elicitation", stepIdx, verb)
				}
				if el.Mode == "form" {
					if err := validateFormContent(el.Schema, resp.Content); err != nil {
						return nil, fmt.Errorf("step %d, form: %w", stepIdx, err)
					}
					if resp.Content == nil {
						resp.Content = map[string]interface{}{}
					}
					scope = resp.Content
					// Optional fields the user left blank resolve as "" so later jq
					// expressions / URL templates don't hit undefined-variable errors
					// (mirrors optional caller params).
					for _, f := range step.Elicit.Form.Fields {
						if f.Optional {
							if _, ok := resp.Content[f.Name]; !ok {
								resp.Content[f.Name] = ""
							}
						}
					}
				} else if len(resp.Content) > 0 {
					// URL completion payload (e.g. OAuth callback query
					// params riding the completion redirect): replaces the
					// scope like any step output. No payload → prior scope kept.
					scope = resp.Content
				}
			} else {
				handoff := el.Mode == "url" && stepIdx == len(tool.Steps)-1 && shapingOf(&step) == ""
				return nil, &ErrSuspend{Susp: &Suspension{
					ToolName:      tool.Name,
					Args:          args,
					Auth:          auth,
					Vars:          copyMap(vars),
					Scope:         copyMap(scopeMap(scope)),
					StepIdx:       stepIdx,
					ElicitationID: elicitationID,
					Elicit:        *el,
					Handoff:       handoff,
				}}
			}
			if vr := step.Responses[0]; vr != nil && vr.Shaping != "" {
				return eval.run(vr.Shaping, scope)
			}
			continue
		}

		// SQL step: bind, run, feed the result into scope. A failing
		// statement fails the tool — SQL has no status codes; the absence
		// of a status line IS the signal.
		if step.SQL != "" {
			if sqlRunner == nil {
				return nil, fmt.Errorf("step %d: SQL step but this service has no database target", stepIdx)
			}
			params := make(map[string]interface{}, len(step.SQLNames))
			for _, name := range step.SQLNames {
				if name == "token" {
					params[name] = auth.Token
					continue
				}
				if omittedOptionals[name] {
					// Caller omitted this optional: bind NULL, not "" — the
					// honest value for "not given" on the database side.
					params[name] = nil
					continue
				}
				// Assigned vars and caller args are the $-variable set jq sees;
				// SQL binds the same names (assigned vars shadow caller args).
				if v, ok := vars[name]; ok {
					params[name] = v
					continue
				}
				if v, ok := args[name]; ok {
					params[name] = v
					continue
				}
				return nil, fmt.Errorf("step %d: unresolved $%s in SQL", stepIdx, name)
			}
			result, err := sqlRunner(step.SQL, params)
			if err != nil {
				return nil, fmt.Errorf("step %d, sql: %w", stepIdx, err)
			}
			scope = jqNormalize(result)
			if vr := step.Responses[0]; vr != nil && vr.Shaping != "" {
				return eval.run(vr.Shaping, scope)
			}
			continue
		}

		// Pre-block-only step (post-processing for a previous step): no
		// request to make. A shaping block riding along is the tool's return
		// value — the pre-block has just run, so it sees everything gathered
		// on the way here.
		if step.Method == "" {
			if vr := step.Responses[0]; vr != nil && vr.Shaping != "" {
				return eval.run(vr.Shaping, scope)
			}
			continue
		}

		// Resolve URL and headers (string $-interpolation; not jq).
		resolvedURL, err := resolveTemplate(step.URL, lookup(), ResolveURL, rawPathNames)
		if err != nil {
			return nil, fmt.Errorf("step %d, url: %w", stepIdx, err)
		}
		resolvedURL = dropEmptyOptionalPairs(resolvedURL, optionalNames)

		resolvedHeaders := make(map[string]string)
		for k, v := range step.Headers {
			resolved, err := resolveTemplate(v, lookup(), ResolveHeader, nil)
			if err != nil {
				return nil, fmt.Errorf("step %d, header %s: %w", stepIdx, k, err)
			}
			resolvedHeaders[k] = resolved
		}

		// The request body is a jq expression. A string result is sent
		// verbatim as bytes (raw — with a required Content-Type, no
		// default); anything else is JSON-encoded and sent as
		// application/json when the step didn't set its own Content-Type.
		var bodyReader io.Reader
		var bodyIsJSON bool
		if step.Body != "" {
			val, err := eval.run(step.Body, scope)
			if err != nil {
				return nil, fmt.Errorf("step %d, body: %w", stepIdx, err)
			}
			if s, ok := val.(string); ok {
				if !hasContentType(resolvedHeaders) {
					return nil, fmt.Errorf("step %d: a string body requires a Content-Type header", stepIdx)
				}
				bodyReader = strings.NewReader(s)
			} else {
				j, err := json.Marshal(val)
				if err != nil {
					return nil, fmt.Errorf("step %d, body: marshal: %w", stepIdx, err)
				}
				bodyReader = strings.NewReader(string(j))
				bodyIsJSON = true
			}
		}

		// Build the HTTP request.
		req, err := http.NewRequestWithContext(context.Background(), step.Method, resolvedURL, bodyReader)
		if err != nil {
			return nil, fmt.Errorf("step %d: %w", stepIdx, err)
		}
		for k, v := range resolvedHeaders {
			req.Header.Set(k, v)
		}
		if bodyIsJSON && !hasContentType(resolvedHeaders) {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("step %d, request: %w", stepIdx, err)
		}

		// Read response body.
		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("step %d, read body: %w", stepIdx, err)
		}

		// Check status against expected responses.
		vr, expected := step.Responses[resp.StatusCode]
		if !expected {
			// Return the raw upstream response so the caller can see what happened.
			var parsedBody interface{}
			if len(respBody) > 0 {
				json.Unmarshal(respBody, &parsedBody)
			}
			return map[string]interface{}{
				"error":  fmt.Sprintf("unexpected status %d", resp.StatusCode),
				"status": resp.StatusCode,
				"body":   parsedBody,
			}, nil
		}

		// Parse response body into scope for the next step.
		var parsed interface{}
		if len(respBody) > 0 {
			if err := json.Unmarshal(respBody, &parsed); err != nil {
				return nil, fmt.Errorf("step %d, parse response: %w", stepIdx, err)
			}
		}
		if m, ok := parsed.(map[string]interface{}); ok {
			scope = jqNormalize(m)
		} else {
			// Non-object response: wrap under a "value" key so jq can still
			// select it (.value).
			scope = map[string]interface{}{"value": jqNormalize(parsed)}
		}

		// Apply shaping if present.
		if vr.Shaping != "" {
			return eval.run(vr.Shaping, scope)
		}
	}

	// No shaping on the final step — return raw scope (the last response body).
	return scope, nil
}

// elicitationID returns a fresh crypto-random ID for one elicitation. It
// is the capability for the whole suspend-resume exchange: the RequestState
// the host echoes on retry, the ElicitationID on the wire, and the completion
// callback path — possession of it is the only credential, so 60 bits over
// the nonce alphabet (same generator as app nonces) is the right strength.
func elicitationID() string {
	return utils.GenNonce()
}

// buildElicitRequest resolves an elicitation directive's templates into the
// neutral request the server adapter sends. lookup already merges scope
// (see runVirtualTool), so a URL step's $verification_url reads the field of
// the last response just as a GET URL reads $id. rawPathNames is the tool's
// `is path` opt-out set, applied to the URL template like any step URL.
func buildElicitRequest(ev *VirtualElicit, lookup map[string]interface{}, rawPathNames map[string]bool) (*ElicitRequest, error) {
	el := &ElicitRequest{}
	msgTmpl := ev.Message
	if ev.Form != nil {
		el.Mode = "form"
		el.Schema = BuildFormSchema(ev.Form)
		msgTmpl = ev.Form.Message
	} else {
		el.Mode = "url"
	}
	if msgTmpl != "" {
		msg, err := resolveTemplate(msgTmpl, lookup, ResolveHeader, nil)
		if err != nil {
			return nil, fmt.Errorf("message: %w", err)
		}
		el.Message = msg
	}
	if ev.Form == nil {
		u, err := resolveTemplate(ev.URL, lookup, ResolveURL, rawPathNames)
		if err != nil {
			return nil, fmt.Errorf("url: %w", err)
		}
		el.URL = u
	}
	if el.Message == "" {
		if el.Mode == "url" {
			el.Message = "Open this link to continue."
		} else {
			el.Message = "Please provide the requested information."
		}
	}
	return el, nil
}

// shapingOf returns a step's anchored shaping text ("" when none).
func shapingOf(step *VirtualStep) string {
	if vr := step.Responses[0]; vr != nil {
		return vr.Shaping
	}
	return ""
}

// copyMap shallow-copies a scope/vars map for suspension snapshots.
func copyMap(m map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// scopeMap normalizes a jq result into a map[string]interface{} for storage
// in a suspension: an object stays itself; anything else (a scalar or array
// a jq mutation produced) is wrapped under "value" so the next step can still
// select it as .value. A nil scope (no response yet) is an empty map.
func scopeMap(v interface{}) map[string]interface{} {
	if v == nil {
		return map[string]interface{}{}
	}
	if m, ok := v.(map[string]interface{}); ok {
		return m
	}
	return map[string]interface{}{"value": v}
}

// BuildFormSchema renders a form's fields as the flat JSON Schema object the
// MCP form-elicitation protocol requires (top-level primitives only).
func BuildFormSchema(form *VirtualForm) map[string]interface{} {
	props := make(map[string]interface{}, len(form.Fields))
	required := make([]string, 0, len(form.Fields))
	for _, f := range form.Fields {
		prop := map[string]interface{}{"type": "string"}
		switch {
		case len(f.Values) > 0:
			prop["enum"] = f.Values
		case f.Format != "":
			prop["format"] = f.Format
		default:
			prop["type"] = string(f.Type)
		}
		props[f.Name] = prop
		if !f.Optional {
			required = append(required, f.Name)
		}
	}
	sort.Strings(required)
	schema := map[string]interface{}{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

// validateFormContent checks a form's submitted answers against the schema
// the form was presented with. Multi-round-trip retries hand the server raw
// client responses with no SDK-side validation, so this is the chokepoint
// that keeps garbage out of scope: required presence, primitive types, enum
// membership. The string formats (email/uri/…) are hints; hosts enforce them
// in the form UI.
func validateFormContent(schema map[string]interface{}, content map[string]interface{}) error {
	props, _ := schema["properties"].(map[string]interface{})
	if props == nil {
		return nil
	}
	for _, name := range stringList(schema["required"]) {
		if _, ok := content[name]; !ok {
			return fmt.Errorf("missing required field %q", name)
		}
	}
	for name, val := range content {
		prop, ok := props[name].(map[string]interface{})
		if !ok {
			return fmt.Errorf("unexpected field %q", name)
		}
		if val == nil {
			continue
		}
		typ, _ := prop["type"].(string)
		switch typ {
		case "string":
			s, ok := val.(string)
			if !ok {
				return fmt.Errorf("field %q: expected a string, got %T", name, val)
			}
			if allowed := stringList(prop["enum"]); len(allowed) > 0 {
				ok := false
				for _, c := range allowed {
					if c == s {
						ok = true
						break
					}
				}
				if !ok {
					return fmt.Errorf("field %q: %q is not one of the allowed values", name, s)
				}
			}
		case "number":
			switch val.(type) {
			case float64, json.Number:
			default:
				return fmt.Errorf("field %q: expected a number, got %T", name, val)
			}
		case "boolean":
			if _, ok := val.(bool); !ok {
				return fmt.Errorf("field %q: expected a boolean, got %T", name, val)
			}
		}
	}
	return nil
}

func stringList(v interface{}) []string {
	switch list := v.(type) {
	case []interface{}:
		out := make([]string, 0, len(list))
		for _, item := range list {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return list
	}
	return nil
}
