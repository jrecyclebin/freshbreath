package formats

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/tidwall/gjson"
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
)

// ToolParam describes an input parameter for a virtual tool.
type ToolParam struct {
	Name     string
	Type     ParamType
	Optional bool // from a `?` annotation; parameters are required by default
}

// VirtualTool defines a single tool in a virtual service description.
type VirtualTool struct {
	Name            string
	Description     string
	Params          []ToolParam // input parameters inferred from template references
	Steps           []VirtualStep
	typeAnnotations []typeAnnotation // parsed from "$name is type" lines, unexported
}

type typeAnnotation struct {
	names    []string // one or more: "$a, $b is number"
	typ      ParamType
	optional bool
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

var toolNameRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`)
var plainVarRe = regexp.MustCompile(`\$([a-zA-Z_]\w*)`)

// ParseVirtualFile parses a virtual service description file into tools.
// Tools are separated by --- delimiters and begin with [name] headers.
func ParseVirtualFile(data []byte) ([]VirtualTool, error) {
	var tools []VirtualTool
	var cur *VirtualTool
	var curLines []string

	flush := func() error {
		if cur != nil {
			if err := parseVirtualToolBody(cur, curLines); err != nil {
				return fmt.Errorf("tool %s: %w", cur.Name, err)
			}
			if err := rejectReservedParams(*cur); err != nil {
				return fmt.Errorf("tool %s: %w", cur.Name, err)
			}
			cur.Params = toolParams(*cur)
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

		if name, desc, ok := parseVirtualToolHeader(trimmed); ok {
			if err := flush(); err != nil {
				return nil, err
			}
			cur = &VirtualTool{Name: name, Description: desc}
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
	`^\$([a-zA-Z_]\w*(?:\s*,\s*\$[a-zA-Z_]\w*)*)\s+is\s+(string|object|number|boolean|array)(\?)?$`)

func toolParams(tool VirtualTool) []ToolParam {
	defined := map[string]bool{"token": true}
	for _, step := range tool.Steps {
		for _, a := range step.Assignments {
			defined[a.VarName] = true
		}
	}

	seen := map[string]bool{}
	types := map[string]ParamType{} // name → inferred type

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
	for _, ann := range tool.typeAnnotations {
		for _, name := range ann.names {
			if _, ok := seen[name]; ok {
				types[name] = ann.typ
				if ann.optional {
					optional[name] = true
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
		params = append(params, ToolParam{Name: name, Type: t, Optional: optional[name]})
	}
	sort.Slice(params, func(i, j int) bool { return params[i].Name < params[j].Name })
	return params
}

// parseVirtualToolHeader validates a [name] header for virtual tools.
// Names must be valid identifiers (letters, digits, underscores, hyphens).
func parseVirtualToolHeader(line string) (name, desc string, ok bool) {
	name, desc, ok = parseHeader(line)
	if !ok || !toolNameRe.MatchString(name) {
		return "", "", false
	}
	return name, desc, true
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
		if step != nil && (step.Method != "" || step.SQL != "" || len(step.Assignments) > 0 || len(step.Assertions) > 0) {
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
			} else if m := typeAnnotationRe.FindStringSubmatch(line); m != nil {
				var names []string
				for _, part := range strings.Split(m[1], ",") {
					names = append(names, strings.TrimPrefix(strings.TrimSpace(part), "$"))
				}
				tool.typeAnnotations = append(tool.typeAnnotations, typeAnnotation{
					names:    names,
					typ:      ParamType(m[2]),
					optional: m[3] == "?",
				})
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
	if step != nil && (step.Method != "" || step.SQL != "" || len(step.Assignments) > 0 || len(step.Assertions) > 0) {
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
func evalExpr(expr string, vars map[string]interface{}, scope interface{}, token string) (interface{}, error) {
	expr = strings.TrimSpace(expr)

	// Function call: name(args)
	if idx := strings.Index(expr, "("); idx > 0 && strings.HasSuffix(expr, ")") {
		fnName := expr[:idx]
		argsStr := expr[idx+1 : len(expr)-1]
		return evalFunction(fnName, argsStr, vars, scope, token)
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

func evalFunction(name, argsStr string, vars map[string]interface{}, scope interface{}, token string) (interface{}, error) {
	args := splitFunctionArgs(argsStr)
	resolved := make([]interface{}, len(args))
	for i, arg := range args {
		val, err := evalExpr(strings.TrimSpace(arg), vars, scope, token)
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
func evalAssertion(expr, msg string, vars map[string]interface{}, scope interface{}, token string) error {
	if parts := splitComparison(expr, "=="); len(parts) == 2 {
		return evalComparison(parts[0], parts[1], msg, vars, scope, token, false)
	}
	if parts := splitComparison(expr, "!="); len(parts) == 2 {
		return evalComparison(parts[0], parts[1], msg, vars, scope, token, true)
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

func evalComparison(leftExpr, rightExpr, msg string, vars map[string]interface{}, scope interface{}, token string, negate bool) error {
	left, err := evalExpr(leftExpr, vars, scope, token)
	if err != nil {
		return fmt.Errorf("%s: %w", msg, err)
	}
	right, err := evalExpr(rightExpr, vars, scope, token)
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

// LoadVirtualTools reads and parses the virtual description file for a service.
func LoadVirtualTools(dir string, svcName string) ([]VirtualTool, error) {
	path := filepath.Join(dir, "virtual", svcName+".txt")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("virtual file not found: %s: %w", path, err)
	}
	return ParseVirtualFile(data)
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
func VirtualToolSummaries(tools []VirtualTool) []map[string]string {
	out := make([]map[string]string, len(tools))
	for i, t := range tools {
		out[i] = map[string]string{"name": t.Name, "description": t.Description}
	}
	return out
}

// SQLRunner executes one compiled SQL statement (with :name bindings) and
// its named parameters on behalf of a virtual tool, returning the result as
// a scope-shaped map ("rows", "rowsAffected", …). It is a callback because
// the formats package cannot know about databases; the server wires it to
// the app-database core, deciding the target from the service's config.
type SQLRunner func(sqlText string, params map[string]interface{}) (map[string]interface{}, error)

// ExecuteVirtualTool runs a virtual tool's steps and returns the result.
// The token comes from auth middleware context (or "" for unauthenticated
// calls); sqlRunner may be nil for tools that never touch a database.
func ExecuteVirtualTool(httpClient *http.Client, tools []VirtualTool, toolName string, args map[string]interface{}, token string, sqlRunner SQLRunner) (interface{}, error) {
	tool := findVirtualTool(tools, toolName)
	if tool == nil {
		return nil, fmt.Errorf("tool %q not found", toolName)
	}

	vars := make(map[string]interface{})
	scope := map[string]interface{}{}
	// Input args become the initial scope for JSON path queries.
	for k, v := range args {
		scope[k] = v
	}

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

	for stepIdx, step := range tool.Steps {
		// Execute assignments first.
		for _, a := range step.Assignments {
			val, err := evalExpr(a.Expr, vars, scope, token)
			if err != nil {
				return nil, fmt.Errorf("step %d, assignment $%s: %w", stepIdx, a.VarName, err)
			}
			vars[a.VarName] = val
		}

		// Run assertions.
		for _, a := range step.Assertions {
			if err := evalAssertion(a.Expr, a.Msg, vars, scope, token); err != nil {
				return nil, fmt.Errorf("step %d: %w", stepIdx, err)
			}
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
		// work is done, nothing to request.
		if step.Method == "" {
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
			val, err := evalExpr(step.BodyRaw, vars, scope, token)
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
