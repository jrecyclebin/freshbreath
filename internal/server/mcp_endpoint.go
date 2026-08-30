package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	"poggers.institute/freshbreath/internal/db"
	"poggers.institute/freshbreath/internal/formats"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// virtualMCPEntry holds the MCP server for a virtual service. The gate is
// NOT baked in: it resolves per request, so a changed protected_by (or a
// changed admin auth record, which empty slots inherit) takes effect
// without a remount.
type virtualMCPEntry struct {
	svc     *db.Service
	mcps    *mcp.Server
	handler http.Handler
}

// virtualMCPRegistry manages MCP server instances for virtual services.
// It supports dynamic registration — services can be added/updated at runtime.
type virtualMCPRegistry struct {
	mu      sync.RWMutex
	entries map[string]*virtualMCPEntry // slug → entry
}

func newVirtualMCPRegistry() *virtualMCPRegistry {
	return &virtualMCPRegistry{entries: make(map[string]*virtualMCPEntry)}
}

// add builds and registers an MCP server for the given virtual service.
func (r *virtualMCPRegistry) add(s *Server, svc *db.Service) {
	slug := strings.TrimPrefix(svc.URL, "/mcp/")

	mcps, err := s.newVirtualMCPServer(svc)
	if err != nil {
		fmt.Printf("warning: virtual MCP server for %s: %v\n", slug, err)
		return
	}

	handler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		return mcps
	}, &mcp.StreamableHTTPOptions{Stateless: true})

	r.mu.Lock()
	r.entries[slug] = &virtualMCPEntry{
		svc:     svc,
		mcps:    mcps,
		handler: s.requireMCPGate(svc, handler),
	}
	r.mu.Unlock()
}

// get returns the entry for a slug, or nil.
func (r *virtualMCPRegistry) get(slug string) *virtualMCPEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.entries[slug]
}

// remove deletes the entry for a slug.
func (r *virtualMCPRegistry) remove(slug string) {
	r.mu.Lock()
	delete(r.entries, slug)
	r.mu.Unlock()
}

// requireMCPGate enforces a mount's inbound gate. Every mount resolves its
// protected_by per request — empty inherits the admin record — and demands
// a bearer; the one exception is an explicit Anonymous record, which mounts
// open. This inverts the old behavior where a service with no auth fields
// mounted with no check at all.
func (s *Server) requireMCPGate(svc *db.Service, next http.Handler) http.Handler {
	slug := strings.TrimPrefix(svc.URL, "/mcp/")
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
// tools via the MCP protocol.
func (s *Server) newVirtualMCPServer(svc *db.Service) (*mcp.Server, error) {
	tools, err := formats.LoadVirtualTools(s.config.DataDir, svc.Name)
	if err != nil {
		return nil, fmt.Errorf("load virtual tools: %w", err)
	}

	mcps := mcp.NewServer(&mcp.Implementation{
		Name:    fmt.Sprintf("frbr-%s", slugify(svc.Name)),
		Version: "1.0.0",
	}, &mcp.ServerOptions{
		Instructions: fmt.Sprintf("Virtual service: %s", svc.Name),
	})

	for _, vt := range tools {
		tool := &mcp.Tool{
			Name:        vt.Name,
			Description: vt.Description,
			InputSchema: virtualToolInputSchema(vt, svc.Descriptor.DatabaseTarget),
		}
		capturedName := vt.Name
		mcps.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			// Re-resolve the gate and outbound credential per call: the
			// middleware verified admission, but $token and the identity
			// built-ins need the claims and the resolver's verdict here.
			gate, err := s.resolveServiceGate(svc)
			if err != nil {
				return mcpAuthError("gate resolution: %v", err), nil
			}

			raw := ""
			var header http.Header
			if req.Extra != nil && req.Extra.Header != nil {
				header = req.Extra.Header
				if ah := header.Get("Authorization"); strings.HasPrefix(ah, "Bearer ") {
					raw = strings.TrimPrefix(ah, "Bearer ")
				}
			}

			var claims *freshbreathClaims
			var presentedKey string
			if !gateIsOpen(gate) {
				claims, _, err = s.verifyGateHeader(gate, header)
				if err != nil {
					return mcpAuthError("auth error: %v", err), nil
				}
				if gate.Kind == db.AuthAPIKey && header != nil {
					presentedKey = headerGateKey(gate, header)
				}
			}

			cred, err := s.resolveOutboundCred(svc, gate, claims, presentedKey)
			if err != nil {
				return mcpAuthError("%v", err), nil
			}
			token := cred.Token
			if cred.Verbatim && !isFreshbreathToken(raw) {
				// An open gate passes a caller's own upstream bearer
				// through verbatim; a Fresh Breath token is not one.
				token = raw
			}

			// Parse arguments from raw JSON.
			args := make(map[string]interface{})
			if len(req.Params.Arguments) > 0 {
				json.Unmarshal(req.Params.Arguments, &args)
			}

			result, err := formats.ExecuteVirtualTool(s.httpClient, tools, capturedName, args,
				s.virtualAuth(token, claims), s.mcpSQLRunner(svc, claims, args))
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
		props[p.Name] = map[string]interface{}{"type": string(p.Type)}
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
	slug := strings.TrimPrefix(svc.URL, "/mcp/")
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
	entry := s.virtualMCPs.get(slug)
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
	entry := s.virtualMCPs.get(slug)
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
