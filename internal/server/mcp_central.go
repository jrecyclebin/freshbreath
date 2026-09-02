package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"poggers.institute/freshbreath/internal/db"
	"poggers.institute/freshbreath/internal/sshkit"
)

// ── Central MCP Server ──────────────────────────────────────────────
//
// The central MCP server at /mcp exposes the Fresh Breath admin API
// as MCP tools. Auth uses the same control panel auth — an MCP client
// discovers protected resource metadata, initiates the OAuth flow
// against Fresh Breath's authorization server, and Fresh Breath
// authenticates the user against the admin auth service (OIDC).
// The resulting Bearer token is a Fresh Breath JWT that identifies the
// user and their role, used to gate tool access.
//
// Role → tool mapping mirrors the HTTP API:
//
//	Superuser: everything
//	Admin:     everything except settings
//	Member:    apps (own), roles, audit, me, ssh-key
//	Read-only: apps (own), roles, audit, me, ssh-key

// parseID is a shorthand for strconv.ParseInt(s, 10, 64).
func parseID(s string) (int64, error) {
	return strconv.ParseInt(s, 10, 64)
}

// argAuthID reads an optional auth-record reference from tool args.
// present distinguishes "omitted" (keep existing) from an explicit 0 or
// null (clear the slot).
func argAuthID(args map[string]interface{}, key string) (id *int64, present bool) {
	v, ok := args[key]
	if !ok {
		return nil, false
	}
	if n, ok := v.(float64); ok && n != 0 {
		i := int64(n)
		return &i, true
	}
	return nil, true
}

// serviceDescriptorSchema is the whole descriptor, and it is short now:
// a service describes *what it is*, and the two auth slots beside it say
// who may call and what goes upstream. The nine auth-ish properties that
// used to live here moved out to auth records.
var serviceDescriptorSchema = map[string]interface{}{
	"type":            map[string]interface{}{"type": "string", "description": "Service type: mcp, api, tasks, virtual, ssh"},
	"proxied":         map[string]interface{}{"type": "boolean", "description": "Route calls through Fresh Breath rather than direct from the browser"},
	"database_target": map[string]interface{}{"type": "string", "description": "virtual only: '' (each app's own), 'global', or 'app:<nonce>'"},
	"database_name":   map[string]interface{}{"type": "string", "description": "virtual only: which database SQL steps run against"},
}

const centralMCPResource = "/mcp"

// ── Central MCP Token Verifier ──────────────────────────────────────
//
// Validates Bearer tokens for the central /mcp endpoint.
// Accepts Fresh Breath central JWTs (from MCP OAuth flow) and also
// direct OIDC ID tokens from the admin auth service (for clients
// that already have a control panel token).

func (s *Server) centralMCPTokenVerifier() auth.TokenVerifier {
	return func(ctx context.Context, token string, req *http.Request) (*auth.TokenInfo, error) {
		// Every token is verified against the admin auth record —
		// verifyGateToken enforces the record binding (so a token minted
		// under some other record can't authenticate here) and the user is
		// re-resolved from the subject. The token's own role is never
		// trusted.
		user, err := s.verifyAdminTokenFromBearer(token)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", auth.ErrInvalidToken, err)
		}
		return &auth.TokenInfo{
			UserID:     user.Email,
			Scopes:     []string{"openid", "email", "profile"},
			Expiration: time.Now().Add(accessTokenTTL),
			Extra:      map[string]any{"role": user.Role},
		}, nil
	}
}

// verifyAdminTokenFromBearer verifies a raw Bearer token against the admin
// auth record. The token's subject must resolve to a real user row — an
// ext: identity from the right provider is still nobody we know.
func (s *Server) verifyAdminTokenFromBearer(idTokenRaw string) (*db.User, error) {
	rec, err := s.adminAuthRecord()
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, fmt.Errorf("no admin auth configured")
	}
	_, user, err := s.verifyGateToken(rec, idTokenRaw)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, fmt.Errorf("no matching user for token subject")
	}
	return user, nil
}

// ── Protected Resource Metadata ─────────────────────────────────────

func (s *Server) buildCentralMCPPRM() *oauthex.ProtectedResourceMetadata {
	resourceURL := s.config.PublicBaseURL + centralMCPResource
	return &oauthex.ProtectedResourceMetadata{
		Resource:               resourceURL,
		AuthorizationServers:   []string{s.config.PublicBaseURL},
		ScopesSupported:        []string{"openid", "email", "profile"},
		BearerMethodsSupported: []string{"header"},
		ResourceName:           "Fresh Breath",
	}
}

// ── MCP Server Construction ─────────────────────────────────────────

// buildCentralMCPServerForRole constructs the MCP server that exposes the
// Fresh Breath admin API as tools, gated to the given role. Superuser sees
// everything; Admin excludes settings; Member/Read-only see only their own
// apps plus self-service tools. Visibility is enforced at registration
// time, so a Member's tools/list never even advertises admin tools —
// least-privilege by construction.
func (s *Server) buildCentralMCPServerForRole(role string) *mcp.Server {
	mcps := mcp.NewServer(&mcp.Implementation{
		Name:    "freshbreath",
		Version: s.version,
	}, &mcp.ServerOptions{
		Instructions: fmt.Sprintf(`Fresh Breath is an app server designed to give flexiblity to standalone HTML
apps - or any other app that needs third-party connections, auth or an SSH agent.
For static HTML apps being developed from file:/// URLs, Fresh Breath can give
you quick access to third-party services and logins that it is proxying and
negotiating auth or CORS issues so you don't have to. Refer to the 'services'
guide for examples on how to login and use APIs, MCPs, OIDC or virtual services
for an app.

Fresh Breath can also host apps the user is creating for long-term hosting and
sharing with other users. If you follow the services guide, users will get
prompted for their credentials. See the publishing guide if the app proceeds
to that point or if you need guidance on reading or writing files to Fresh
Breath.

Fresh Breath server URL: %q`, s.config.PublicBaseURL),
	})

	// NOTE: Role access to all of the following tools should match
	// the `requireAnyRole` handlers in `handler.go`. Specific tool access
	// is enforced in the tool handlers themselves.

	// ── Apps (mixed: list/get are all-roles; mutate is admin+) ────
	s.registerAppTools(mcps, role)

	// ── Databases (all roles; gateDBTarget decides per call) ──────────
	s.registerDatabaseTools(mcps)

	// ── Services (admin+) ─────────────────────────────────────────
	if roleIn(role, rolesAdminPlus) {
		s.registerServiceTools(mcps)
	}

	// ── Auth records (admin+) ─────────────────────────────────────
	if roleIn(role, rolesAdminPlus) {
		s.registerAuthTools(mcps)
	}

	// ── Users (admin+) ────────────────────────────────────────────
	if roleIn(role, rolesAdminPlus) {
		s.registerUserTools(mcps)
	}

	// ── Roles (all) ───────────────────────────────────────────────
	s.registerRoleTools(mcps)

	// ── Audit (all) ───────────────────────────────────────────────
	s.registerAuditTools(mcps)

	// ── Me (all) ──────────────────────────────────────────────────
	s.registerPersonalTools(mcps)

	// ── Settings (superuser only) ──────────────────────────────────
	if roleIn(role, rolesSuperuser) {
		s.registerSettingsTools(mcps)
	}

	return mcps
}

// centralMCPServerForRole returns the per-role MCP server, building and
// caching it on first use. The per-request HTTP closure calls this with the
// role read from the verified bearer token, so each role gets a stable,
// pre-built server with only its allowed tools registered.
func (s *Server) centralMCPServerForRole(role string) *mcp.Server {
	if role == "" {
		role = "Read-only" // defensive: never hit in practice (verifier always stashes a role)
	}
	s.centralMCPSrvMu.Lock()
	defer s.centralMCPSrvMu.Unlock()
	if s.centralMCPServers == nil {
		s.centralMCPServers = make(map[string]*mcp.Server)
	}
	if mcps, ok := s.centralMCPServers[role]; ok {
		return mcps
	}
	mcps := s.buildCentralMCPServerForRole(role)
	s.centralMCPServers[role] = mcps
	return mcps
}

