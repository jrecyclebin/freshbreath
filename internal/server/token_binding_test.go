package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"poggers.institute/freshbreath/internal/db"
)

// TestTokenRecordBinding is a regression test for the IdP-confusion
// exploit: a token minted under one auth record must NOT authenticate
// against a different record's gate. Otherwise an attacker who can register
// an account at some secondary provider under a victim's email could mint a
// token there and use it to impersonate the victim at the admin gate.
func TestTokenRecordBinding(t *testing.T) {
	srv := newTestServer(t)
	srv.localKey = []byte("0123456789abcdef0123456789abcdef") // 32 bytes for HS256

	// adminRec backs the admin gate; otherRec is an unrelated provider the
	// attacker can register an account with.
	adminRec, err := srv.store.CreateAuthRecord("Admin IdP", db.AuthOIDC, db.AuthDescriptor{Issuer: "https://admin.example", Provider: "admin-idp"})
	if err != nil {
		t.Fatalf("create admin record: %v", err)
	}
	otherRec, err := srv.store.CreateAuthRecord("Other IdP", db.AuthOIDC, db.AuthDescriptor{Issuer: "https://other.example", Provider: "other-idp"})
	if err != nil {
		t.Fatalf("create other record: %v", err)
	}

	victim, err := srv.store.CreateUser("Vic Tim", "victim@example.com", "Superuser", "Active")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	subject := subjectForUser(victim)

	// A token minted under adminRec verifies against adminRec's gate.
	tokAdmin, err := srv.mintFreshbreathToken(subject, victim.Email, "Superuser", "Vic Tim", adminRec.ID, adminRec.ID, nil, nil, time.Now().Unix())
	if err != nil {
		t.Fatalf("mint admin-bound token: %v", err)
	}
	_, user, err := srv.verifyGateToken(adminRec, tokAdmin)
	if err != nil {
		t.Fatalf("admin-bound token should verify against admin record: %v", err)
	}
	if user == nil || user.Email != "victim@example.com" {
		t.Fatalf("got user %+v, want victim@example.com", user)
	}

	// THE EXPLOIT: a token minted under otherRec (attacker-controlled) must
	// be rejected at adminRec's gate.
	tokOther, err := srv.mintFreshbreathToken(subject, victim.Email, "Superuser", "Vic Tim", otherRec.ID, otherRec.ID, nil, nil, time.Now().Unix())
	if err != nil {
		t.Fatalf("mint other-bound token: %v", err)
	}
	if _, _, err := srv.verifyGateToken(adminRec, tokOther); err == nil {
		t.Fatal("SECURITY: token minted under another record was accepted at the admin gate")
	}

	// And the same protection through the admin gate used by the central MCP.
	if err := srv.store.SetSetting("admin_auth_service", strconv.FormatInt(adminRec.ID, 10)); err != nil {
		t.Fatalf("set admin_auth_service: %v", err)
	}
	if _, err := srv.verifyAdminTokenFromBearer(tokOther); err == nil {
		t.Fatal("SECURITY: other-record token passed the admin gate")
	}
	got, err := srv.verifyAdminTokenFromBearer(tokAdmin)
	if err != nil {
		t.Fatalf("admin-bound token should pass the admin gate: %v", err)
	}
	if got.Email != "victim@example.com" {
		t.Fatalf("got user %q, want victim@example.com", got.Email)
	}

	// A token whose Legs include the admin record also passes — that's a
	// multi-leg login that genuinely cleared the admin gate.
	tokLegs, err := srv.mintFreshbreathToken(subject, victim.Email, "Superuser", "Vic Tim", otherRec.ID, otherRec.ID, []int64{adminRec.ID}, nil, time.Now().Unix())
	if err != nil {
		t.Fatalf("mint legs token: %v", err)
	}
	if _, _, err := srv.verifyGateToken(adminRec, tokLegs); err != nil {
		t.Fatalf("legs-bound token should verify against admin record: %v", err)
	}
	// But foreign legs don't help at an unrelated gate.
	thirdRec, _ := srv.store.CreateAuthRecord("Third", db.AuthOIDC, db.AuthDescriptor{Issuer: "https://third.example"})
	if _, _, err := srv.verifyGateToken(thirdRec, tokLegs); err == nil {
		t.Fatal("SECURITY: token with unrelated legs accepted at a foreign gate")
	}

	// An ext: subject from the right record still isn't a user — the admin
	// gate must refuse identities with no user row.
	tokExt, err := srv.mintFreshbreathToken(extSubject("admin-idp", "12345"), "stranger@example.com", "", "", adminRec.ID, adminRec.ID, nil, nil, time.Now().Unix())
	if err != nil {
		t.Fatalf("mint ext token: %v", err)
	}
	if _, err := srv.verifyAdminTokenFromBearer(tokExt); err == nil {
		t.Fatal("SECURITY: ext: subject with no user row passed the admin gate")
	}
}

