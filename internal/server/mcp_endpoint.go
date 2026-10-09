package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"poggers.institute/freshbreath/internal/db"
	"poggers.institute/freshbreath/internal/formats"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// mcpMountEntry holds the MCP server for a virtual or task service. The gate is
// NOT baked in: it resolves per request, so a changed protected_by (or a
// changed admin auth record, which empty slots inherit) takes effect
// without a remount.
type mcpMountEntry struct {
	svc     *db.Service
	mcps    *mcp.Server
	handler http.Handler
}

// mcpMountRegistry manages MCP server instances for virtual and task
// services, each mounted at /mcp/<slug>.
// It supports dynamic registration — services can be added/updated at runtime.
type mcpMountRegistry struct {
	mu      sync.RWMutex
	entries map[string]*mcpMountEntry // slug → entry
}

func newMCPMountRegistry() *mcpMountRegistry {
	return &mcpMountRegistry{entries: make(map[string]*mcpMountEntry)}
}

// add builds and registers an MCP server for a virtual or task service.
// Any other service has no mount of its own and is skipped.
func (r *mcpMountRegistry) add(s *Server, svc *db.Service) {
	slug := mcpSlug(svc.URL)
	var mcps *mcp.Server
	var err error
	switch {
	case slug == "":
		return
	case svc.Descriptor.Type == "virtual":
		mcps, err = s.newVirtualMCPServer(svc)
	case svc.Descriptor.Type == "tasks":
		mcps, err = s.newTaskMCPServer(svc)
	default:
		return
	}
	if err != nil {
		fmt.Printf("warning: %s MCP server for %s: %v\n", svc.Descriptor.Type, slug, err)
		return
	}

	handler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		return mcps
	}, &mcp.StreamableHTTPOptions{
		// Stateful: elicitation steps suspend the tool across a
		// multi-round-trip retry (SEP-2322), which needs a live session on
		// both POSTs — the SDK rejects server-initiated requests from
		// stateless sessions outright. SessionTimeout reaps abandoned
		// sessions; the idle timer pauses while a request is in flight, so
		// a suspended run inside an open tools/call can't be reaped.
		Stateless:      false,
		SessionTimeout: 30 * time.Minute,
	})

	r.mu.Lock()
	r.entries[slug] = &mcpMountEntry{
		svc:     svc,
		mcps:    mcps,
		handler: s.requireMCPGate(svc, handler),
	}
	r.mu.Unlock()
}

// get returns the entry for a slug, or nil.
func (r *mcpMountRegistry) get(slug string) *mcpMountEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.entries[slug]
}

// remove deletes the entry for a slug.
func (r *mcpMountRegistry) remove(slug string) {
	r.mu.Lock()
	delete(r.entries, slug)
	r.mu.Unlock()
}

// removeApp deletes an app service's entry, whatever slug the app had when
// it was mounted.
func (r *mcpMountRegistry) removeApp(nonce string) {
	r.mu.Lock()
	for slug, e := range r.entries {
		if e.svc.AppNonce == nonce {
			delete(r.entries, slug)
		}
	}
	r.mu.Unlock()
}