// roleFromRequest reads the role stashed by auth.RequireBearerToken into the
// request context. Falls back to "Read-only" if absent (defensive only).
func roleFromRequest(r *http.Request) string {
	if ti := auth.TokenInfoFromContext(r.Context()); ti != nil {
		if role, _ := ti.Extra["role"].(string); role != "" {
			return role
		}
	}
	return "Read-only"
}

// addToolIf registers t/h on mcps only when allow is true. Used for
// per-role gating inside mixed-visibility tool groups (the app tools are
// a blend of all-roles and admin-only, so the group can't be gated wholesale).
func addToolIf(allow bool, mcps *mcp.Server, t *mcp.Tool, h mcp.ToolHandler) {
	if allow {
		mcps.AddTool(t, h)
	}
}

// ── Tool helper ─────────────────────────────────────────────────────

// mcpUser extracts the user from an MCP tool call request.
// It checks the Bearer token against both central MCP JWTs and
// the admin auth service OIDC. Returns a synthetic superuser if
// auth is not configured.
func (s *Server) mcpUser(req *mcp.CallToolRequest) (*db.User, error) {
	// Setup mode: no admin auth record configured yet.
	if rec, err := s.adminAuthRecord(); err == nil && rec == nil {
		return &db.User{ID: -1, Name: "Setup Account", Role: "Superuser", Status: "Active"}, nil
	}

	// Extract Bearer token from the MCP request header.
	token := ""
	if req.Extra != nil && req.Extra.Header != nil {
		if ah := req.Extra.Header.Get("Authorization"); strings.HasPrefix(ah, "Bearer ") {
			token = strings.TrimPrefix(ah, "Bearer ")
		}
	}
	if token == "" {
		return nil, fmt.Errorf("missing bearer token")
	}
	return s.verifyAdminTokenFromBearer(token)
}

// mcpInlineMaxBytes is the threshold above which a whole-file read result
// escapes to an act-token URL instead of returning inline. Chunked reads
// (offset/limit set) always stay inline — the client bounded the size. Writes
// have no threshold: if MCP received the bytes, write them; a client that
// wants to skip the inline bloat uses transport:"http" up front.
const mcpInlineMaxBytes = 10 * 1024

// actTokenTransport is the closed enum for the transfer tools' `transport`
// option. "mcp" (default) returns bytes inline; "http" mints an act-token URL
// the client fetches/PUTs over HTTP. Named to accept future sensible additions
// without restructuring.
const (
	actTokenTransportMCP  = "mcp"
	actTokenTransportHTTP = "http"
)

// mintActFileURL mints an act token for method+pathQuery and returns the full
// PublicBaseURL/api/act/<token> URL the MCP client can hand to its fetch tool.
func (s *Server) mintActFileURL(user *db.User, method, pathQuery string) (string, error) {
	tok, err := s.mintActToken(user, method, pathQuery, actTokenTTL)
	if err != nil {
		return "", err
	}
	return s.config.PublicBaseURL + "/api/act/" + tok, nil
}

// appFileActPath builds the act-token target path for an app web file:
// /api/apps/{nonce}/web?file=<relpath>, with the file path query-encoded.
func appFileActPath(nonce, filePath string) string {
	q := url.Values{}
	q.Set("file", filePath)
	return "/api/apps/" + nonce + "/web?" + q.Encode()
}

// serviceFileActPath builds the act-token target path for a service
// definition file: /api/services/{id}/files. Services take no client path —
// the definition path is server-derived.
func serviceFileActPath(id int64) string {
	return "/api/services/" + strconv.FormatInt(id, 10) + "/files"
}

// mcpToolError returns a tool result with an error message.
func mcpToolError(format string, args ...interface{}) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(format, args...)}},
		IsError: true,
	}
}

// mcpToolResult returns a tool result with a JSON-encoded payload.
func mcpToolResult(v interface{}) (*mcp.CallToolResult, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return mcpToolError("marshal error: %v", err), nil
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
	}, nil
}

// mcpFileContent builds a result map for file read content. Valid UTF-8 is
// returned as a plain string; binary data is returned base64-encoded.
func mcpFileContent(data []byte) map[string]interface{} {
	if utf8.Valid(data) {
		return map[string]interface{}{"content": string(data)}
	}
	return map[string]interface{}{
		"content":  base64.StdEncoding.EncodeToString(data),
		"encoding": "base64",
	}
}

// int64Arg returns an argument as an int64, defaulting to 0.
func int64Arg(args map[string]interface{}, key string) int64 {
	if v, ok := args[key].(float64); ok {
		return int64(v)
	}
	return 0
}

// resolveOwnerEmail looks up a user by email and returns their ID. An empty
// email returns nil; a missing user returns an error.
func (s *Server) resolveOwnerEmail(email string) (*int64, error) {
	if email == "" {
		return nil, nil
	}
	u, err := s.store.GetUserByEmail(email)
	if err != nil {
		return nil, fmt.Errorf("owner not found: %v", err)
	}
	return &u.ID, nil
}

// serviceByName looks up a service by name. It returns an error if the name is
// missing, ambiguous, or not found. The caller is responsible for role gating.
func (s *Server) serviceByName(name string) (*db.Service, error) {
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	svc, err := s.store.GetServiceByName(name)
	if err != nil {
		return nil, fmt.Errorf("service not found: %v", err)
	}
	return svc, nil
}

// ── App Tools ───────────────────────────────────────────────────────

