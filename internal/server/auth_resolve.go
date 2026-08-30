package server

import (
	"crypto/hmac"
	"fmt"
	"net/http"
	"strings"

	"poggers.institute/freshbreath/internal/db"
)

// ── Gate and credential resolution ──────────────────────────────────
//
// The inbound gate belongs to the entry point: a request through an app
// (X-App-Nonce) answers to the app's protected_by; a direct request
// (/mcp/{slug}, /service/{id}/) answers to the service's. An empty slot
// inherits the admin auth record — the lazy default is the safe one — and
// "open to anyone on the LAN" is only ever the explicit Anonymous record.

// adminAuthRecord resolves the admin_auth_service setting to its auth
// record. Returns (nil, nil) when unset: setup mode, the same fail-open
// authWrap has always had (see design/decoupled-auth.md notes).
func (s *Server) adminAuthRecord() (*db.AuthRecord, error) {
	idStr, err := s.store.GetSetting("admin_auth_service")
	if err != nil || idStr == "" {
		return nil, nil
	}
	id, err := parseID(idStr)
	if err != nil {
		return nil, fmt.Errorf("invalid admin_auth_service setting %q", idStr)
	}
	return s.store.GetAuthRecord(id)
}

// resolveServiceGate returns the inbound gate for a service's direct door.
func (s *Server) resolveServiceGate(svc *db.Service) (*db.AuthRecord, error) {
	if svc.ProtectedBy != nil {
		return s.store.GetAuthRecord(*svc.ProtectedBy)
	}
	return s.adminAuthRecord()
}

// resolveAppGate returns the inbound gate for an app's door. A service
// reached through an app is governed by this gate, full stop — the
// service's own protected_by does not stack.
func (s *Server) resolveAppGate(app *db.App) (*db.AuthRecord, error) {
	if app.ProtectedBy != nil {
		return s.store.GetAuthRecord(*app.ProtectedBy)
	}
	return s.adminAuthRecord()
}

// keysEqual compares two credential strings in constant time.
func keysEqual(a, b string) bool {
	return hmac.Equal([]byte(a), []byte(b))
}

// gateIsOpen reports whether a resolved gate admits anonymous callers:
// the explicit Anonymous record, or setup mode (nil — no admin auth yet).
func gateIsOpen(rec *db.AuthRecord) bool {
	return rec == nil || rec.Kind == db.AuthAnonymous
}

// headerGateKey extracts the credential a caller presented for an api_key
// gate: the record's custom header, or a bearer Authorization.
func headerGateKey(rec *db.AuthRecord, h http.Header) string {
	if rec.Descriptor.Header != "" {
		return h.Get(rec.Descriptor.Header)
	}
	return strings.TrimPrefix(h.Get("Authorization"), "Bearer ")
}

// presentedGateKey is headerGateKey for a whole request.
func presentedGateKey(rec *db.AuthRecord, r *http.Request) string {
	return headerGateKey(rec, r.Header)
}

// verifyGateRequest checks a request against an inbound gate. On success it
// returns the verified claims (nil for anonymous and key-presenting api_key
// gates) and the resolved Fresh Breath user (nil for ext: subjects and
// identity-free gates).
func (s *Server) verifyGateRequest(rec *db.AuthRecord, r *http.Request) (*freshbreathClaims, *db.User, error) {
	return s.verifyGateHeader(rec, r.Header)
}

