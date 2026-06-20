package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// ── Central MCP Server ──────────────────────────────────────────────
//
// The central MCP server at /mcp exposes the Freshbreath admin API
// as MCP tools. Auth uses the same control panel auth — an MCP client
// discovers protected resource metadata, initiates the OAuth flow
// against Freshbreath's authorization server, and Freshbreath
// authenticates the user against the admin auth service (OIDC).
// The resulting Bearer token is a Freshbreath JWT that identifies the
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

const centralMCPResource = "/mcp"

// ── Central MCP Token Verifier ──────────────────────────────────────
//
// Validates Bearer tokens for the central /mcp endpoint.
// Accepts Freshbreath central JWTs (from MCP OAuth flow) and also
// direct OIDC ID tokens from the admin auth service (for clients
// that already have a control panel token).

func (s *Server) centralMCPTokenVerifier() auth.TokenVerifier {
	return func(ctx context.Context, token string, req *http.Request) (*auth.TokenInfo, error) {
		// Try Freshbreath JWT (central or panel kind).
		claims, err := s.verifyFreshbreathToken(token)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", auth.ErrInvalidToken, err)
		}
		if claims != nil && claims.Kind == "admin" {
			return &auth.TokenInfo{
				UserID:     claims.UserEmail,
				Scopes:     []string{"openid", "email", "profile"},
				Expiration: claims.Expiry.Time(),
				Extra:      map[string]any{"role": claims.UserRole},
			}, nil
		}

		// Try as an OIDC ID token against the admin auth service.
		svcIDStr, err := s.store.GetSetting("admin_auth_service")
		if err != nil || svcIDStr == "" {
			return nil, fmt.Errorf("%w: no admin auth service configured", auth.ErrInvalidToken)
		}
		user, err := s.verifyAdminTokenFromBearer(ctx, svcIDStr, token)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", auth.ErrInvalidToken, err)
		}
		return &auth.TokenInfo{
			UserID:     user.Email,
			Scopes:     []string{"openid", "email", "profile"},
			Expiration: time.Now().Add(1 * time.Hour), // OIDC tokens don't carry reliable expiry for us
			Extra:      map[string]any{"role": user.Role},
		}, nil
	}
}

// verifyAdminTokenFromBearer verifies a raw Bearer token against the
// admin auth service — extracted from verifyAdminToken to accept the
// token string directly instead of parsing from the HTTP header.
func (s *Server) verifyAdminTokenFromBearer(ctx context.Context, serviceID string, idTokenRaw string) (*User, error) {
	svcID, err := parseID(serviceID)
	if err != nil {
		return nil, fmt.Errorf("invalid service ID in settings")
	}
	svc, err := s.store.GetService(svcID)
	if err != nil {
		return nil, fmt.Errorf("admin auth service not found")
	}
	email, err := s.verifyIDToken(ctx, svc, idTokenRaw)
	if err != nil {
		return nil, err
	}
	user, err := s.store.GetUserByEmail(email)
	if err != nil {
		return nil, fmt.Errorf("user not found for %s", email)
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
		ResourceName:           "Freshbreath",
	}
}

// ── MCP Server Construction ─────────────────────────────────────────

