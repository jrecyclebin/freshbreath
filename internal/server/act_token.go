package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"poggers.institute/freshbreath/internal/db"
)

// actTokenLabel is the HKDF subkey label for act-token signing — the same
// deriveSubkey pattern as jwtSignLabel/sealLabel in services.go. A separate
// subkey means act-token HMACs can't interact with the JWT or seal keys
// even though all three derive from the one localKey. It rotates with
// localKey, so outstanding act tokens die on a relaunch that re-derives the
// key — accepted (J's call: the short TTL is the replay defense).
const actTokenLabel = "freshbreath/act-token"

// actTokenTTL is how long a minted act token stays valid. Short by design —
// it's the replay defense (no nonces, no server-side consumption state). A
// leaked token dies on its own clock, and on server relaunch if localKey is
// re-derived. See docs/mcp-file-transfer.md.
const actTokenTTL = 10 * time.Minute

// actTokenPayload is the signed content of a capability ("act") token.
// Every field is pinned at mint time and compared by the verifier, so the
// capability can't be widened after minting — no swapping path, method, or
// user. Path is a full path+query under /api/*.
type actTokenPayload struct {
	Path      string `json:"path"`
	Method    string `json:"method"`
	Expiry    int64  `json:"expiry"` // unix seconds
	UserEmail string `json:"user_email"`
}

// mintActToken issues a short-lived capability token letting an anonymous
// HTTP client perform one pinned operation on /api/* as user. The token
// carries its own HMAC: the URL grants nothing without it, and it grants
// nothing without the server's key. Returns the opaque token string; the
// caller builds the full /api/act/<token> URL.
func (s *Server) mintActToken(user *db.User, method, pathQuery string, ttl time.Duration) (string, error) {
	if !strings.HasPrefix(pathQuery, "/api/") {
		return "", fmt.Errorf("act token path must be under /api/: %q", pathQuery)
	}
	p := actTokenPayload{
		Path:      pathQuery,
		Method:    method,
		Expiry:    time.Now().Add(ttl).Unix(),
		UserEmail: user.Email,
	}
	plain, err := json.Marshal(&p)
	if err != nil {
		return "", fmt.Errorf("marshal act payload: %w", err)
	}
	enc := base64.RawURLEncoding.EncodeToString(plain)
	sig := s.actTokenMAC(enc)
	return enc + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// actTokenMAC returns HMAC-SHA256 of message under the act-token subkey.
func (s *Server) actTokenMAC(message string) []byte {
	mac := hmac.New(sha256.New, s.deriveSubkey(actTokenLabel))
	mac.Write([]byte(message))
	return mac.Sum(nil)
}

// verifyActToken validates a raw act token: HMAC (constant-time), expiry,
// and /api/* scope. The method-pin and user re-resolution happen in
// handleAct (they need the live request); this stays a pure check of the
// token itself, which keeps it cheap to test in isolation.
func (s *Server) verifyActToken(raw string) (*actTokenPayload, error) {
	enc, sigB64, ok := strings.Cut(raw, ".")
	if !ok {
		return nil, errors.New("act token: malformed")
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return nil, fmt.Errorf("act token: decode signature: %w", err)
	}
	if !hmac.Equal(sig, s.actTokenMAC(enc)) {
		return nil, errors.New("act token: signature mismatch")
	}
	plain, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return nil, fmt.Errorf("act token: decode payload: %w", err)
	}
	var p actTokenPayload
	if err := json.Unmarshal(plain, &p); err != nil {
		return nil, fmt.Errorf("act token: parse payload: %w", err)
	}
	if time.Now().Unix() > p.Expiry {
		return nil, errors.New("act token: expired")
	}
	// Defense-in-depth: the minter rejects non-/api/ paths, but re-check
	// here so a hand-crafted or carelessly-minted token can't widen scope.
	if !strings.HasPrefix(p.Path, "/api/") {
		return nil, errors.New("act token: path out of scope")
	}
	return &p, nil
}

// handleAct serves /api/act/{token}. It's mounted bare (not authWrap'd) and
// anonymous by design: it authenticates via the capability token, not a
// bearer. After verifying, it resolves the named user fresh from the DB —
// so a demoted user's outstanding tokens stop working — pins the method,
// and re-dispatches through s.mux with the user pre-stashed in the context.
// authWrap then sees the pre-set userKey and short-circuits its bearer
// check, so every downstream role/authz gate runs exactly as for a bearer
// request. The only other setter of userKey is authWrap; the trust
// boundary stays a two-member club.
func (s *Server) handleAct(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.URL.Path, "/api/act/")
	if token == "" || strings.Contains(token, "/") {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	payload, err := s.verifyActToken(token)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != payload.Method {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	user, err := s.store.GetUserByEmail(payload.UserEmail)
	if err != nil || user == nil || user.Status != "Active" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	parsed, err := url.Parse(payload.Path)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	r.URL = parsed
	r.RequestURI = parsed.RequestURI() // keep in sync with the rewritten URL
	ctx := context.WithValue(r.Context(), userKey, user)
	s.mux.ServeHTTP(w, r.WithContext(ctx))
}