func (s *Server) registerAppTools(mcps *mcp.Server, role string) {
	// list_apps
	mcps.AddTool(&mcp.Tool{
		Name:        "list_apps",
		Description: "List all apps (admin+) or apps you're a member of (member/read-only).",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}

		var apps []map[string]interface{}
		if user.Role == "Member" || user.Role == "Read-only" {
			apps, err = s.store.ListAppsForUser(user.ID)
		} else {
			apps, err = s.store.ListApps()
		}
		if err != nil {
			return mcpToolError("db error: %v", err), nil
		}
		return mcpToolResult(map[string]interface{}{"apps": apps})
	})

	// get_app
	mcps.AddTool(&mcp.Tool{
		Name:        "get_app",
		Description: "Get details for a specific app by nonce.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"nonce": map[string]interface{}{"type": "string", "description": "App nonce"},
			},
			"required": []string{"nonce"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}

		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		nonce, _ := args["nonce"].(string)
		if err := s.gateApp(user, nonce); err != nil {
			return mcpToolError("%v", err), nil
		}

		app, err := s.store.GetApp(nonce)
		if err != nil {
			return mcpToolError("app not found: %v", err), nil
		}

		return mcpToolResult(map[string]interface{}{
			"nonce":        app.Nonce,
			"name":         app.Name,
			"url":          app.URL,
			"protected_by": app.ProtectedBy,
		})
	})

	// create_app
	addToolIf(roleIn(role, rolesAdminPlus), mcps, &mcp.Tool{
		Name:        "create_app",
		Description: "Create a new app. Admin+ only.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"name":         map[string]interface{}{"type": "string", "description": "App name"},
				"environment":  map[string]interface{}{"type": "string", "description": "Environment (Development, Staging, Production)"},
				"url":          map[string]interface{}{"type": "string", "description": "App URL"},
				"owner_email":  map[string]interface{}{"type": "string", "description": "Owner email address (optional)"},
				"protected_by": map[string]interface{}{"type": "integer", "description": "Auth record id gating the app; 0 or omitted = Anonymous (open)"},
			},
			"required": []string{"name"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}

		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		name, _ := args["name"].(string)
		if name == "" {
			return mcpToolError("name is required"), nil
		}
		env, _ := args["environment"].(string)
		appURL, _ := args["url"].(string)
		ownerEmail, _ := args["owner_email"].(string)
		ownerID, err := s.resolveOwnerEmail(ownerEmail)
		if err != nil {
			return mcpToolError("%v", err), nil
		}

		protectedBy, _ := argAuthID(args, "protected_by")
		nonce, err := s.coreCreateApp(user, name, env, appURL, ownerID, protectedBy)
		if err != nil {
			return mcpToolError("%v", err), nil
		}
		return mcpToolResult(map[string]string{"nonce": nonce, "name": name})
	})

	// update_app
	addToolIf(roleIn(role, rolesAdminPlus), mcps, &mcp.Tool{
		Name:        "update_app",
		Description: "Update an existing app. Admin+ only.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"nonce":        map[string]interface{}{"type": "string", "description": "App nonce"},
				"name":         map[string]interface{}{"type": "string", "description": "New name"},
				"environment":  map[string]interface{}{"type": "string", "description": "New environment"},
				"url":          map[string]interface{}{"type": "string", "description": "New URL"},
				"owner_email":  map[string]interface{}{"type": "string", "description": "New owner email address (optional)"},
				"protected_by": map[string]interface{}{"type": "integer", "description": "Auth record id gating the app; 0 clears to inherit-admin, omitted keeps the current gate"},
			},
			"required": []string{"nonce", "name"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}

		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		nonce, _ := args["nonce"].(string)
		name, _ := args["name"].(string)
		if nonce == "" || name == "" {
			return mcpToolError("nonce and name are required"), nil
		}
		env, _ := args["environment"].(string)
		appURL, _ := args["url"].(string)
		ownerEmail, _ := args["owner_email"].(string)
		ownerID, err := s.resolveOwnerEmail(ownerEmail)
		if err != nil {
			return mcpToolError("%v", err), nil
		}

		protectedBy, present := argAuthID(args, "protected_by")
		if !present {
			if app, err := s.store.GetApp(nonce); err == nil {
				protectedBy = app.ProtectedBy
			}
		}
		if err := s.coreUpdateApp(user, nonce, name, env, appURL, ownerID, protectedBy); err != nil {
			return mcpToolError("%v", err), nil
		}
		return mcpToolResult(map[string]string{"status": "updated"})
	})

	// delete_app
	addToolIf(roleIn(role, rolesAdminPlus), mcps, &mcp.Tool{
		Name:        "delete_app",
		Description: "Delete an app. Admin+ only.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"nonce": map[string]interface{}{"type": "string", "description": "App nonce"},
			},
			"required": []string{"nonce"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}

		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		nonce, _ := args["nonce"].(string)
		if nonce == "" {
			return mcpToolError("nonce is required"), nil
		}

		if err := s.coreDeleteApp(user, nonce); err != nil {
			return mcpToolError("%v", err), nil
		}
		return mcpToolResult(map[string]string{"status": "deleted"})
	})

	// get_app_members
	mcps.AddTool(&mcp.Tool{
		Name:        "get_app_members",
		Description: "Get the member user IDs for an app. Any role (must be a member for non-admin).",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"nonce": map[string]interface{}{"type": "string", "description": "App nonce"},
			},
			"required": []string{"nonce"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}

		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		nonce, _ := args["nonce"].(string)
		if err := s.gateApp(user, nonce); err != nil {
			return mcpToolError("%v", err), nil
		}

		ids, err := s.store.ListAppMembers(nonce)
		if err != nil {
			return mcpToolError("db error: %v", err), nil
		}
		return mcpToolResult(map[string]interface{}{"members": ids})
	})

	// set_app_members
	addToolIf(roleIn(role, rolesAdminPlus), mcps, &mcp.Tool{
		Name:        "set_app_members",
		Description: "Set the members for an app. Admin+ only.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"nonce":   map[string]interface{}{"type": "string", "description": "App nonce"},
				"members": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "number"}, "description": "User IDs"},
			},
			"required": []string{"nonce", "members"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}

		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		nonce, _ := args["nonce"].(string)
		if nonce == "" {
			return mcpToolError("nonce is required"), nil
		}
		rawMembers, _ := args["members"].([]interface{})
		var members []int64
		for _, m := range rawMembers {
			if f, ok := m.(float64); ok {
				members = append(members, int64(f))
			}
		}

		if err := s.coreSetAppMembers(user, nonce, members); err != nil {
			return mcpToolError("%v", err), nil
		}
		return mcpToolResult(map[string]string{"status": "updated"})
	})

	// get_app_services
	mcps.AddTool(&mcp.Tool{
		Name:        "get_app_services",
		Description: "Get the services linked to an app. Any role (must be a member for non-admin).",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"nonce": map[string]interface{}{"type": "string", "description": "App nonce"},
			},
			"required": []string{"nonce"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}

		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		nonce, _ := args["nonce"].(string)
		if err := s.gateApp(user, nonce); err != nil {
			return mcpToolError("%v", err), nil
		}

		links, err := s.store.GetAppServiceLinks(nonce)
		if err != nil {
			return mcpToolError("db error: %v", err), nil
		}
		return mcpToolResult(map[string]interface{}{"services": links})
	})

	// set_app_services
	addToolIf(roleIn(role, rolesAdminPlus), mcps, &mcp.Tool{
		Name:        "set_app_services",
		Description: "Set the services linked to an app. Admin+ only.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"nonce":    map[string]interface{}{"type": "string", "description": "App nonce"},
				"services": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "number"}, "description": "Service IDs"},
			},
			"required": []string{"nonce", "services"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}

		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		nonce, _ := args["nonce"].(string)
		if nonce == "" {
			return mcpToolError("nonce is required"), nil
		}
		rawServices, _ := args["services"].([]interface{})
		var services []int64
		for _, m := range rawServices {
			if f, ok := m.(float64); ok {
				services = append(services, int64(f))
			}
		}

		if err := s.coreSetAppServices(user, nonce, services); err != nil {
			return mcpToolError("%v", err), nil
		}
		return mcpToolResult(map[string]string{"status": "updated"})
	})

	// list_app_files
	mcps.AddTool(&mcp.Tool{
		Name:        "list_app_files",
		Description: "List an app's hosted web files (path + size). Empty if nothing is published. Optionally search file paths and contents.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"nonce":  map[string]interface{}{"type": "string", "description": "App nonce"},
				"search": map[string]interface{}{"type": "string", "description": "Optional term to filter by path or content (case-insensitive)"},
			},
			"required": []string{"nonce"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}
		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		nonce, _ := args["nonce"].(string)
		search, _ := args["search"].(string)
		files, err := s.coreListAppWeb(user, nonce, search)
		if err != nil {
			return mcpToolError("%v", err), nil
		}
		return mcpToolResult(map[string]interface{}{"files": files})
	})

	// read_app_file
	mcps.AddTool(&mcp.Tool{
		Name:        "read_app_file",
		Description: "Read all or part of a file from an app's web directory. Valid UTF-8 content is returned as a string; binary content is returned base64-encoded.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"nonce":     map[string]interface{}{"type": "string", "description": "App nonce"},
				"path":      map[string]interface{}{"type": "string", "description": "File path relative to the app's web directory"},
				"offset":    map[string]interface{}{"type": "number", "description": "Optional zero-based byte offset"},
				"limit":     map[string]interface{}{"type": "number", "description": "Optional maximum bytes to read"},
				"transport": map[string]interface{}{"type": "string", "enum": []string{"mcp", "http"}, "description": "How to transfer bytes: \"mcp\" (inline, default) or \"http\" (return an act-token URL to fetch/PUT over HTTP — for large files)"},
			},
			"required": []string{"nonce", "path"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}
		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		nonce, _ := args["nonce"].(string)
		path, _ := args["path"].(string)
		offset := int64Arg(args, "offset")
		limit := int64Arg(args, "limit")
		transport, _ := args["transport"].(string)
		chunked := offset != 0 || limit != 0

		switch transport {
		case "", actTokenTransportMCP:
			if chunked {
				data, err := s.coreReadAppFile(user, nonce, path, offset, limit)
				if err != nil {
					return mcpToolError("%v", err), nil
				}
				return mcpToolResult(mcpFileContent(data))
			}
			// Whole-file: bound the read so a huge file isn't loaded just to
			// decide it's too big; escape to an act-token URL if it is.
			data, err := s.coreReadAppFile(user, nonce, path, 0, int64(mcpInlineMaxBytes)+1)
			if err != nil {
				return mcpToolError("%v", err), nil
			}
			if int64(len(data)) > mcpInlineMaxBytes {
				size, ct, serr := s.coreStatAppFile(user, nonce, path)
				if serr != nil {
					return mcpToolError("%v", serr), nil
				}
				u, merr := s.mintActFileURL(user, http.MethodGet, appFileActPath(nonce, path))
				if merr != nil {
					return mcpToolError("%v", merr), nil
				}
				return mcpToolResult(map[string]interface{}{
					"error":            "file too large for inline MCP response",
					"url":              u,
					"method":           http.MethodGet,
					"size":             size,
					"content_type":     ct,
					"max_inline_bytes": mcpInlineMaxBytes,
				})
			}
			return mcpToolResult(mcpFileContent(data))

		case actTokenTransportHTTP:
			if chunked {
				return mcpToolError("transport:\"http\" is incompatible with offset/limit (the URL targets the whole file)"), nil
			}
			size, ct, err := s.coreStatAppFile(user, nonce, path)
			if err != nil {
				return mcpToolError("%v", err), nil
			}
			u, err := s.mintActFileURL(user, http.MethodGet, appFileActPath(nonce, path))
			if err != nil {
				return mcpToolError("%v", err), nil
			}
			return mcpToolResult(map[string]interface{}{
				"url":          u,
				"method":       http.MethodGet,
				"size":         size,
				"content_type": ct,
			})

		default:
			return mcpToolError("transport must be \"mcp\" or \"http\", got %q", transport), nil
		}
	})

	// write_app_file
	mcps.AddTool(&mcp.Tool{
		Name:        "write_app_file",
		Description: "Write or patch a file in an app's web directory. Without old_text the entire file is replaced. With old_text, the single occurrence of old_text is replaced with content. An error is returned if old_text is not found or appears more than once.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"nonce":     map[string]interface{}{"type": "string", "description": "App nonce"},
				"path":      map[string]interface{}{"type": "string", "description": "File path relative to the app's web directory"},
				"content":   map[string]interface{}{"type": "string", "description": "New file content"},
				"old_text":  map[string]interface{}{"type": "string", "description": "Optional existing text to replace (must appear exactly once)"},
				"transport": map[string]interface{}{"type": "string", "enum": []string{"mcp", "http"}, "description": "How to transfer bytes: \"mcp\" (inline, default) or \"http\" (return an act-token URL to PUT over HTTP — for large files). Incompatible with old_text."},
			},
			"required": []string{"nonce", "path", "content"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}
		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		nonce, _ := args["nonce"].(string)
		path, _ := args["path"].(string)
		content, _ := args["content"].(string)
		oldText, _ := args["old_text"].(string)
		transport, _ := args["transport"].(string)

		switch transport {
		case "", actTokenTransportMCP:
			if err := s.coreWriteAppFile(user, nonce, path, []byte(content), oldText); err != nil {
				return mcpToolError("%v", err), nil
			}
			return mcpToolResult(map[string]string{"status": "written"})

		case actTokenTransportHTTP:
			if oldText != "" {
				return mcpToolError("transport:\"http\" is incompatible with old_text (patches stay inline)"), nil
			}
			// Gate at mint time so a non-member gets a clear error instead of a
			// URL that 403s at dispatch. coreWriteAppFile gates on the mcp path.
			if err := s.gateApp(user, nonce); err != nil {
				return mcpToolError("%v", err), nil
			}
			u, err := s.mintActFileURL(user, http.MethodPut, appFileActPath(nonce, path))
			if err != nil {
				return mcpToolError("%v", err), nil
			}
			return mcpToolResult(map[string]string{"url": u, "method": http.MethodPut})

		default:
			return mcpToolError("transport must be \"mcp\" or \"http\", got %q", transport), nil
		}
	})

	// delete_app_file
	mcps.AddTool(&mcp.Tool{
		Name:        "delete_app_file",
		Description: "Delete a file from an app's web directory.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"nonce": map[string]interface{}{"type": "string", "description": "App nonce"},
				"path":  map[string]interface{}{"type": "string", "description": "File path relative to the app's web directory"},
			},
			"required": []string{"nonce", "path"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}
		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		nonce, _ := args["nonce"].(string)
		path, _ := args["path"].(string)
		if err := s.coreDeleteAppFile(user, nonce, path); err != nil {
			return mcpToolError("%v", err), nil
		}
		return mcpToolResult(map[string]string{"status": "deleted"})
	})
}

