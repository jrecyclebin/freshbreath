package main

import (
  "context"
  "crypto/aes"
  "crypto/cipher"
  "crypto/rand"
  "encoding/base64"
  "encoding/json"
  "fmt"
  "io"
  "net/http"
  "net/url"
  "strings"
  "time"

  jose "github.com/go-jose/go-jose/v4"
  josejwt "github.com/go-jose/go-jose/v4/jwt"
  "github.com/coreos/go-oidc/v3/oidc"
  "github.com/modelcontextprotocol/go-sdk/auth"
  "github.com/modelcontextprotocol/go-sdk/oauthex"
  "golang.org/x/oauth2"
)

// buildOIDCScopes splits the descriptor scopes and ensures "openid" is always present.
func buildOIDCScopes(descriptorScopes string) []string {
	raw := strings.TrimSpace(descriptorScopes)
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

// serviceBeginAuth discovers the service's OAuth config, dynamically registers a client,
// and returns the authorization URL plus the values needed for the callback.
func (s *Server) serviceBeginAuth(ctx context.Context, svc *Service, redirectURI string) (authURL, clientID, clientSecret, tokenURL, state, verifier string, err error) {
  svcURL, err := url.Parse(svc.URL)
  if err != nil {
    return "", "", "", "", "", "", fmt.Errorf("bad service url: %w", err)
  }

  // If the descriptor provides an oauth_url, use that as the auth server base.
  // Otherwise derive it from the service URL.
  authServerBase := svc.Descriptor.OAuthURL
  if authServerBase == "" {
    authServerBase = (&url.URL{Scheme: svcURL.Scheme, Host: svcURL.Host}).String()
  } else {
    authServerBase = strings.TrimSuffix(authServerBase, "/authorize")
  }

  asm, err := auth.GetAuthServerMetadata(ctx, authServerBase, s.httpClient)
  if err != nil {
    return "", "", "", "", "", "", fmt.Errorf("metadata discovery: %w", err)
  }
  if asm == nil {
    asm = &oauthex.AuthServerMeta{
      AuthorizationEndpoint: authServerBase + "/authorize",
      TokenEndpoint:         authServerBase + "/token",
      RegistrationEndpoint:  authServerBase + "/register",
    }
  }

  // If the descriptor has a pre-registered client_id, skip DCR.
  clientID = svc.Descriptor.ClientID
  clientSecret = svc.Descriptor.ClientSecret
  tokenURL = asm.TokenEndpoint

  if clientID == "" {
    if asm.RegistrationEndpoint == "" {
      return "", "", "", "", "", "", fmt.Errorf("no registration endpoint")
    }

    regResp, err := oauthex.RegisterClient(ctx, asm.RegistrationEndpoint, &oauthex.ClientRegistrationMetadata{
      ClientName:    "freshbreath",
      RedirectURIs:  []string{redirectURI},
      GrantTypes:    []string{"authorization_code", "refresh_token"},
      ResponseTypes: []string{"code"},
      Scope:         strings.Join(asm.ScopesSupported, " "),
      TokenEndpointAuthMethod: "none",
    }, s.httpClient)
    if err != nil {
      return "", "", "", "", "", "", fmt.Errorf("client registration: %w", err)
    }
    clientID = regResp.ClientID
    clientSecret = regResp.ClientSecret
  }

  state = randomNonce()
  verifier = oauth2.GenerateVerifier()

  reqScopes := buildOIDCScopes(svc.Descriptor.Scopes)
  if svc.Descriptor.Type == "mcp" {
    for _, s := range asm.ScopesSupported {
      if strings.Contains(strings.ToLower(s), "mcp") {
	reqScopes = append(reqScopes, s)
      }
    }
  }

  cfg := &oauth2.Config{
    ClientID:     clientID,
    ClientSecret: clientSecret,
    Endpoint: oauth2.Endpoint{
      AuthURL:  asm.AuthorizationEndpoint,
      TokenURL: asm.TokenEndpoint,
    },
    RedirectURL: redirectURI,
    Scopes:      reqScopes,
  }
  authURL = cfg.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oauth2.SetAuthURLParam("resource", svc.URL))
  return authURL, clientID, clientSecret, tokenURL, state, verifier, nil
}

