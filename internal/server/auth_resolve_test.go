package server

import (
	"net/http"
	"testing"

	"poggers.institute/freshbreath/internal/db"
)

// ── Slot eligibility: every kind, both slots ────────────────────────
//
// design/decoupled-auth.md's kind table: all five kinds are eligible in
// both protected_by and acts_as — what a kind *means* differs by slot, but
// nothing rejects the assignment itself. This drives every kind through
// both slots on a live service and confirms gate/outbound resolution never
// errors just from the assignment.

func TestSlotEligibilityMatrix(t *testing.T) {
	srv := newTestServer(t)
	anon := builtinAuth(t, srv, db.AuthAnonymous)
	sshRec := builtinAuth(t, srv, db.AuthSSHKey)
	apiKey := newAuthRecord(t, srv, "Key", db.AuthAPIKey, db.AuthDescriptor{Key: "s3cret"})
	oidcRec := newAuthRecord(t, srv, "OIDC", db.AuthOIDC, db.AuthDescriptor{Issuer: "https://idp.example", Provider: "idp"})
	oauth2Rec := newAuthRecord(t, srv, "OAuth2", db.AuthOAuth2,
		db.AuthDescriptor{AuthorizeURL: "https://up.example/authorize", TokenURL: "https://up.example/token", Provider: "up"})

	kinds := []*db.AuthRecord{anon, sshRec, apiKey, oidcRec, oauth2Rec}

	for _, protectedBy := range kinds {
		for _, actsAs := range kinds {
			svc := &db.Service{
				ID:          1,
				Descriptor:  db.ServiceDescriptor{Type: "api"},
				ProtectedBy: &protectedBy.ID,
				ActsAs:      &actsAs.ID,
			}
			gate, err := srv.resolveServiceGate(svc)
			if err != nil {
				t.Fatalf("protected_by=%s: resolveServiceGate: %v", protectedBy.Kind, err)
			}
			if gate.ID != protectedBy.ID {
				t.Fatalf("protected_by=%s: gate = %d, want %d", protectedBy.Kind, gate.ID, protectedBy.ID)
			}

			// Build claims a caller of this gate could plausibly present,
			// so resolveOutboundCred sees a realistic (not nil) input for
			// interactive gates.
			var claims *freshbreathClaims
			if authInteractive(protectedBy) {
				tok, err := srv.mintFreshbreathToken(extSubject(authProvider(protectedBy), "u1"), "", "", "",
					protectedBy.ID, nil, sealedCreds{authProvider(protectedBy): {UpstreamToken: "gate-cred"}})
				if err != nil {
					t.Fatalf("mint: %v", err)
				}
				claims, err = srv.verifyFreshbreathToken(tok)
				if err != nil {
					t.Fatalf("verify: %v", err)
				}
			}

			// acts_as always resolves to a verdict, never an error, for any
			// combination — even when the interactive acts_as record has no
			// matching credential on the gate's token (that's a caller error
			// surfaced as a verdict-time err, exercised separately below).
			_, err = srv.resolveOutboundCred(svc, gate, claims, "s3cret")
			if err != nil && !authInteractive(actsAs) {
				t.Errorf("protected_by=%s acts_as=%s: unexpected error: %v", protectedBy.Kind, actsAs.Kind, err)
			}
		}
	}
}

// ── Gate resolution: door, inherit, override ────────────────────────

func TestGateResolutionEmptyInheritsAdmin(t *testing.T) {
	srv := newTestServer(t)
	admin := setAdminAuth(t, srv)

	svc := &db.Service{ID: 1, Descriptor: db.ServiceDescriptor{Type: "api"}}
	gate, err := srv.resolveServiceGate(svc)
	if err != nil {
		t.Fatalf("resolveServiceGate: %v", err)
	}
	if gate == nil || gate.ID != admin.ID {
		t.Errorf("empty protected_by: gate = %+v, want admin record %d", gate, admin.ID)
	}

	app := &db.App{ID: 1, Nonce: "n"}
	agate, err := srv.resolveAppGate(app)
	if err != nil {
		t.Fatalf("resolveAppGate: %v", err)
	}
	if agate == nil || agate.ID != admin.ID {
		t.Errorf("empty app protected_by: gate = %+v, want admin record %d", agate, admin.ID)
	}
}

