package server

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"
	josejwt "github.com/go-jose/go-jose/v4/jwt"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"

	"poggers.institute/freshbreath/internal/db"
)

// buildOIDCScopes splits the record scopes and ensures "openid" is always present.
func buildOIDCScopes(recordScopes string) []string {
	raw := strings.TrimSpace(recordScopes)
	if raw == "" {
		return []string{"openid", "email", "profile"}
	}
	parts := strings.Split(raw, " ")
	hasOpenID := false
	for _, s := range parts {
		if s == "openid" {
			hasOpenID = true
			break
		}
	}
	if !hasOpenID {
		parts = append([]string{"openid"}, parts...)
	}
	return parts
}

// authProvider returns the record's provider slug — the key for ext:
// subjects and the sealed-credential map. Records that don't set one get a
// slug of their name, so the map key is always non-empty for interactive kinds.
func authProvider(rec *db.AuthRecord) string {
	if rec.Descriptor.Provider != "" {
		return rec.Descriptor.Provider
	}
	return slugify(rec.Name)
}

// authInteractive reports whether the record's kind runs a human through an
// upstream OAuth flow (and therefore yields a sealable upstream credential).
func authInteractive(rec *db.AuthRecord) bool {
	return rec.Kind == db.AuthOIDC || rec.Kind == db.AuthOAuth2
}

func randomNonce() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// ── OIDC (discovery-based) ──────────────────────────────────────────

type OIDCClaims struct {
	Issuer   string                 `json:"iss"`
	Subject  string                 `json:"sub"`
	Audience []string               `json:"aud"`
	Expiry   int64                  `json:"exp"`
	IssuedAt int64                  `json:"iat"`
	Nonce    string                 `json:"nonce,omitempty"`
	Email    string                 `json:"email,omitempty"`
	Name     string                 `json:"name,omitempty"`
	Picture  string                 `json:"picture,omitempty"`
	Raw      map[string]interface{} `json:"-"`
}

func (s *Server) oidcBeginAuth(ctx context.Context, rec *db.AuthRecord, redirectURI string) (authURL, state, verifier, oidcNonce, tokenURL string, err error) {
	provider, err := s.getOIDCProvider(ctx, rec.ID, rec.Descriptor.Issuer)
	if err != nil {
		return "", "", "", "", "", fmt.Errorf("OIDC discovery for %s: %w", rec.Descriptor.Issuer, err)
	}

	endpoint := provider.Endpoint()
	cfg := &oauth2.Config{
		ClientID:     rec.Descriptor.ClientID,
		ClientSecret: rec.Descriptor.ClientSecret,
		Endpoint:     endpoint,
		RedirectURL:  redirectURI,
		Scopes:       buildOIDCScopes(rec.Descriptor.Scopes),
	}

	state = randomNonce()
	verifier = oauth2.GenerateVerifier()
	oidcNonce = randomNonce()

	authURL = cfg.AuthCodeURL(state,
		oauth2.S256ChallengeOption(verifier),
		oauth2.SetAuthURLParam("nonce", oidcNonce),
	)
	return authURL, state, verifier, oidcNonce, endpoint.TokenURL, nil
}

// oidcExchangeCode exchanges the code against the record's issuer and
// verifies the id_token. Providers that complete OAuth without an id_token
// fall back to userinfo resolution.
func (s *Server) oidcExchangeCode(ctx context.Context, rec *db.AuthRecord, code, verifier, oidcNonce, redirectURI string) (*OIDCClaims, string, string, error) {
	provider, err := s.getOIDCProvider(ctx, rec.ID, rec.Descriptor.Issuer)
	if err != nil {
		return nil, "", "", fmt.Errorf("OIDC discovery: %w", err)
	}

	cfg := &oauth2.Config{
		ClientID:     rec.Descriptor.ClientID,
		ClientSecret: rec.Descriptor.ClientSecret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  redirectURI,
		Scopes:       buildOIDCScopes(rec.Descriptor.Scopes),
	}

	clientCtx := context.WithValue(ctx, oauth2.HTTPClient, s.httpClient)
	tok, err := cfg.Exchange(clientCtx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, "", "", fmt.Errorf("token exchange: %w", err)
	}

	idTokenRaw, ok := tok.Extra("id_token").(string)
	if !ok || idTokenRaw == "" {
		claims, err := s.identityFromUserInfo(ctx, rec, provider, tok)
		if err != nil {
			return nil, "", "", err
		}
		return claims, tok.AccessToken, tok.RefreshToken, nil
	}

	verifierConfig := &oidc.Config{ClientID: rec.Descriptor.ClientID}
	idToken, err := provider.Verifier(verifierConfig).Verify(ctx, idTokenRaw)
	if err != nil {
		return nil, "", "", fmt.Errorf("id token verification: %w", err)
	}

	claims := &OIDCClaims{Raw: make(map[string]interface{})}
	if err := idToken.Claims(claims); err != nil {
		return nil, "", "", fmt.Errorf("id token claims: %w", err)
	}
	if claims.Nonce != oidcNonce {
		return nil, "", "", fmt.Errorf("nonce mismatch")
	}
	if err := idToken.Claims(&claims.Raw); err != nil {
		return nil, "", "", fmt.Errorf("id token raw claims: %w", err)
	}
	return claims, tok.AccessToken, tok.RefreshToken, nil
}

