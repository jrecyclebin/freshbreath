# Unified Token Refresh

Unify Freshbreath's three JWT families (wrapped virtual-service, central MCP, control-panel) into a single `freshbreathClaims` type with 15-min access TTL, 2-week encrypted refresh tokens, and encrypted sensitive claims for wrapped tokens. All refresh goes through `/oauth/token`.

## Relevant Files

- `handler.go` - Auth middleware (`authWrap`, `verifyAdminToken`), callback/login handlers, route setup, `isFreshbreathToken`
- `oauth_server.go` - `mintWrappedToken`, `wrappedTokenClaims`, `verifyAndUnwrapToken`, `handleToken` (needs refresh grant)
- `mcp_central.go` - `centralMCPClaims`, `mintCentralMCPToken`, `verifyCentralMCPToken`, central MCP token verifier
- `services.go` - `fabricateIDToken`, `verifyFreshbreathToken`, `OIDCClaims`
- `mcp_endpoint.go` - `verifyAndUnwrapToken` callers (virtual MCP endpoint auth)
- `main.go` - Server struct (2-space, never gofmt)
- `web/frbr.js` - `ServiceProxy.refresh()` — needs to hit `/oauth/token` for refresh
- `web/control/app.js` - Admin UI token handling, `api()` function Bearer header

## Quality Check

After each phase: `go build ./...`, `go vet ./...`, `go test ./...`

## Phase 1: Foundation

- [ ] Add `seal(localKey, plaintext []byte) (string, error)` and `open(localKey, ciphertext string) ([]byte, error)` — AES-256-GCM, keyed from localKey (32 bytes), prior art in `ssh_keys.go`
- [ ] Define `freshbreathClaims` struct (unified: Kind, UserEmail, UserRole, UserName, ServiceID, Sealed)
- [ ] Define `sealedUpstreamData` struct (UpstreamToken, UpstreamRefresh, UpstreamTokenURL, UpstreamScopes)
- [ ] Define `freshbreathRefreshData` struct (Kind, ServiceID, UserEmail, UserRole, UpstreamRefresh, UpstreamTokenURL, UpstreamScopes — the sealed payload for refresh tokens)

## Phase 2: Unified Access Tokens

- [ ] Write `mintFreshbreathToken(opts)` — single mint function replacing `mintWrappedToken`, `mintCentralMCPToken`, `fabricateIDToken`
  - For Kind="wrapped": seal upstream data into Sealed field
  - For Kind="central": set UserEmail + UserRole
  - For Kind="panel": set UserEmail + UserName
- [ ] Write `verifyFreshbreathToken(raw) (*freshbreathClaims, error)` — single verify function
  - If Sealed != "": decrypt and populate Upstream* fields on the struct (json:"-" tags)
  - Returns nil, nil if not a Freshbreath JWT (callers try other verification)
- [ ] Replace `mintWrappedToken` callers → `mintFreshbreathToken`
- [ ] Replace `mintCentralMCPToken` callers → `mintFreshbreathToken`
- [ ] Replace `fabricateIDToken` callers → `mintFreshbreathToken`
- [ ] Replace `verifyAndUnwrapToken` callers → `verifyFreshbreathToken`
- [ ] Replace `verifyCentralMCPToken` callers → `verifyFreshbreathToken`
- [ ] Replace old `verifyFreshbreathToken` callers → new unified version
- [ ] Remove old claim structs and mint/verify functions (`wrappedTokenClaims`, `centralMCPClaims`, inline panel struct)

## Phase 3: 15-min TTL + Encrypted Sealed Claims

- [ ] Set access token TTL to 15 minutes in `mintFreshbreathToken`
- [ ] Update `handleToken` `expires_in` to use 15-min window
- [ ] Update control-panel `OAuthData.ExpiresAt` to 15 min
- [ ] Verify seal/open works end-to-end for Kind="wrapped" tokens

## Phase 4: Refresh Tokens

- [ ] Write `mintRefreshToken(localKey, data freshbreathRefreshData) (string, error)` — signed JWT, 14-day TTL, encrypted sealed payload
- [ ] Write `verifyRefreshToken(localKey, raw string) (*freshbreathRefreshData, error)` — verify + decrypt
- [ ] Extend `handleToken` to accept `grant_type=refresh_token`
  - Read refresh token from cookie or request body
  - Decrypt and dispatch on Kind
  - Kind="wrapped": upstream refresh → re-wrap → new access + refresh tokens
  - Kind="central": look up user → re-mint central access token → new refresh
  - Kind="panel": look up user → re-mint panel access token → new refresh
- [ ] Set refresh cookie on all token issuance (access token response + initial login)
  - HttpOnly, Secure (if TLS), Path=/oauth/token, SameSite=Lax
- [ ] Also return `refresh_token` in JSON body for programmatic clients
- [ ] Update control-panel login flow (`completeAuth` / `writeCallbackPage`) to set refresh cookie
- [ ] Update `handleToken` `authorization_code` flow to set refresh cookie + return refresh_token in body

## Phase 5: Control Panel Integration

- [ ] Update `web/frbr.js` `ServiceProxy.refresh()` to POST to `/oauth/token` with `grant_type=refresh_token`
- [ ] Update `web/control/app.js` `api()` to handle 401 → refresh via `/oauth/token` (cookie auto-attaches)
- [ ] Admin UI should use access_token (not id_token) as Bearer after unification

## Phase 6: Tests + Cleanup

- [ ] Add unit tests for seal/open helpers
- [ ] Add tests for mintFreshbreathToken / verifyFreshbreathToken per Kind
- [ ] Add tests for mintRefreshToken / verifyRefreshToken per Kind
- [ ] Add test for handleToken refresh grant
- [ ] Update existing tests that reference old claim structs
- [ ] Verify `isFreshbreathToken` still works with unified claims

## Legend

- 🔒 — Security-sensitive change (review carefully)
- 🍪 — Cookie-related (HttpOnly, Secure, Path, SameSite flags)
- ⚠️ — main.go: 2-space indent, never gofmt
- 🔑 — Encryption (AES-256-GCM, key from localKey)

## Notes

- All three token families now share `/oauth/token` for refresh — no separate `/api/refresh` endpoint
- The `Sealed` field in access tokens is only populated for Kind="wrapped" (virtual services)
- ALL refresh tokens have their sensitive data encrypted (sealed), not just wrapped ones
- The `isFreshbreathToken` helper still works because `iss=freshbreath` remains in the visible JWT payload
- Control panel admin UI uses `id_token` as Bearer today — after unification, the access token IS the Freshbreath JWT (no separate id_token needed)
- MCP clients get refresh_token in JSON body; browser clients get it via HttpOnly cookie
- `/service/refresh` (existing) is for UPSTREAM provider tokens — leave it alone, don't conflate

## Online References

- go-jose/go-jose v4: https://github.com/go-jose/go-jose — JWT signing (HS256) already in use
- AES-256-GCM in stdlib: `crypto/aes` + `crypto/cipher` — prior art in `ssh_keys.go`
