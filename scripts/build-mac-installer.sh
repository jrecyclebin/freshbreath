#!/usr/bin/env bash
set -euo pipefail

# Packages the macOS .pkg installer for freshbreath — a signed, notarized,
# stapled package that installs the server as a boot-time launchd daemon
# (the macOS peer of the Windows NSSM service) and is upgradable: one package
# identifier is used across versions, so installing a newer build over an
# older one upgrades in place, preserving the data directory.
#
# This task does NOT build the freshbreath binary — it depends on the matching
# build:mac task, which emits the portable archive at
# dist/freshbreath-<version>-macos-<arch>.tar.gz. That tarball already contains
# the freshbreath binary, README.txt, web/ and skills/ — the exact payload a
# portable install ships — so the service runs byte-for-byte what the archive
# does. Here we untar it, codesign the binary, wrap the payload (plus a launchd
# plist and a postinstall) into a component pkg, sign the pkg with the Developer
# ID Installer cert, notarize it with Apple, and staple the notarization ticket.
#
# ── Config from env (set by mise tasks or CI) ──────────────────────
# Required: GOOS, GOARCH
# Optional: VERSION (defaults to git describe via GIT_VERSION)
#
# ── Secrets ────────────────────────────────────────────────────────
# Signing (always required):
#   MACOS_CERTIFICATES_P12          base64 of a .p12 that contains BOTH the
#                                   Developer ID Application AND Developer ID
#                                   Installer certificates (export both from
#                                   Keychain Access into one .p12)
#   MACOS_CERTIFICATES_PASSWORD      that .p12's password
#
# Notarization (choose ONE set):
#   App Store Connect API key (recommended for CI — no 2FA / password rotation):
#     APPLE_API_KEY        base64 of the .p8 key file
#     APPLE_API_KEY_ID     the key's ID (e.g. ABC123DEF4)
#     APPLE_API_ISSUER     the issuer ID (UUID)
#   OR Apple ID + app-specific password:
#     APPLE_ID                      Apple ID email
#     APPLE_APP_SPECIFIC_PASSWORD   app-specific password (appleid.apple.com)
#     APPLE_TEAM_ID                 Developer Team ID (10-char)
#
# Required tools: Xcode (xcrun notarytool / stapler), security, pkgbuild,
# productbuild, codesign, spctl — all present on a macOS runner with Xcode.
VERSION=${VERSION:-$GIT_VERSION}

if [ "$GOOS" != "darwin" ]; then
  echo "build-mac-installer.sh is macOS-only (got GOOS=$GOOS)" >&2
  exit 1
fi

case "$GOARCH" in
  arm64) arch="arm64" ;;
  amd64) arch="x64"   ;;
  *)     echo "unsupported GOARCH=$GOARCH" >&2; exit 1 ;;
esac

archive="dist/freshbreath-${VERSION}-macos-${arch}.tar.gz"
if [ ! -f "$archive" ]; then
  echo "build-mac-installer.sh: $archive not found." >&2
  echo "  This task depends on 'mise run build:mac' to produce it; run that first." >&2
  exit 1
fi

# ── Required secrets: signing ──────────────────────────────────────
for var in MACOS_CERTIFICATES_P12 MACOS_CERTIFICATES_PASSWORD; do
  if [ -z "${!var:-}" ]; then
    echo "build-mac-installer.sh: $var is not set — signing needs it." >&2
    echo "  Set it in the mise task env or CI secrets (see this script's header)." >&2
    exit 1
  fi
done