// ── OAuth2 (explicit-endpoint, GitHub-shaped) ───────────────────────

// oauth2BeginAuth starts an OAuth2 flow against the record's explicit
// endpoints. Records without a pre-registered client_id fall back to
// metadata discovery + dynamic client registration from the authorize URL's
// base — that's how upstream MCP servers with open DCR keep working.
func (s *Server) oauth2BeginAuth(ctx context.Context, rec *db.AuthRecord, redirectURI string) (authURL, clientID, clientSecret, tokenURL, state, verifier string, err error) {
	authorizeURL := rec.Descriptor.AuthorizeURL
	tokenURL = rec.Descriptor.TokenURL
	clientID = rec.Descriptor.ClientID
	clientSecret = rec.Descriptor.ClientSecret

	if clientID == "" || authorizeURL == "" || tokenURL == "" {
		base := strings.TrimSuffix(authorizeURL, "/authorize")
		if base == "" {
			return "", "", "", "", "", "", fmt.Errorf("oauth2 record %q needs authorize_url", rec.Name)
		}
		asm, err := auth.GetAuthServerMetadata(ctx, base, s.httpClient)
		if err != nil {
			return "", "", "", "", "", "", fmt.Errorf("metadata discovery: %w", err)
		}
		if asm == nil {
			asm = &oauthex.AuthServerMeta{
				AuthorizationEndpoint: base + "/authorize",
				TokenEndpoint:         base + "/token",
				RegistrationEndpoint:  base + "/register",
			}
		}
		if authorizeURL == "" {
			authorizeURL = asm.AuthorizationEndpoint
		}
		if tokenURL == "" {
			tokenURL = asm.TokenEndpoint
		}
		if clientID == "" {
			if asm.RegistrationEndpoint == "" {
				return "", "", "", "", "", "", fmt.Errorf("oauth2 record %q has no client_id and the server offers no registration endpoint", rec.Name)
			}
			regResp, err := oauthex.RegisterClient(ctx, asm.RegistrationEndpoint, &oauthex.ClientRegistrationMetadata{
				ClientName:              "freshbreath",
				RedirectURIs:            []string{redirectURI},
				GrantTypes:              []string{"authorization_code", "refresh_token"},
				ResponseTypes:           []string{"code"},
				Scope:                   strings.Join(asm.ScopesSupported, " "),
				TokenEndpointAuthMethod: "none",
			}, s.httpClient)
			if err != nil {
				return "", "", "", "", "", "", fmt.Errorf("client registration: %w", err)
			}
			clientID = regResp.ClientID
			clientSecret = regResp.ClientSecret
		}
	}

	state = randomNonce()
	verifier = oauth2.GenerateVerifier()

	cfg := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint: oauth2.Endpoint{
			AuthURL:  authorizeURL,
			TokenURL: tokenURL,
		},
		RedirectURL: redirectURI,
		Scopes:      strings.Fields(rec.Descriptor.Scopes),
	}
	authURL = cfg.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier))
	return authURL, clientID, clientSecret, tokenURL, state, verifier, nil
}