// TestReusedLegLendsNoIdentity: a token from a login whose gate was a weak
// provider, with the admin record cleared as a second leg, must not sign in
// to the admin record's gate as whoever the weak provider said the user
// was. Before IdentityAuth, the central MCP's consent step reused the admin
// leg with the token's email and minted a panel token for that email.
func TestReusedLegLendsNoIdentity(t *testing.T) {
	srv := newTestServer(t)
	admin := newAuthRecord(t, srv, "corp-sso", db.AuthOIDC, db.AuthDescriptor{Issuer: "https://idp.invalid", ClientID: "c"})
	weak := newAuthRecord(t, srv, "weak-oauth", db.AuthOAuth2, db.AuthDescriptor{AuthorizeURL: "https://weak.invalid/authorize", ClientID: "c"})
	mustSet(t, srv.store.SetSetting("admin_auth_service", strconv.FormatInt(admin.ID, 10)))
	if _, err := srv.store.CreateUser("Boss", "boss@corp.example", "Superuser", "Active"); err != nil {
		t.Fatal(err)
	}

	// Gate = weak (it proved the identity), corp-sso cleared as acts_as.
	tok, err := srv.mintFreshbreathToken("ext:weak-oauth:attacker", "boss@corp.example", "", "",
		admin.ID, weak.ID, []int64{weak.ID}, sealedCreds{authProvider(admin): {UpstreamToken: "x"}}, time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}

	// consentCode runs the central MCP's OAuth consent step with tok and
	// returns the authorization code it hands out, if any.
	consentCode := func(tok string) string {
		rr := testRequest(t, srv, "POST", "/oauth/register", strings.NewReader(`{"redirect_uris":["http://localhost:7777/cb"]}`), nil)
		var reg struct {
			ClientID string `json:"client_id"`
		}
		json.Unmarshal(rr.Body.Bytes(), &reg)
		sum := sha256.Sum256([]byte("verifier-verifier-verifier-verifier-verifier-verifier"))
		q := url.Values{"client_id": {reg.ClientID}, "redirect_uri": {"http://localhost:7777/cb"}, "state": {"s"},
			"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}, "resource": {"/mcp"}}
		rr = testRequest(t, srv, "GET", "/oauth/authorize?"+q.Encode(), nil, nil)
		cookies := setCookies(rr)
		_, rest, _ := strings.Cut(rr.Body.String(), `var state = "`)
		state, _, _ := strings.Cut(rest, `"`)
		body, _ := json.Marshal(map[string]interface{}{"state": state, "tokens": []string{tok}})
		rr = testRequest(t, srv, "POST", "/oauth/authorize", strings.NewReader(string(body)), cookies)
		var cont struct{ Redirect string }
		json.Unmarshal(rr.Body.Bytes(), &cont)
		u, err := url.Parse(cont.Redirect)
		if err != nil {
			return ""
		}
		return u.Query().Get("code")
	}
	if code := consentCode(tok); code != "" {
		t.Fatalf("consent handed out a code for a borrowed identity")
	}

	// A token the admin record itself proved is reused as before.
	own, _ := srv.mintFreshbreathToken("ext:corp-sso:boss", "boss@corp.example", "", "",
		admin.ID, admin.ID, nil, sealedCreds{authProvider(admin): {UpstreamToken: "x"}}, time.Now().Unix())
	if code := consentCode(own); code == "" {
		t.Errorf("consent refused a token the admin record proved")
	}
	claims, _ := srv.verifyFreshbreathToken(own)
	if !legCovered(claims, admin, admin.ID) {
		t.Errorf("a token the admin record proved should cover its gate leg")
	}
	claims, _ = srv.verifyFreshbreathToken(tok)
	if legCovered(claims, admin, admin.ID) {
		t.Errorf("a token another record proved must not cover the gate leg")
	}
	if !legCovered(claims, admin, weak.ID) {
		t.Errorf("as a non-gate leg the admin credential should still be reusable")
	}
	if leg := srv.legFromClaims(admin, claims); leg.email != "" || leg.user != nil || leg.sub != "" {
		t.Errorf("non-identity leg carried identity: %+v", leg)
	}
}

// A reused identity leg keeps its token's sign-in time, so the recent
// sign-in check can't be satisfied by stacking a new leg onto an old token.
func TestReusedLegKeepsSignInTime(t *testing.T) {
	srv := newTestServer(t)
	gate := newAuthRecord(t, srv, "corp-sso", db.AuthOIDC, db.AuthDescriptor{Issuer: "https://idp.invalid", ClientID: "c"})
	old := time.Now().Add(-2 * time.Hour).Unix()
	tok, _ := srv.mintFreshbreathToken("ext:corp-sso:u1", "u@example.com", "", "", gate.ID, gate.ID, nil,
		sealedCreds{authProvider(gate): {UpstreamToken: "x"}}, old)
	claims, _ := srv.verifyFreshbreathToken(tok)
	access, _, err := srv.mintForLegs([]*completedLeg{srv.legFromClaims(gate, claims)}, gate.ID, gate.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := srv.verifyFreshbreathToken(access)
	if got.AuthTime != old {
		t.Errorf("auth_time = %d, want the original %d", got.AuthTime, old)
	}
}
