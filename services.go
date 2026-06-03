package main

import (
  "context"
  "crypto/rand"
  "encoding/base64"
  "encoding/json"
  "fmt"
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

func randomState() string {
  b := make([]byte, 32)
  rand.Read(b)
  return base64.RawURLEncoding.EncodeToString(b)
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

  state = randomState()
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

  state = randomState()
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
  idTokenRaw, err := s.fabricateIDToken(email, name, sub)
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

func (s *Server) fabricateIDToken(email, name, sub string) (string, error) {
  sig, err := jose.NewSigner(
    jose.SigningKey{Algorithm: jose.HS256, Key: s.localKey},
    (&jose.SignerOptions{}).WithType("JWT"),
  )
  if err != nil {
    return "", err
  }
  now := time.Now()
  claims := struct {
    josejwt.Claims
    Email string `json:"email"`
    Name  string `json:"name,omitempty"`
  }{
    Claims: josejwt.Claims{
      Issuer:   "freshbreath",
      Subject:  sub,
      Audience: josejwt.Audience{"freshbreath"},
      IssuedAt: josejwt.NewNumericDate(now),
      Expiry:   josejwt.NewNumericDate(now.Add(24 * time.Hour)),
    },
    Email: email,
    Name:  name,
  }
  return josejwt.Signed(sig).Claims(claims).Serialize()
}

func (s *Server) verifyFreshbreathToken(raw string) (string, error) {
  tok, err := josejwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.HS256})
  if err != nil {
    return "", fmt.Errorf("parse: %w", err)
  }
  var claims struct {
    josejwt.Claims
    Email string `json:"email"`
  }
  if err := tok.Claims(s.localKey, &claims); err != nil {
    return "", fmt.Errorf("verify: %w", err)
  }
  if err := claims.ValidateWithLeeway(josejwt.Expected{
    Issuer:      "freshbreath",
    AnyAudience: josejwt.Audience{"freshbreath"},
    Time:        time.Now(),
  }, time.Minute); err != nil {
    return "", fmt.Errorf("invalid: %w", err)
  }
  if claims.Email == "" {
    return "", fmt.Errorf("no email in token")
  }
  return claims.Email, nil
}