// resolveTokenEndpoint discovers the token endpoint for any service.
// OIDC services use .well-known/openid-configuration; MCP/API services use
// OAuth metadata discovery (OAuthURL or derived from svc.URL).
func (s *Server) resolveTokenEndpoint(ctx context.Context, svc *Service) (string, error) {
  if svc.Descriptor.Type == "oidc" {
    provider, err := s.getOIDCProvider(ctx, svc.ID, svc.URL)
    if err != nil {
      return "", fmt.Errorf("oidc provider: %w", err)
    }
    var claims struct {
      TokenEndpoint string `json:"token_endpoint"`
    }
    if err := provider.Claims(&claims); err != nil || claims.TokenEndpoint == "" {
      return "", fmt.Errorf("oidc: no token_endpoint in provider metadata")
    }
    return claims.TokenEndpoint, nil
  }

  svcURL, err := url.Parse(svc.URL)
  if err != nil {
    return "", fmt.Errorf("bad service url: %w", err)
  }

  base := svc.Descriptor.OAuthURL
  if base == "" {
    base = (&url.URL{Scheme: svcURL.Scheme, Host: svcURL.Host}).String()
  } else {
    base = strings.TrimSuffix(base, "/authorize")
  }

  asm, err := auth.GetAuthServerMetadata(ctx, base, s.httpClient)
  if err != nil || asm == nil {
    return base + "/token", nil
  }
  if asm.TokenEndpoint != "" {
    return asm.TokenEndpoint, nil
  }
  return base + "/token", nil
}

func (s *Server) serviceExchangeCode(ctx context.Context, tokenEndpoint, code, verifier, clientID, clientSecret, redirectURI string) (*OAuthData, error) {
  cfg := &oauth2.Config{
    ClientID:     clientID,
    ClientSecret: clientSecret,
    Endpoint:     oauth2.Endpoint{TokenURL: tokenEndpoint},
    RedirectURL:  redirectURI,
  }
  clientCtx := context.WithValue(ctx, oauth2.HTTPClient, s.httpClient)
  tok, err := cfg.Exchange(clientCtx, code, oauth2.VerifierOption(verifier))
  if err != nil {
    return nil, fmt.Errorf("token exchange: %w", err)
  }
  return &OAuthData{
    ClientID:      clientID,
    AccessToken:   tok.AccessToken,
    RefreshToken:  tok.RefreshToken,
    TokenType:     tok.Type(),
    TokenEndpoint: tokenEndpoint,
    ExpiresAt:     tok.Expiry,
  }, nil
}

// ── OIDC ──

func randomNonce() string {
  b := make([]byte, 32)
  rand.Read(b)
  return base64.RawURLEncoding.EncodeToString(b)
}

