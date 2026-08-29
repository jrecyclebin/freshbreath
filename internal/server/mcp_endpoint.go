package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"poggers.institute/freshbreath/internal/db"
	"poggers.institute/freshbreath/internal/formats"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// virtualMCPEntry holds the MCP server and auth config for a virtual service.
type virtualMCPEntry struct {
	mcps    *mcp.Server
	handler http.Handler // final handler (with or without auth middleware)
	prm     *oauthex.ProtectedResourceMetadata
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

	hasAuth := svc.Descriptor.OAuthURL != "" || svc.Descriptor.ClientID != "" || svc.Descriptor.Auth == "key"
	var finalHandler http.Handler
	var prm *oauthex.ProtectedResourceMetadata

	if hasAuth {
		verifier := s.virtualTokenVerifier(svc)
		prm = s.virtualPRM(svc)
		prmURL := s.config.PublicBaseURL + "/.well-known/oauth-protected-resource/mcp/" + slug
		finalHandler = auth.RequireBearerToken(verifier, &auth.RequireBearerTokenOptions{
			ResourceMetadataURL: prmURL,
		})(handler)
	} else {
		finalHandler = handler
	}

	r.mu.Lock()
	r.entries[slug] = &virtualMCPEntry{
		mcps:    mcps,
		handler: finalHandler,
		prm:     prm,
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
			InputSchema: virtualToolInputSchema(vt),
		}
		capturedName := vt.Name
		mcps.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			// Extract token from request header.
			// If it's a Freshbreath-wrapped JWT (MCP OAuth flow), unwrap to get
			// the real upstream token for $token in virtual scripts.
			// If no bearer token and the service uses API-key auth, fall back
			// to the admin-configured key.
			token := ""
			if req.Extra != nil && req.Extra.Header != nil {
				if ah := req.Extra.Header.Get("Authorization"); strings.HasPrefix(ah, "Bearer ") {
					raw := strings.TrimPrefix(ah, "Bearer ")
					wrapped, err := s.verifyAndUnwrapToken(raw, svc.ID)
					if err != nil {
						return &mcp.CallToolResult{
							Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("auth error: %v", err)}},
							IsError: true,
						}, nil
					}
					if wrapped != nil {
						token = wrapped.UpstreamToken
					} else {
						token = raw
					}
				}
			}
			if token == "" && svc.Descriptor.Auth == "key" {
				token = svc.Descriptor.APIKey
			}

			// Parse arguments from raw JSON.
			args := make(map[string]interface{})
			if len(req.Params.Arguments) > 0 {
				json.Unmarshal(req.Params.Arguments, &args)
			}

			result, err := formats.ExecuteVirtualTool(s.httpClient, tools, capturedName, args, token, nil) // SQL runner: Phase 4 wiring
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

// virtualToolInputSchema builds a JSON Schema input object for a virtual tool.
// Parameters are inferred from $name references in the tool's templates that
// aren't locally assigned — these are what the caller must supply.
func virtualToolInputSchema(vt formats.VirtualTool) map[string]interface{} {
	props := map[string]interface{}{}
	for _, p := range vt.Params {
		props[p.Name] = map[string]interface{}{"type": string(p.Type)}
	}
	return map[string]interface{}{
		"type":       "object",
		"properties": props,
	}
}

// ── Token Verification ───────────────────────────────────────────────

// virtualTokenVerifier returns a TokenVerifier that validates Bearer tokens
// using the service's inline OIDC config.
//
// Tokens arrive in two forms:
//  1. Freshbreath-wrapped JWT (from MCP OAuth flow) — contains upstream_token claim
//  2. Direct upstream OIDC JWT — verified against the upstream issuer
func (s *Server) virtualTokenVerifier(svc *db.Service) auth.TokenVerifier {
	return func(ctx context.Context, token string, req *http.Request) (*auth.TokenInfo, error) {
		// Try to unwrap a Freshbreath-wrapped JWT (MCP OAuth flow).
		claims, err := s.verifyAndUnwrapToken(token, svc.ID)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", auth.ErrInvalidToken, err)
		}
		if claims != nil {
			return &auth.TokenInfo{
				UserID:     claims.Subject,
				Scopes:     buildOIDCScopes(claims.UpstreamScopes),
				Expiration: claims.Expiry.Time(),
			}, nil
		}

		// Verify as an OIDC JWT against the service's issuer.
		issuer := svc.Descriptor.OAuthURL
		if issuer == "" {
			return nil, fmt.Errorf("%w: no issuer configured for token verification", auth.ErrInvalidToken)
		}
		issuer = strings.TrimSuffix(issuer, "/authorize")

		provider, err := s.getOIDCProvider(ctx, svc.ID, issuer)
		if err != nil {
			return nil, fmt.Errorf("%w: OIDC discovery failed: %v", auth.ErrInvalidToken, err)
		}

		idToken, err := provider.Verifier(&oidc.Config{ClientID: svc.Descriptor.ClientID}).Verify(ctx, token)
		if err != nil {
			return nil, fmt.Errorf("%w: token verification: %v", auth.ErrInvalidToken, err)
		}

		var oidcClaims struct {
			Email string `json:"email"`
			Sub   string `json:"sub"`
			Exp   int64  `json:"exp"`
		}
		if err := idToken.Claims(&oidcClaims); err != nil {
			return nil, fmt.Errorf("%w: claims: %v", auth.ErrInvalidToken, err)
		}

		return &auth.TokenInfo{
			UserID:     firstNonEmpty(oidcClaims.Email, oidcClaims.Sub),
			Scopes:     buildOIDCScopes(svc.Descriptor.Scopes),
			Expiration: time.Unix(oidcClaims.Exp, 0),
		}, nil
	}
}

// ── Protected Resource Metadata ─────────────────────────────────────

// virtualPRM builds the Protected Resource Metadata document for a virtual service.
// The authorization_servers field points to Freshbreath itself, since Freshbreath
// acts as the OAuth authorization server for MCP clients.
func (s *Server) virtualPRM(svc *db.Service) *oauthex.ProtectedResourceMetadata {
	slug := strings.TrimPrefix(svc.URL, "/mcp/")
	resourceURL := s.config.PublicBaseURL + "/mcp/" + slug

	return &oauthex.ProtectedResourceMetadata{
		Resource:               resourceURL,
		AuthorizationServers:   []string{s.config.PublicBaseURL},
		ScopesSupported:        buildOIDCScopes(svc.Descriptor.Scopes),
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

// handleMCPPRM is the single route handler for all
// /.well-known/oauth-protected-resource/mcp/{name} requests.
// It returns the Protected Resource Metadata for the given virtual service.
func (s *Server) handleMCPPRM(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("name")
	entry := s.virtualMCPs.get(slug)
	if entry == nil {
		http.Error(w, "virtual service not found", http.StatusNotFound)
		return
	}
	if entry.prm == nil {
		http.Error(w, "no auth configured for this service", http.StatusNotFound)
		return
	}
	auth.ProtectedResourceMetadataHandler(entry.prm).ServeHTTP(w, r)
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
