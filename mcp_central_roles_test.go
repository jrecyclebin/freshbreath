package main

import (
	"context"
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// toolNamesViaInMemory connects an in-memory MCP client to mcps and
// returns the names advertised by tools/list. This is the only way to
// read a *mcp.Server's registered tools, since the SDK keeps them
// unexported.
func toolNamesViaInMemory(t *testing.T, mcps *mcp.Server) []string {
	t.Helper()
	ctx := context.Background()
	t1, t2 := mcp.NewInMemoryTransports()
	ss, err := mcps.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { ss.Close() })

	c := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0.0.1"}, nil)
	cs, err := c.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })

	// The initialize handshake happens implicitly during Connect.
	res, err := cs.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func TestCentralMCPToolsPerRole(t *testing.T) {
	srv := newTestServer(t)

	// The full admin-API tool surface (38 tools) bucketed by minimum role.
	// Mirrors the HTTP routes in handler.go's requireAnyRole groups.
	allAppTools := []string{
		"list_apps", "get_app", "get_app_members", "get_app_services", // all-roles
		"list_app_files", "download_app_files", "publish_app_files", "delete_app_files", "xfer_fallback", // all-roles
	}
	allAppManageTools := []string{"create_app", "update_app", "delete_app", "set_app_members", "set_app_services"}
	allServiceTools := []string{"list_services", "get_service", "create_service", "update_service", "delete_service", "get_service_apps"}
	allUserTools := []string{"list_users", "get_user", "create_user", "update_user", "delete_user", "get_user_apps", "set_user_apps", "get_user_ssh_key", "generate_user_ssh_key", "delete_user_ssh_key"}
	allRoleTools := []string{"list_roles"}
	allAuditTools := []string{"list_audit"}
	allPersonalTools := []string{"get_me", "get_my_ssh_key", "generate_my_ssh_key", "delete_my_ssh_key", "get_guide"} // available to every authenticated role
	allSettingsTools := []string{"get_settings", "update_settings"}

	wantSuperuser := concat(allAppTools, allAppManageTools, allServiceTools, allUserTools, allRoleTools, allAuditTools, allPersonalTools, allSettingsTools)
	wantAdmin := concat(allAppTools, allAppManageTools, allServiceTools, allUserTools, allRoleTools, allAuditTools, allPersonalTools)
	wantMember := concat(allAppTools, allRoleTools, allAuditTools, allPersonalTools)

	cases := []struct {
		role     string
		want     []string
		mustHave []string
		mustLack []string
	}{
		{"Superuser", wantSuperuser, []string{"get_settings", "update_settings", "create_app", "delete_user", "list_services"}, nil},
		{"Admin", wantAdmin, []string{"create_app", "list_users", "list_services", "delete_service"}, []string{"get_settings", "update_settings"}},
		{"Member", wantMember, []string{"list_apps", "get_app", "get_app_members", "get_app_services", "list_roles", "list_audit", "get_me", "get_my_ssh_key"},
			[]string{"create_app", "update_app", "delete_app", "set_app_members", "set_app_services", "list_services", "list_users", "create_user", "delete_user", "get_settings", "update_settings"}},
		{"Read-only", wantMember, []string{"list_apps", "get_me", "list_roles"},
			[]string{"create_app", "delete_app", "list_users", "get_settings"}},
	}

	for _, tc := range cases {
		t.Run(tc.role, func(t *testing.T) {
			got := toolNamesViaInMemory(t, srv.buildCentralMCPServerForRole(tc.role))

			if len(got) != len(tc.want) {
				t.Errorf("%s: got %d tools, want %d", tc.role, len(got), len(tc.want))
			}
			for _, name := range tc.mustHave {
				if !slices.Contains(got, name) {
					t.Errorf("%s: missing expected tool %q", tc.role, name)
				}
			}
			for _, name := range tc.mustLack {
				if slices.Contains(got, name) {
					t.Errorf("%s: should not expose %q to this role", tc.role, name)
				}
			}
		})
	}
}

func TestCentralMCPServerCaching(t *testing.T) {
	srv := newTestServer(t)
	a := srv.centralMCPServerForRole("Admin")
	b := srv.centralMCPServerForRole("Admin")
	if a != b {
		t.Error("centralMCPServerForRole should return the same *mcp.Server for a role on repeated calls")
	}
	su := srv.centralMCPServerForRole("Superuser")
	if su == a {
		t.Error("different roles should yield different servers")
	}
	// Empty role falls back to Read-only (defensive), not a crash.
	ro := srv.centralMCPServerForRole("")
	ro2 := srv.centralMCPServerForRole("Read-only")
	if ro != ro2 {
		t.Error("empty role should fall back to Read-only and hit the same cache entry")
	}
}

func TestRoleInAdminPlus(t *testing.T) {
	cases := map[string]bool{
		"Superuser": true,
		"Admin":     true,
		"Member":    false,
		"Read-only": false,
		"":          false,
	}
	for role, want := range cases {
		if got := roleIn(role, rolesAdminPlus); got != want {
			t.Errorf("roleIn(%q, rolesAdminPlus) = %v, want %v", role, got, want)
		}
	}
}

// concat flattens the given slices into one. (tired of append boilerplate.)
func concat[S ~[]E, E any](ss ...S) S {
	var out S
	for _, s := range ss {
		out = append(out, s...)
	}
	return out
}