// requireMCPGate enforces a mount's inbound gate. Every mount resolves its
// protected_by per request — empty inherits the admin record — and demands
// a bearer; the one exception is an explicit Anonymous record, which mounts
// open. This inverts the old behavior where a service with no auth fields
// mounted with no check at all.
func (s *Server) requireMCPGate(svc *db.Service, next http.Handler) http.Handler {
	slug := mcpSlug(svc.URL)
	prmURL := s.config.PublicBaseURL + "/.well-known/oauth-protected-resource/mcp/" + slug
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gate, err := s.resolveServiceGate(svc)
		if err != nil {
			http.Error(w, "gate resolution failed", http.StatusInternalServerError)
			return
		}
		if !gateIsOpen(gate) {
			if _, _, err := s.verifyGateHeader(gate, r.Header); err != nil {
				w.Header().Set("WWW-Authenticate", fmt.Sprintf("Bearer resource_metadata=%q", prmURL))
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// ── MCP Server Factory ───────────────────────────────────────────────

// newVirtualMCPServer creates an MCP server that exposes the virtual service's
// tools via the MCP protocol. Optional opts tweak the ServerOptions before the
// server is built (tests use them to force an old protocol version).
func (s *Server) newVirtualMCPServer(svc *db.Service, opts ...func(*mcp.ServerOptions)) (*mcp.Server, error) {
	tools, err := s.loadVirtualTools(svc)
	if err != nil {
		return nil, fmt.Errorf("load virtual tools: %w", err)
	}

	sopts := &mcp.ServerOptions{
		Instructions: fmt.Sprintf("Virtual service: %s", svc.Name),
	}
	for _, opt := range opts {
		opt(sopts)
	}
	mcps := mcp.NewServer(&mcp.Implementation{
		Name:    fmt.Sprintf("frbr-%s", slugify(svc.Name)),
		Version: "1.0.0",
	}, sopts)

	for _, vt := range tools {
		tool := &mcp.Tool{
			Name:        vt.Name,
			Description: vt.Description,
			InputSchema: virtualToolInputSchema(vt, svc.Descriptor.DatabaseTarget),
		}
		// App-only tools (declared with "!") carry MCP Apps visibility metadata:
		// hosts that support MCP Apps hide them from the model and let only the
		// app UI call them. Plain hosts see the marker but enforce nothing yet.
		if vt.AppOnly {
			tool.Meta = mcp.Meta{"ui": map[string]any{"visibility": []string{"app"}}}
		}
		capturedName := vt.Name
		svcSlug := mcpSlug(svc.URL)
		hooks := s.virtualHooks(svc)
		mcps.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			token, claims, denied := s.mcpCallerCred(svc, req)
			if denied != nil {
				return denied, nil
			}

			// Parse arguments from raw JSON.
			args := make(map[string]interface{})
			if len(req.Params.Arguments) > 0 {
				json.Unmarshal(req.Params.Arguments, &args)
			}

			// A RequestState means this is the retry of a run that was
			// suspended at an elicitation step (SEP-2322 multi-round-trip):
			// the host answers the form/URL prompt and re-issues the call
			// with the responses echoed. Old-protocol hosts reach this same
			// path through the SDK's server middleware, which fulfills the
			// input request with a blocking ss.Elicit and re-invokes the
			// handler.
			if req.Params.RequestState != "" {
				return s.resumeVirtualTool(tools, req)
			}

			auth := s.virtualAuth(token, claims)
			sqlRunner := s.mcpSQLRunner(svc, claims, args)
			result, err := formats.ExecuteVirtualTool(s.httpClient, tools, capturedName, args, auth, sqlRunner,
				&formats.ExecContext{Hooks: hooks})
			var susp *formats.ErrSuspend
			if errors.As(err, &susp) {
				return s.suspendVirtualTool(susp.Susp, svcSlug, req.Session, hooks, sqlRunner), nil
			}
			if err != nil {
				return &mcp.CallToolResult{
					Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
					IsError: true,
				}, nil
			}

			resultJSON, _ := json.Marshal(result)
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: string(resultJSON)}},
			}, nil
		})
	}

	return mcps, nil
}