// ── Service Tools ───────────────────────────────────────────────────

func (s *Server) registerServiceTools(mcps *mcp.Server) {
	// list_service_files
	mcps.AddTool(&mcp.Tool{
		Name:        "list_service_files",
		Description: "List a virtual or task service's definition file. Empty if nothing is published. Optionally search the file's content. Admin+ only.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"name":   map[string]interface{}{"type": "string", "description": "Service name"},
				"search": map[string]interface{}{"type": "string", "description": "Optional term to filter by file content (case-insensitive)"},
			},
			"required": []string{"name"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}
		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		name, _ := args["name"].(string)
		search, _ := args["search"].(string)
		if err := s.gate(user, rolesAdminPlus); err != nil {
			return mcpToolError("%v", err), nil
		}
		svc, err := s.serviceByName(name)
		if err != nil {
			return mcpToolError("%v", err), nil
		}
		files, err := s.coreListServiceFiles(user, svc.ID, search)
		if err != nil {
			return mcpToolError("%v", err), nil
		}
		return mcpToolResult(map[string]interface{}{"files": files})
	})

	// read_service_file
	mcps.AddTool(&mcp.Tool{
		Name:        "read_service_file",
		Description: "Read all or part of a virtual or task service's definition file. Admin+ only.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"name":      map[string]interface{}{"type": "string", "description": "Service name"},
				"offset":    map[string]interface{}{"type": "number", "description": "Optional zero-based byte offset"},
				"limit":     map[string]interface{}{"type": "number", "description": "Optional maximum bytes to read"},
				"transport": map[string]interface{}{"type": "string", "enum": []string{"mcp", "http"}, "description": "How to transfer bytes: \"mcp\" (inline, default) or \"http\" (return an act-token URL to fetch over HTTP — for large files)"},
			},
			"required": []string{"name"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}
		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		name, _ := args["name"].(string)
		offset := int64Arg(args, "offset")
		limit := int64Arg(args, "limit")
		transport, _ := args["transport"].(string)
		chunked := offset != 0 || limit != 0

		if err := s.gate(user, rolesAdminPlus); err != nil {
			return mcpToolError("%v", err), nil
		}
		svc, err := s.serviceByName(name)
		if err != nil {
			return mcpToolError("%v", err), nil
		}
		pathQuery := serviceFileActPath(svc.ID)

		switch transport {
		case "", actTokenTransportMCP:
			if chunked {
				data, _, err := s.coreReadServiceFile(user, svc.ID, offset, limit)
				if err != nil {
					return mcpToolError("%v", err), nil
				}
				return mcpToolResult(mcpFileContent(data))
			}
			data, _, err := s.coreReadServiceFile(user, svc.ID, 0, int64(mcpInlineMaxBytes)+1)
			if err != nil {
				return mcpToolError("%v", err), nil
			}
			if int64(len(data)) > mcpInlineMaxBytes {
				size, ct, serr := s.coreStatServiceFile(user, svc.ID)
				if serr != nil {
					return mcpToolError("%v", serr), nil
				}
				u, merr := s.mintActFileURL(user, http.MethodGet, pathQuery)
				if merr != nil {
					return mcpToolError("%v", merr), nil
				}
				return mcpToolResult(map[string]interface{}{
					"error":            "file too large for inline MCP response",
					"url":              u,
					"method":           http.MethodGet,
					"size":             size,
					"content_type":     ct,
					"max_inline_bytes": mcpInlineMaxBytes,
				})
			}
			return mcpToolResult(mcpFileContent(data))

		case actTokenTransportHTTP:
			if chunked {
				return mcpToolError("transport:\"http\" is incompatible with offset/limit (the URL targets the whole file)"), nil
			}
			size, ct, err := s.coreStatServiceFile(user, svc.ID)
			if err != nil {
				return mcpToolError("%v", err), nil
			}
			u, err := s.mintActFileURL(user, http.MethodGet, pathQuery)
			if err != nil {
				return mcpToolError("%v", err), nil
			}
			return mcpToolResult(map[string]interface{}{
				"url":          u,
				"method":       http.MethodGet,
				"size":         size,
				"content_type": ct,
			})

		default:
			return mcpToolError("transport must be \"mcp\" or \"http\", got %q", transport), nil
		}
	})

	// write_service_file
	mcps.AddTool(&mcp.Tool{
		Name:        "write_service_file",
		Description: "Write or patch a virtual or task service's definition file. Without old_text the entire file is replaced. With old_text, the single occurrence of old_text is replaced with content. An error is returned if old_text is not found or appears more than once. Admin+ only.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"name":      map[string]interface{}{"type": "string", "description": "Service name"},
				"content":   map[string]interface{}{"type": "string", "description": "New file content"},
				"old_text":  map[string]interface{}{"type": "string", "description": "Optional existing text to replace (must appear exactly once)"},
				"transport": map[string]interface{}{"type": "string", "enum": []string{"mcp", "http"}, "description": "How to transfer bytes: \"mcp\" (inline, default) or \"http\" (return an act-token URL to PUT over HTTP — for large files). Incompatible with old_text."},
			},
			"required": []string{"name", "content"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}
		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		name, _ := args["name"].(string)
		content, _ := args["content"].(string)
		oldText, _ := args["old_text"].(string)
		transport, _ := args["transport"].(string)

		switch transport {
		case "", actTokenTransportMCP:
			if err := s.gate(user, rolesAdminPlus); err != nil {
				return mcpToolError("%v", err), nil
			}
			svc, err := s.serviceByName(name)
			if err != nil {
				return mcpToolError("%v", err), nil
			}
			if err := s.coreWriteServiceFile(user, svc.ID, []byte(content), oldText); err != nil {
				return mcpToolError("%v", err), nil
			}
			return mcpToolResult(map[string]string{"status": "written"})

		case actTokenTransportHTTP:
			if oldText != "" {
				return mcpToolError("transport:\"http\" is incompatible with old_text (patches stay inline)"), nil
			}
			if err := s.gate(user, rolesAdminPlus); err != nil {
				return mcpToolError("%v", err), nil
			}
			svc, err := s.serviceByName(name)
			if err != nil {
				return mcpToolError("%v", err), nil
			}
			u, err := s.mintActFileURL(user, http.MethodPut, serviceFileActPath(svc.ID))
			if err != nil {
				return mcpToolError("%v", err), nil
			}
			return mcpToolResult(map[string]string{"url": u, "method": http.MethodPut})

		default:
			return mcpToolError("transport must be \"mcp\" or \"http\", got %q", transport), nil
		}
	})

	// delete_service_file
	mcps.AddTool(&mcp.Tool{
		Name:        "delete_service_file",
		Description: "Delete a virtual or task service's definition file. Admin+ only.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"name": map[string]interface{}{"type": "string", "description": "Service name"},
			},
			"required": []string{"name"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}
		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		name, _ := args["name"].(string)
		svc, err := s.serviceByName(name)
		if err != nil {
			return mcpToolError("%v", err), nil
		}
		if err := s.coreDeleteServiceFiles(user, svc.ID); err != nil {
			return mcpToolError("%v", err), nil
		}
		return mcpToolResult(map[string]string{"status": "deleted"})
	})

	// list_services
	mcps.AddTool(&mcp.Tool{
		Name:        "list_services",
		Description: "List all services. Admin+ only.",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}

		services, err := s.store.ListServices()
		if err != nil {
			return mcpToolError("db error: %v", err), nil
		}
		return mcpToolResult(map[string]interface{}{"services": services})
	})

	// get_service
	mcps.AddTool(&mcp.Tool{
		Name:        "get_service",
		Description: "Get details for a specific service. Admin+ only.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"name": map[string]interface{}{"type": "string", "description": "Service name"},
			},
			"required": []string{"name"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}

		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		name, _ := args["name"].(string)

		svc, err := s.serviceByName(name)
		if err != nil {
			return mcpToolError("%v", err), nil
		}
		return mcpToolResult(svc)
	})

	// create_service
	mcps.AddTool(&mcp.Tool{
		Name:        "create_service",
		Description: "Create a new service. Admin+ only.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"name": map[string]interface{}{"type": "string", "description": "Service name"},
				"url":  map[string]interface{}{"type": "string", "description": "Service URL"},
				"descriptor": map[string]interface{}{
					"type":        "object",
					"description": "Service descriptor — what the service *is*. Authentication is not in here; it lives in the two slots below.",
					"properties":  serviceDescriptorSchema,
				},
				"protected_by": map[string]interface{}{"type": "integer", "description": "Auth record id callers must clear; omitted or 0 = inherit the admin record (NOT open — use an anonymous record for open)"},
				"acts_as":      map[string]interface{}{"type": "integer", "description": "Auth record id supplying the upstream credential; omitted or 0 = pass the caller's own"},
			},
			"required": []string{"name"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}

		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		name, _ := args["name"].(string)
		if name == "" {
			return mcpToolError("name is required"), nil
		}
		svcURL, _ := args["url"].(string)

		var desc db.ServiceDescriptor
		if raw, ok := args["descriptor"].(map[string]interface{}); ok {
			descBytes, _ := json.Marshal(raw)
			json.Unmarshal(descBytes, &desc)
		}

		protectedBy, _ := argAuthID(args, "protected_by")
		actsAs, _ := argAuthID(args, "acts_as")
		svc, err := s.coreCreateService(user, name, svcURL, desc, protectedBy, actsAs)
		if err != nil {
			return mcpToolError("%v", err), nil
		}
		return mcpToolResult(map[string]interface{}{
			"id": svc.ID, "name": svc.Name, "url": svc.URL, "descriptor": svc.Descriptor,
		})
	})

	// update_service
	mcps.AddTool(&mcp.Tool{
		Name:        "update_service",
		Description: "Update an existing service. Admin+ only.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"name":     map[string]interface{}{"type": "string", "description": "Current service name"},
				"new_name": map[string]interface{}{"type": "string", "description": "New name"},
				"url":      map[string]interface{}{"type": "string", "description": "New URL"},
				"descriptor": map[string]interface{}{
					"type":        "object",
					"description": "Service descriptor — what the service *is*. Authentication is not in here; it lives in the two slots below.",
					"properties":  serviceDescriptorSchema,
				},
				"protected_by": map[string]interface{}{"type": "integer", "description": "Auth record id callers must clear; 0 clears the slot back to inheriting the admin record"},
				"acts_as":      map[string]interface{}{"type": "integer", "description": "Auth record id supplying the upstream credential; 0 clears the slot back to the caller's own"},
			},
			"required": []string{"name"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}

		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		name, _ := args["name"].(string)
		if name == "" {
			return mcpToolError("name is required"), nil
		}

		// MCP update is a patch: fill blanks from the existing record before
		// handing fully-resolved fields to the (replace-semantics) core.
		existing, err := s.serviceByName(name)
		if err != nil {
			return mcpToolError("%v", err), nil
		}
		newName, _ := args["new_name"].(string)
		if newName == "" {
			newName = existing.Name
		}
		svcURL, _ := args["url"].(string)
		if svcURL == "" {
			svcURL = existing.URL
		}
		desc := existing.Descriptor
		if raw, ok := args["descriptor"].(map[string]interface{}); ok {
			descBytes, _ := json.Marshal(raw)
			json.Unmarshal(descBytes, &desc)
		}

		protectedBy, present := argAuthID(args, "protected_by")
		if !present {
			protectedBy = existing.ProtectedBy
		}
		actsAs, present := argAuthID(args, "acts_as")
		if !present {
			actsAs = existing.ActsAs
		}
		if err := s.coreUpdateService(user, existing.ID, newName, svcURL, desc, protectedBy, actsAs); err != nil {
			return mcpToolError("%v", err), nil
		}
		return mcpToolResult(map[string]string{"status": "updated"})
	})

	// delete_service
	mcps.AddTool(&mcp.Tool{
		Name:        "delete_service",
		Description: "Delete a service (cannot delete the built-in SSH service). Admin+ only.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"name": map[string]interface{}{"type": "string", "description": "Service name"},
			},
			"required": []string{"name"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}

		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		name, _ := args["name"].(string)
		svc, err := s.serviceByName(name)
		if err != nil {
			return mcpToolError("%v", err), nil
		}

		if err := s.coreDeleteService(user, svc.ID); err != nil {
			return mcpToolError("%v", err), nil
		}
		return mcpToolResult(map[string]string{"status": "deleted"})
	})

	// get_service_apps
	mcps.AddTool(&mcp.Tool{
		Name:        "get_service_apps",
		Description: "Get the apps using a specific service. Admin+ only.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"name": map[string]interface{}{"type": "string", "description": "Service name"},
			},
			"required": []string{"name"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}

		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		name, _ := args["name"].(string)
		svc, err := s.serviceByName(name)
		if err != nil {
			return mcpToolError("%v", err), nil
		}

		apps, err := s.store.GetAppsUsingService(svc.ID)
		if err != nil {
			return mcpToolError("db error: %v", err), nil
		}
		return mcpToolResult(map[string]interface{}{"apps": apps})
	})
}

