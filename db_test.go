package main

import (
  "database/sql"
  "encoding/json"
  "sync"
  "testing"
  "time"
)

func newTestStore(t *testing.T) *Store {
  t.Helper()
  db, err := sql.Open("sqlite3", "file::memory:?cache=shared")
  if err != nil {
    t.Fatalf("open db: %v", err)
  }
  t.Cleanup(func() { db.Close() })
  store := &Store{db: db}
  if err := store.Migrate(); err != nil {
    t.Fatalf("migrate: %v", err)
  }
  return store
}

func TestStoreCreateApp(t *testing.T) {
  store := newTestStore(t)

  nonce, err := store.CreateApp("test-app", "", "", nil)
  if err != nil {
    t.Fatalf("create app: %v", err)
  }
  if nonce == "" {
    t.Fatal("expected non-empty nonce")
  }

  app, err := store.GetApp(nonce)
  if err != nil {
    t.Fatalf("get app: %v", err)
  }
  if app.Name != "test-app" {
    t.Errorf("name = %q, want test-app", app.Name)
  }
}

func TestStoreCreateAppDuplicateNonceRetries(t *testing.T) {
  store := newTestStore(t)

  n1, err := store.CreateApp("app-1", "", "", nil)
  if err != nil {
    t.Fatalf("first app: %v", err)
  }
  n2, err := store.CreateApp("app-2", "", "", nil)
  if err != nil {
    t.Fatalf("second app: %v", err)
  }
  if n1 == n2 {
    t.Fatal("expected unique nonces")
  }
}

func TestStoreGetAppNotFound(t *testing.T) {
  store := newTestStore(t)

  _, err := store.GetApp("nonexistent")
  if err == nil {
    t.Fatal("expected error for missing app")
  }
}

func TestStoreListApps(t *testing.T) {
  store := newTestStore(t)
  store.CreateApp("alpha", "", "", nil)
  store.CreateApp("beta", "", "", nil)

  apps, err := store.ListApps()
  if err != nil {
    t.Fatalf("list apps: %v", err)
  }
  if len(apps) != 2 {
    t.Errorf("len(apps) = %d, want 2", len(apps))
  }
}

func TestStoreRegisterAndGetService(t *testing.T) {
  store := newTestStore(t)

  id, err := store.RegisterService("slack", "https://slack.example/mcp", ServiceDescriptor{Type: "mcp"})
  if err != nil {
    t.Fatalf("register service: %v", err)
  }
  if id == 0 {
    t.Fatal("expected non-zero id")
  }

  svc, err := store.GetService(id)
  if err != nil {
    t.Fatalf("get service: %v", err)
  }
  if svc.Name != "slack" {
    t.Errorf("name = %q, want slack", svc.Name)
  }
  if svc.URL != "https://slack.example/mcp" {
    t.Errorf("url = %q, want https://slack.example/mcp", svc.URL)
  }
  if svc.Descriptor.Type != "mcp" {
    t.Errorf("descriptor.type = %q, want mcp", svc.Descriptor.Type)
  }

  services, err := store.ListServices()
  if err != nil {
    t.Fatalf("list services: %v", err)
  }
  if len(services) != 1 {
    t.Errorf("len(services) = %d, want 1", len(services))
  }
}

func TestStoreRegisterServiceSameURL(t *testing.T) {
  store := newTestStore(t)

  _, err := store.RegisterService("slack", "https://example/mcp", ServiceDescriptor{Type: "mcp"})
  if err != nil {
    t.Fatalf("first register: %v", err)
  }

  _, err = store.RegisterService("slack2", "https://example/mcp", ServiceDescriptor{Type: "mcp"})
  if err == nil {
    t.Fatal("expected error for duplicate url")
  }
}