// newTaskMCPServer creates an MCP server exposing a task service's tasks
// as tools. A task declares no arguments, so each tool's schema lists the
// TASK_<NAME> variables its script reads, all as strings (see
// formats.Task.Args). A file argument is a path on this machine over MCP —
// JSON has no way to carry a file.
func (s *Server) newTaskMCPServer(svc *db.Service) (*mcp.Server, error) {
	tasks, err := s.loadTasksForService(svc)
	if err != nil {
		return nil, err
	}
	mcps := mcp.NewServer(&mcp.Implementation{
		Name:    fmt.Sprintf("frbr-%s", slugify(svc.Name)),
		Version: "1.0.0",
	}, &mcp.ServerOptions{
		Instructions: fmt.Sprintf("Task service: %s", svc.Name),
	})

	for i := range tasks {
		task := &tasks[i]
		properties := map[string]interface{}{}
		for _, arg := range task.Args() {
			properties[arg] = map[string]interface{}{"type": "string"}
		}
		tool := &mcp.Tool{
			Name:        task.Name,
			Description: task.Desc,
			InputSchema: map[string]interface{}{"type": "object", "properties": properties},
		}
		mcps.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			token, _, denied := s.mcpCallerCred(svc, req)
			if denied != nil {
				return denied, nil
			}
			var args map[string]interface{}
			if len(req.Params.Arguments) > 0 {
				if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
					return mcpAuthError("arguments must be an object: %v", err), nil
				}
			}
			result, err := runTask(ctx, task, args, nil, token)
			if err != nil {
				return mcpAuthError("%v", err), nil
			}
			return result, nil
		})
	}
	return mcps, nil
}

// mcpCallerCred re-resolves, per tool call, the caller's claims and the
// credential that goes upstream for a mounted service. The gate middleware
// already admitted the request; the tools need the claims (identity
// built-ins) and the resolver's verdict ($token, TASK_TOKEN). A non-nil
// denied is the error result to hand back instead.
func (s *Server) mcpCallerCred(svc *db.Service, req *mcp.CallToolRequest) (token string, claims *freshbreathClaims, denied *mcp.CallToolResult) {
	gate, err := s.resolveServiceGate(svc)
	if err != nil {
		return "", nil, mcpAuthError("gate resolution: %v", err)
	}

	raw := ""
	var header http.Header
	if req.Extra != nil && req.Extra.Header != nil {
		header = req.Extra.Header
		if ah := header.Get("Authorization"); strings.HasPrefix(ah, "Bearer ") {
			raw = strings.TrimPrefix(ah, "Bearer ")
		}
	}

	var presentedKey string
	if !gateIsOpen(gate) {
		claims, _, err = s.verifyGateHeader(gate, header)
		if err != nil {
			return "", nil, mcpAuthError("auth error: %v", err)
		}
		if gate.Kind == db.AuthAPIKey && header != nil {
			presentedKey = headerGateKey(gate, header)
		}
	}

	cred, err := s.resolveOutboundCred(svc, gate, claims, presentedKey)
	if err != nil {
		return "", nil, mcpAuthError("%v", err)
	}
	token = cred.Token
	if cred.Verbatim && !isFreshbreathToken(raw) {
		// An open gate passes a caller's own upstream bearer through
		// verbatim; a Fresh Breath token is not one.
		token = raw
	}
	return token, claims, nil
}

func mcpAuthError(format string, a ...interface{}) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(format, a...)}},
		IsError: true,
	}
}

// virtualAuth builds the caller identity for a virtual tool execution from
// the verified gate claims. UserID is the numerical Fresh Breath user when
// the subject is a frbr: one; an ext: caller leaves it nil (and $token_id
// resolves to null).
func (s *Server) virtualAuth(token string, claims *freshbreathClaims) formats.VirtualAuth {
	auth := formats.VirtualAuth{Token: token}
	if claims == nil {
		return auth
	}
	auth.Email = claims.UserEmail
	auth.Sub = claims.Subject
	if u, _ := s.userFromSubject(claims.Subject); u != nil {
		auth.UserID = u.ID
	}
	return auth
}