// oauth2ExchangeCode exchanges the code at tokenEndpoint and resolves the
// caller's identity through the record's userinfo endpoints.
func (s *Server) oauth2ExchangeCode(ctx context.Context, rec *db.AuthRecord, tokenEndpoint, code, verifier, clientID, clientSecret, redirectURI string) (*OIDCClaims, string, string, error) {
	cfg := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     oauth2.Endpoint{TokenURL: tokenEndpoint},
		RedirectURL:  redirectURI,
	}
	clientCtx := context.WithValue(ctx, oauth2.HTTPClient, s.httpClient)
	tok, err := cfg.Exchange(clientCtx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, "", "", fmt.Errorf("token exchange: %w", err)
	}
	claims, err := s.identityFromUserInfo(ctx, rec, nil, tok)
	if err != nil {
		return nil, "", "", err
	}
	return claims, tok.AccessToken, tok.RefreshToken, nil
}

// identityFromUserInfo resolves who the user is when no id_token exists:
// the record's userinfo endpoint (or the OIDC provider's, when available),
// with the emails endpoint as the GitHub-private-email fallback.
func (s *Server) identityFromUserInfo(ctx context.Context, rec *db.AuthRecord, provider *oidc.Provider, tok *oauth2.Token) (*OIDCClaims, error) {
	var email, name, sub string

	if rec.Descriptor.UserInfoURL != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rec.Descriptor.UserInfoURL, nil)
		if err != nil {
			return nil, fmt.Errorf("userinfo request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
		req.Header.Set("Accept", "application/json")
		resp, err := s.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("userinfo fetch: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("userinfo returned %d", resp.StatusCode)
		}
		var ui struct {
			Email string `json:"email"`
			Name  string `json:"name"`
			Login string `json:"login"`
			Sub   string `json:"sub"`
			ID    any    `json:"id"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&ui); err != nil {
			return nil, fmt.Errorf("userinfo decode: %w", err)
		}
		email, name = ui.Email, ui.Name
		// Provider-subject preference: a real sub, then a numeric id, then
		// the login handle — the most stable identifier available.
		sub = ui.Sub
		if sub == "" && ui.ID != nil {
			sub = fmt.Sprintf("%v", ui.ID)
		}
		if sub == "" {
			sub = ui.Login
		}
	} else if provider != nil {
		userInfo, err := provider.UserInfo(ctx, oauth2.StaticTokenSource(tok))
		if err != nil {
			return nil, fmt.Errorf("userinfo: %w", err)
		}
		var ui struct {
			Email string `json:"email"`
			Name  string `json:"name"`
		}
		if err := userInfo.Claims(&ui); err != nil {
			return nil, fmt.Errorf("userinfo claims: %w", err)
		}
		email, name, sub = ui.Email, ui.Name, userInfo.Subject
	} else {
		return nil, fmt.Errorf("record %q has no userinfo_url and the provider returned no id_token", rec.Name)
	}

	// Some providers (e.g. GitHub with private email) omit email from the
	// main profile.
	if email == "" && rec.Descriptor.UserEmailsURL != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rec.Descriptor.UserEmailsURL, nil)
		if err != nil {
			return nil, fmt.Errorf("emails request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
		req.Header.Set("Accept", "application/json")
		resp, err := s.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("emails fetch: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("emails returned %d", resp.StatusCode)
		}
		var entries []struct {
			Email    string `json:"email"`
			Primary  bool   `json:"primary"`
			Verified bool   `json:"verified"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
			return nil, fmt.Errorf("emails decode: %w", err)
		}
		for _, e := range entries {
			if e.Primary && e.Verified {
				email = e.Email
				break
			}
		}
	}

	if email == "" {
		return nil, fmt.Errorf("no email in userinfo")
	}
	if sub == "" {
		sub = email
	}
	return &OIDCClaims{
		Email:   email,
		Name:    name,
		Subject: sub,
		Raw:     map[string]interface{}{},
	}, nil
}

// ── Fresh Breath JWT ────────────────────────────────────────────────
//
// One claims type for every Fresh Breath token. Subject is the canonical
// user key: "frbr:<user_id>" when the login's email matched a users row,
// "ext:<provider>:<provider_subject>" when it didn't. AuthID names the auth
// record the token was minted under; Legs lists any other records cleared
// in the same login. Upstream credentials ride encrypted in Sealed as a
// map keyed by provider slug, so passthrough and two-leg logins share one
// token shape.

const (
	accessTokenTTL     = 15 * time.Minute
	refreshTokenTTL    = 14 * 24 * time.Hour
	refreshGraceWindow = 30 * time.Second
	pendingAuthTTL     = 30 * time.Minute // login link stays usable this long; retries allowed
)