func TestStoreGetServiceByURL(t *testing.T) {
  store := newTestStore(t)
  id, _ := store.RegisterService("slack", "https://slack.example/mcp", ServiceDescriptor{Type: "mcp"})

  svc, err := store.GetServiceByURL("https://slack.example/mcp")
  if err != nil {
    t.Fatalf("get by url: %v", err)
  }
  if svc.ID != id {
    t.Errorf("id = %d, want %d", svc.ID, id)
  }
  if svc.Name != "slack" {
    t.Errorf("name = %q, want slack", svc.Name)
  }

  _, err = store.GetServiceByURL("https://unknown.example/mcp")
  if err == nil {
    t.Fatal("expected error for unknown url")
  }
}

func TestStoreServiceDescriptorRoundTrip(t *testing.T) {
  store := newTestStore(t)

  desc := ServiceDescriptor{
    Type:         "api",
    Proxied:      true,
    ClientID:     "my-client-id",
    ClientSecret: "my-client-secret",
    OAuthURL:     "https://github.com/login/oauth",
  }
  _, err := store.RegisterService("github", "https://api.github.com", desc)
  if err != nil {
    t.Fatalf("register: %v", err)
  }

  services, _ := store.ListServices()
  svc := services[0]
  if svc.Descriptor.Type != "api" {
    t.Errorf("type = %q, want api", svc.Descriptor.Type)
  }
  if !svc.Descriptor.Proxied {
    t.Error("proxied = false, want true")
  }
  if svc.Descriptor.ClientID != "my-client-id" {
    t.Errorf("client_id = %q, want my-client-id", svc.Descriptor.ClientID)
  }
  if svc.Descriptor.ClientSecret != "my-client-secret" {
    t.Errorf("client_secret = %q, want my-client-secret", svc.Descriptor.ClientSecret)
  }
  if svc.Descriptor.OAuthURL != "https://github.com/login/oauth" {
    t.Errorf("oauth_url = %q", svc.Descriptor.OAuthURL)
  }
}

func TestStoreOIDCDescriptorRoundTrip(t *testing.T) {
  store := newTestStore(t)

  desc := ServiceDescriptor{
    Type:         "oidc",
    ClientID:     "xxx.apps.googleusercontent.com",
    ClientSecret: "GOCSPF-xxx",
    Scopes:       "openid profile email",
  }
  id, err := store.RegisterService("google", "https://accounts.google.com", desc)
  if err != nil {
    t.Fatalf("register: %v", err)
  }

  svc, err := store.GetService(id)
  if err != nil {
    t.Fatalf("get service: %v", err)
  }
  if svc.Descriptor.Type != "oidc" {
    t.Errorf("type = %q, want oidc", svc.Descriptor.Type)
  }
  if svc.Descriptor.ClientID != "xxx.apps.googleusercontent.com" {
    t.Errorf("client_id = %q", svc.Descriptor.ClientID)
  }
  if svc.Descriptor.Scopes != "openid profile email" {
    t.Errorf("scopes = %q, want openid profile email", svc.Descriptor.Scopes)
  }
  // These should be zero-valued / omitted
  if svc.Descriptor.Auth != "" {
    t.Errorf("auth = %q, want empty", svc.Descriptor.Auth)
  }
  if svc.Descriptor.Proxied {
    t.Error("proxied should be false")
  }
}

func TestStoreAPIKeyDescriptorRoundTrip(t *testing.T) {
  store := newTestStore(t)

  desc := ServiceDescriptor{
    Type:   "api",
    Auth:   "key",
    APIKey: "admin-secret-key",
  }
  id, err := store.RegisterService("weather", "https://api.weather.com", desc)
  if err != nil {
    t.Fatalf("register: %v", err)
  }

  svc, err := store.GetService(id)
  if err != nil {
    t.Fatalf("get service: %v", err)
  }
  if svc.Descriptor.Type != "api" {
    t.Errorf("type = %q, want api", svc.Descriptor.Type)
  }
  if svc.Descriptor.Auth != "key" {
    t.Errorf("auth = %q, want key", svc.Descriptor.Auth)
  }
  if svc.Descriptor.APIKey != "admin-secret-key" {
    t.Errorf("api_key = %q, want admin-secret-key", svc.Descriptor.APIKey)
  }
}

