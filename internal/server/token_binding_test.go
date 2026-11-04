package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	jose "github.com/go-jose/go-jose/v4"
	josejwt "github.com/go-jose/go-jose/v4/jwt"

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
	tokAdmin, err := srv.mintFreshbreathToken(subject, victim.Email, "Superuser", "Vic Tim", adminRec.ID, adminRec.ID, nil, nil, true)
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
	tokOther, err := srv.mintFreshbreathToken(subject, victim.Email, "Superuser", "Vic Tim", otherRec.ID, otherRec.ID, nil, nil, true)
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
	tokLegs, err := srv.mintFreshbreathToken(subject, victim.Email, "Superuser", "Vic Tim", otherRec.ID, otherRec.ID, []int64{adminRec.ID}, nil, true)
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
	tokExt, err := srv.mintFreshbreathToken(extSubject("admin-idp", "12345"), "stranger@example.com", "", "", adminRec.ID, adminRec.ID, nil, nil, true)
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
		admin.ID, weak.ID, []int64{weak.ID}, sealedCreds{authProvider(admin): {UpstreamToken: "x"}}, true)
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
		admin.ID, admin.ID, nil, sealedCreds{authProvider(admin): {UpstreamToken: "x"}}, true)
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

// Only a login that really signed in mints a fresh token. Reusing the gate
// leg must not, or stacking legs would renew a fresh token forever.
func TestOnlyASignInMintsFresh(t *testing.T) {
	srv := newTestServer(t)
	gate := newAuthRecord(t, srv, "corp-sso", db.AuthOIDC, db.AuthDescriptor{Issuer: "https://idp.invalid", ClientID: "c"})
	side := newAuthRecord(t, srv, "side-sso", db.AuthOIDC, db.AuthDescriptor{Issuer: "https://side.invalid", ClientID: "c"})
	signIn := func(rec *db.AuthRecord) *completedLeg {
		return &completedLeg{rec: rec, email: "u@example.com", sub: "u1", upstream: &sealedUpstreamData{UpstreamToken: "x"}}
	}
	tok, _ := srv.mintFreshbreathToken("ext:corp-sso:u1", "u@example.com", "", "", gate.ID, gate.ID, []int64{side.ID},
		sealedCreds{authProvider(gate): {UpstreamToken: "x"}, authProvider(side): {UpstreamToken: "y"}}, true)
	claims, _ := srv.verifyFreshbreathToken(tok)

	for _, c := range []struct {
		name string
		legs []*completedLeg
		want bool
	}{
		{"signed in", []*completedLeg{signIn(gate)}, true},
		{"reused gate leg", []*completedLeg{srv.legFromClaims(gate, claims)}, false},
		{"reused gate leg, new side leg", []*completedLeg{srv.legFromClaims(gate, claims), signIn(side)}, false},
		{"signed in, reused side leg", []*completedLeg{signIn(gate), srv.legFromClaims(side, claims)}, true},
	} {
		access, _, err := srv.mintForLegs(c.legs, gate.ID, gate.ID)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got, err := srv.verifyFreshbreathToken(access)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if isFresh := got.TokenUse == "signin"; isFresh != c.want {
			t.Errorf("%s: token_use = %q, want fresh = %v", c.name, got.TokenUse, c.want)
		}
	}
}

// relabelToken re-signs a token with a different token_use, or none ("").
func relabelToken(t *testing.T, srv *Server, raw, use string) string {
	t.Helper()
	tok, err := josejwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.HS256})
	if err != nil {
		t.Fatal(err)
	}
	claims := map[string]any{}
	if err := tok.Claims(srv.deriveSubkey(jwtSignLabel), &claims); err != nil {
		t.Fatal(err)
	}
	delete(claims, "token_use")
	if use != "" {
		claims["token_use"] = use
	}
	sig, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: srv.deriveSubkey(jwtSignLabel)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	out, err := josejwt.Signed(sig).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// token_use alone decides: these payloads would otherwise pass, so a refusal
// here can only come from the claim. No claim is a token from before it.
func TestTokenUseDecidesWhichVerifierAccepts(t *testing.T) {
	srv := newTestServer(t)
	access, _ := srv.mintFreshbreathToken("frbr:1", "u@example.com", "", "", 1, 1, nil, nil, false)
	refresh, err := srv.mintRefreshToken(freshbreathRefreshData{Subject: "frbr:1", AuthID: 1, FamilyID: "f", JTI: "j"})
	if err != nil {
		t.Fatal(err)
	}
	for use, want := range map[string]bool{"signin": true, "access": true, "": true, "refresh": false, "other": false} {
		c, err := srv.verifyFreshbreathToken(relabelToken(t, srv, access, use))
		if got := err == nil && c != nil; got != want {
			t.Errorf("access verifier on token_use %q: accepted = %v, want %v (%v)", use, got, want, err)
		}
	}
	for use, want := range map[string]bool{"refresh": true, "": true, "access": false, "signin": false, "other": false} {
		_, err := srv.verifyRefreshToken(relabelToken(t, srv, refresh, use))
		if got := err == nil; got != want {
			t.Errorf("refresh verifier on token_use %q: accepted = %v, want %v (%v)", use, got, want, err)
		}
	}
}

// Each mint labels its token, and the real tokens don't stand in for each other.
func TestMintedTokensCarryTheirUse(t *testing.T) {
	srv := newTestServer(t)
	creds := sealedCreds{"up": {UpstreamToken: "x"}}
	signin, _ := srv.mintFreshbreathToken("frbr:1", "u@example.com", "", "", 1, 1, nil, creds, true)
	access, _ := srv.mintFreshbreathToken("frbr:1", "u@example.com", "", "", 1, 1, nil, creds, false)
	refresh, err := srv.mintRefreshToken(freshbreathRefreshData{Subject: "frbr:1", AuthID: 1, FamilyID: "f", JTI: "j"})
	if err != nil {
		t.Fatal(err)
	}
	for raw, want := range map[string]string{signin: "signin", access: "access"} {
		c, err := srv.verifyFreshbreathToken(raw)
		if err != nil || c == nil || c.TokenUse != want {
			t.Errorf("want token_use %q: %v %+v", want, err, c)
		}
		if _, err := srv.verifyRefreshToken(raw); err == nil {
			t.Errorf("token_use %q verified as a refresh token", want)
		}
	}
	if _, err := srv.verifyRefreshToken(refresh); err != nil {
		t.Errorf("refresh token: %v", err)
	}
	if c, err := srv.verifyFreshbreathToken(refresh); err == nil && c != nil {
		t.Errorf("refresh token verified as an access token")
	}
}

// A token from before token_use, with sealed creds, parses as refresh data
// with no family. The refresh endpoint is what turns it away.
func TestLegacyAccessTokenCantRefresh(t *testing.T) {
	srv := newTestServer(t)
	tok, _ := srv.mintFreshbreathToken("frbr:1", "u@example.com", "", "", 1, 1, nil, sealedCreds{"up": {UpstreamToken: "x"}}, false)
	rr := postForm(t, srv, "/oauth/token", url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {relabelToken(t, srv, tok, "")},
	})
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "invalid_grant") {
		t.Errorf("legacy access token at the refresh endpoint: %d %s", rr.Code, rr.Body.String())
	}
}