// ── Auth Record Tools ───────────────────────────────────────────────
//
// Slots point at auth records, so an agent that can fill a slot but not
// create the record it names can only ever rewire what a human already
// built. These four close that loop.

// authRecordByName looks a record up by name, the handle an agent has.
// Ambiguity and absence are both errors — the caller gates the role.
func (s *Server) authRecordByName(name string) (*db.AuthRecord, error) {
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	records, err := s.store.ListAuthRecords()
	if err != nil {
		return nil, err
	}
	var found *db.AuthRecord
	for _, rec := range records {
		if strings.EqualFold(rec.Name, name) {
			if found != nil {
				return nil, fmt.Errorf("more than one auth record is named %q", name)
			}
			found = rec
		}
	}
	if found == nil {
		return nil, fmt.Errorf("auth record not found: %s", name)
	}
	return found, nil
}

// authDescriptorSchema mirrors db.AuthDescriptor. Which fields matter
// depends entirely on the kind, which is why the descriptions say so
// rather than pretending to a shape they don't have.
var authDescriptorSchema = map[string]interface{}{
	"issuer":              map[string]interface{}{"type": "string", "description": "oidc: issuer URL; endpoints are discovered from it"},
	"authorize_url":       map[string]interface{}{"type": "string", "description": "oauth2: authorization endpoint"},
	"token_url":           map[string]interface{}{"type": "string", "description": "oauth2: token endpoint"},
	"userinfo_url":        map[string]interface{}{"type": "string", "description": "oauth2: profile endpoint"},
	"userinfo_emails_url": map[string]interface{}{"type": "string", "description": "oauth2: email endpoint, when the provider keeps email off the profile"},
	"client_id":           map[string]interface{}{"type": "string", "description": "oidc/oauth2: OAuth client id"},
	"client_secret":       map[string]interface{}{"type": "string", "description": "oidc/oauth2: OAuth client secret. Never read back; omit on update to keep the stored one"},
	"scopes":              map[string]interface{}{"type": "string", "description": "oidc/oauth2: space-separated scopes"},
	"provider":            map[string]interface{}{"type": "string", "description": "oidc/oauth2: upstream slug (e.g. 'github'). Two records over the same upstream share a slug, and so share one identity for the people behind them"},
	"key":                 map[string]interface{}{"type": "string", "description": "api_key: the stored key. ssh_key: an optional stored private key; empty checks passphrases against each user's own. Never read back"},
	"header":              map[string]interface{}{"type": "string", "description": "api_key: header to send it under; empty means Authorization: Bearer"},
}