func TestStoreOAuthClientRoundTrip(t *testing.T) {
  store := newTestStore(t)

  uris := []string{"https://client.example/callback"}
  if err := store.RegisterOAuthClient("test-client-id", "test-secret", uris); err != nil {
    t.Fatalf("register: %v", err)
  }

  secret, gotURIs, ok, err := store.GetOAuthClient("test-client-id")
  if err != nil {
    t.Fatalf("get: %v", err)
  }
  if !ok {
    t.Fatal("expected client to be found")
  }
  if secret != "test-secret" {
    t.Errorf("secret = %q, want test-secret", secret)
  }
  if len(gotURIs) != 1 || gotURIs[0] != uris[0] {
    t.Errorf("redirect_uris = %v, want %v", gotURIs, uris)
  }

  // Nonexistent client returns ok=false, no error.
  _, _, ok, err = store.GetOAuthClient("nope")
  if err != nil {
    t.Fatalf("get nonexistent: %v", err)
  }
  if ok {
    t.Error("expected ok=false for nonexistent client")
  }
}

func TestStoreServiceDescriptorJSON(t *testing.T) {
  // Verify the descriptor serializes cleanly with omitempty
  desc := ServiceDescriptor{Type: "mcp"}
  b, err := json.Marshal(desc)
  if err != nil {
    t.Fatalf("marshal: %v", err)
  }
  s := string(b)
  // Should have "type":"mcp" but NOT have auth, proxied, client_id, etc.
  if !contains(s, `"type":"mcp"`) {
    t.Errorf("expected type:mcp in %s", s)
  }
  if contains(s, "auth") || contains(s, "proxied") || contains(s, "client_id") {
    t.Errorf("expected omitted fields absent in %s", s)
  }
}

func contains(s, substr string) bool {
  return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
  for i := 0; i <= len(s)-len(substr); i++ {
    if s[i:i+len(substr)] == substr {
      return true
    }
  }
  return false
}

// ── Refresh Families ──

func TestRefreshFamilyCreateAndGet(t *testing.T) {
  store := newTestStore(t)
  fam := &RefreshFamily{
    ID:          genNonce(),
    UserEmail:   "test@example.com",
    ServiceID:   1,
    DeviceLabel: "test-device",
    CurrentJTI:  "jti-initial",
    ExpiresAt:   time.Now().Add(24 * time.Hour),
  }
  if err := store.CreateRefreshFamily(fam); err != nil {
    t.Fatalf("create: %v", err)
  }

  got, ok, err := store.GetRefreshFamily(fam.ID)
  if err != nil {
    t.Fatalf("get: %v", err)
  }
  if !ok {
    t.Fatal("expected family to exist")
  }
  if got.UserEmail != fam.UserEmail {
    t.Errorf("email = %q, want %q", got.UserEmail, fam.UserEmail)
  }
  if got.CurrentJTI != fam.CurrentJTI {
    t.Errorf("current_jti = %q, want %q", got.CurrentJTI, fam.CurrentJTI)
  }
  if got.Revoked {
    t.Error("expected not revoked")
  }
}

func TestRefreshFamilyGetNotFound(t *testing.T) {
  store := newTestStore(t)
  _, ok, err := store.GetRefreshFamily("nonexistent")
  if err != nil {
    t.Fatalf("get: %v", err)
  }
  if ok {
    t.Fatal("expected not found")
  }
}

