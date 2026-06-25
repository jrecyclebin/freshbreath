# Refresh Token Rotation with Token Families

Replace the current stateless, non-revocable refresh tokens with a **token-family**
model (OAuth 2.0 Security BCP / RFC 9700): one family per device-login, one-time-use
rotation within a family, and reuse detection that revokes the whole family on theft.
Adds multi-device session support (independent families per machine, listable and
individually revocable).

The refresh token stays a **signed JWT carrying sealed upstream data** (so the DB never
holds upstream secrets in plaintext); we add `family_id` + `jti` claims, and the DB
tracks which `jti` is current per family.

## Relevant Files

- `db.go:15` - `Store.Migrate` — add the `refresh_families` table + indexes here.
- `db.go:960-991` - `RegisterOAuthClient`/`GetOAuthClient` — the store-method style to mirror.
- `db_test.go` - Unit tests for store methods (where the Phase 1 tests go).
- `services.go:594` - `mintRefreshToken` — add `family_id`/`jti` to the refresh JWT.
- `services.go:646` - `verifyRefreshToken` — surface `family_id`/`jti` to callers.
- `services.go:497` - `deriveSubkey` + `freshbreathRefreshData` — payload shape.
- `oauth_server.go:519` - `handleRefreshTokenGrant` — rotation policy (happy/grace/reuse) lives here.
- `oauth_server.go:691` - `writeTokenResponse` — already gates body delivery (browser vs CLI).
- `oauth_server_test.go` - OAuth-layer tests (rotation, grace, reuse-revoke).
- `handler.go:546-690` - `completeAuth`/`completeMCP*` — issuance paths that must create a family.
- `main.go:277` - the 60s ticker — hang the expired-family sweep here.
- `web/frbr.js:215` - browser `refresh()` — add single-flight (defense-in-depth; grace window covers it regardless).

## Quality Check

After each task: `go test ./...` (must stay green, output pristine), then `go vet ./...`.
New behavior is TDD — failing test first, confirm red, implement, confirm green.
After each phase: `mise coverage` to confirm the new code is actually exercised.

## Phase 1: Store layer (the foundation)

- [x] Add `refresh_families` table to `Migrate` (+ index on `user_email`, `expires_at`).
- [x] `RefreshFamily` struct mirroring the columns.
- [x] `CreateRefreshFamily(*RefreshFamily) error` — insert on login.
- [x] `GetRefreshFamily(id) (*RefreshFamily, bool, error)`.
- [x] `RotateRefreshFamily(id, fromJTI, toJTI string, now) (ok bool, err error)` — **atomic CAS**:
      `UPDATE ... SET prev_jti=current_jti, current_jti=toJTI, rotated_at=now, last_used_at=now
      WHERE id=? AND current_jti=fromJTI AND revoked=0`; `ok = rowsAffected==1`.
  - [x] Test: concurrent double-rotate from the same `fromJTI` → exactly one `ok=true`.
- [x] `RevokeRefreshFamily(id) error` and `RevokeUserRefreshFamilies(email) error`.
- [x] `ListRefreshFamilies(email) ([]RefreshFamily, error)` — active (non-revoked, unexpired).
- [x] `DeleteExpiredRefreshFamilies(now) (int64, error)` — cleanup sweep.

## Phase 2: Token claims

- [ ] Add `FamilyID`, `JTI` to the refresh-token JWT (mint side).
- [ ] `verifyRefreshToken` returns them alongside `freshbreathRefreshData`.
- [ ] Roundtrip test: mint → verify surfaces the same `family_id`/`jti`.

## Phase 3: Rotation policy in the refresh grant

- [ ] Rewrite `handleRefreshTokenGrant`:
  - [ ] Look up family; missing / revoked / expired → `invalid_grant`.
  - [ ] Enforce `family.ServiceID` matches the token's bound service (binding invariant).
  - [ ] `jti == current_jti` → CAS-rotate to new jti → mint + issue.
  - [ ] `jti == prev_jti` within grace window (≈30s) → idempotent re-issue, **no second rotate**, no revoke.
  - [ ] otherwise (stale jti) → **reuse detected** → revoke family → `invalid_grant`.
- [ ] Tests: happy rotation invalidates old; grace-window retry succeeds; replay outside grace revokes family; revoked family rejects.

## Phase 4: Family creation on login

- [ ] On every fresh issuance (authz_code grant + `completeMCP*` + panel/OIDC/SSH login), create a family and stamp the first `jti` into the issued refresh token.
- [ ] Capture `device_label` (auto from `User-Agent` for v1).
- [ ] Test: a login creates exactly one family; its first refresh rotates within that family.

## Phase 5: Session management surface

- [ ] Store already has list/revoke; expose `get_my_sessions` + `revoke_session` on the central MCP (personal tools) and matching panel API routes.
- [ ] "Revoke all" / logout-everywhere path.
- [ ] Tests: list shows N devices; revoking one leaves the others working.

## Phase 6: Cleanup + migration

- [ ] Hang `DeleteExpiredRefreshFamilies` on the 60s ticker.
- [ ] `frbr.js` single-flight `refresh()` (defense-in-depth).
- [ ] Confirm legacy stateless RTs (no `family_id`) cleanly fail → re-login. Rides the deploy's existing forced re-login (HKDF subkey change), so no legacy-adoption code.

## Legend

- 🔒 Binding invariant: `service_id` stays on the family; identity refresh still re-resolves the user from the DB (role is never trusted from the token).
- ⚠️ Grace window is load-bearing: `frbr.js` is not single-flight today, so naive one-time-use would spuriously log users out. Phase 3's `prev_jti` window is what makes all clients (incl. CLI) safe.

## Notes

- Session lifetime: sliding idle timeout (30d since `last_used_at`) with an absolute cap (90d from `created_at`).
- Device labeling: auto from `User-Agent` for v1; user-rename is a later nicety.
- Keep the signed JWT (not opaque tokens) — smaller delta, preserves "no upstream plaintext server-side."

## Online References

- RFC 9700 (OAuth 2.0 Security Best Current Practice) — refresh token rotation & reuse detection.