// verifyGateHeader is verifyGateRequest against bare headers — the MCP tool
// path only has those.
func (s *Server) verifyGateHeader(rec *db.AuthRecord, h http.Header) (*freshbreathClaims, *db.User, error) {
	if gateIsOpen(rec) {
		return nil, nil, nil
	}
	if h == nil {
		return nil, nil, fmt.Errorf("missing credentials")
	}
	if rec.Kind == db.AuthAPIKey {
		presented := headerGateKey(rec, h)
		if presented != "" && rec.Descriptor.Key != "" && keysEqual(presented, rec.Descriptor.Key) {
			return nil, nil, nil
		}
		// A Fresh Breath token that cleared this gate as a leg of a larger
		// login also passes — the browser can't send a key and a token in
		// one Authorization header, so the token vouches for both.
		if raw := strings.TrimPrefix(h.Get("Authorization"), "Bearer "); isFreshbreathToken(raw) {
			return s.verifyGateToken(rec, raw)
		}
		return nil, nil, fmt.Errorf("invalid api key")
	}

	ah := h.Get("Authorization")
	if !strings.HasPrefix(ah, "Bearer ") {
		return nil, nil, fmt.Errorf("missing bearer token")
	}
	return s.verifyGateToken(rec, strings.TrimPrefix(ah, "Bearer "))
}

// verifyGateToken checks a raw bearer against a token-carrying gate: the
// token must be a Fresh Breath JWT bound to this record (directly or via
// legs). The user is re-resolved from the subject on every check, so role
// changes and deletions take effect within one token lifetime.
func (s *Server) verifyGateToken(rec *db.AuthRecord, raw string) (*freshbreathClaims, *db.User, error) {
	claims, err := s.verifyAndUnwrapToken(raw, rec.ID)
	if err != nil {
		return nil, nil, err
	}
	if claims == nil {
		return nil, nil, fmt.Errorf("not a freshbreath token")
	}
	user, err := s.userFromSubject(claims.Subject)
	if err != nil {
		return nil, nil, err
	}
	return claims, user, nil
}

// ── Outbound credential ─────────────────────────────────────────────

// outboundCred is the resolved upstream credential for one request — the
// single verdict feeding both injection sites (the HTTP proxy header and
// $token in virtual tools), so the two doors cannot drift.
type outboundCred struct {
	Token    string // credential value; "" means none resolved
	Header   string // header to inject under; "" means Authorization: Bearer
	Verbatim bool   // leave the caller's own Authorization untouched
	Strip    bool   // drop the caller's Authorization (explicit Anonymous acts_as)
}

// resolveOutboundCred decides what goes upstream for a request that already
// cleared its gate. presentedKey is the raw key a caller presented at an
// api_key gate (claims are nil there); it becomes the passthrough value.
//
//   - acts_as stored (api_key/ssh_key): that credential, same for everyone
//   - acts_as interactive (oidc/oauth2): the caller's sealed credential for
//     that record's provider — the second leg of a two-leg login
//   - acts_as Anonymous: explicitly credential-free; strip what the caller sent
//   - acts_as empty, gate interactive: passthrough — the caller's own sealed
//     upstream credential
//   - acts_as empty, gate api_key: passthrough of the presented key
//   - acts_as empty, gate open: leave the caller's Authorization alone
//   - acts_as empty, gate ssh_key: nothing — a passphrase yields no upstream
func (s *Server) resolveOutboundCred(svc *db.Service, gate *db.AuthRecord, claims *freshbreathClaims, presentedKey string) (outboundCred, error) {
	if svc.ActsAs != nil {
		rec, err := s.store.GetAuthRecord(*svc.ActsAs)
		if err != nil {
			return outboundCred{}, fmt.Errorf("acts_as record: %w", err)
		}
		switch rec.Kind {
		case db.AuthAnonymous:
			return outboundCred{Strip: true}, nil
		case db.AuthAPIKey:
			return outboundCred{Token: rec.Descriptor.Key, Header: rec.Descriptor.Header}, nil
		case db.AuthSSHKey:
			return outboundCred{Token: rec.Descriptor.Key}, nil
		default: // oidc, oauth2
			if claims == nil {
				return outboundCred{}, fmt.Errorf("service acts as %q but the caller has no token", rec.Name)
			}
			cred, ok := claims.Creds[authProvider(rec)]
			if !ok {
				return outboundCred{}, fmt.Errorf("caller's token carries no %s credential — log in to %q", authProvider(rec), rec.Name)
			}
			return outboundCred{Token: cred.UpstreamToken}, nil
		}
	}

	// Empty slot: the caller's own credential wins.
	switch {
	case gateIsOpen(gate):
		return outboundCred{Verbatim: true}, nil
	case gate.Kind == db.AuthAPIKey:
		return outboundCred{Token: presentedKey, Header: gate.Descriptor.Header}, nil
	case authInteractive(gate):
		if claims == nil {
			return outboundCred{}, nil
		}
		if cred, ok := claims.Creds[authProvider(gate)]; ok {
			return outboundCred{Token: cred.UpstreamToken}, nil
		}
		return outboundCred{}, nil
	default: // ssh_key gate — no upstream credential exists
		return outboundCred{}, nil
	}
}