func TestRefreshFamilyRotate(t *testing.T) {
  store := newTestStore(t)
  fam := &RefreshFamily{
    ID:         genNonce(),
    UserEmail:  "a@x.co",
    ServiceID:  1,
    CurrentJTI: "old-jti",
    ExpiresAt:  time.Now().Add(24 * time.Hour),
  }
  if err := store.CreateRefreshFamily(fam); err != nil {
    t.Fatalf("create: %v", err)
  }

  ok, err := store.RotateRefreshFamily(fam.ID, "old-jti", "new-jti", time.Now())
  if err != nil {
    t.Fatalf("rotate: %v", err)
  }
  if !ok {
    t.Fatal("expected rotate to succeed")
  }

  got, _, _ := store.GetRefreshFamily(fam.ID)
  if got.CurrentJTI != "new-jti" {
    t.Errorf("current_jti = %q, want new-jti", got.CurrentJTI)
  }
  if got.PrevJTI != "old-jti" {
    t.Errorf("prev_jti = %q, want old-jti", got.PrevJTI)
  }
  if got.LastUsedAt.IsZero() {
    t.Error("expected last_used_at set")
  }
}

func TestRefreshFamilyRotateCAS(t *testing.T) {
  store := newTestStore(t)
  fam := &RefreshFamily{
    ID:         genNonce(),
    UserEmail:  "a@x.co",
    ServiceID:  1,
    CurrentJTI: "jti-1",
    ExpiresAt:  time.Now().Add(24 * time.Hour),
  }
  if err := store.CreateRefreshFamily(fam); err != nil {
    t.Fatalf("create: %v", err)
  }

  ok, err := store.RotateRefreshFamily(fam.ID, "wrong-jti", "new-jti", time.Now())
  if err != nil {
    t.Fatalf("rotate: %v", err)
  }
  if ok {
    t.Fatal("expected rotate to fail with wrong fromJTI")
  }

  got, _, _ := store.GetRefreshFamily(fam.ID)
  if got.CurrentJTI != "jti-1" {
    t.Errorf("current_jti = %q, want jti-1", got.CurrentJTI)
  }
}

func TestRefreshFamilyRotateConcurrent(t *testing.T) {
  store := newTestStore(t)
  fam := &RefreshFamily{
    ID:         genNonce(),
    UserEmail:  "a@x.co",
    ServiceID:  1,
    CurrentJTI: "jti-1",
    ExpiresAt:  time.Now().Add(24 * time.Hour),
  }
  if err := store.CreateRefreshFamily(fam); err != nil {
    t.Fatalf("create: %v", err)
  }

  var wg sync.WaitGroup
  successes := make(chan bool, 10)
  for i := 0; i < 10; i++ {
    wg.Add(1)
    go func() {
      defer wg.Done()
      ok, _ := store.RotateRefreshFamily(fam.ID, "jti-1", "jti-winner", time.Now())
      successes <- ok
    }()
  }
  wg.Wait()
  close(successes)

  count := 0
  for ok := range successes {
    if ok {
      count++
    }
  }
  if count != 1 {
    t.Errorf("concurrent rotations: %d succeeded, want 1", count)
  }

  got, _, _ := store.GetRefreshFamily(fam.ID)
  if got.CurrentJTI != "jti-winner" {
    t.Errorf("current_jti = %q, want jti-winner", got.CurrentJTI)
  }
}

func TestRefreshFamilyRevoke(t *testing.T) {
  store := newTestStore(t)
  fam := &RefreshFamily{
    ID:         genNonce(),
    UserEmail:  "a@x.co",
    ServiceID:  1,
    CurrentJTI: "jti-1",
    ExpiresAt:  time.Now().Add(24 * time.Hour),
  }
  if err := store.CreateRefreshFamily(fam); err != nil {
    t.Fatalf("create: %v", err)
  }

  if err := store.RevokeRefreshFamily(fam.ID); err != nil {
    t.Fatalf("revoke: %v", err)
  }

  got, _, _ := store.GetRefreshFamily(fam.ID)
  if !got.Revoked {
    t.Error("expected revoked")
  }

  ok, err := store.RotateRefreshFamily(fam.ID, got.CurrentJTI, "new-jti", time.Now())
  if err != nil {
    t.Fatalf("rotate after revoke: %v", err)
  }
  if ok {
    t.Fatal("expected rotate to fail on revoked family")
  }
}