func TestGateResolutionNoAdminConfiguredIsSetupMode(t *testing.T) {
	srv := newTestServer(t)
	svc := &db.Service{ID: 1, Descriptor: db.ServiceDescriptor{Type: "api"}}
	gate, err := srv.resolveServiceGate(svc)
	if err != nil {
		t.Fatalf("resolveServiceGate: %v", err)
	}
	if gate != nil {
		t.Errorf("no admin_auth_service set: gate = %+v, want nil (setup mode)", gate)
	}
	if !gateIsOpen(gate) {
		t.Error("a nil gate must read as open — setup mode has no gate yet")
	}
}

func TestGateResolutionExplicitOverridesAdmin(t *testing.T) {
	srv := newTestServer(t)
	setAdminAuth(t, srv)
	own := newAuthRecord(t, srv, "Service Gate", db.AuthAPIKey, db.AuthDescriptor{Key: "x"})

	svc := &db.Service{ID: 1, Descriptor: db.ServiceDescriptor{Type: "api"}, ProtectedBy: &own.ID}
	gate, err := srv.resolveServiceGate(svc)
	if err != nil {
		t.Fatalf("resolveServiceGate: %v", err)
	}
	if gate.ID != own.ID {
		t.Errorf("explicit protected_by: gate = %d, want own record %d", gate.ID, own.ID)
	}
}

// The door owns the gate: a service reached through an app answers to the
// app's protected_by, full stop — the service's own gate does not stack.
// legsForLogin is where that rule actually bites (it decides what a caller
// must clear), so exercise it directly with an app-resolved gate.
func TestDoorOwnsTheGate(t *testing.T) {
	srv := newTestServer(t)
	appGate := newAuthRecord(t, srv, "App Gate", db.AuthAPIKey, db.AuthDescriptor{Key: "app-key"})
	svcGate := newAuthRecord(t, srv, "Service Gate", db.AuthAPIKey, db.AuthDescriptor{Key: "svc-key"})

	app := &db.App{ID: 1, Nonce: "n", ProtectedBy: &appGate.ID}
	svc := &db.Service{ID: 1, Descriptor: db.ServiceDescriptor{Type: "api"}, ProtectedBy: &svcGate.ID}

	gate, err := srv.resolveAppGate(app)
	if err != nil {
		t.Fatalf("resolveAppGate: %v", err)
	}
	if gate.ID != appGate.ID {
		t.Fatalf("app door gate = %d, want app's own %d", gate.ID, appGate.ID)
	}

	// legsForLogin walks from the app's gate — the service's ProtectedBy is
	// never consulted for legs at all, only its ActsAs.
	legs, err := srv.legsForLogin(gate, svc)
	if err != nil {
		t.Fatalf("legsForLogin: %v", err)
	}
	if len(legs) != 1 || legs[0].ID != appGate.ID {
		t.Fatalf("legs = %v, want exactly [app gate %d]", legs, appGate.ID)
	}
}

// ── Subject minting ──────────────────────────────────────────────────

func TestMintSubjectKnownEmailYieldsFrbr(t *testing.T) {
	srv := newTestServer(t)
	rec := newAuthRecord(t, srv, "IdP", db.AuthOIDC, db.AuthDescriptor{Issuer: "https://idp.example", Provider: "idp"})
	user, err := srv.store.CreateUser("Known", "known@example.com", "Member", "Active")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	subject, u := srv.mintSubject(rec, "known@example.com", "provider-sub-1")
	if subject != subjectForUser(user) {
		t.Errorf("subject = %q, want %q", subject, subjectForUser(user))
	}
	if u == nil || u.ID != user.ID {
		t.Errorf("resolved user = %+v, want %+v", u, user)
	}
}