// sealedUpstreamData is one provider's upstream credential inside a token.
type sealedUpstreamData struct {
	UpstreamToken    string `json:"upstream_token"`
	UpstreamRefresh  string `json:"upstream_refresh,omitempty"`
	UpstreamTokenURL string `json:"upstream_token_url,omitempty"`
	UpstreamScopes   string `json:"upstream_scopes,omitempty"`
}

// sealedCreds maps auth provider slug → that provider's upstream credential.
type sealedCreds map[string]sealedUpstreamData

type freshbreathClaims struct {
	josejwt.Claims
	UserEmail string  `json:"user_email,omitempty"` // human label; Subject is the key
	UserRole  string  `json:"user_role,omitempty"`  // advisory; gates re-resolve from DB
	UserName  string  `json:"user_name,omitempty"`
	AuthID    int64   `json:"auth_id"`
	Legs      []int64 `json:"legs,omitempty"`
	Sealed    string  `json:"sealed,omitempty"`

	// Unsealed credentials — populated by verifyFreshbreathToken, never serialized.
	Creds sealedCreds `json:"-"`
}

// boundTo reports whether the token was minted under recordID, directly or
// as one of its legs.
func (c *freshbreathClaims) boundTo(recordID int64) bool {
	return c.AuthID == recordID || slices.Contains(c.Legs, recordID)
}

// subjectForUser is the canonical subject for a Fresh Breath user row.
func subjectForUser(u *db.User) string {
	return fmt.Sprintf("frbr:%d", u.ID)
}

// extSubject is the subject for a login whose email matched no user row.
func extSubject(provider, providerSub string) string {
	return fmt.Sprintf("ext:%s:%s", provider, providerSub)
}

// userFromSubject resolves a frbr: subject back to its user row. Returns
// (nil, nil) for ext: and other non-frbr subjects — not an error, just not
// one of ours.
func (s *Server) userFromSubject(subject string) (*db.User, error) {
	idStr, ok := strings.CutPrefix(subject, "frbr:")
	if !ok {
		return nil, nil
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("malformed subject %q", subject)
	}
	return s.store.GetUser(id)
}

// mintSubject decides the token subject for a completed interactive login:
// frbr:<id> when the reported email belongs to a Fresh Breath user (and
// role/name come from that row, never the provider), else ext:<provider>:<sub>.
func (s *Server) mintSubject(rec *db.AuthRecord, email, providerSub string) (subject string, user *db.User) {
	if email != "" {
		if u, err := s.store.GetUserByEmail(email); err == nil {
			return subjectForUser(u), u
		}
	}
	return extSubject(authProvider(rec), providerSub), nil
}

// ── AES-256-GCM seal/open ───────────────────────────────────────────

func seal(key, plaintext []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("aes cipher: %w", err)
	}
	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("gcm: %w", err)
	}
	nonce := make([]byte, aesgcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("nonce: %w", err)
	}
	ciphertext := aesgcm.Seal(nonce, nonce, plaintext, nil) // prepends nonce
	return base64.RawURLEncoding.EncodeToString(ciphertext), nil
}

func open(key []byte, sealed string) ([]byte, error) {
	raw, err := base64.RawURLEncoding.DecodeString(sealed)
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes cipher: %w", err)
	}
	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}
	nonceSize := aesgcm.NonceSize()
	if len(raw) < nonceSize+aesgcm.Overhead() {
		return nil, fmt.Errorf("sealed data too short")
	}
	nonce, ciphertext := raw[:nonceSize], raw[nonceSize:]
	plaintext, err := aesgcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	return plaintext, nil
}

// ── Key derivation ──────────────────────────────────────────────────
//
// localKey is the single master secret (random, or derived from the TLS key).
// It's never used directly for crypto: deriveSubkey splits it into independent
// subkeys per purpose via HKDF-SHA256, so the JWT-signing (HMAC) key and the
// AES-GCM sealing key can't interact even though they share one master.

const (
	jwtSignLabel = "freshbreath/jwt-sign"
	sealLabel    = "freshbreath/seal"
)

func (s *Server) deriveSubkey(label string) []byte {
	k, err := hkdf.Key(sha256.New, s.localKey, nil, label, 32)
	if err != nil {
		// HKDF-SHA256 only errors on absurd output lengths; 32 bytes never does.
		panic(fmt.Sprintf("hkdf derive %q: %v", label, err))
	}
	return k
}

// ── Access token mint/verify ────────────────────────────────────────