// buildCentralMCPServerForRole constructs the MCP server that exposes the
// Freshbreath admin API as tools, gated to the given role. Superuser sees
// everything; Admin excludes settings; Member/Read-only see only their own
// apps plus self-service tools. Visibility is enforced at registration
// time, so a Member's tools/list never even advertises admin tools —
// least-privilege by construction.
func (s *Server) buildCentralMCPServerForRole(role string) *mcp.Server {
	mcps := mcp.NewServer(&mcp.Implementation{
		Name:    "freshbreath",
		Version: version,
	}, &mcp.ServerOptions{
		Instructions: "Freshbreath control panel API. Manage apps, services, users, and settings.",
	})

	// ── Apps (mixed: list/get are all-roles; mutate is admin+) ────
	s.registerAppTools(mcps, role)

	// ── Services (admin+) ─────────────────────────────────────────
	if isAdminPlus(role) {
		s.registerServiceTools(mcps)
	}

	// ── Users (admin+) ────────────────────────────────────────────
	if isAdminPlus(role) {
		s.registerUserTools(mcps)
	}

	// ── Roles (all) ───────────────────────────────────────────────
	s.registerRoleTools(mcps)

	// ── Audit (all) ───────────────────────────────────────────────
	s.registerAuditTools(mcps)

	// ── Me (all) ──────────────────────────────────────────────────
	s.registerMeTools(mcps)

	// ── Settings (superuser only) ──────────────────────────────────
	if role == "Superuser" {
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

// isAdminPlus reports whether role may perform admin actions
// (Superuser or Admin). Mirrors requireAnyRole("Superuser","Admin").
func isAdminPlus(role string) bool {
	return role == "Superuser" || role == "Admin"
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
func (s *Server) mcpUser(req *mcp.CallToolRequest) (*User, error) {
	// Check if auth is enabled.
	svcIDStr, _ := s.store.GetSetting("admin_auth_service")
	if svcIDStr == "" {
		return &User{ID: -1, Name: "Setup Account", Role: "Superuser", Status: "Active"}, nil
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

	// Try Freshbreath JWT (central or panel kind).
	claims, err := s.verifyFreshbreathToken(token)
	if err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}
	if claims != nil && claims.Kind == "admin" {
		user, err := s.store.GetUserByEmail(claims.UserEmail)
		if err != nil {
			return nil, fmt.Errorf("user not found: %s", claims.UserEmail)
		}
		return user, nil
	}

	// Try OIDC ID token against admin auth service.
	user, err := s.verifyAdminTokenFromBearer(context.Background(), svcIDStr, token)
	if err != nil {
		return nil, err
	}
	return user, nil
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

// mcpAuditLog logs an audit entry for an MCP tool call using the user's email.
func (s *Server) mcpAuditLog(user *User, action, target string) {
	actor := "unknown"
	if user != nil {
		if user.Email != "" {
			actor = user.Email
		} else if user.Name != "" {
			actor = user.Name
		} else {
			actor = fmt.Sprintf("user:%d", user.ID)
		}
	}
	_ = s.store.LogAudit(actor, action, target)
}
func roleGate(user *User, allowed ...string) error {
	for _, r := range allowed {
		if user.Role == r {
			return nil
		}
	}
	return fmt.Errorf("forbidden: requires %s", strings.Join(allowed, " or "))
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
		if err := roleGate(user, "Superuser", "Admin", "Member", "Read-only"); err != nil {
			return mcpToolError("%v", err), nil
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
		if err := roleGate(user, "Superuser", "Admin", "Member", "Read-only"); err != nil {
			return mcpToolError("%v", err), nil
		}

		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		nonce, _ := args["nonce"].(string)
		if nonce == "" {
			return mcpToolError("nonce is required"), nil
		}

		app, err := s.store.GetApp(nonce)
		if err != nil {
			return mcpToolError("app not found: %v", err), nil
		}

		// Members can only see apps they belong to.
		if user.Role != "Superuser" && user.Role != "Admin" {
			ok, _ := s.store.IsAppMember(nonce, user.ID)
			if !ok {
				return mcpToolError("forbidden: not a member of this app"), nil
			}
		}

		return mcpToolResult(map[string]interface{}{
			"nonce": app.Nonce,
			"name":  app.Name,
			"url":   app.URL,
		})
	})

	// create_app
	addToolIf(isAdminPlus(role), mcps, &mcp.Tool{
		Name:        "create_app",
		Description: "Create a new app. Admin+ only.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"name":        map[string]interface{}{"type": "string", "description": "App name"},
				"environment": map[string]interface{}{"type": "string", "description": "Environment (Development, Staging, Production)"},
				"url":         map[string]interface{}{"type": "string", "description": "App URL"},
				"owner_id":    map[string]interface{}{"type": "number", "description": "Owner user ID (optional)"},
			},
			"required": []string{"name"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}
		if err := roleGate(user, "Superuser", "Admin"); err != nil {
			return mcpToolError("%v", err), nil
		}

		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		name, _ := args["name"].(string)
		if name == "" {
			return mcpToolError("name is required"), nil
		}
		env, _ := args["environment"].(string)
		appURL, _ := args["url"].(string)
		var ownerID *int64
		if oid, ok := args["owner_id"].(float64); ok {
			id := int64(oid)
			ownerID = &id
		}

		nonce, err := s.store.CreateApp(name, env, appURL, ownerID)
		if err != nil {
			return mcpToolError("db error: %v", err), nil
		}
		s.mcpAuditLog(user, "created app", name)
		return mcpToolResult(map[string]string{"nonce": nonce, "name": name})
	})

	// update_app
	addToolIf(isAdminPlus(role), mcps, &mcp.Tool{
		Name:        "update_app",
		Description: "Update an existing app. Admin+ only.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"nonce":       map[string]interface{}{"type": "string", "description": "App nonce"},
				"name":        map[string]interface{}{"type": "string", "description": "New name"},
				"environment": map[string]interface{}{"type": "string", "description": "New environment"},
				"url":         map[string]interface{}{"type": "string", "description": "New URL"},
				"owner_id":    map[string]interface{}{"type": "number", "description": "New owner user ID (optional)"},
			},
			"required": []string{"nonce", "name"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}
		if err := roleGate(user, "Superuser", "Admin"); err != nil {
			return mcpToolError("%v", err), nil
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
		var ownerID *int64
		if oid, ok := args["owner_id"].(float64); ok {
			id := int64(oid)
			ownerID = &id
		}

		if err := s.store.UpdateApp(nonce, name, env, appURL, ownerID); err != nil {
			return mcpToolError("db error: %v", err), nil
		}
		s.rebuildHostedRoutes()
		s.mcpAuditLog(user, "updated app", name)
		return mcpToolResult(map[string]string{"status": "updated"})
	})

	// delete_app
	addToolIf(isAdminPlus(role), mcps, &mcp.Tool{
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
		if err := roleGate(user, "Superuser", "Admin"); err != nil {
			return mcpToolError("%v", err), nil
		}

		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		nonce, _ := args["nonce"].(string)
		if nonce == "" {
			return mcpToolError("nonce is required"), nil
		}

		app, err := s.store.GetApp(nonce)
		if err != nil {
			return mcpToolError("app not found: %v", err), nil
		}
		if err := s.store.DeleteApp(nonce); err != nil {
			return mcpToolError("db error: %v", err), nil
		}
		_ = app
		s.rebuildHostedRoutes()
		s.mcpAuditLog(user, "deleted app", nonce)
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
		if err := roleGate(user, "Superuser", "Admin", "Member", "Read-only"); err != nil {
			return mcpToolError("%v", err), nil
		}

		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		nonce, _ := args["nonce"].(string)
		if nonce == "" {
			return mcpToolError("nonce is required"), nil
		}

		if user.Role != "Superuser" && user.Role != "Admin" {
			ok, _ := s.store.IsAppMember(nonce, user.ID)
			if !ok {
				return mcpToolError("forbidden: not a member of this app"), nil
			}
		}

		ids, err := s.store.ListAppMembers(nonce)
		if err != nil {
			return mcpToolError("db error: %v", err), nil
		}
		return mcpToolResult(map[string]interface{}{"members": ids})
	})

	// set_app_members
	addToolIf(isAdminPlus(role), mcps, &mcp.Tool{
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
		if err := roleGate(user, "Superuser", "Admin"); err != nil {
			return mcpToolError("%v", err), nil
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

		if err := s.store.SetAppMembers(nonce, members); err != nil {
			return mcpToolError("db error: %v", err), nil
		}
		s.mcpAuditLog(user, "updated app members", nonce)
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
		if err := roleGate(user, "Superuser", "Admin", "Member", "Read-only"); err != nil {
			return mcpToolError("%v", err), nil
		}

		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		nonce, _ := args["nonce"].(string)
		if nonce == "" {
			return mcpToolError("nonce is required"), nil
		}

		if user.Role != "Superuser" && user.Role != "Admin" {
			ok, _ := s.store.IsAppMember(nonce, user.ID)
			if !ok {
				return mcpToolError("forbidden: not a member of this app"), nil
			}
		}

		links, err := s.store.GetAppServiceLinks(nonce)
		if err != nil {
			return mcpToolError("db error: %v", err), nil
		}
		return mcpToolResult(map[string]interface{}{"services": links})
	})

	// set_app_services
	addToolIf(isAdminPlus(role), mcps, &mcp.Tool{
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
		if err := roleGate(user, "Superuser", "Admin"); err != nil {
			return mcpToolError("%v", err), nil
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

		if err := s.store.SetAppServiceLinks(nonce, services); err != nil {
			return mcpToolError("db error: %v", err), nil
		}
		s.mcpAuditLog(user, "updated app services", nonce)
		return mcpToolResult(map[string]string{"status": "updated"})
	})
}

// ── Service Tools ───────────────────────────────────────────────────

func (s *Server) registerServiceTools(mcps *mcp.Server) {
	// list_services
	mcps.AddTool(&mcp.Tool{
		Name:        "list_services",
		Description: "List all services. Admin+ only.",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}
		if err := roleGate(user, "Superuser", "Admin"); err != nil {
			return mcpToolError("%v", err), nil
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
				"id": map[string]interface{}{"type": "number", "description": "Service ID"},
			},
			"required": []string{"id"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}
		if err := roleGate(user, "Superuser", "Admin"); err != nil {
			return mcpToolError("%v", err), nil
		}

		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		id, _ := args["id"].(float64)
		if id == 0 {
			return mcpToolError("id is required"), nil
		}

		svc, err := s.store.GetService(int64(id))
		if err != nil {
			return mcpToolError("service not found: %v", err), nil
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
					"description": "Service descriptor",
					"properties": map[string]interface{}{
						"type":          map[string]interface{}{"type": "string", "description": "Service type: mcp, api, oidc, tasks, virtual, ssh"},
						"auth":          map[string]interface{}{"type": "string", "description": "Auth type (e.g. 'key')"},
						"api_key":       map[string]interface{}{"type": "string", "description": "API key for key-auth services"},
						"header":        map[string]interface{}{"type": "string", "description": "Custom header name for API key"},
						"proxied":       map[string]interface{}{"type": "boolean", "description": "Whether to proxy requests"},
						"client_id":     map[string]interface{}{"type": "string", "description": "Pre-registered OAuth client ID"},
						"client_secret": map[string]interface{}{"type": "string", "description": "Pre-registered OAuth client secret"},
						"oauth_url":     map[string]interface{}{"type": "string", "description": "OAuth base URL override"},
						"scopes":        map[string]interface{}{"type": "string", "description": "Space-separated scopes"},
					},
				},
			},
			"required": []string{"name"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}
		if err := roleGate(user, "Superuser", "Admin"); err != nil {
			return mcpToolError("%v", err), nil
		}

		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		name, _ := args["name"].(string)
		if name == "" {
			return mcpToolError("name is required"), nil
		}
		svcURL, _ := args["url"].(string)

		var desc ServiceDescriptor
		if raw, ok := args["descriptor"].(map[string]interface{}); ok {
			descBytes, _ := json.Marshal(raw)
			json.Unmarshal(descBytes, &desc)
		}

		if svcURL == "" && desc.Type != "tasks" && desc.Type != "virtual" {
			return mcpToolError("url is required for this service type"), nil
		}
		if svcURL == "" && desc.Type == "tasks" {
			svcURL = "tasks://" + slugify(name)
		}
		if svcURL == "" && desc.Type == "virtual" {
			svcURL = "/mcp/" + slugify(name)
		}

		id, err := s.store.RegisterService(name, svcURL, desc)
		if err != nil {
			return mcpToolError("db error: %v", err), nil
		}
		if desc.Type == "virtual" {
			svc := &Service{ID: id, Name: name, URL: svcURL, Descriptor: desc}
			s.virtualMCPs.add(s, svc)
		}
		s.mcpAuditLog(user, "created service", name)
		return mcpToolResult(map[string]interface{}{
			"id": id, "name": name, "url": svcURL, "descriptor": desc,
		})
	})

	// update_service
	mcps.AddTool(&mcp.Tool{
		Name:        "update_service",
		Description: "Update an existing service. Admin+ only.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"id":   map[string]interface{}{"type": "number", "description": "Service ID"},
				"name": map[string]interface{}{"type": "string", "description": "New name"},
				"url":  map[string]interface{}{"type": "string", "description": "New URL"},
				"descriptor": map[string]interface{}{
					"type":        "object",
					"description": "Service descriptor",
					"properties": map[string]interface{}{
						"type":          map[string]interface{}{"type": "string"},
						"auth":          map[string]interface{}{"type": "string"},
						"api_key":       map[string]interface{}{"type": "string"},
						"header":        map[string]interface{}{"type": "string"},
						"proxied":       map[string]interface{}{"type": "boolean"},
						"client_id":     map[string]interface{}{"type": "string"},
						"client_secret": map[string]interface{}{"type": "string"},
						"oauth_url":     map[string]interface{}{"type": "string"},
						"scopes":        map[string]interface{}{"type": "string"},
					},
				},
			},
			"required": []string{"id", "name"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}
		if err := roleGate(user, "Superuser", "Admin"); err != nil {
			return mcpToolError("%v", err), nil
		}

		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		id, _ := args["id"].(float64)
		name, _ := args["name"].(string)
		if id == 0 || name == "" {
			return mcpToolError("id and name are required"), nil
		}

		serviceID := int64(id)
		existing, err := s.store.GetService(serviceID)
		if err != nil {
			return mcpToolError("service not found: %v", err), nil
		}

		svcURL, _ := args["url"].(string)
		var desc ServiceDescriptor
		if raw, ok := args["descriptor"].(map[string]interface{}); ok {
			descBytes, _ := json.Marshal(raw)
			json.Unmarshal(descBytes, &desc)
		} else {
			desc = existing.Descriptor
		}

		if svcURL == "" {
			svcURL = existing.URL
		}
		// Built-in SSH service: preserve name and URL
		if existing.Descriptor.Type == "ssh" {
			name = existing.Name
			svcURL = existing.URL
		}

		if err := s.store.UpdateService(serviceID, name, svcURL, desc); err != nil {
			return mcpToolError("db error: %v", err), nil
		}
		if desc.Type == "virtual" {
			svc := &Service{ID: serviceID, Name: name, URL: svcURL, Descriptor: desc}
			s.virtualMCPs.add(s, svc)
		} else {
			slug := strings.TrimPrefix(existing.URL, "/mcp/")
			s.virtualMCPs.remove(slug)
		}
		s.mcpAuditLog(user, "updated service", name)
		return mcpToolResult(map[string]string{"status": "updated"})
	})

	// delete_service
	mcps.AddTool(&mcp.Tool{
		Name:        "delete_service",
		Description: "Delete a service (cannot delete the built-in SSH service). Admin+ only.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"id": map[string]interface{}{"type": "number", "description": "Service ID"},
			},
			"required": []string{"id"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}
		if err := roleGate(user, "Superuser", "Admin"); err != nil {
			return mcpToolError("%v", err), nil
		}

		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		id, _ := args["id"].(float64)
		if id == 0 {
			return mcpToolError("id is required"), nil
		}

		serviceID := int64(id)
		svc, err := s.store.GetService(serviceID)
		if err != nil {
			return mcpToolError("service not found: %v", err), nil
		}
		if svc.Descriptor.Type == "ssh" {
			return mcpToolError("cannot delete built-in SSH service"), nil
		}
		if err := s.store.DeleteService(serviceID); err != nil {
			return mcpToolError("db error: %v", err), nil
		}
		if svc.Descriptor.Type == "virtual" {
			slug := strings.TrimPrefix(svc.URL, "/mcp/")
			s.virtualMCPs.remove(slug)
		}
		s.mcpAuditLog(user, "deleted service", svc.Name)
		return mcpToolResult(map[string]string{"status": "deleted"})
	})

	// get_service_apps
	mcps.AddTool(&mcp.Tool{
		Name:        "get_service_apps",
		Description: "Get the apps using a specific service. Admin+ only.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"id": map[string]interface{}{"type": "number", "description": "Service ID"},
			},
			"required": []string{"id"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}
		if err := roleGate(user, "Superuser", "Admin"); err != nil {
			return mcpToolError("%v", err), nil
		}

		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		id, _ := args["id"].(float64)
		if id == 0 {
			return mcpToolError("id is required"), nil
		}

		apps, err := s.store.GetAppsUsingService(int64(id))
		if err != nil {
			return mcpToolError("db error: %v", err), nil
		}
		return mcpToolResult(map[string]interface{}{"apps": apps})
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
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}
		if err := roleGate(user, "Superuser", "Admin"); err != nil {
			return mcpToolError("%v", err), nil
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
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}
		if err := roleGate(user, "Superuser", "Admin"); err != nil {
			return mcpToolError("%v", err), nil
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
		if err := roleGate(user, "Superuser", "Admin"); err != nil {
			return mcpToolError("%v", err), nil
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

		newUser, err := s.store.CreateUser(name, email, role, status)
		if err != nil {
			return mcpToolError("db error: %v", err), nil
		}
		s.mcpAuditLog(user, "created user", name)
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
		if err := roleGate(user, "Superuser", "Admin"); err != nil {
			return mcpToolError("%v", err), nil
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

		if err := s.store.UpdateUser(userID, name, email, role, status, existing.Metadata); err != nil {
			return mcpToolError("db error: %v", err), nil
		}
		s.mcpAuditLog(user, "updated user", name)
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
		if err := roleGate(user, "Superuser", "Admin"); err != nil {
			return mcpToolError("%v", err), nil
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
		if err := s.store.DeleteUser(int64(id)); err != nil {
			return mcpToolError("db error: %v", err), nil
		}
		s.mcpAuditLog(user, "deleted user", u.Name)
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
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}
		if err := roleGate(user, "Superuser", "Admin"); err != nil {
			return mcpToolError("%v", err), nil
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
		if err := roleGate(user, "Superuser", "Admin"); err != nil {
			return mcpToolError("%v", err), nil
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

		if err := s.store.SetUserApps(int64(id), apps); err != nil {
			return mcpToolError("db error: %v", err), nil
		}
		s.mcpAuditLog(user, "updated user apps", fmt.Sprintf("user:%d", int64(id)))
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
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}
		if err := roleGate(user, "Superuser", "Admin"); err != nil {
			return mcpToolError("%v", err), nil
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
		info := (*SSHKeyInfo)(nil)
		if u.Metadata != nil && u.Metadata.SSHKey != nil {
			info = &SSHKeyInfo{
				PublicKey:   u.Metadata.SSHKey.PublicKey,
				Fingerprint: u.Metadata.SSHKey.Fingerprint,
				KeyType:     u.Metadata.SSHKey.KeyType,
			}
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
		if err := roleGate(user, "Superuser", "Admin"); err != nil {
			return mcpToolError("%v", err), nil
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
		if u.Metadata != nil && u.Metadata.SSHKey != nil {
			return mcpToolError("SSH key already exists — delete it first"), nil
		}

		keyInfo, err := GenerateSSHKey(passphrase)
		if err != nil {
			return mcpToolError("key generation failed: %v", err), nil
		}

		meta := u.Metadata
		if meta == nil {
			meta = &UserMetadata{}
		}
		meta.SSHKey = keyInfo
		if err := s.store.UpdateUser(u.ID, u.Name, u.Email, u.Role, u.Status, meta); err != nil {
			return mcpToolError("db error: %v", err), nil
		}
		s.mcpAuditLog(user, "generated SSH key for user", u.Email)
		return mcpToolResult(map[string]interface{}{"ssh_key": &SSHKeyInfo{
			PublicKey:   keyInfo.PublicKey,
			Fingerprint: keyInfo.Fingerprint,
			KeyType:     keyInfo.KeyType,
		}})
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
		if err := roleGate(user, "Superuser", "Admin"); err != nil {
			return mcpToolError("%v", err), nil
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
		if u.Metadata == nil || u.Metadata.SSHKey == nil {
			return mcpToolError("no SSH key to delete"), nil
		}
		if err := s.store.UpdateUser(u.ID, u.Name, u.Email, u.Role, u.Status, &UserMetadata{}); err != nil {
			return mcpToolError("db error: %v", err), nil
		}
		s.mcpAuditLog(user, "deleted SSH key for user", u.Email)
		return mcpToolResult(map[string]string{"status": "deleted"})
	})
}

// ── Role Tools ──────────────────────────────────────────────────────

func (s *Server) registerRoleTools(mcps *mcp.Server) {
	mcps.AddTool(&mcp.Tool{
		Name:        "list_roles",
		Description: "List all roles. Any authenticated role.",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}
		if err := roleGate(user, "Superuser", "Admin", "Member", "Read-only"); err != nil {
			return mcpToolError("%v", err), nil
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
		Description: "List the last 100 audit log entries. Any authenticated role.",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}
		if err := roleGate(user, "Superuser", "Admin", "Member", "Read-only"); err != nil {
			return mcpToolError("%v", err), nil
		}

		entries, err := s.store.ListAudit(100)
		if err != nil {
			return mcpToolError("db error: %v", err), nil
		}
		return mcpToolResult(map[string]interface{}{"audit": entries})
	})
}

// ── Me Tools ────────────────────────────────────────────────────────

func (s *Server) registerMeTools(mcps *mcp.Server) {
	// get_me
	mcps.AddTool(&mcp.Tool{
		Name:        "get_me",
		Description: "Get the currently authenticated user's profile. Any authenticated role.",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}
		if err := roleGate(user, "Superuser", "Admin", "Member", "Read-only"); err != nil {
			return mcpToolError("%v", err), nil
		}
		return mcpToolResult(map[string]interface{}{"user": user})
	})

	// get_my_ssh_key
	mcps.AddTool(&mcp.Tool{
		Name:        "get_my_ssh_key",
		Description: "Get the SSH key info for the currently authenticated user. Any authenticated role.",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}
		if err := roleGate(user, "Superuser", "Admin", "Member", "Read-only"); err != nil {
			return mcpToolError("%v", err), nil
		}
		if user.ID < 0 {
			return mcpToolError("no SSH key for setup account"), nil
		}

		info := (*SSHKeyInfo)(nil)
		if user.Metadata != nil && user.Metadata.SSHKey != nil {
			info = &SSHKeyInfo{
				PublicKey:   user.Metadata.SSHKey.PublicKey,
				Fingerprint: user.Metadata.SSHKey.Fingerprint,
				KeyType:     user.Metadata.SSHKey.KeyType,
			}
		}
		return mcpToolResult(map[string]interface{}{"ssh_key": info})
	})

	// generate_my_ssh_key
	mcps.AddTool(&mcp.Tool{
		Name:        "generate_my_ssh_key",
		Description: "Generate an SSH key for the currently authenticated user. Any authenticated role.",
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
		if err := roleGate(user, "Superuser", "Admin", "Member", "Read-only"); err != nil {
			return mcpToolError("%v", err), nil
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

		if user.Metadata != nil && user.Metadata.SSHKey != nil {
			return mcpToolError("SSH key already exists — delete it first"), nil
		}

		keyInfo, err := GenerateSSHKey(passphrase)
		if err != nil {
			return mcpToolError("key generation failed: %v", err), nil
		}

		meta := user.Metadata
		if meta == nil {
			meta = &UserMetadata{}
		}
		meta.SSHKey = keyInfo
		if err := s.store.UpdateUser(user.ID, user.Name, user.Email, user.Role, user.Status, meta); err != nil {
			return mcpToolError("db error: %v", err), nil
		}
		s.mcpAuditLog(user, "generated SSH key for user", user.Email)
		return mcpToolResult(map[string]interface{}{"ssh_key": &SSHKeyInfo{
			PublicKey:   keyInfo.PublicKey,
			Fingerprint: keyInfo.Fingerprint,
			KeyType:     keyInfo.KeyType,
		}})
	})

	// delete_my_ssh_key
	mcps.AddTool(&mcp.Tool{
		Name:        "delete_my_ssh_key",
		Description: "Delete the SSH key for the currently authenticated user. Any authenticated role.",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}
		if err := roleGate(user, "Superuser", "Admin", "Member", "Read-only"); err != nil {
			return mcpToolError("%v", err), nil
		}
		if user.ID < 0 {
			return mcpToolError("cannot delete SSH key for setup account"), nil
		}

		if user.Metadata == nil || user.Metadata.SSHKey == nil {
			return mcpToolError("no SSH key to delete"), nil
		}
		if err := s.store.UpdateUser(user.ID, user.Name, user.Email, user.Role, user.Status, &UserMetadata{}); err != nil {
			return mcpToolError("db error: %v", err), nil
		}
		s.mcpAuditLog(user, "deleted SSH key for user", user.Email)
		return mcpToolResult(map[string]string{"status": "deleted"})
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
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}
		if err := roleGate(user, "Superuser"); err != nil {
			return mcpToolError("%v", err), nil
		}

		result := map[string]interface{}{"admin_auth_service": nil, "default_app": nil}
		if v, err := s.store.GetSetting("admin_auth_service"); err == nil && v != "" {
			result["admin_auth_service"] = v
		}
		if v, err := s.store.GetSetting("default_app"); err == nil && v != "" {
			result["default_app"] = v
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
			},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}
		if err := roleGate(user, "Superuser"); err != nil {
			return mcpToolError("%v", err), nil
		}

		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)

		if v, ok := args["admin_auth_service"].(string); ok {
			if v != "" {
				if _, err := parseID(v); err != nil {
					return mcpToolError("admin_auth_service must be a numeric service ID"), nil
				}
			}
			if err := s.store.SetSetting("admin_auth_service", v); err != nil {
				return mcpToolError("db error: %v", err), nil
			}
			s.mcpAuditLog(user, "updated settings", "admin_auth_service")
		}

		if v, ok := args["default_app"].(string); ok {
			if v != "" && v != "control" {
				found := false
				s.hostedMu.RLock()
				for _, n := range s.hostedRoutes {
					if n == v {
						found = true
						break
					}
				}
				s.hostedMu.RUnlock()
				if !found {
					return mcpToolError("default_app must be a hosted app nonce or \"control\""), nil
				}
			}
			if err := s.store.SetSetting("default_app", v); err != nil {
				return mcpToolError("db error: %v", err), nil
			}
			s.mcpAuditLog(user, "updated settings", "default_app")
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