func TestMintSubjectUnknownEmailYieldsExt(t *testing.T) {
	srv := newTestServer(t)
	rec := newAuthRecord(t, srv, "IdP", db.AuthOIDC, db.AuthDescriptor{Issuer: "https://idp.example", Provider: "idp"})

	subject, u := srv.mintSubject(rec, "stranger@example.com", "provider-sub-2")
	if subject != "ext:idp:provider-sub-2" {
		t.Errorf("subject = %q, want ext:idp:provider-sub-2", subject)
	}
	if u != nil {
		t.Errorf("expected no user, got %+v", u)
	}
}

// Two records over the same upstream app share a provider slug — the same
// human gets the same ext: subject through either.
func TestProviderSlugSharedAcrossRecords(t *testing.T) {
	srv := newTestServer(t)
	staff := newAuthRecord(t, srv, "GitHub (staff)", db.AuthOAuth2, db.AuthDescriptor{
		AuthorizeURL: "https://github.example/authorize", Provider: "github"})
	public := newAuthRecord(t, srv, "GitHub (public)", db.AuthOAuth2, db.AuthDescriptor{
		AuthorizeURL: "https://github.example/authorize", Provider: "github"})

	subject1, _ := srv.mintSubject(staff, "", "same-human")
	subject2, _ := srv.mintSubject(public, "", "same-human")
	if subject1 != subject2 {
		t.Errorf("subjects diverged across records sharing a provider: %q vs %q", subject1, subject2)
	}
}

// ── Sealed credential map ───────────────────────────────────────────

func TestSealedMapMintVerifyUnwrap(t *testing.T) {
	srv := newTestServer(t)
	tok, err := srv.mintFreshbreathToken("ext:up:1", "", "", "", 7, nil,
		sealedCreds{"up": {UpstreamToken: "secret-1"}})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	claims, err := srv.verifyFreshbreathToken(tok)
	if err != nil || claims == nil {
		t.Fatalf("verify: claims=%v err=%v", claims, err)
	}
	if claims.Creds["up"].UpstreamToken != "secret-1" {
		t.Errorf("unsealed cred = %q, want secret-1", claims.Creds["up"].UpstreamToken)
	}
}

// A two-leg login's map carries both providers' credentials in one token.
func TestSealedMapAugmentedWithSecondLeg(t *testing.T) {
	srv := newTestServer(t)
	creds := sealedCreds{
		"gate": {UpstreamToken: "gate-cred"},
		"acts": {UpstreamToken: "acts-cred"},
	}
	tok, err := srv.mintFreshbreathToken("ext:gate:1", "", "", "", 1, []int64{2}, creds)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	claims, err := srv.verifyFreshbreathToken(tok)
	if err != nil || claims == nil {
		t.Fatalf("verify: claims=%v err=%v", claims, err)
	}
	if claims.Creds["gate"].UpstreamToken != "gate-cred" || claims.Creds["acts"].UpstreamToken != "acts-cred" {
		t.Errorf("Creds = %+v, want both providers present", claims.Creds)
	}
}

func TestSealedMapRejectsForeignRecord(t *testing.T) {
	srv := newTestServer(t)
	tok, err := srv.mintFreshbreathToken("ext:up:1", "", "", "", 7, nil, sealedCreds{"up": {UpstreamToken: "x"}})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := srv.verifyAndUnwrapToken(tok, 99); err == nil {
		t.Error("token bound to record 7 must be rejected when checked against 99")
	}
}

// ── Outbound verdict matrix ──────────────────────────────────────────

func TestOutboundVerdictStoredAPIKey(t *testing.T) {
	srv := newTestServer(t)
	rec := newAuthRecord(t, srv, "Key", db.AuthAPIKey, db.AuthDescriptor{Key: "stored-key", Header: "X-Key"})
	svc := &db.Service{ID: 1, Descriptor: db.ServiceDescriptor{Type: "api"}, ActsAs: &rec.ID}

	cred, err := srv.resolveOutboundCred(svc, nil, nil, "")
	if err != nil {
		t.Fatalf("resolveOutboundCred: %v", err)
	}
	if cred.Token != "stored-key" || cred.Header != "X-Key" || cred.Verbatim || cred.Strip {
		t.Errorf("verdict = %+v, want stored key under X-Key", cred)
	}
}