func (s *Server) registerAuthTools(mcps *mcp.Server) {
	// list_auth
	mcps.AddTool(&mcp.Tool{
		Name: "list_auth",
		Description: "List auth records — the credentials and login methods services and apps point at. " +
			"Secrets are masked; has_secret says whether one is on file. Admin+ only.",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if _, err := s.mcpUser(req); err != nil {
			return mcpToolError("auth: %v", err), nil
		}
		records, err := s.store.ListAuthRecords()
		if err != nil {
			return mcpToolError("db error: %v", err), nil
		}
		if records == nil {
			records = []*db.AuthRecord{}
		}
		return mcpToolResult(map[string]interface{}{"auth": records})
	})

	// create_auth
	mcps.AddTool(&mcp.Tool{
		Name: "create_auth",
		Description: "Create an auth record. Point a service or app at it with the protected_by " +
			"(who may call in) or acts_as (what goes upstream) slots. Admin+ only.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"name": map[string]interface{}{"type": "string", "description": "Record name, unique across the instance"},
				"kind": map[string]interface{}{
					"type": "string",
					"enum": []string{db.AuthAnonymous, db.AuthSSHKey, db.AuthOIDC, db.AuthOAuth2, db.AuthAPIKey},
					"description": "anonymous (explicitly open) | ssh_key (a registered user's passphrase) | " +
						"oidc (discovery) | oauth2 (explicit endpoints) | api_key (a stored key). " +
						"Every kind is eligible in both slots; what it means is what changes",
				},
				"descriptor": map[string]interface{}{
					"type":        "object",
					"description": "Kind-specific configuration",
					"properties":  authDescriptorSchema,
				},
			},
			"required": []string{"name", "kind"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}
		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		name, _ := args["name"].(string)
		kind, _ := args["kind"].(string)

		var desc db.AuthDescriptor
		if raw, ok := args["descriptor"].(map[string]interface{}); ok {
			descBytes, _ := json.Marshal(raw)
			json.Unmarshal(descBytes, &desc)
		}
		rec, err := s.coreCreateAuth(user, name, kind, desc)
		if err != nil {
			return mcpToolError("%v", err), nil
		}
		return mcpToolResult(rec)
	})

	// update_auth
	mcps.AddTool(&mcp.Tool{
		Name: "update_auth",
		Description: "Update an auth record. A patch: omitted fields keep their stored values, and an " +
			"omitted client_secret or key keeps the stored secret. Built-in records keep their name " +
			"and kind. Admin+ only.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"name":     map[string]interface{}{"type": "string", "description": "Current record name"},
				"new_name": map[string]interface{}{"type": "string", "description": "New name"},
				"kind":     map[string]interface{}{"type": "string", "description": "New kind; changing it usually means rewriting the descriptor too"},
				"descriptor": map[string]interface{}{
					"type":        "object",
					"description": "Kind-specific configuration; replaces the stored one apart from omitted secrets",
					"properties":  authDescriptorSchema,
				},
			},
			"required": []string{"name"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}
		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		name, _ := args["name"].(string)

		existing, err := s.authRecordByName(name)
		if err != nil {
			return mcpToolError("%v", err), nil
		}
		newName, _ := args["new_name"].(string)
		if newName == "" {
			newName = existing.Name
		}
		kind, _ := args["kind"].(string)
		if kind == "" {
			kind = existing.Kind
		}
		desc := existing.Descriptor
		if raw, ok := args["descriptor"].(map[string]interface{}); ok {
			descBytes, _ := json.Marshal(raw)
			json.Unmarshal(descBytes, &desc)
		}
		if err := s.coreUpdateAuth(user, existing.ID, newName, kind, desc); err != nil {
			return mcpToolError("%v", err), nil
		}
		return mcpToolResult(map[string]string{"status": "updated"})
	})

	// delete_auth
	mcps.AddTool(&mcp.Tool{
		Name: "delete_auth",
		Description: "Delete an auth record. Refused while any service or app still points at it, and " +
			"for built-in records. Admin+ only.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"name": map[string]interface{}{"type": "string", "description": "Record name"},
			},
			"required": []string{"name"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}
		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		name, _ := args["name"].(string)

		rec, err := s.authRecordByName(name)
		if err != nil {
			return mcpToolError("%v", err), nil
		}
		if err := s.coreDeleteAuth(user, rec.ID); err != nil {
			return mcpToolError("%v", err), nil
		}
		return mcpToolResult(map[string]string{"status": "deleted"})
	})
}