// ── Completed legs → one token ──────────────────────────────────────

// completedLeg is one cleared auth leg: the record, the identity it proved,
// and any upstream credential it yielded.
type completedLeg struct {
	rec          *db.AuthRecord
	user         *db.User            // ssh_key logins resolve a user directly
	email        string              // reported email (interactive kinds)
	name         string              // display name from the provider or user row
	sub          string              // provider subject (interactive kinds)
	upstream     *sealedUpstreamData // interactive kinds; nil otherwise
	presentedKey string              // api_key gates: the key the caller typed
}

// identity resolves the subject and user row a leg proved.
func (s *Server) legIdentity(leg *completedLeg) (subject string, user *db.User) {
	if leg.user != nil {
		return subjectForUser(leg.user), leg.user
	}
	if leg.email == "" && leg.sub == "" {
		// An identity-free leg (api_key gate): the identity is "whoever
		// holds the key".
		return extSubject(authProvider(leg.rec), "key"), nil
	}
	return s.mintSubject(leg.rec, leg.email, leg.sub)
}

// mintForLegs turns a completed login into one access token and its
// refresh payload. primaryID names the record the token is minted under —
// the outbound record when there is one, since that credential is the
// scarcer one and names the session; every other cleared record rides in
// Legs. Identity comes from the first leg that proved one.
func (s *Server) mintForLegs(legs []*completedLeg, primaryID int64) (accessToken string, rd freshbreathRefreshData, err error) {
	if len(legs) == 0 {
		return "", rd, fmt.Errorf("no legs to mint")
	}

	// Identity comes from the first leg that proved one — normally the
	// gate; an identity-free gate (api_key) defers to a later interactive leg.
	idLeg := legs[0]
	for _, leg := range legs {
		if leg.user != nil || leg.email != "" {
			idLeg = leg
			break
		}
	}
	subject, user := s.legIdentity(idLeg)
	email, role, name := idLeg.email, "", idLeg.name
	if user != nil {
		email, role = user.Email, user.Role
		if user.Name != "" {
			name = user.Name
		}
	}

	var legIDs []int64
	creds := sealedCreds{}
	upstreams := map[string]upstreamRefreshLeg{}
	for _, leg := range legs {
		if leg.rec.ID != primaryID {
			legIDs = append(legIDs, leg.rec.ID)
		}
		if leg.upstream != nil {
			provider := authProvider(leg.rec)
			creds[provider] = *leg.upstream
			if leg.upstream.UpstreamRefresh != "" {
				upstreams[provider] = upstreamRefreshLeg{
					AuthID:       leg.rec.ID,
					RefreshToken: leg.upstream.UpstreamRefresh,
					TokenURL:     leg.upstream.UpstreamTokenURL,
					Scopes:       leg.upstream.UpstreamScopes,
				}
			}
		}
	}

	accessToken, err = s.mintFreshbreathToken(subject, email, role, name, primaryID, legIDs, creds)
	if err != nil {
		return "", rd, err
	}
	rd = freshbreathRefreshData{
		Subject:   subject,
		UserEmail: email,
		AuthID:    primaryID,
		Legs:      legIDs,
	}
	if len(upstreams) > 0 {
		rd.Upstreams = upstreams
	}
	return accessToken, rd, nil
}