func TestOutboundVerdictStoredSSHKey(t *testing.T) {
	srv := newTestServer(t)
	rec := newAuthRecord(t, srv, "Deploy Key", db.AuthSSHKey, db.AuthDescriptor{Key: "ssh-priv"})
	svc := &db.Service{ID: 1, Descriptor: db.ServiceDescriptor{Type: "api"}, ActsAs: &rec.ID}

	cred, err := srv.resolveOutboundCred(svc, nil, nil, "")
	if err != nil {
		t.Fatalf("resolveOutboundCred: %v", err)
	}
	if cred.Token != "ssh-priv" || cred.Header != "" {
		t.Errorf("verdict = %+v, want the stored ssh key under Authorization", cred)
	}
}

func TestOutboundVerdictAnonymousActsAsStrips(t *testing.T) {
	srv := newTestServer(t)
	anon := builtinAuth(t, srv, db.AuthAnonymous)
	svc := &db.Service{ID: 1, Descriptor: db.ServiceDescriptor{Type: "api"}, ActsAs: &anon.ID}

	cred, err := srv.resolveOutboundCred(svc, nil, nil, "")
	if err != nil {
		t.Fatalf("resolveOutboundCred: %v", err)
	}
	if !cred.Strip || cred.Token != "" {
		t.Errorf("verdict = %+v, want Strip with no token", cred)
	}
}

func TestOutboundVerdictInteractiveActsAsUsesCallerCred(t *testing.T) {
	srv := newTestServer(t)
	acts := newAuthRecord(t, srv, "GitHub", db.AuthOAuth2, db.AuthDescriptor{
		AuthorizeURL: "https://gh.example/authorize", Provider: "github"})
	svc := &db.Service{ID: 1, Descriptor: db.ServiceDescriptor{Type: "api"}, ActsAs: &acts.ID}

	claims := &freshbreathClaims{Creds: sealedCreds{"github": {UpstreamToken: "gh-cred"}}}
	cred, err := srv.resolveOutboundCred(svc, nil, claims, "")
	if err != nil {
		t.Fatalf("resolveOutboundCred: %v", err)
	}
	if cred.Token != "gh-cred" {
		t.Errorf("verdict = %+v, want gh-cred", cred)
	}

	// No claims at all → the caller never logged in to that leg: an error,
	// not a silent empty credential.
	if _, err := srv.resolveOutboundCred(svc, nil, nil, ""); err == nil {
		t.Error("expected error when acts_as is interactive but the caller has no token")
	}

	// A token that never cleared this provider's leg → an error naming it.
	otherClaims := &freshbreathClaims{Creds: sealedCreds{"other": {UpstreamToken: "x"}}}
	if _, err := srv.resolveOutboundCred(svc, nil, otherClaims, ""); err == nil {
		t.Error("expected error when the caller's token carries no github credential")
	}
}

func TestOutboundVerdictEmptySlotOpenGateIsVerbatim(t *testing.T) {
	srv := newTestServer(t)
	anon := builtinAuth(t, srv, db.AuthAnonymous)
	svc := &db.Service{ID: 1, Descriptor: db.ServiceDescriptor{Type: "api"}}

	cred, err := srv.resolveOutboundCred(svc, anon, nil, "")
	if err != nil {
		t.Fatalf("resolveOutboundCred: %v", err)
	}
	if !cred.Verbatim {
		t.Errorf("verdict = %+v, want Verbatim (open gate, no acts_as)", cred)
	}
}

func TestOutboundVerdictEmptySlotAPIKeyGateIsPassthrough(t *testing.T) {
	srv := newTestServer(t)
	gate := newAuthRecord(t, srv, "Gate Key", db.AuthAPIKey, db.AuthDescriptor{Key: "gate-key", Header: "X-Gate"})
	svc := &db.Service{ID: 1, Descriptor: db.ServiceDescriptor{Type: "api"}}

	cred, err := srv.resolveOutboundCred(svc, gate, nil, "presented-key")
	if err != nil {
		t.Fatalf("resolveOutboundCred: %v", err)
	}
	if cred.Token != "presented-key" || cred.Header != "X-Gate" {
		t.Errorf("verdict = %+v, want the presented key passed through under the gate's header", cred)
	}
}