func (s *Server) mintFreshbreathToken(subject, email, role, name string, authID int64, legs []int64, creds sealedCreds) (string, error) {
	sig, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.HS256, Key: s.deriveSubkey(jwtSignLabel)},
		(&jose.SignerOptions{}).WithType("JWT"),
	)
	if err != nil {
		return "", err
	}
	now := time.Now()
	claims := freshbreathClaims{
		Claims: josejwt.Claims{
			Issuer:   "freshbreath",
			Subject:  subject,
			Audience: josejwt.Audience{"freshbreath"},
			IssuedAt: josejwt.NewNumericDate(now),
			Expiry:   josejwt.NewNumericDate(now.Add(accessTokenTTL)),
		},
		UserEmail: email,
		UserRole:  role,
		UserName:  name,
		AuthID:    authID,
		Legs:      legs,
	}
	if len(creds) > 0 {
		plain, err := json.Marshal(creds)
		if err != nil {
			return "", fmt.Errorf("marshal creds: %w", err)
		}
		claims.Sealed, err = seal(s.deriveSubkey(sealLabel), plain)
		if err != nil {
			return "", fmt.Errorf("seal creds: %w", err)
		}
	}
	return josejwt.Signed(sig).Claims(claims).Serialize()
}

// verifyFreshbreathToken verifies a Fresh Breath JWT and returns the claims
// with any sealed credentials decrypted into Creds. Returns (nil, nil) if
// the token is not a Fresh Breath JWT — the caller should try other paths.
func (s *Server) verifyFreshbreathToken(raw string) (*freshbreathClaims, error) {
	if !isFreshbreathToken(raw) {
		return nil, nil
	}
	tok, err := josejwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.HS256})
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	var claims freshbreathClaims
	if err := tok.Claims(s.deriveSubkey(jwtSignLabel), &claims); err != nil {
		return nil, fmt.Errorf("verify: %w", err)
	}
	if err := claims.Claims.Validate(josejwt.Expected{
		Issuer:      "freshbreath",
		AnyAudience: josejwt.Audience{"freshbreath"},
		Time:        time.Now(),
	}); err != nil {
		return nil, fmt.Errorf("invalid: %w", err)
	}
	if claims.Sealed != "" {
		plain, err := open(s.deriveSubkey(sealLabel), claims.Sealed)
		if err != nil {
			return nil, fmt.Errorf("unseal creds: %w", err)
		}
		if err := json.Unmarshal(plain, &claims.Creds); err != nil {
			return nil, fmt.Errorf("unmarshal creds: %w", err)
		}
	}
	return &claims, nil
}

// verifyAndUnwrapToken verifies a Fresh Breath JWT and checks it is bound
// to the expected auth record (directly or via legs). Returns (nil, nil)
// for non-Fresh-Breath tokens.
func (s *Server) verifyAndUnwrapToken(raw string, expectedAuthID int64) (*freshbreathClaims, error) {
	claims, err := s.verifyFreshbreathToken(raw)
	if err != nil {
		return nil, err
	}
	if claims == nil {
		return nil, nil
	}
	if !claims.boundTo(expectedAuthID) {
		return nil, fmt.Errorf("token not bound to this auth record")
	}
	return claims, nil
}

// ── Refresh tokens ──────────────────────────────────────────────────

// upstreamRefreshLeg carries what's needed to refresh one provider's
// upstream credential: the refresh token itself, the endpoint it rotated
// from, and the record whose client credentials authenticate the request.
type upstreamRefreshLeg struct {
	AuthID       int64  `json:"auth_id"`
	RefreshToken string `json:"refresh_token"`
	TokenURL     string `json:"token_url,omitempty"`
	Scopes       string `json:"scopes,omitempty"`
}

// freshbreathRefreshData is the payload sealed inside a refresh token.
type freshbreathRefreshData struct {
	Subject   string                        `json:"subject"`
	UserEmail string                        `json:"user_email,omitempty"`
	AuthID    int64                         `json:"auth_id"`
	Legs      []int64                       `json:"legs,omitempty"`
	Upstreams map[string]upstreamRefreshLeg `json:"upstreams,omitempty"` // provider → leg

	// Token-family fields for rotation (stored in JWT claims, unsealed).
	FamilyID string `json:"family_id,omitempty"`
	JTI      string `json:"jti,omitempty"`
}