// ── User Tools ──────────────────────────────────────────────────────

func (s *Server) registerUserTools(mcps *mcp.Server) {
	// list_users
	mcps.AddTool(&mcp.Tool{
		Name:        "list_users",
		Description: "List all users with their app assignments. Admin+ only.",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}

		users, err := s.store.ListUsers()
		if err != nil {
			return mcpToolError("db error: %v", err), nil
		}
		for _, u := range users {
			apps, _ := s.store.GetUserApps(u.ID)
			u.Apps = apps
		}
		return mcpToolResult(map[string]interface{}{"users": users})
	})

	// get_user
	mcps.AddTool(&mcp.Tool{
		Name:        "get_user",
		Description: "Get details for a specific user. Admin+ only.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"id": map[string]interface{}{"type": "number", "description": "User ID"},
			},
			"required": []string{"id"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}

		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		id, _ := args["id"].(float64)
		if id == 0 {
			return mcpToolError("id is required"), nil
		}

		u, err := s.store.GetUser(int64(id))
		if err != nil {
			return mcpToolError("user not found: %v", err), nil
		}
		return mcpToolResult(u)
	})

	// create_user
	mcps.AddTool(&mcp.Tool{
		Name:        "create_user",
		Description: "Create a new user. Admin+ only.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"name":   map[string]interface{}{"type": "string", "description": "User name"},
				"email":  map[string]interface{}{"type": "string", "description": "User email"},
				"role":   map[string]interface{}{"type": "string", "description": "Role: Superuser, Admin, Member, Read-only"},
				"status": map[string]interface{}{"type": "string", "description": "Status: Active, Inactive"},
			},
			"required": []string{"name", "email"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}

		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		name, _ := args["name"].(string)
		email, _ := args["email"].(string)
		if name == "" || email == "" {
			return mcpToolError("name and email are required"), nil
		}
		role, _ := args["role"].(string)
		status, _ := args["status"].(string)

		newUser, err := s.coreCreateUser(user, name, email, role, status)
		if err != nil {
			return mcpToolError("%v", err), nil
		}
		return mcpToolResult(newUser)
	})

	// update_user
	mcps.AddTool(&mcp.Tool{
		Name:        "update_user",
		Description: "Update a user's name, email, role, or status. Admin+ only.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"id":     map[string]interface{}{"type": "number", "description": "User ID"},
				"name":   map[string]interface{}{"type": "string", "description": "New name"},
				"email":  map[string]interface{}{"type": "string", "description": "New email"},
				"role":   map[string]interface{}{"type": "string", "description": "New role"},
				"status": map[string]interface{}{"type": "string", "description": "New status"},
			},
			"required": []string{"id"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}

		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		id, _ := args["id"].(float64)
		if id == 0 {
			return mcpToolError("id is required"), nil
		}

		userID := int64(id)
		existing, err := s.store.GetUser(userID)
		if err != nil {
			return mcpToolError("user not found: %v", err), nil
		}

		name, _ := args["name"].(string)
		if name == "" {
			name = existing.Name
		}
		email, _ := args["email"].(string)
		if email == "" {
			email = existing.Email
		}
		role, _ := args["role"].(string)
		if role == "" {
			role = existing.Role
		}
		status, _ := args["status"].(string)
		if status == "" {
			status = existing.Status
		}

		if err := s.coreUpdateUser(user, userID, name, email, role, status, existing.Metadata); err != nil {
			return mcpToolError("%v", err), nil
		}
		return mcpToolResult(map[string]string{"status": "updated"})
	})

	// delete_user
	mcps.AddTool(&mcp.Tool{
		Name:        "delete_user",
		Description: "Delete a user. Admin+ only.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"id": map[string]interface{}{"type": "number", "description": "User ID"},
			},
			"required": []string{"id"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}

		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		id, _ := args["id"].(float64)
		if id == 0 {
			return mcpToolError("id is required"), nil
		}

		u, err := s.store.GetUser(int64(id))
		if err != nil {
			return mcpToolError("user not found: %v", err), nil
		}
		if err := s.coreDeleteUser(user, u); err != nil {
			return mcpToolError("%v", err), nil
		}
		return mcpToolResult(map[string]string{"status": "deleted"})
	})

	// get_user_apps
	mcps.AddTool(&mcp.Tool{
		Name:        "get_user_apps",
		Description: "Get the app nonces assigned to a user. Admin+ only.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"id": map[string]interface{}{"type": "number", "description": "User ID"},
			},
			"required": []string{"id"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}

		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		id, _ := args["id"].(float64)
		if id == 0 {
			return mcpToolError("id is required"), nil
		}

		nonces, err := s.store.GetUserApps(int64(id))
		if err != nil {
			return mcpToolError("db error: %v", err), nil
		}
		return mcpToolResult(map[string]interface{}{"apps": nonces})
	})

	// set_user_apps
	mcps.AddTool(&mcp.Tool{
		Name:        "set_user_apps",
		Description: "Set the app nonces assigned to a user. Admin+ only.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"id":   map[string]interface{}{"type": "number", "description": "User ID"},
				"apps": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "App nonces"},
			},
			"required": []string{"id", "apps"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}

		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		id, _ := args["id"].(float64)
		if id == 0 {
			return mcpToolError("id is required"), nil
		}
		rawApps, _ := args["apps"].([]interface{})
		var apps []string
		for _, a := range rawApps {
			if s, ok := a.(string); ok {
				apps = append(apps, s)
			}
		}

		if err := s.coreSetUserApps(user, int64(id), apps); err != nil {
			return mcpToolError("%v", err), nil
		}
		return mcpToolResult(map[string]string{"status": "updated"})
	})

	// get_user_ssh_key
	mcps.AddTool(&mcp.Tool{
		Name:        "get_user_ssh_key",
		Description: "Get the SSH key info for a user. Admin+ only.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"id": map[string]interface{}{"type": "number", "description": "User ID"},
			},
			"required": []string{"id"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}

		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		id, _ := args["id"].(float64)
		if id == 0 {
			return mcpToolError("id is required"), nil
		}

		u, err := s.store.GetUser(int64(id))
		if err != nil {
			return mcpToolError("user not found: %v", err), nil
		}
		var info *sshkit.SSHKeyInfo
		if u.Metadata != nil {
			info = publicSSHInfo(u.Metadata.SSHKey)
		}
		return mcpToolResult(map[string]interface{}{"ssh_key": info})
	})

	// generate_user_ssh_key
	mcps.AddTool(&mcp.Tool{
		Name:        "generate_user_ssh_key",
		Description: "Generate an SSH key for a user. Admin+ only.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"id":         map[string]interface{}{"type": "number", "description": "User ID"},
				"passphrase": map[string]interface{}{"type": "string", "description": "Passphrase (min 8 chars)"},
			},
			"required": []string{"id", "passphrase"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}

		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		id, _ := args["id"].(float64)
		passphrase, _ := args["passphrase"].(string)
		if id == 0 {
			return mcpToolError("id is required"), nil
		}
		if len(passphrase) < 8 {
			return mcpToolError("passphrase must be at least 8 characters"), nil
		}

		u, err := s.store.GetUser(int64(id))
		if err != nil {
			return mcpToolError("user not found: %v", err), nil
		}
		info, err := s.coreGenerateSSHKey(user, u, passphrase)
		if err != nil {
			return mcpToolError("%v", err), nil
		}
		return mcpToolResult(map[string]interface{}{"ssh_key": info})
	})

	// delete_user_ssh_key
	mcps.AddTool(&mcp.Tool{
		Name:        "delete_user_ssh_key",
		Description: "Delete the SSH key for a user. Admin+ only.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"id": map[string]interface{}{"type": "number", "description": "User ID"},
			},
			"required": []string{"id"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}

		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		id, _ := args["id"].(float64)
		if id == 0 {
			return mcpToolError("id is required"), nil
		}

		u, err := s.store.GetUser(int64(id))
		if err != nil {
			return mcpToolError("user not found: %v", err), nil
		}
		if err := s.coreDeleteSSHKey(user, u); err != nil {
			return mcpToolError("%v", err), nil
		}
		return mcpToolResult(map[string]string{"status": "deleted"})
	})
}