func TestOutboundVerdictEmptySlotInteractiveGateIsPassthrough(t *testing.T) {
	srv := newTestServer(t)
	gate := newAuthRecord(t, srv, "Gate IdP", db.AuthOIDC, db.AuthDescriptor{Issuer: "https://idp.example", Provider: "idp"})
	svc := &db.Service{ID: 1, Descriptor: db.ServiceDescriptor{Type: "api"}}

	claims := &freshbreathClaims{Creds: sealedCreds{"idp": {UpstreamToken: "gate-cred"}}}
	cred, err := srv.resolveOutboundCred(svc, gate, claims, "")
	if err != nil {
		t.Fatalf("resolveOutboundCred: %v", err)
	}
	if cred.Token != "gate-cred" {
		t.Errorf("verdict = %+v, want the gate's own sealed credential", cred)
	}

	// No claims at all (gate accepted anonymously — shouldn't happen for an
	// interactive gate, but the resolver must not panic): empty verdict.
	empty, err := srv.resolveOutboundCred(svc, gate, nil, "")
	if err != nil {
		t.Fatalf("resolveOutboundCred: %v", err)
	}
	if empty.Token != "" || empty.Verbatim || empty.Strip {
		t.Errorf("verdict with no claims = %+v, want an empty no-op verdict", empty)
	}
}

func TestOutboundVerdictEmptySlotSSHGateYieldsNothing(t *testing.T) {
	srv := newTestServer(t)
	gate := builtinAuth(t, srv, db.AuthSSHKey)
	svc := &db.Service{ID: 1, Descriptor: db.ServiceDescriptor{Type: "api"}}

	cred, err := srv.resolveOutboundCred(svc, gate, nil, "")
	if err != nil {
		t.Fatalf("resolveOutboundCred: %v", err)
	}
	if cred.Token != "" || cred.Verbatim || cred.Strip {
		t.Errorf("verdict = %+v, want a no-op verdict (a passphrase yields no upstream credential)", cred)
	}
}

// ── api_key gates accept their own tokens ───────────────────────────
//
// A browser can't send a key AND a token in one Authorization header, so an
// api_key gate accepts EITHER the raw key OR a Fresh Breath token whose legs
// include the gate record — the mechanism that makes gate=api_key +
// acts_as=oauth2 workable with a single header.

func TestAPIKeyGateAcceptsOwnToken(t *testing.T) {
	srv := newTestServer(t)
	gate := newAuthRecord(t, srv, "Gate Key", db.AuthAPIKey, db.AuthDescriptor{Key: "gate-secret"})

	// The raw key.
	h := http.Header{"Authorization": []string{"Bearer gate-secret"}}
	if _, _, err := srv.verifyGateHeader(gate, h); err != nil {
		t.Errorf("raw key: %v", err)
	}

	// A token whose Legs include the gate record.
	tok, err := srv.mintFreshbreathToken("ext:up:1", "", "", "", 99, []int64{gate.ID}, nil)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	h2 := http.Header{"Authorization": []string{"Bearer " + tok}}
	if _, _, err := srv.verifyGateHeader(gate, h2); err != nil {
		t.Errorf("legs-bound token: %v", err)
	}

	// A token bound to an unrelated record satisfies neither the key check
	// nor the legs check.
	unrelated, _ := srv.mintFreshbreathToken("ext:up:1", "", "", "", 12345, nil, nil)
	h3 := http.Header{"Authorization": []string{"Bearer " + unrelated}}
	if _, _, err := srv.verifyGateHeader(gate, h3); err == nil {
		t.Error("unrelated token should not clear the api_key gate")
	}

	// The wrong key is rejected outright.
	h4 := http.Header{"Authorization": []string{"Bearer wrong-key"}}
	if _, _, err := srv.verifyGateHeader(gate, h4); err == nil {
		t.Error("wrong key should be rejected")
	}
}