func TestRefreshFamilyRevokeUser(t *testing.T) {
  store := newTestStore(t)
  f1 := &RefreshFamily{ID: genNonce(), UserEmail: "a@x.co", ServiceID: 1, CurrentJTI: "j1", ExpiresAt: time.Now().Add(24 * time.Hour)}
  f2 := &RefreshFamily{ID: genNonce(), UserEmail: "a@x.co", ServiceID: 1, CurrentJTI: "j2", ExpiresAt: time.Now().Add(24 * time.Hour)}
  f3 := &RefreshFamily{ID: genNonce(), UserEmail: "b@x.co", ServiceID: 1, CurrentJTI: "j3", ExpiresAt: time.Now().Add(24 * time.Hour)}
  store.CreateRefreshFamily(f1)
  store.CreateRefreshFamily(f2)
  store.CreateRefreshFamily(f3)

  if err := store.RevokeUserRefreshFamilies("a@x.co"); err != nil {
    t.Fatalf("revoke user: %v", err)
  }

  g1, _, _ := store.GetRefreshFamily(f1.ID)
  g2, _, _ := store.GetRefreshFamily(f2.ID)
  g3, _, _ := store.GetRefreshFamily(f3.ID)
  if !g1.Revoked || !g2.Revoked {
    t.Error("expected a@x.co families revoked")
  }
  if g3.Revoked {
    t.Error("expected b@x.co family not revoked")
  }
}

func TestRefreshFamilyList(t *testing.T) {
  store := newTestStore(t)
  now := time.Now()
  f1 := &RefreshFamily{ID: genNonce(), UserEmail: "a@x.co", ServiceID: 1, CurrentJTI: "j1", ExpiresAt: now.Add(24 * time.Hour)}
  f2 := &RefreshFamily{ID: genNonce(), UserEmail: "a@x.co", ServiceID: 1, CurrentJTI: "j2", ExpiresAt: now.Add(-1 * time.Hour)}
  f3 := &RefreshFamily{ID: genNonce(), UserEmail: "a@x.co", ServiceID: 1, CurrentJTI: "j3", ExpiresAt: now.Add(24 * time.Hour)}
  store.CreateRefreshFamily(f1)
  store.CreateRefreshFamily(f2)
  store.CreateRefreshFamily(f3)
  store.RevokeRefreshFamily(f3.ID)

  list, err := store.ListRefreshFamilies("a@x.co")
  if err != nil {
    t.Fatalf("list: %v", err)
  }
  if len(list) != 1 {
    t.Fatalf("len(list) = %d, want 1", len(list))
  }
  if list[0].ID != f1.ID {
    t.Errorf("got id %q, want %q", list[0].ID, f1.ID)
  }
}

func TestRefreshFamilyDeleteExpired(t *testing.T) {
  store := newTestStore(t)
  now := time.Now()
  f1 := &RefreshFamily{ID: genNonce(), UserEmail: "a@x.co", ServiceID: 1, CurrentJTI: "j1", ExpiresAt: now.Add(-1 * time.Hour)}
  f2 := &RefreshFamily{ID: genNonce(), UserEmail: "a@x.co", ServiceID: 1, CurrentJTI: "j2", ExpiresAt: now.Add(24 * time.Hour)}
  store.CreateRefreshFamily(f1)
  store.CreateRefreshFamily(f2)

  n, err := store.DeleteExpiredRefreshFamilies(now)
  if err != nil {
    t.Fatalf("delete: %v", err)
  }
  if n != 1 {
    t.Errorf("deleted = %d, want 1", n)
  }

  _, ok, _ := store.GetRefreshFamily(f1.ID)
  if ok {
    t.Error("expected expired family deleted")
  }
  _, ok, _ = store.GetRefreshFamily(f2.ID)
  if !ok {
    t.Error("expected unexpired family to remain")
  }
}