func (s *Server) oidcBeginAuth(ctx context.Context, svc *Service, redirectURI string) (authURL, state, verifier, oidcNonce, tokenURL string, err error) {
  issuer := svc.URL

  provider, err := oidc.NewProvider(ctx, issuer)
  if err != nil {
    return "", "", "", "", "", fmt.Errorf("OIDC discovery for %s: %w", issuer, err)
  }

  scopes := buildOIDCScopes(svc.Descriptor.Scopes)

  endpoint := provider.Endpoint()
  cfg := &oauth2.Config{
    ClientID:     svc.Descriptor.ClientID,
    ClientSecret: svc.Descriptor.ClientSecret,
    Endpoint:     endpoint,
    RedirectURL:  redirectURI,
    Scopes:       scopes,
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

type OIDCClaims struct {
  Issuer  string                 `json:"iss"`
  Subject string                 `json:"sub"`
  Audience []string              `json:"aud"`
  Expiry  int64                  `json:"exp"`
  IssuedAt int64                 `json:"iat"`
  Nonce   string                 `json:"nonce,omitempty"`
  Email   string                 `json:"email,omitempty"`
  Name    string                 `json:"name,omitempty"`
  Picture string                 `json:"picture,omitempty"`
  Raw     map[string]interface{} `json:"-"`
}

func (s *Server) oidcExchangeCode(ctx context.Context, svc *Service, code, verifier, oidcNonce, redirectURI string) (*OIDCClaims, string, string, string, error) {
  issuer := svc.URL

  provider, err := oidc.NewProvider(ctx, issuer)
  if err != nil {
    return nil, "", "", "", fmt.Errorf("OIDC discovery: %w", err)
  }

  cfg := &oauth2.Config{
    ClientID:     svc.Descriptor.ClientID,
    ClientSecret: svc.Descriptor.ClientSecret,
    Endpoint:     provider.Endpoint(),
    RedirectURL:  redirectURI,
    Scopes:       buildOIDCScopes(svc.Descriptor.Scopes),
  }

  clientCtx := context.WithValue(ctx, oauth2.HTTPClient, s.httpClient)
  tok, err := cfg.Exchange(clientCtx, code, oauth2.VerifierOption(verifier))
  if err != nil {
    return nil, "", "", "", fmt.Errorf("token exchange: %w", err)
  }

  idTokenRaw, ok := tok.Extra("id_token").(string)
  if !ok || idTokenRaw == "" {
    // Provider didn't return an id_token — fetch identity via userinfo and mint a local token.
    return s.exchangeViaUserInfo(ctx, svc, provider, tok)
  }

  verifierConfig := &oidc.Config{
    ClientID: svc.Descriptor.ClientID,
  }
  idToken, err := provider.Verifier(verifierConfig).Verify(ctx, idTokenRaw)
  if err != nil {
    return nil, "", "", "", fmt.Errorf("id token verification: %w", err)
  }

  claims := &OIDCClaims{Raw: make(map[string]interface{})}
  if err := idToken.Claims(claims); err != nil {
    return nil, "", "", "", fmt.Errorf("id token claims: %w", err)
  }

  if claims.Nonce != oidcNonce {
    return nil, "", "", "", fmt.Errorf("nonce mismatch")
  }

  // Also grab all claims into Raw for anything we didn't explicitly map
  if err := idToken.Claims(&claims.Raw); err != nil {
    return nil, "", "", "", fmt.Errorf("id token raw claims: %w", err)
  }

  return claims, tok.AccessToken, tok.RefreshToken, idTokenRaw, nil
}

// exchangeViaUserInfo is the fallback path for providers (e.g. GitHub) that complete
// OAuth2 successfully but don't return an id_token. We verify identity by calling the
// userinfo endpoint, then mint a freshbreath token signed with our local key.
func (s *Server) exchangeViaUserInfo(ctx context.Context, svc *Service, provider *oidc.Provider, tok *oauth2.Token) (*OIDCClaims, string, string, string, error) {
  var email, name, sub string

  if svc.Descriptor.UserInfoURL != "" {
    req, err := http.NewRequestWithContext(ctx, http.MethodGet, svc.Descriptor.UserInfoURL, nil)
    if err != nil {
      return nil, "", "", "", fmt.Errorf("userinfo request: %w", err)
    }
    req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
    req.Header.Set("Accept", "application/json")
    resp, err := s.httpClient.Do(req)
    if err != nil {
      return nil, "", "", "", fmt.Errorf("userinfo fetch: %w", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
      return nil, "", "", "", fmt.Errorf("userinfo returned %d", resp.StatusCode)
    }
    var ui struct {
      Email string `json:"email"`
      Name  string `json:"name"`
      Login string `json:"login"`
      Sub   string `json:"sub"`
    }
    if err := json.NewDecoder(resp.Body).Decode(&ui); err != nil {
      return nil, "", "", "", fmt.Errorf("userinfo decode: %w", err)
    }
    email, name = ui.Email, ui.Name
    sub = ui.Sub
    if sub == "" {
      sub = ui.Login
    }
  } else {
    userInfo, err := provider.UserInfo(ctx, oauth2.StaticTokenSource(tok))
    if err != nil {
      return nil, "", "", "", fmt.Errorf("userinfo: %w", err)
    }
    var ui struct {
      Email string `json:"email"`
      Name  string `json:"name"`
    }
    if err := userInfo.Claims(&ui); err != nil {
      return nil, "", "", "", fmt.Errorf("userinfo claims: %w", err)
    }
    email, name, sub = ui.Email, ui.Name, userInfo.Subject
  }

  // Some providers (e.g. GitHub with private email) omit email from the main profile.
  if email == "" && svc.Descriptor.UserEmailsURL != "" {
    req, err := http.NewRequestWithContext(ctx, http.MethodGet, svc.Descriptor.UserEmailsURL, nil)
    if err != nil {
      return nil, "", "", "", fmt.Errorf("emails request: %w", err)
    }
    req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
    req.Header.Set("Accept", "application/json")
    resp, err := s.httpClient.Do(req)
    if err != nil {
      return nil, "", "", "", fmt.Errorf("emails fetch: %w", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
      return nil, "", "", "", fmt.Errorf("emails returned %d", resp.StatusCode)
    }
    var entries []struct {
      Email    string `json:"email"`
      Primary  bool   `json:"primary"`
      Verified bool   `json:"verified"`
    }
    if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
      return nil, "", "", "", fmt.Errorf("emails decode: %w", err)
    }
    for _, e := range entries {
      if e.Primary && e.Verified {
        email = e.Email
        break
      }
    }
  }

  if email == "" {
    return nil, "", "", "", fmt.Errorf("no email in userinfo")
  }
  if sub == "" {
    sub = email
  }
  idTokenRaw, err := s.mintFreshbreathToken("admin", email, "", name, 0, nil)
  if err != nil {
    return nil, "", "", "", fmt.Errorf("fabricate token: %w", err)
  }
  claims := &OIDCClaims{
    Email:   email,
    Name:    name,
    Subject: sub,
    Raw:     map[string]interface{}{},
  }
  return claims, tok.AccessToken, tok.RefreshToken, idTokenRaw, nil
}

// ── Unified Freshbreath JWT ─────────────────────────────────────────
//
// All Freshbreath-issued tokens share a single claims type, distinguished
// by Kind: "wrapped" (virtual-service upstream token) or "admin"
// (control-panel login / central MCP). Sensitive upstream credentials
// in wrapped tokens are AES-256-GCM encrypted into the Sealed field;
// verifyFreshbreathToken decrypts them into the Upstream* fields (json:"-").

const (
  accessTokenTTL  = 15 * time.Minute
  refreshTokenTTL = 14 * 24 * time.Hour
)

// freshbreathClaims is the single claim type for all Freshbreath JWTs.
type freshbreathClaims struct {
  josejwt.Claims
  Kind      string `json:"kind"`                // "wrapped" or "admin"
  UserEmail string `json:"user_email"`           // present for all kinds
  UserRole  string `json:"user_role,omitempty"`  // central + panel
  UserName  string `json:"user_name,omitempty"`  // panel
  ServiceID int64  `json:"service_id,omitempty"` // wrapped
  Sealed    string `json:"sealed,omitempty"`     // wrapped: encrypted upstream data

  // Unsealed upstream data — populated by verifyFreshbreathToken, never serialized.
  UpstreamToken    string `json:"-"`
  UpstreamRefresh  string `json:"-"`
  UpstreamTokenURL string `json:"-"`
  UpstreamScopes   string `json:"-"`
}

// sealedUpstreamData is the plaintext encrypted into Sealed for Kind="wrapped".
type sealedUpstreamData struct {
  UpstreamToken    string `json:"upstream_token"`
  UpstreamRefresh  string `json:"upstream_refresh,omitempty"`
  UpstreamTokenURL string `json:"upstream_token_url,omitempty"`
  UpstreamScopes   string `json:"upstream_scopes,omitempty"`
}

// freshbreathRefreshData is the payload sealed inside a refresh token.
// It carries everything needed to re-mint an access token.
type freshbreathRefreshData struct {
  Kind             string `json:"kind"`
  ServiceID        int64  `json:"service_id,omitempty"`  // wrapped
  UserEmail        string `json:"user_email"`
  UserRole         string `json:"user_role,omitempty"`   // admin
  UserName         string `json:"user_name,omitempty"`   // admin
  UpstreamRefresh  string `json:"upstream_refresh,omitempty"`  // wrapped
  UpstreamTokenURL string `json:"upstream_token_url,omitempty"` // wrapped
  UpstreamScopes   string `json:"upstream_scopes,omitempty"`    // wrapped
}

// ── AES-256-GCM seal/open ───────────────────────────────────────────
//
// seal encrypts plaintext with AES-256-GCM using the given 32-byte key.
// Output: base64(nonce[12] || ciphertext || tag[16]).
// open decrypts the sealed string back to plaintext.

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

// ── Unified access token mint/verify ────────────────────────────────

func (s *Server) mintFreshbreathToken(kind, email, role, name string, serviceID int64, upstream *sealedUpstreamData) (string, error) {
  sig, err := jose.NewSigner(
    jose.SigningKey{Algorithm: jose.HS256, Key: s.localKey},
    (&jose.SignerOptions{}).WithType("JWT"),
  )
  if err != nil {
    return "", err
  }
  now := time.Now()
  claims := freshbreathClaims{
    Claims: josejwt.Claims{
      Issuer:   "freshbreath",
      Subject:  email,
      Audience: josejwt.Audience{"freshbreath"},
      IssuedAt: josejwt.NewNumericDate(now),
      Expiry:   josejwt.NewNumericDate(now.Add(accessTokenTTL)),
    },
    Kind:      kind,
    UserEmail: email,
    UserRole:  role,
    UserName:  name,
    ServiceID: serviceID,
  }
  // Seal upstream data for wrapped tokens.
  if upstream != nil {
    plain, err := json.Marshal(upstream)
    if err != nil {
      return "", fmt.Errorf("marshal upstream: %w", err)
    }
    claims.Sealed, err = seal(s.localKey, plain)
    if err != nil {
      return "", fmt.Errorf("seal upstream: %w", err)
    }
  }
  return josejwt.Signed(sig).Claims(claims).Serialize()
}

// verifyFreshbreathToken verifies a Freshbreath-issued JWT and returns the
// claims. For wrapped tokens, the Sealed field is decrypted and the
// Upstream* fields are populated. Returns (nil, nil) if the token is not
// a Freshbreath JWT — the caller should try other verification paths.
func (s *Server) verifyFreshbreathToken(raw string) (*freshbreathClaims, error) {
  if !isFreshbreathToken(raw) {
    return nil, nil
  }
  tok, err := josejwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.HS256})
  if err != nil {
    return nil, fmt.Errorf("parse: %w", err)
  }
  var claims freshbreathClaims
  if err := tok.Claims(s.localKey, &claims); err != nil {
    return nil, fmt.Errorf("verify: %w", err)
  }
  if err := claims.Claims.Validate(josejwt.Expected{
    Issuer:      "freshbreath",
    AnyAudience: josejwt.Audience{"freshbreath"},
    Time:        time.Now(),
  }); err != nil {
    return nil, fmt.Errorf("invalid: %w", err)
  }
  // Decrypt sealed upstream data for wrapped tokens.
  if claims.Sealed != "" {
    plain, err := open(s.localKey, claims.Sealed)
    if err != nil {
      return nil, fmt.Errorf("unseal upstream: %w", err)
    }
    var ud sealedUpstreamData
    if err := json.Unmarshal(plain, &ud); err != nil {
      return nil, fmt.Errorf("unmarshal upstream: %w", err)
    }
    claims.UpstreamToken = ud.UpstreamToken
    claims.UpstreamRefresh = ud.UpstreamRefresh
    claims.UpstreamTokenURL = ud.UpstreamTokenURL
    claims.UpstreamScopes = ud.UpstreamScopes
  }
  return &claims, nil
}

// ── Refresh token mint/verify ───────────────────────────────────────

func (s *Server) mintRefreshToken(data freshbreathRefreshData) (string, error) {
  sig, err := jose.NewSigner(
    jose.SigningKey{Algorithm: jose.HS256, Key: s.localKey},
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
  sealed, err := seal(s.localKey, plain)
  if err != nil {
    return "", fmt.Errorf("seal refresh data: %w", err)
  }
  now := time.Now()
  claims := struct {
    josejwt.Claims
    Sealed string `json:"sealed"`
  }{
    Claims: josejwt.Claims{
      Issuer:   "freshbreath",
      Subject:  data.UserEmail,
      Audience: josejwt.Audience{"freshbreath"},
      IssuedAt: josejwt.NewNumericDate(now),
      Expiry:   josejwt.NewNumericDate(now.Add(refreshTokenTTL)),
    },
    Sealed: sealed,
  }
  return josejwt.Signed(sig).Claims(claims).Serialize()
}

func (s *Server) makeRefreshCookie(w http.ResponseWriter, data freshbreathRefreshData) (string, error) {
  rt, err := s.mintRefreshToken(data)
  if err != nil {
    return "", err
  }
  http.SetCookie(w, &http.Cookie{
    Name:     "refresh_token",
    Value:    rt,
    Path:     "/oauth/token",
    MaxAge:   int(refreshTokenTTL.Seconds()),
    HttpOnly: true,
    Secure:   s.config.TLSCertFile != "",
    SameSite: http.SameSiteLaxMode,
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
    Sealed string `json:"sealed"`
  }
  if err := tok.Claims(s.localKey, &outer); err != nil {
    return nil, fmt.Errorf("verify refresh: %w", err)
  }
  if err := outer.Claims.Validate(josejwt.Expected{
    Issuer:      "freshbreath",
    AnyAudience: josejwt.Audience{"freshbreath"},
    Time:        time.Now(),
  }); err != nil {
    return nil, fmt.Errorf("refresh token invalid: %w", err)
  }
  plain, err := open(s.localKey, outer.Sealed)
  if err != nil {
    return nil, fmt.Errorf("unseal refresh: %w", err)
  }
  var data freshbreathRefreshData
  if err := json.Unmarshal(plain, &data); err != nil {
    return nil, fmt.Errorf("unmarshal refresh: %w", err)
  }
  return &data, nil
}