// ── Role Tools ──────────────────────────────────────────────────────

func (s *Server) registerRoleTools(mcps *mcp.Server) {
	mcps.AddTool(&mcp.Tool{
		Name:        "list_roles",
		Description: "List all roles.",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}

		roles, err := s.store.ListRoles()
		if err != nil {
			return mcpToolError("db error: %v", err), nil
		}
		return mcpToolResult(map[string]interface{}{"roles": roles})
	})
}

// ── Audit Tools ─────────────────────────────────────────────────────

func (s *Server) registerAuditTools(mcps *mcp.Server) {
	mcps.AddTool(&mcp.Tool{
		Name:        "list_audit",
		Description: "List the last 100 audit log entries.",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}

		entries, err := s.store.ListAudit(100)
		if err != nil {
			return mcpToolError("db error: %v", err), nil
		}
		return mcpToolResult(map[string]interface{}{"audit": entries})
	})
}

// ── Me Tools ────────────────────────────────────────────────────────

func (s *Server) registerPersonalTools(mcps *mcp.Server) {
	// get_me
	mcps.AddTool(&mcp.Tool{
		Name:        "get_me",
		Description: "Get the currently authenticated user's profile.",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}
		return mcpToolResult(map[string]interface{}{"user": user})
	})

	// get_my_ssh_key
	mcps.AddTool(&mcp.Tool{
		Name:        "get_my_ssh_key",
		Description: "Get the SSH key info for the currently authenticated user.",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}
		if user.ID < 0 {
			return mcpToolError("no SSH key for setup account"), nil
		}

		var info *sshkit.SSHKeyInfo
		if user.Metadata != nil {
			info = publicSSHInfo(user.Metadata.SSHKey)
		}
		return mcpToolResult(map[string]interface{}{"ssh_key": info})
	})

	// generate_my_ssh_key
	mcps.AddTool(&mcp.Tool{
		Name:        "generate_my_ssh_key",
		Description: "Generate an SSH key for the currently authenticated user.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"passphrase": map[string]interface{}{"type": "string", "description": "Passphrase (min 8 chars)"},
			},
			"required": []string{"passphrase"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}
		if user.ID < 0 {
			return mcpToolError("cannot generate SSH key for setup account"), nil
		}

		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		passphrase, _ := args["passphrase"].(string)
		if len(passphrase) < 8 {
			return mcpToolError("passphrase must be at least 8 characters"), nil
		}

		info, err := s.coreGenerateSSHKey(user, user, passphrase)
		if err != nil {
			return mcpToolError("%v", err), nil
		}
		return mcpToolResult(map[string]interface{}{"ssh_key": info})
	})

	// delete_my_ssh_key
	mcps.AddTool(&mcp.Tool{
		Name:        "delete_my_ssh_key",
		Description: "Delete the SSH key for the currently authenticated user.",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}
		if user.ID < 0 {
			return mcpToolError("cannot delete SSH key for setup account"), nil
		}

		if err := s.coreDeleteSSHKey(user, user); err != nil {
			return mcpToolError("%v", err), nil
		}
		return mcpToolResult(map[string]string{"status": "deleted"})
	})

	// get_guide
	mcps.AddTool(&mcp.Tool{
		Name:        "get_guide",
		Description: "Load one or more Fresh Breath guides by name. Available guides: publishing, services, ssh, tasks, virtuals.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"names": map[string]interface{}{
					"type":        "array",
					"items":       map[string]interface{}{"type": "string"},
					"description": "Guide names to load (most commonly 'services' or 'publishing')",
				},
			},
			"required": []string{"names"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if _, err := s.mcpUser(req); err != nil {
			return mcpToolError("auth: %v", err), nil
		}

		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		rawNames, _ := args["names"].([]interface{})
		if len(rawNames) == 0 {
			return mcpToolError("names is required"), nil
		}

		guidesDir := filepath.Join(s.config.Dir, "skills", "freshbreath", "guides")
		var b strings.Builder
		for _, n := range rawNames {
			name, ok := n.(string)
			if !ok || name == "" {
				continue
			}
			clean := filepath.Base(name) // no path traversal
			path := filepath.Join(guidesDir, clean+".md")
			data, err := os.ReadFile(path)
			if err != nil {
				fmt.Fprintf(&b, "\n# Guide not found: %s\n\n", name)
				continue
			}
			b.Write(data)
			b.WriteString("\n\n")
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: b.String()},
			},
		}, nil
	})
}

// ── Settings Tools ──────────────────────────────────────────────────

func (s *Server) registerSettingsTools(mcps *mcp.Server) {
	// get_settings
	mcps.AddTool(&mcp.Tool{
		Name:        "get_settings",
		Description: "Get system settings. Superuser only.",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}

		result := map[string]interface{}{"admin_auth_service": nil, "default_app": nil, "mcp_database_mode": "read-only"}
		if v, err := s.store.GetSetting("admin_auth_service"); err == nil && v != "" {
			result["admin_auth_service"] = v
		}
		if v, err := s.store.GetSetting("default_app"); err == nil && v != "" {
			result["default_app"] = v
		}
		if v, err := s.store.GetSetting("mcp_database_mode"); err == nil && v == "full-access" {
			result["mcp_database_mode"] = v
		}
		return mcpToolResult(result)
	})

	// update_settings
	mcps.AddTool(&mcp.Tool{
		Name:        "update_settings",
		Description: "Update system settings. Superuser only.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"admin_auth_service": map[string]interface{}{"type": "string", "description": "Admin auth service ID (numeric, or empty to disable)"},
				"default_app":        map[string]interface{}{"type": "string", "description": "Default app nonce or 'control'"},
				"mcp_database_mode":  map[string]interface{}{"type": "string", "description": "Central MCP database writes: \"read-only\" (default) or \"full-access\"", "enum": []string{"read-only", "full-access"}},
			},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}

		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)

		var adminAuthService, defaultApp, mcpDBMode *string
		if v, ok := args["admin_auth_service"].(string); ok {
			adminAuthService = &v
		}
		if v, ok := args["default_app"].(string); ok {
			defaultApp = &v
		}
		if v, ok := args["mcp_database_mode"].(string); ok {
			mcpDBMode = &v
		}
		if err := s.coreUpdateSettings(user, adminAuthService, defaultApp, mcpDBMode); err != nil {
			return mcpToolError("%v", err), nil
		}
		return mcpToolResult(map[string]string{"status": "updated"})
	})
}

// ── Route Setup ─────────────────────────────────────────────────────

// setupCentralMCP wires up the /mcp endpoint and its protected resource
// metadata. Called from SetupRoutes.
func (s *Server) setupCentralMCP() {
	handler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		return s.centralMCPServerForRole(roleFromRequest(r))
	}, &mcp.StreamableHTTPOptions{Stateless: true})

	prm := s.buildCentralMCPPRM()
	prmURL := s.config.PublicBaseURL + "/.well-known/oauth-protected-resource/mcp"
	s.centralMCPHandler = auth.RequireBearerToken(s.centralMCPTokenVerifier(), &auth.RequireBearerTokenOptions{
		ResourceMetadataURL: prmURL,
	})(handler)
	s.centralMCPPRMVal = prm
}

// handleCentralMCP serves the /mcp endpoint for the central MCP server.
func (s *Server) handleCentralMCP(w http.ResponseWriter, r *http.Request) {
	if s.centralMCPHandler == nil {
		http.Error(w, "central MCP not configured", http.StatusServiceUnavailable)
		return
	}
	s.centralMCPHandler.ServeHTTP(w, r)
}

// handleCentralMCPPRM serves the protected resource metadata for /mcp.
func (s *Server) handleCentralMCPPRM(w http.ResponseWriter, r *http.Request) {
	if s.centralMCPPRMVal == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	auth.ProtectedResourceMetadataHandler(s.centralMCPPRMVal).ServeHTTP(w, r)
}
