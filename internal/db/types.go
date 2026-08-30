package db

import (
	"encoding/json"
	"time"

	"poggers.institute/freshbreath/internal/sshkit"
)

type App struct {
	ID          int64
	Nonce       string
	Name        string
	URL         string      `json:"url,omitempty"`
	OwnerID     *int64      `json:"owner_id,omitempty"`
	Environment string      `json:"environment,omitempty"`
	Details     *AppDetails `json:"details,omitempty"`
	ProtectedBy *int64      `json:"protected_by,omitempty"` // inbound gate auth record
	CreatedAt   time.Time
}

type AppDetails struct {
	LastUploaded           *time.Time `json:"last_uploaded,omitempty"`
	LastDeployedStaging    *time.Time `json:"last_deployed_staging,omitempty"`
	LastDeployedProduction *time.Time `json:"last_deployed_production,omitempty"`
}

type User struct {
	ID        int64         `json:"id"`
	Name      string        `json:"name"`
	Email     string        `json:"email"`
	Role      string        `json:"role"`
	Status    string        `json:"status"`
	LastSeen  *time.Time    `json:"last_seen,omitempty"`
	CreatedAt time.Time     `json:"created_at"`
	Apps      []string      `json:"apps,omitempty"`
	Metadata  *UserMetadata `json:"metadata,omitempty"`
}

type UserMetadata struct {
	SSHKey *SSHKeyInfo `json:"ssh_key,omitempty"`
}

// SSHKeyInfo is an alias for sshkit.SSHKeyInfo so that the root package
// (and future internal/server) can reference it without qualification.
type SSHKeyInfo = sshkit.SSHKeyInfo

// MarshalJSON masks sensitive SSH key fields before sending to the frontend.
// The DB stores metadata separately via json.Marshal(metadata) which is unaffected.
func (u User) MarshalJSON() ([]byte, error) {
	type Alias User
	out := Alias(u)
	if out.Metadata != nil && out.Metadata.SSHKey != nil {
		out.Metadata = &UserMetadata{
			SSHKey: &SSHKeyInfo{
				PublicKey:   out.Metadata.SSHKey.PublicKey,
				Fingerprint: out.Metadata.SSHKey.Fingerprint,
				KeyType:     out.Metadata.SSHKey.KeyType,
			},
		}
	}
	return json.Marshal(out)
}

type Role struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Members     int    `json:"members"`
}

type AuditEntry struct {
	ID     int64  `json:"id"`
	When   string `json:"when"`
	Actor  string `json:"actor"`
	Action string `json:"action"`
	Target string `json:"target"`
}

type Service struct {
	ID          int64             `json:"id"`
	Name        string            `json:"name"`
	URL         string            `json:"url"`
	Descriptor  ServiceDescriptor `json:"descriptor"`
	ProtectedBy *int64            `json:"protected_by"` // inbound gate auth record; nil = inherit admin
	ActsAs      *int64            `json:"acts_as"`      // outbound credential record; nil = caller's own
}

// UpdateFeed is one remote-updates entry: either a receive feed (a remote URL
// Freshbreath pulls key-authenticated archives from) or a publish feed (a
// label for archives this instance produces). KeyHex is the per-feed random
// AES key — never included in API responses (see coreListUpdateFeeds).
type UpdateFeed struct {
	ID                 string     `json:"id"`
	URL                string     `json:"url"`
	Mode               string     `json:"mode"` // "receive" | "publish"
	KeyHex             string     `json:"-"`
	Name               string     `json:"name"`
	CreatedBy          int64      `json:"created_by"`
	CreatedAt          time.Time  `json:"created_at"`
	LastAppliedVersion string     `json:"last_applied_version"`
	LastAppliedAt      *time.Time `json:"last_applied_at"`
	LastSeenVersion    string     `json:"last_seen_version"`
	LastETag           string     `json:"last_etag"`
	LastModified       string     `json:"last_modified"`
	LastError          string     `json:"last_error"`
	LastErrorAt        *time.Time `json:"last_error_at"`
}

// ServiceDescriptor describes what a service IS. Auth lives elsewhere: the
// protected_by / acts_as columns reference auth_records
// (design/decoupled-auth.md).
type ServiceDescriptor struct {
	Type    string `json:"type"`              // "mcp", "api", "tasks", "virtual", or "ssh"
	Proxied bool   `json:"proxied,omitempty"` // needs server-side proxy

	// App-database targeting for virtual services with SQL steps
	// (design/app-databases.md). "" = each caller's own app.db (default);
	// "global" = db/<name>.db shared between apps; "app:<nonce>" = pinned
	// to one app's data. DatabaseName defaults to "app".
	DatabaseTarget string `json:"database_target,omitempty"`
	DatabaseName   string `json:"database_name,omitempty"`
}

// serviceTypes is the closed set of descriptor types.
var serviceTypes = map[string]bool{
	"mcp": true, "api": true, "tasks": true, "virtual": true, "ssh": true,
}

// ValidServiceType reports whether t names a known service type.
func ValidServiceType(t string) bool { return serviceTypes[t] }

// MarshalJSON serializes the descriptor, omitting zero-valued fields.
// We use a custom marshal so that `Proxied: false` is omitted (omitempty on bool
// already handles this, but this makes the intent explicit and gives us a single
// place to tweak serialization).
func (d ServiceDescriptor) MarshalJSON() ([]byte, error) {
	type Alias ServiceDescriptor
	return json.Marshal((*Alias)(&d))
}

type ServiceUpdate struct {
	Name        string            `json:"name"`
	URL         string            `json:"url"`
	Descriptor  ServiceDescriptor `json:"descriptor"`
	ProtectedBy *int64            `json:"protected_by"`
	ActsAs      *int64            `json:"acts_as"`
}

type OAuthData struct {
	ClientID      string                 `json:"client_id"`
	AccessToken   string                 `json:"access_token"`
	RefreshToken  string                 `json:"refresh_token"`
	TokenType     string                 `json:"token_type"`
	TokenEndpoint string                 `json:"token_endpoint"`
	ExpiresAt     time.Time              `json:"expires_at"`
	Claims        map[string]interface{} `json:"claims,omitempty"`
	IDToken       string                 `json:"id_token,omitempty"`
	Proxied       bool                   `json:"proxied,omitempty"`
	Scopes        string                 `json:"scopes,omitempty"`
}
