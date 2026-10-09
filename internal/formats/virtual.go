package formats

import (
	"context"
	"encoding/base64"
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

	"github.com/tidwall/gjson"

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
// statement, or an assignments-only post-processing step.
type VirtualStep struct {
	Assignments []VirtualAssignment
	Assertions  []VirtualAssertion
	Method      string // GET, POST, PUT, PATCH, DELETE
	URL         string // URL template with $variable interpolation
	Headers     map[string]string
	Body        string                   // Raw JSON body template
	BodyRaw     string                   // String-spread body expression, e.g. "$content" or "base64dec($content)"
	SQL         string                   // Compiled SQL: $var → :var bindings
	SQLNames    []string                 // Unique bind names, first-appearance order
	Responses   map[int]*VirtualResponse // Expected status → response handling (0 = SQL step shaping)
	Elicit      *VirtualElicit           // FORM or URL directive: elicitation step
}

// VirtualAssignment binds a variable name to an expression.
type VirtualAssignment struct {
	VarName string // without the $
	Expr    string // e.g. "host($url)", "$.value", "$['@odata']['nextLink']"
}

// VirtualAssertion is a safety check that must pass before proceeding.
type VirtualAssertion struct {
	Expr string // e.g. 'host($next_link) == "graph.microsoft.com"'
	Msg  string // error message if the assertion fails
}

// VirtualResponse describes what to do after receiving a particular status code.
type VirtualResponse struct {
	Shaping string // Raw JSON template for response shaping
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
//   - Variables used in spread syntax (e.g. ...$fields) are typed as "object".
//   - Explicit type annotations (e.g. "$fields is object") override inference.
//   - All other parameters default to "string".
var spreadVarRe = regexp.MustCompile(`\.\.\.\$([a-zA-Z_]\w*)`)
var typeAnnotationRe = regexp.MustCompile(
	`^\$([a-zA-Z_]\w*(?:\s*,\s*\$[a-zA-Z_]\w*)*)\s+is\s+(string|object|number|boolean|array|encrypted|email|uri|date-time|date)(\?)?$`)

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
		for _, a := range step.Assignments {
			defined[a.VarName] = true
		}
		// FORM fields bind runtime scope from the user's answers, not caller
		// arguments — they must never surface as caller params.
		if step.Elicit != nil && step.Elicit.Form != nil {
			for _, f := range step.Elicit.Form.Fields {
				defined[f.Name] = true
			}
		}
	}

	seen := map[string]bool{}
	types := map[string]ParamType{} // name → inferred type
	formats := map[string]string{}  // name → JSON Schema format (email/uri/…)

	scan := func(s string) {
		for _, m := range plainVarRe.FindAllStringSubmatch(s, -1) {
			if name := m[1]; !defined[name] {
				seen[name] = true
			}
		}
		// Spread syntax: ...$var implies object type
		for _, m := range spreadVarRe.FindAllStringSubmatch(s, -1) {
			if name := m[1]; !defined[name] {
				seen[name] = true
				types[name] = ParamObject
			}
		}
	}

	for _, step := range tool.Steps {
		scan(step.URL)
		for _, v := range step.Headers {
			scan(v)
		}
		scan(step.Body)
		scan(step.BodyRaw)
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
		for _, a := range step.Assignments {
			scan(a.Expr)
		}
		for _, a := range step.Assertions {
			scan(a.Expr)
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
func parseVirtualToolBody(tool *VirtualTool, lines []string) error {
	var step *VirtualStep
	var bodyLines, shapingLines []string
	var braceDepth int
	var lastHTTPStatus int

	const (
		sPre = iota
		sHeaders
		sBody
		sResp
		sShaping
	)
	state := sPre

	newStep := func() {
		if step != nil && (step.Method != "" || step.SQL != "" || step.Elicit != nil || len(step.Assignments) > 0 || len(step.Assertions) > 0) {
			tool.Steps = append(tool.Steps, *step)
		}
		step = &VirtualStep{Responses: make(map[int]*VirtualResponse)}
		state = sPre
	}
	newStep()

	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])

		// Inside JSON blocks, only skip comment lines; preserve blanks
		if state == sBody || state == sShaping {
			if strings.HasPrefix(line, "#") {
				continue
			}
		} else {
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
		}

		// In response/shaping states, detect start of a new step
		if state == sResp || state == sShaping {
			if isPreRequestLine(line) {
				if state == sShaping && len(shapingLines) > 0 {
					if block := step.Responses[lastHTTPStatus]; block != nil {
						block.Shaping = strings.Join(shapingLines, "\n")
					}
					shapingLines = nil
				}
				newStep()
			}
		}

		switch state {
		case sPre:
			if ok, vn, ex := tryParseAssignment(line); ok {
				step.Assignments = append(step.Assignments, VirtualAssignment{VarName: vn, Expr: ex})
			} else if ok, ex, msg := tryParseAssertion(line); ok {
				step.Assertions = append(step.Assertions, VirtualAssertion{Expr: ex, Msg: msg})
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
				shapingLines = []string{lines[i]}
				braceDepth = countBraces(line)
				if braceDepth <= 0 {
					step.Responses[0].Shaping = strings.Join(shapingLines, "\n")
					shapingLines = nil
					state = sResp
				} else {
					state = sShaping
				}
			}

		case sHeaders:
			if strings.HasPrefix(line, "...") {
				// String-spread body: ...$expr sends the expr's string value
				// verbatim (no JSON). Discriminated from object-spread
				// {...$fields} by the lack of braces.
				step.BodyRaw = strings.TrimSpace(line[3:])
				state = sResp
			} else if strings.HasPrefix(line, "{") {
				bodyLines = []string{lines[i]}
				braceDepth = countBraces(line)
				if braceDepth <= 0 {
					step.Body = strings.Join(bodyLines, "\n")
					bodyLines = nil
					state = sResp
				} else {
					state = sBody
				}
			} else if ok, code := tryParseHTTPStatus(line); ok {
				step.Responses[code] = &VirtualResponse{}
				lastHTTPStatus = code
				state = sResp
			} else if k, v, ok := parseHeaderLine(line); ok {
				if step.Headers == nil {
					step.Headers = make(map[string]string)
				}
				step.Headers[k] = v
			}

		case sBody:
			bodyLines = append(bodyLines, lines[i])
			braceDepth += countBraces(line)
			if braceDepth <= 0 {
				step.Body = strings.Join(bodyLines, "\n")
				bodyLines = nil
				state = sResp
			}

		case sResp:
			if strings.HasPrefix(line, "{") {
				shapingLines = []string{lines[i]}
				braceDepth = countBraces(line)
				if braceDepth <= 0 {
					if block := step.Responses[lastHTTPStatus]; block != nil {
						block.Shaping = strings.Join(shapingLines, "\n")
					}
					shapingLines = nil
				} else {
					state = sShaping
				}
			} else if ok, code := tryParseHTTPStatus(line); ok {
				step.Responses[code] = &VirtualResponse{}
				lastHTTPStatus = code
			}

		case sShaping:
			shapingLines = append(shapingLines, lines[i])
			braceDepth += countBraces(line)
			if braceDepth <= 0 {
				if block := step.Responses[lastHTTPStatus]; block != nil {
					block.Shaping = strings.Join(shapingLines, "\n")
				}
				shapingLines = nil
				state = sResp
			}
		}
	}

	// Finalize any open shaping block
	if state == sShaping && len(shapingLines) > 0 {
		if block := step.Responses[lastHTTPStatus]; block != nil {
			block.Shaping = strings.Join(shapingLines, "\n")
		}
	}

	// Finalize last step
	if step != nil && (step.Method != "" || step.SQL != "" || step.Elicit != nil || len(step.Assignments) > 0 || len(step.Assertions) > 0) {
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

func tryParseAssertion(line string) (ok bool, expr, msg string) {
	if !strings.HasPrefix(line, "assert(") || !strings.HasSuffix(line, ")") {
		return false, "", ""
	}
	inner := line[7 : len(line)-1]

	// Find the last top-level comma to split expr from message.
	// The message is always a string literal at the end.
	depth := 0
	lastComma := -1
	for i, ch := range inner {
		switch ch {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case ',':
			if depth == 0 {
				lastComma = i
			}
		}
	}
	if lastComma < 0 {
		return true, inner, ""
	}
	expr = strings.TrimSpace(inner[:lastComma])
	msg = strings.TrimSpace(inner[lastComma+1:])
	msg = strings.Trim(msg, "\"")
	return true, expr, msg
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
		for _, s := range []string{st.URL, st.Body, st.BodyRaw} {
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
		for _, a := range st.Assignments {
			if err := check(a.Expr); err != nil {
				return err
			}
		}
		for _, a := range st.Assertions {
			if err := check(a.Expr); err != nil {
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
	if ann.typ == ParamObject || ann.typ == ParamArray || ann.typ == ParamEncrypted {
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

func isPreRequestLine(line string) bool {
	if ok, _, _ := tryParseAssignment(line); ok {
		return true
	}
	if ok, _, _ := tryParseAssertion(line); ok {
		return true
	}
	if strings.HasPrefix(line, "FORM ") || strings.HasPrefix(line, "URL ") {
		return true
	}
	if isSQLVerbLine(line) {
		return true
	}
	if ok, _, _ := tryParseRequest(line); ok {
		return true
	}
	return false
}

func countBraces(s string) int {
	d := 0
	for _, ch := range s {
		if ch == '{' {
			d++
		}
		if ch == '}' {
			d--
		}
	}
	return d
}

// ── Variable Resolution ──────────────────────────────────────────────

// ResolveMode controls how variable values are encoded when substituted into a template.
type ResolveMode int

const (
	ResolveURL    ResolveMode = iota // Split path/query; query values escaped
	ResolveBody                      // JSON-encode values; skip $ inside JSON strings
	ResolveHeader                    // Raw string substitution

	resolvePath  // Internal: raw substitution, $$ → $
	resolveQuery // Internal: QueryEscape values, $$ → $
)

// resolveTemplate replaces $variable references in a template string.
func resolveTemplate(tmpl string, vars map[string]interface{}, scope interface{}, token string, mode ResolveMode) (string, error) {
	// For URL mode, split path and query so query values get escaped.
	if mode == ResolveURL {
		qIdx := strings.Index(tmpl, "?")
		if qIdx == -1 {
			return resolveTemplateInner(tmpl, vars, scope, token, resolvePath)
		}
		path, err := resolveTemplateInner(tmpl[:qIdx], vars, scope, token, resolvePath)
		if err != nil {
			return "", err
		}
		query, err := resolveTemplateInner(tmpl[qIdx+1:], vars, scope, token, resolveQuery)
		if err != nil {
			return "", err
		}
		return path + "?" + query, nil
	}
	return resolveTemplateInner(tmpl, vars, scope, token, mode)
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

// resolveTemplateInner does the actual variable replacement.
func resolveTemplateInner(tmpl string, vars map[string]interface{}, scope interface{}, token string, mode ResolveMode) (string, error) {
	var b strings.Builder
	i := 0
	inString := false

	for i < len(tmpl) {
		ch := tmpl[i]

		// In body mode, track JSON string boundaries so $ inside
		// quoted strings stays literal.
		if mode == ResolveBody {
			if ch == '"' && (i == 0 || tmpl[i-1] != '\\') {
				inString = !inString
				b.WriteByte(ch)
				i++
				continue
			}
			if inString {
				b.WriteByte(ch)
				i++
				continue
			}
		}

		// In body mode, detect spread syntax: ...$var
		if mode == ResolveBody && ch == '.' && i+2 < len(tmpl) && tmpl[i+1] == '.' && tmpl[i+2] == '.' {
			// Look ahead for $
			if i+3 < len(tmpl) && tmpl[i+3] == '$' {
				val, consumed, err := resolveVarRef(tmpl[i+3:], vars, scope, token)
				if err != nil {
					return "", fmt.Errorf("position %d: %w", i, err)
				}
				m, ok := val.(map[string]interface{})
				if !ok {
					return "", fmt.Errorf("position %d: spread requires an object, got %T", i, val)
				}
				j, err := json.Marshal(m)
				if err != nil {
					return "", fmt.Errorf("position %d: marshal spread: %w", i, err)
				}
				// Strip outer { } so the inner key-value pairs merge into the surrounding object.
				inner := string(j[1 : len(j)-1])
				b.WriteString(inner)
				i += 3 + consumed // skip ... and the variable reference
				continue
			}
			// Not a spread — literal dots
			b.WriteString("...")
			i += 3
			continue
		}

		if ch != '$' {
			b.WriteByte(ch)
			i++
			continue
		}

		// $$ → literal $ (path and query modes)
		if (mode == resolvePath || mode == resolveQuery) && i+1 < len(tmpl) && tmpl[i+1] == '$' {
			b.WriteByte('$')
			i += 2
			continue
		}

		// Resolve variable reference starting at $
		val, consumed, err := resolveVarRef(tmpl[i:], vars, scope, token)
		if err != nil {
			return "", fmt.Errorf("position %d: %w", i, err)
		}

		encoded, err := encodeValue(val, mode)
		if err != nil {
			return "", err
		}
		b.WriteString(encoded)
		i += consumed
	}

	return b.String(), nil
}

// resolveVarRef parses a variable reference starting at $ and returns
// the resolved value and the number of characters consumed.
func resolveVarRef(s string, vars map[string]interface{}, scope interface{}, token string) (interface{}, int, error) {
	if len(s) < 2 {
		return nil, 0, fmt.Errorf("lonely $ at end of string")
	}

	// $.field.path — dot-notation JSON path (gjson)
	if s[1] == '.' {
		path, consumed := parseDotPath(s[1:])
		val, err := gjsonQuery(scope, path)
		if err != nil {
			return nil, consumed + 1, err
		}
		return val, consumed + 1, nil
	}

	// $['key'] — bracket-notation JSON path (gjson)
	if len(s) > 2 && s[1] == '[' {
		path, consumed := parseBracketPath(s[1:])
		if len(path) > 0 {
			val, err := gjsonQuery(scope, path)
			if err != nil {
				return nil, consumed + 1, err
			}
			return val, consumed + 1, nil
		}
	}

	// $name — plain variable lookup
	name, consumed := parseIdent(s[1:])
	if name == "" {
		return nil, 0, fmt.Errorf("invalid variable reference after $")
	}
	total := consumed + 1

	// Special: $token from auth context
	if name == "token" && token != "" {
		return token, total, nil
	}

	// Look up in assigned variables
	if val, ok := vars[name]; ok {
		return val, total, nil
	}

	// Look up in scope (input args before first request, response after)
	if m, ok := scope.(map[string]interface{}); ok {
		if val, ok := m[name]; ok {
			return val, total, nil
		}
	}

	return nil, 0, fmt.Errorf("undefined variable: $%s", name)
}

func parseDotPath(s string) ([]string, int) {
	var segs []string
	i := 0
	for i < len(s) && s[i] == '.' {
		i++ // skip dot
		name, consumed := parseIdent(s[i:])
		if name == "" {
			break
		}
		segs = append(segs, name)
		i += consumed
	}
	return segs, i
}

func parseBracketPath(s string) ([]string, int) {
	var segs []string
	i := 0
	for i < len(s) && s[i] == '[' {
		if i+1 >= len(s) || s[i+1] != '\'' {
			break
		}
		closeIdx := strings.Index(s[i+2:], "']")
		if closeIdx < 0 {
			break
		}
		key := s[i+2 : i+2+closeIdx]
		segs = append(segs, key)
		i += 2 + closeIdx + 2 // [ ' key ' ]
	}
	return segs, i
}

func parseIdent(s string) (string, int) {
	for i, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
			return s[:i], i
		}
	}
	return s, len(s)
}

func encodeValue(val interface{}, mode ResolveMode) (string, error) {
	// A JSON null (or an unknown $token_id) renders as nothing in URLs and
	// headers — "<nil>" is never what anyone wants.
	if val == nil {
		if mode == ResolveBody {
			return "null", nil
		}
		return "", nil
	}
	switch mode {
	case ResolveURL, resolvePath, ResolveHeader:
		return fmt.Sprintf("%v", val), nil
	case resolveQuery:
		return url.QueryEscape(fmt.Sprintf("%v", val)), nil
	case ResolveBody:
		j, err := json.Marshal(val)
		if err != nil {
			return "", err
		}
		return string(j), nil
	default:
		return fmt.Sprintf("%v", val), nil
	}
}

// ── JSON Path Queries ────────────────────────────────────────────────

// jsonPathQuery navigates a JSON structure using path segments.
// gjsonQuery resolves a JSON path against a scope using gjson.
// path is the dot-separated keys (e.g. ["displayName", "webUrl"] → "displayName.webUrl").
func gjsonQuery(scope interface{}, path []string) (interface{}, error) {
	jsonBytes, err := json.Marshal(scope)
	if err != nil {
		return nil, fmt.Errorf("$.%s: marshal scope: %w", strings.Join(path, "."), err)
	}
	gjsonPath := strings.Join(path, ".")
	result := gjson.GetBytes(jsonBytes, gjsonPath)
	if !result.Exists() {
		return nil, fmt.Errorf("$.%s: not found", gjsonPath)
	}
	return result.Value(), nil
}

// ── Expression Evaluation ────────────────────────────────────────────

// evalExpr evaluates an expression string and returns its value.
func evalExpr(expr string, vars map[string]interface{}, scope interface{}, token string, hooks *VirtualHooks) (interface{}, error) {
	expr = strings.TrimSpace(expr)

	// Function call: name(args)
	if idx := strings.Index(expr, "("); idx > 0 && strings.HasSuffix(expr, ")") {
		fnName := expr[:idx]
		argsStr := expr[idx+1 : len(expr)-1]
		return evalFunction(fnName, argsStr, vars, scope, token, hooks)
	}

	// JSON path or variable: $.field, $['key'], $name
	if strings.HasPrefix(expr, "$") {
		val, _, err := resolveVarRef(expr, vars, scope, token)
		return val, err
	}

	// String literal
	if len(expr) >= 2 && expr[0] == '"' && expr[len(expr)-1] == '"' {
		return expr[1 : len(expr)-1], nil
	}

	// Number literal
	if n, err := strconv.ParseInt(expr, 10, 64); err == nil {
		return n, nil
	}

	return nil, fmt.Errorf("unsupported expression: %s", expr)
}

func evalFunction(name, argsStr string, vars map[string]interface{}, scope interface{}, token string, hooks *VirtualHooks) (interface{}, error) {
	args := splitFunctionArgs(argsStr)
	resolved := make([]interface{}, len(args))
	for i, arg := range args {
		val, err := evalExpr(strings.TrimSpace(arg), vars, scope, token, hooks)
		if err != nil {
			return nil, fmt.Errorf("%s arg %d: %w", name, i+1, err)
		}
		resolved[i] = val
	}

	switch name {
	case "host":
		if len(resolved) != 1 {
			return nil, fmt.Errorf("host() takes 1 argument, got %d", len(resolved))
		}
		u, err := url.Parse(fmt.Sprintf("%v", resolved[0]))
		if err != nil {
			return nil, fmt.Errorf("host(): %w", err)
		}
		return u.Host, nil
	case "path":
		if len(resolved) != 1 {
			return nil, fmt.Errorf("path() takes 1 argument, got %d", len(resolved))
		}
		u, err := url.Parse(fmt.Sprintf("%v", resolved[0]))
		if err != nil {
			return nil, fmt.Errorf("path(): %w", err)
		}
		return u.Path, nil
	case "base64dec":
		if len(resolved) != 1 {
			return nil, fmt.Errorf("base64dec() takes 1 argument, got %d", len(resolved))
		}
		s, ok := resolved[0].(string)
		if !ok {
			return nil, fmt.Errorf("base64dec() requires a string argument, got %T", resolved[0])
		}
		decoded, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("base64dec(): %w", err)
		}
		return string(decoded), nil
	case "base64enc":
		if len(resolved) != 1 {
			return nil, fmt.Errorf("base64enc() takes 1 argument, got %d", len(resolved))
		}
		s, ok := resolved[0].(string)
		if !ok {
			return nil, fmt.Errorf("base64enc() requires a string argument, got %T", resolved[0])
		}
		return base64.StdEncoding.EncodeToString([]byte(s)), nil
	case "decrypt":
		if len(resolved) != 1 {
			return nil, fmt.Errorf("decrypt() takes 1 argument, got %d", len(resolved))
		}
		s, ok := resolved[0].(string)
		if !ok {
			return nil, fmt.Errorf("decrypt() requires a string argument, got %T", resolved[0])
		}
		if hooks == nil || hooks.Open == nil {
			return nil, fmt.Errorf("decrypt(): this server has no secret key for the service")
		}
		plain, err := hooks.Open(s)
		if err != nil {
			// Never echo the argument: it's either ciphertext or something
			// that was meant to be.
			return nil, fmt.Errorf("decrypt(): value was not encrypted by this service")
		}
		return plain, nil
	default:
		return nil, fmt.Errorf("unknown function: %s", name)
	}
}

// splitFunctionArgs splits a comma-separated argument list, respecting nesting.
func splitFunctionArgs(s string) []string {
	var args []string
	depth := 0
	start := 0
	for i, ch := range s {
		switch ch {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case ',':
			if depth == 0 {
				args = append(args, s[start:i])
				start = i + 1
			}
		}
	}
	if start < len(s) {
		args = append(args, s[start:])
	}
	return args
}

// ── Assertion Evaluation ─────────────────────────────────────────────

// evalAssertion evaluates an assertion expression. Returns nil on success.
func evalAssertion(expr, msg string, vars map[string]interface{}, scope interface{}, token string, hooks *VirtualHooks) error {
	if parts := splitComparison(expr, "=="); len(parts) == 2 {
		return evalComparison(parts[0], parts[1], msg, vars, scope, token, hooks, false)
	}
	if parts := splitComparison(expr, "!="); len(parts) == 2 {
		return evalComparison(parts[0], parts[1], msg, vars, scope, token, hooks, true)
	}
	return fmt.Errorf("unsupported assertion: %s", expr)
}

func splitComparison(expr, op string) []string {
	search := " " + op + " "
	idx := strings.Index(expr, search)
	if idx < 0 {
		return nil
	}
	return []string{
		strings.TrimSpace(expr[:idx]),
		strings.TrimSpace(expr[idx+len(search):]),
	}
}

func evalComparison(leftExpr, rightExpr, msg string, vars map[string]interface{}, scope interface{}, token string, hooks *VirtualHooks, negate bool) error {
	left, err := evalExpr(leftExpr, vars, scope, token, hooks)
	if err != nil {
		return fmt.Errorf("%s: %w", msg, err)
	}
	right, err := evalExpr(rightExpr, vars, scope, token, hooks)
	if err != nil {
		return fmt.Errorf("%s: %w", msg, err)
	}
	lStr := fmt.Sprintf("%v", left)
	rStr := fmt.Sprintf("%v", right)
	eq := lStr == rStr
	if negate {
		eq = !eq
	}
	if !eq {
		op := "=="
		if negate {
			op = "!="
		}
		if msg != "" {
			return fmt.Errorf("%s (%v %s %v)", msg, lStr, op, rStr)
		}
		return fmt.Errorf("assertion failed: %v %s %v", lStr, op, rStr)
	}
	return nil
}

// ── Executor ─────────────────────────────────────────────────────────

// hasContentType reports whether the headers contain a Content-Type entry
// (case-insensitive). Used to enforce that string-spread bodies declare their
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
	scope := map[string]interface{}{}
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
		// Input args become the initial scope for JSON path queries.
		for k, v := range args {
			scope[k] = v
		}
	}

	// Identity built-ins are server-injected claims, not inputs: namesake
	// caller arguments are dropped outright. They're injected only when the
	// caller has a verified identity — a tool referencing one without a
	// logged-in caller fails with the same "undefined variable" error as
	// $token, rather than stamping empty values into anything.
	delete(scope, "token_email")
	delete(scope, "token_sub")
	delete(scope, "token_id")
	delete(scope, "elicitation_url") // reserved: injected per elicitation step
	delete(scope, "handoff_url")     // reserved: hand-off URL step output
	if auth.Email != "" {
		vars["token_email"] = auth.Email
		vars["token_sub"] = auth.Sub
		vars["token_id"] = auth.UserID
	}
	token := auth.Token

	// Optional params the caller didn't supply resolve as empty strings
	// (so template resolution doesn't error), and their empty query pairs
	// are dropped from resolved URLs: an optional filter the caller passed
	// on should vanish from the query, not ride along as `state=`. In SQL
	// steps the same omission binds as NULL — the honest value for "not
	// given" in a database.
	optionalNames := make(map[string]bool)
	omittedOptionals := make(map[string]bool)
	for _, p := range tool.Params {
		if p.Optional {
			optionalNames[p.Name] = true
			if _, ok := scope[p.Name]; !ok {
				scope[p.Name] = ""
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
		val, ok := scope[p.Name]
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

	for stepIdx := startIdx; stepIdx < len(tool.Steps); stepIdx++ {
		step := tool.Steps[stepIdx]

		// Execute assignments first.
		// Execute assignments first.
		for _, a := range step.Assignments {
			val, err := evalExpr(a.Expr, vars, scope, token, hooks)
			if err != nil {
				return nil, fmt.Errorf("step %d, assignment $%s: %w", stepIdx, a.VarName, err)
			}
			vars[a.VarName] = val
		}

		// Run assertions.
		for _, a := range step.Assertions {
			if err := evalAssertion(a.Expr, a.Msg, vars, scope, token, hooks); err != nil {
				return nil, fmt.Errorf("step %d: %w", stepIdx, err)
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
				u, err := resolveTemplate(step.Elicit.URL, vars, scope, token, ResolveURL)
				if err != nil {
					return nil, fmt.Errorf("step %d, url: %w", stepIdx, err)
				}
				scope["handoff_url"] = u
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
			el, err := buildElicitRequest(step.Elicit, vars, scope, token)
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
					// Optional fields the user left blank resolve as "" so
					// later templates don't hit undefined-variable errors
					// (mirrors optional caller params).
					for _, f := range step.Elicit.Form.Fields {
						if f.Optional {
							if _, ok := scope[f.Name]; !ok {
								scope[f.Name] = ""
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
					Scope:         copyMap(scope),
					StepIdx:       stepIdx,
					ElicitationID: elicitationID,
					Elicit:        *el,
					Handoff:       handoff,
				}}
			}
			if vr := step.Responses[0]; vr != nil && vr.Shaping != "" {
				return applyShaping(vr.Shaping, vars, scope, token, stepIdx)
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
					params[name] = token
					continue
				}
				if omittedOptionals[name] {
					// Caller omitted this optional: bind NULL, not "" — the
					// honest value for "not given" on the database side.
					params[name] = nil
					continue
				}
				if v, ok := vars[name]; ok {
					params[name] = v
					continue
				}
				if v, ok := scope[name]; ok {
					params[name] = v
					continue
				}
				// scope was replaced by the previous SQL result; caller args
				// stay bindable for the whole tool regardless.
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
			scope = result
			if vr := step.Responses[0]; vr != nil && vr.Shaping != "" {
				return applyShaping(vr.Shaping, vars, scope, token, stepIdx)
			}
			continue
		}

		// Assignments-only step (post-processing for a previous step): its
		// work is done, nothing to request. A shaping block riding along is
		// the tool's return value — the assignments above have just run, so
		// it sees everything gathered on the way here.
		if step.Method == "" {
			if vr := step.Responses[0]; vr != nil && vr.Shaping != "" {
				return applyShaping(vr.Shaping, vars, scope, token, stepIdx)
			}
			continue
		}

		// Resolve URL, headers, body.
		resolvedURL, err := resolveTemplate(step.URL, vars, scope, token, ResolveURL)
		if err != nil {
			return nil, fmt.Errorf("step %d, url: %w", stepIdx, err)
		}
		resolvedURL = dropEmptyOptionalPairs(resolvedURL, optionalNames)

		resolvedHeaders := make(map[string]string)
		for k, v := range step.Headers {
			resolved, err := resolveTemplate(v, vars, scope, token, ResolveHeader)
			if err != nil {
				return nil, fmt.Errorf("step %d, header %s: %w", stepIdx, k, err)
			}
			resolvedHeaders[k] = resolved
		}

		var bodyReader io.Reader
		if step.BodyRaw != "" {
			// String-spread body: evaluate the expression and send the
			// resulting string's bytes verbatim (no JSON encoding, no
			// $-interpolation, so $ in file data survives intact).
			val, err := evalExpr(step.BodyRaw, vars, scope, token, hooks)
			if err != nil {
				return nil, fmt.Errorf("step %d, raw body: %w", stepIdx, err)
			}
			s, ok := val.(string)
			if !ok {
				return nil, fmt.Errorf("step %d, raw body: ...%s requires a string, got %T", stepIdx, step.BodyRaw, val)
			}
			if !hasContentType(resolvedHeaders) {
				return nil, fmt.Errorf("step %d, raw body: ...%s requires a Content-Type header", stepIdx, step.BodyRaw)
			}
			bodyReader = strings.NewReader(s)
		} else if step.Body != "" {
			resolvedBody, err := resolveTemplate(step.Body, vars, scope, token, ResolveBody)
			if err != nil {
				return nil, fmt.Errorf("step %d, body: %w", stepIdx, err)
			}
			bodyReader = strings.NewReader(resolvedBody)
		}

		// Build the HTTP request.
		req, err := http.NewRequestWithContext(context.Background(), step.Method, resolvedURL, bodyReader)
		if err != nil {
			return nil, fmt.Errorf("step %d: %w", stepIdx, err)
		}
		for k, v := range resolvedHeaders {
			req.Header.Set(k, v)
		}
		if step.Body != "" {
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
			scope = m
		} else {
			// Non-object response: wrap under a "value" key so JSON path still works.
			scope = map[string]interface{}{"value": parsed}
		}

		// Apply shaping if present.
		if vr.Shaping != "" {
			return applyShaping(vr.Shaping, vars, scope, token, stepIdx)
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
// neutral request the server adapter sends.
func buildElicitRequest(ev *VirtualElicit, vars map[string]interface{}, scope interface{}, token string) (*ElicitRequest, error) {
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
		msg, err := resolveTemplate(msgTmpl, vars, scope, token, ResolveHeader)
		if err != nil {
			return nil, fmt.Errorf("message: %w", err)
		}
		el.Message = msg
	}
	if ev.Form == nil {
		u, err := resolveTemplate(ev.URL, vars, scope, token, ResolveURL)
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

// applyShaping resolves a shaping template against the current scope and
// parses the result into structured data. Shaping terminates the tool —
// it is the tool's return value.
func applyShaping(shaping string, vars map[string]interface{}, scope interface{}, token string, stepIdx int) (interface{}, error) {
	shaped, err := resolveTemplate(shaping, vars, scope, token, ResolveBody)
	if err != nil {
		return nil, fmt.Errorf("step %d, shaping: %w", stepIdx, err)
	}
	// Parse shaped output so we return structured data, not a string.
	var shapedVal interface{}
	if err := json.Unmarshal([]byte(shaped), &shapedVal); err != nil {
		return nil, fmt.Errorf("step %d, shaping parse: %w", stepIdx, err)
	}
	return shapedVal, nil
}