// virtualToolInputSchema builds a JSON Schema input object for a virtual
// tool. Parameters are inferred from $name references in the tool's templates
// that aren't locally assigned — these are what the caller must supply.
//
// Every parameter is required unless its annotation carries `?` — a schema
// where nothing is required teaches a model nothing. Tools with SQL steps on
// a default-target service also expose app_nonce: the MCP path has no
// ambient app context, so the caller must name the app whose data to touch.
// Fixed targets (global / app:<nonce>) know their database already, and pure
// HTTP tools have nothing to point at a database, so the property appears
// exactly where it means something.
func virtualToolInputSchema(vt formats.VirtualTool, dbTarget string) map[string]interface{} {
	props := map[string]interface{}{}
	required := []string{}
	for _, p := range vt.Params {
		prop := map[string]interface{}{"type": string(p.Type)}
		if p.Type == formats.ParamEncrypted {
			// Plaintext on the wire; the executor seals it on arrival.
			prop["type"] = "string"
		}
		if p.Format != "" {
			// Format-typed strings (email/uri/date/date-time): JSON Schema
			// type string + format — the type word itself isn't a schema type.
			prop["type"] = "string"
			prop["format"] = p.Format
		}
		if len(p.Values) > 0 {
			prop["enum"] = p.Values
		}
		props[p.Name] = prop
		if !p.Optional {
			required = append(required, p.Name)
		}
	}
	if hasSQLSteps(vt) && dbTarget == "" {
		props["app_nonce"] = map[string]interface{}{
			"type":        "string",
			"description": "App nonce whose database this tool should use",
		}
		required = append(required, "app_nonce")
	}
	sort.Strings(required)
	return map[string]interface{}{
		"type":       "object",
		"properties": props,
		"required":   required,
	}
}

// hasSQLSteps reports whether any step in the tool runs SQL.
func hasSQLSteps(vt formats.VirtualTool) bool {
	for _, st := range vt.Steps {
		if st.SQL != "" {
			return true
		}
	}
	return false
}

// ── Protected Resource Metadata ─────────────────────────────────────

// virtualPRM builds the Protected Resource Metadata document for a virtual service.
// The authorization_servers field points to Freshbreath itself, since Freshbreath
// acts as the OAuth authorization server for MCP clients.
func (s *Server) virtualPRM(svc *db.Service) *oauthex.ProtectedResourceMetadata {
	slug := mcpSlug(svc.URL)
	return &oauthex.ProtectedResourceMetadata{
		Resource:               s.config.PublicBaseURL + "/mcp/" + slug,
		AuthorizationServers:   []string{s.config.PublicBaseURL},
		ScopesSupported:        []string{"openid", "email", "profile"},
		BearerMethodsSupported: []string{"header"},
		ResourceName:           svc.Name,
	}
}

// ── Route Handlers ───────────────────────────────────────────────────

// handleMCP is the single route handler for all /mcp/{name} requests.
// It looks up the virtual service by slug and dispatches to its MCP server.
func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("name")
	entry := s.mcpMounts.get(slug)
	if entry == nil {
		http.Error(w, "virtual service not found", http.StatusNotFound)
		return
	}
	entry.handler.ServeHTTP(w, r)
}

// handleMCPPRM serves /.well-known/oauth-protected-resource/mcp/{name}.
// An explicitly Anonymous mount advertises nothing; every other gate points
// clients at Fresh Breath's own authorization server.
func (s *Server) handleMCPPRM(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("name")
	entry := s.mcpMounts.get(slug)
	if entry == nil {
		http.Error(w, "virtual service not found", http.StatusNotFound)
		return
	}
	gate, err := s.resolveServiceGate(entry.svc)
	if err != nil {
		http.Error(w, "gate resolution failed", http.StatusInternalServerError)
		return
	}
	if gateIsOpen(gate) {
		http.Error(w, "no auth configured for this service", http.StatusNotFound)
		return
	}
	auth.ProtectedResourceMetadataHandler(s.virtualPRM(entry.svc)).ServeHTTP(w, r)
}

// ── Helpers ──────────────────────────────────────────────────────────

// firstNonEmpty returns the first non-empty string from its arguments.
func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
