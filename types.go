package main

import (
  "encoding/json"
  "time"
)

type App struct {
  ID          int64
  Nonce       string
  Name        string
  URL         string     `json:"url,omitempty"`
  OwnerID     *int64     `json:"owner_id,omitempty"`
  Environment string     `json:"environment,omitempty"`
  CreatedAt   time.Time
}

type User struct {
  ID        int64   `json:"id"`
  Name      string  `json:"name"`
  Email     string  `json:"email"`
  Role      string  `json:"role"`
  Status    string  `json:"status"`
  LastSeen  *time.Time `json:"last_seen,omitempty"`
  CreatedAt time.Time  `json:"created_at"`
  Apps      []string   `json:"apps,omitempty"`
}

type Role struct {
  ID          int64  `json:"id"`
  Name        string `json:"name"`
  Description string `json:"description"`
  Members     int    `json:"members"`
}

type AuditEntry struct {
  ID     int64   `json:"id"`
  When   string  `json:"when"`
  Actor  string  `json:"actor"`
  Action string  `json:"action"`
  Target string  `json:"target"`
}

type Service struct {
  ID         int64              `json:"id"`
  Name       string             `json:"name"`
  URL        string             `json:"url"`
  Descriptor ServiceDescriptor  `json:"descriptor"`
}

type ServiceDescriptor struct {
  Type         string `json:"type"`                     // "mcp", "api", or "oidc"
  Auth         string `json:"auth,omitempty"`            // "key" for API-key auth
  APIKey       string `json:"api_key,omitempty"`         // admin-set API key (injected by proxy)
  Header       string `json:"header,omitempty"`          // custom header for API key; empty = Bearer
  Proxied      bool   `json:"proxied,omitempty"`         // needs server-side proxy
  ClientID     string `json:"client_id,omitempty"`       // pre-registered client_id
  ClientSecret string `json:"client_secret,omitempty"`   // pre-registered client_secret
  OAuthURL     string `json:"oauth_url,omitempty"`       // OAuth base URL override
  Scopes         string `json:"scopes,omitempty"`               // space-separated scopes (OIDC)
  UserInfoURL    string `json:"userinfo_url,omitempty"`         // explicit userinfo endpoint returning {email,name,...} for providers that don't advertise one
  UserEmailsURL  string `json:"userinfo_emails_url,omitempty"`  // fallback endpoint returning [{email,primary,verified}] when userinfo has no email
}

// MarshalJSON serializes the descriptor, omitting zero-valued fields.
// We use a custom marshal so that `Proxied: false` is omitted (omitempty on bool
// already handles this, but this makes the intent explicit and gives us a single
// place to tweak serialization).
func (d ServiceDescriptor) MarshalJSON() ([]byte, error) {
  type Alias ServiceDescriptor
  return json.Marshal((*Alias)(&d))
}

type ServiceUpdate struct {
  Name       string            `json:"name"`
  URL        string            `json:"url"`
  Descriptor ServiceDescriptor `json:"descriptor"`
}

type OAuthData struct {
  ClientID      string                 `json:"client_id"`
  AccessToken   string                 `json:"access_token"`
  RefreshToken  string                 `json:"refresh_token"`
  TokenType     string                 `json:"token_type"`
  TokenEndpoint string                 `json:"token_endpoint"`
  ExpiresAt     time.Time              `json:"-"`
  ExpiresIn     int                    `json:"expires_in"`
  Claims        map[string]interface{} `json:"claims,omitempty"`
  IDToken       string                 `json:"id_token,omitempty"`
  Proxied       bool                   `json:"proxied,omitempty"`
  Scopes        string                 `json:"scopes,omitempty"`
}