# ── Notarization auth: API key preferred, else Apple ID ─────────────
# Sensitive temp files are cleaned on any exit (including the early bail-outs
# below) so a p12 / API key never lingers on the runner.
KEYCHAIN="freshbreath-build.keychain-db"
tmp_files=()
cleanup() {
  if [ ${#tmp_files[@]} -gt 0 ]; then rm -rf "${tmp_files[@]}"; fi
  security delete-keychain "$KEYCHAIN" 2>/dev/null || true
}
trap cleanup EXIT

cert="$(mktemp -t frbr)"; tmp_files+=("$cert")
printf '%s' "$MACOS_CERTIFICATES_P12" | base64 -D > "$cert"

notary_args=()
if [ -n "${APPLE_API_KEY:-}" ] && [ -n "${APPLE_API_KEY_ID:-}" ] && [ -n "${APPLE_API_ISSUER:-}" ]; then
  key_file="$(mktemp -t frbr)"; tmp_files+=("$key_file")
  printf '%s' "$APPLE_API_KEY" | base64 -D > "$key_file"
  notary_args=(--key "$key_file" --key-id "$APPLE_API_KEY_ID" --issuer "$APPLE_API_ISSUER")
elif [ -n "${APPLE_ID:-}" ] && [ -n "${APPLE_APP_SPECIFIC_PASSWORD:-}" ] && [ -n "${APPLE_TEAM_ID:-}" ]; then
  notary_args=(--apple-id "$APPLE_ID" --password "$APPLE_APP_SPECIFIC_PASSWORD" --team-id "$APPLE_TEAM_ID")
else
  echo "build-mac-installer.sh: notarization needs either" >&2
  echo "  (App Store Connect API key) APPLE_API_KEY + APPLE_API_KEY_ID + APPLE_API_ISSUER, or" >&2
  echo "  (Apple ID) APPLE_ID + APPLE_APP_SPECIFIC_PASSWORD + APPLE_TEAM_ID" >&2
  exit 1
fi

# ── Stage payload from the portable archive ────────────────────────
staging="dist/pkg-staging"
rm -rf "$staging"; mkdir -p "$staging"
tar -xzf "$archive" -C "$staging"

# The pkg installs to /usr/local/freshbreath (binary + resources) and
# /Library/LaunchDaemons (the plist). Build a root tree mirroring those
# absolute paths; pkgbuild --install-location / lays them down at /.
root="dist/pkg-root"
rm -rf "$root"
mkdir -p "$root/usr/local/freshbreath" "$root/Library/LaunchDaemons"

cp "$staging/freshbreath" "$root/usr/local/freshbreath/freshbreath"
cp "$staging/README.txt"  "$root/usr/local/freshbreath/README.txt"
cp -R "$staging/web"      "$root/usr/local/freshbreath/web"
cp -R "$staging/skills"   "$root/usr/local/freshbreath/skills"
chmod 0755 "$root/usr/local/freshbreath/freshbreath"

cp scripts/macos/institute.poggers.freshbreath.plist \
   "$root/Library/LaunchDaemons/institute.poggers.freshbreath.plist"
chmod 0644 "$root/Library/LaunchDaemons/institute.poggers.freshbreath.plist"

cp scripts/macos/uninstall.sh "$root/usr/local/freshbreath/uninstall.sh"
chmod 0755 "$root/usr/local/freshbreath/uninstall.sh"

# ── Import signing identities into a throwaway keychain ─────────────
# CI runners have no login keychain carrying our certs, so make one, import
# the .p12, and let the signing tools use it headless. set-key-partition-list
# is the part that stops codesign / productbuild prompting for permission on
# a GUI-less runner (the prompt would hang the build forever).
security delete-keychain "$KEYCHAIN" 2>/dev/null || true
KEYCHAIN_PW="$(uuidgen)"
security create-keychain -p "$KEYCHAIN_PW" "$KEYCHAIN"
security set-keychain-settings -lut 21600 "$KEYCHAIN"
# Prepend ours to the user search list (keeping the runner's existing keychains)
# so codesign / productbuild / find-identity resolve our identities.
security list-keychains -d user -s "$KEYCHAIN" $(security list-keychains -d user | tr -d '"')
security default-keychain -s "$KEYCHAIN"
security import "$cert" -k "$KEYCHAIN" -P "$MACOS_CERTIFICATES_PASSWORD" \
  -T /usr/bin/codesign -T /usr/bin/productbuild -T /usr/bin/pkgbuild 2>/dev/null || true
security set-key-partition-list \
  -S apple-tool:,apple:,codesign:,productbuild:,pkgbuild: \
  -s -k "$KEYCHAIN_PW" "$KEYCHAIN" 2>/dev/null || true

# Resolve the two identities by name. A .p12 carrying both certs yields one
# "Developer ID Application: …" (for the binary) and one "Developer ID
# Installer: …" (for the package).
app_identity=$(security find-identity -p codesigning -v "$KEYCHAIN" \
  | grep -m1 'Developer ID Application' | sed 's/.*"\(.*\)".*/\1/')
installer_identity=$(security find-identity -v "$KEYCHAIN" \
  | grep -m1 'Developer ID Installer' | sed 's/.*"\(.*\)".*/\1/')
if [ -z "$app_identity" ] || [ -z "$installer_identity" ]; then
  echo "build-mac-installer.sh: could not find both signing identities." >&2
  echo "  The .p12 (MACOS_CERTIFICATES_P12) must contain a Developer ID Application" >&2
  echo "  AND a Developer ID Installer certificate. Identities found:" >&2
  security find-identity -v "$KEYCHAIN" >&2
  exit 1
fi

# ── Codesign the binary (hardened runtime + secure timestamp) ──────
# Notarization requires every nested executable to carry a valid signature
# with a secure timestamp and the hardened runtime (-o runtime).
echo "→ codesign (binary) as: $app_identity"
codesign --force --options runtime --timestamp --sign "$app_identity" \
  "$root/usr/local/freshbreath/freshbreath"
codesign -vvv "$root/usr/local/freshbreath/freshbreath"

# ── Build + sign the component pkg ──────────────────────────────────
# --identifier is constant across versions — that receipt is what makes a
# newer pkg upgrade an older one in place. --version must be bare numeric
# (no leading v); the archive keeps the v, the pkg metadata drops it.
PKG_VERSION="${VERSION#v}"
if ! printf '%s' "$PKG_VERSION" | grep -qE '^[0-9]+(\.[0-9]+)*$'; then
  echo "→ version '$PKG_VERSION' isn't a clean pkg version; coercing to 0.0.0" >&2
  PKG_VERSION="0.0.0"
fi

# postinstall runs on a fresh install; postupgrade on an upgrade. They share
# one body (idempotent either way), shipped under both names so the installer
# runs it exactly once regardless of which path it takes.
scripts_dir="$(mktemp -d -t frbr)"; tmp_files+=("$scripts_dir")
cp scripts/macos/postinstall "$scripts_dir/postinstall"
cp scripts/macos/postinstall "$scripts_dir/postupgrade"
chmod 0755 "$scripts_dir/postinstall" "$scripts_dir/postupgrade"

component="dist/freshbreath-${VERSION}-component.pkg"
pkgbuild \
  --root "$root" \
  --identifier "institute.poggers.freshbreath" \
  --version "$PKG_VERSION" \
  --scripts "$scripts_dir" \
  --install-location / \
  "$component"

out="dist/freshbreath-${VERSION}-macos-${arch}-setup.pkg"
echo "→ productbuild + sign as: $installer_identity"
# productbuild wraps the component pkg into a distribution package and signs
# it with the Developer ID Installer identity, attaching a trusted timestamp
# (which notarization requires). pkgbuild's own --sign is murkier about
# timestamping, so sign via productbuild rather than on the component.
productbuild --package "$component" --sign "$installer_identity" "$out"
rm -f "$component"

# ── Notarize + staple ───────────────────────────────────────────────
echo "→ notarytool submit (can take a few minutes)…"
xcrun notarytool submit "$out" "${notary_args[@]}" --wait

echo "→ stapler staple"
xcrun stapler staple "$out"
xcrun stapler validate "$out"
spctl -a -t install -v "$out"

# ── Tidy up ─────────────────────────────────────────────────────────
rm -rf "$staging" "$root" "$scripts_dir"
echo "✓ dist/$out"
