package main

import (
  "context"
  "encoding/json"
  "fmt"
  "net/http"
  "strings"
  "sync"
  "time"

  "github.com/coreos/go-oidc/v3/oidc"
  "github.com/modelcontextprotocol/go-sdk/auth"
  "github.com/modelcontextprotocol/go-sdk/mcp"
  "github.com/modelcontextprotocol/go-sdk/oauthex"
)

// virtualMCPEntry holds the MCP server and auth config for a virtual service.
type virtualMCPEntry struct {
  mcps      *mcp.Server
  handler   http.Handler // final handler (with or without auth middleware)
  prm       *oauthex.ProtectedResourceMetadata
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
func (r *virtualMCPRegistry) add(s *Server, svc *Service) {
  slug := strings.TrimPrefix(svc.URL, "/mcp/")

  mcps, err := s.newVirtualMCPServer(svc)
  if err != nil {
    fmt.Printf("warning: virtual MCP server for %s: %v\n", slug, err)
    return
  }

  handler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
    return mcps
  }, &mcp.StreamableHTTPOptions{Stateless: true})

  hasAuth := svc.Descriptor.OAuthURL != "" || svc.Descriptor.ClientID != ""
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
func (s *Server) newVirtualMCPServer(svc *Service) (*mcp.Server, error) {
  tools, err := loadVirtualTools(s.config.Dir, svc.Name)
  if err != nil {
    return nil, fmt.Errorf("load virtual tools: %w", err)
  }

  mcps := mcp.NewServer(&mcp.Implementation{
    Name:    "freshbreath-virtual",
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
      token := ""
      if req.Extra != nil && req.Extra.Header != nil {
        if ah := req.Extra.Header.Get("Authorization"); strings.HasPrefix(ah, "Bearer ") {
          token = strings.TrimPrefix(ah, "Bearer ")
        }
      }

      // Parse arguments from raw JSON.
      args := make(map[string]interface{})
      if len(req.Params.Arguments) > 0 {
        json.Unmarshal(req.Params.Arguments, &args)
      }

      result, err := executeVirtualTool(s.httpClient, tools, capturedName, args, token)
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
// Since virtual tools accept arbitrary arguments (resolved via $name in templates),
// we use an open object schema with no required properties.
func virtualToolInputSchema(vt VirtualTool) map[string]interface{} {
  return map[string]interface{}{
    "type":       "object",
    "properties": map[string]interface{}{},
  }
}

// ── Token Verification ───────────────────────────────────────────────

// virtualTokenVerifier returns a TokenVerifier that validates Bearer tokens
// using the service's inline OIDC config.
func (s *Server) virtualTokenVerifier(svc *Service) auth.TokenVerifier {
  return func(ctx context.Context, token string, req *http.Request) (*auth.TokenInfo, error) {
    // First check if it's a freshbreath-issued token (from the login flow).
    if isFreshbreathToken(token) {
      email, err := s.verifyFreshbreathToken(token)
      if err != nil {
        return nil, fmt.Errorf("%w: %v", auth.ErrInvalidToken, err)
      }
      return &auth.TokenInfo{
        UserID:     email,
        Scopes:     buildOIDCScopes(svc.Descriptor.Scopes),
        Expiration: time.Now().Add(1 * time.Hour),
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

    var claims struct {
      Email string `json:"email"`
      Sub   string `json:"sub"`
      Exp   int64  `json:"exp"`
    }
    if err := idToken.Claims(&claims); err != nil {
      return nil, fmt.Errorf("%w: claims: %v", auth.ErrInvalidToken, err)
    }

    return &auth.TokenInfo{
      UserID:     firstNonEmpty(claims.Email, claims.Sub),
      Scopes:     buildOIDCScopes(svc.Descriptor.Scopes),
      Expiration: time.Unix(claims.Exp, 0),
    }, nil
  }
}

// ── Protected Resource Metadata ─────────────────────────────────────

// virtualPRM builds the Protected Resource Metadata document for a virtual service.
func (s *Server) virtualPRM(svc *Service) *oauthex.ProtectedResourceMetadata {
  slug := strings.TrimPrefix(svc.URL, "/mcp/")
  resourceURL := s.config.PublicBaseURL + "/mcp/" + slug

  issuer := svc.Descriptor.OAuthURL
  if issuer != "" {
    issuer = strings.TrimSuffix(issuer, "/authorize")
  }

  var authServers []string
  if issuer != "" {
    authServers = []string{issuer}
  }

  return &oauthex.ProtectedResourceMetadata{
    Resource:               resourceURL,
    AuthorizationServers:   authServers,
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