// newRefreshFamily creates a refresh-family record and returns the family ID
// and its initial JTI. The caller stamps both into the minted refresh token.
func (s *Server) newRefreshFamily(subject string, authID int64, deviceLabel string) (familyID, jti string, err error) {
	familyID = db.GenNonce()
	jti = newJTI()
	fam := &db.RefreshFamily{
		ID:          familyID,
		Subject:     subject,
		AuthID:      authID,
		DeviceLabel: deviceLabel,
		CurrentJTI:  jti,
		ExpiresAt:   time.Now().Add(refreshTokenTTL),
	}
	if err := s.store.CreateRefreshFamily(fam); err != nil {
		return "", "", fmt.Errorf("create refresh family: %w", err)
	}
	return familyID, jti, nil
}

func (s *Server) mintRefreshToken(data freshbreathRefreshData) (string, error) {
	sig, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.HS256, Key: s.deriveSubkey(jwtSignLabel)},
		(&jose.SignerOptions{}).WithType("JWT"),
	)
	if err != nil {
		return "", err
	}
	// Encrypt the entire refresh payload.
	plain, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("marshal refresh data: %w", err)
	}
	sealed, err := seal(s.deriveSubkey(sealLabel), plain)
	if err != nil {
		return "", fmt.Errorf("seal refresh data: %w", err)
	}
	now := time.Now()
	type refreshClaims struct {
		josejwt.Claims
		Sealed   string `json:"sealed"`
		FamilyID string `json:"family_id"`
		JTI      string `json:"jti"`
	}
	claims := refreshClaims{
		Claims: josejwt.Claims{
			Issuer:   "freshbreath",
			Subject:  data.Subject,
			Audience: josejwt.Audience{"freshbreath"},
			IssuedAt: josejwt.NewNumericDate(now),
			Expiry:   josejwt.NewNumericDate(now.Add(refreshTokenTTL)),
		},
		Sealed:   sealed,
		FamilyID: data.FamilyID,
		JTI:      data.JTI,
	}
	return josejwt.Signed(sig).Claims(claims).Serialize()
}

func (s *Server) makeRefreshCookie(w http.ResponseWriter, data freshbreathRefreshData) (string, error) {
	rt, err := s.mintRefreshToken(data)
	if err != nil {
		return "", err
	}
	// Scope the cookie to the auth record's own token path so a browser
	// holding refresh tokens for several records keeps them in separate
	// cookie slots (the jar keys cookies by name+domain+path). Record-scoped
	// rather than service-scoped so every session behind one gate — however
	// many services share it — refreshes from the same slot. SameSite=None is
	// required because legitimate consumers include file://-loaded apps and
	// apps served from a foreign origin registered in the app config; the
	// cross-site CSRF surface this opens is closed by the app/record
	// authorization in handleRefreshTokenGrant rather than by SameSite.
	http.SetCookie(w, &http.Cookie{
		Name:     "refresh_token",
		Value:    rt,
		Path:     "/oauth/token/" + strconv.FormatInt(data.AuthID, 10),
		MaxAge:   int(refreshTokenTTL.Seconds()),
		HttpOnly: true,
		Secure:   s.config.TLSCertFile != "",
		SameSite: http.SameSiteNoneMode,
	})
	return rt, nil
}

// verifyRefreshToken decrypts a refresh token and returns its payload.
func (s *Server) verifyRefreshToken(raw string) (*freshbreathRefreshData, error) {
	tok, err := josejwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.HS256})
	if err != nil {
		return nil, fmt.Errorf("parse refresh: %w", err)
	}
	var outer struct {
		josejwt.Claims
		Sealed   string `json:"sealed"`
		FamilyID string `json:"family_id"`
		JTI      string `json:"jti"`
	}
	if err := tok.Claims(s.deriveSubkey(jwtSignLabel), &outer); err != nil {
		return nil, fmt.Errorf("verify refresh: %w", err)
	}
	if err := outer.Claims.Validate(josejwt.Expected{
		Issuer:      "freshbreath",
		AnyAudience: josejwt.Audience{"freshbreath"},
		Time:        time.Now(),
	}); err != nil {
		return nil, fmt.Errorf("refresh token invalid: %w", err)
	}
	plain, err := open(s.deriveSubkey(sealLabel), outer.Sealed)
	if err != nil {
		return nil, fmt.Errorf("unseal refresh: %w", err)
	}
	var data freshbreathRefreshData
	if err := json.Unmarshal(plain, &data); err != nil {
		return nil, fmt.Errorf("unmarshal refresh: %w", err)
	}
	data.FamilyID = outer.FamilyID
	data.JTI = outer.JTI
	return &data, nil
}
