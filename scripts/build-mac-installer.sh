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
# plist and a postinstall) into a component pkg, wrap that in an arch-locked
# distribution pkg signed with the Developer ID Installer cert, notarize it
# with Apple, and staple the notarization ticket.
#
# ── Config from env (set by mise tasks or CI) ──────────────────────
# Required: GOOS, GOARCH
# Optional: VERSION (defaults to git describe via GIT_VERSION)
#
# ── Secrets (all required) ─────────────────────────────────────────
#   MACOS_CERTIFICATES_P12       base64 of a .p12 that contains BOTH the
#                                Developer ID Application AND Developer ID
#                                Installer certificates (export both from
#                                Keychain Access into one .p12)
#   MACOS_CERTIFICATES_PASSWORD  that .p12's password
#   APPLE_API_KEY                base64 of an App Store Connect API key (.p8),
#                                used by notarytool — no 2FA, no rotation
#
# ── Identifiers (required; repo variables in CI, not secrets) ───────
#   APPLE_API_KEY_ID             the API key's ID (e.g. ABC123DEF4)
#   APPLE_API_ISSUER             the issuer ID (UUID)
#
# Required tools: Xcode (xcrun notarytool / stapler), security, pkgbuild,
# productbuild, codesign, spctl — all present on a macOS runner with Xcode.
VERSION=${VERSION:-$GIT_VERSION}
PKG_ID="institute.poggers.freshbreath"

if [ "$GOOS" != "darwin" ]; then
  echo "build-mac-installer.sh is macOS-only (got GOOS=$GOOS)" >&2
  exit 1
fi

# arch labels the archive; host_arch is what Installer checks the Mac against.
case "$GOARCH" in
  arm64) arch="arm64"; host_arch="arm64"  ;;
  amd64) arch="x64";   host_arch="x86_64" ;;
  *)     echo "unsupported GOARCH=$GOARCH" >&2; exit 1 ;;
esac

archive="dist/freshbreath-${VERSION}-macos-${arch}.tar.gz"
if [ ! -f "$archive" ]; then
  echo "build-mac-installer.sh: $archive not found." >&2
  echo "  This task depends on 'mise run build:mac' to produce it; run that first." >&2
  exit 1
fi

for var in MACOS_CERTIFICATES_P12 MACOS_CERTIFICATES_PASSWORD \
           APPLE_API_KEY APPLE_API_KEY_ID APPLE_API_ISSUER; do
  if [ -z "${!var:-}" ]; then
    echo "build-mac-installer.sh: $var is not set." >&2
    echo "  Set it in the mise task env or CI secrets/vars (see this script's header)." >&2
    exit 1
  fi
done

# Sensitive temp files and the build keychain are cleaned on any exit, and the
# user's keychain search list is put back as it was — this script reorders it,
# which matters on a dev Mac even if a CI runner is thrown away afterwards.
KEYCHAIN="freshbreath-build.keychain-db"
orig_keychains=()
while IFS= read -r kc; do
  kc="${kc#"${kc%%[![:space:]]*}"}"; kc="${kc//\"/}"
  [ -n "$kc" ] && orig_keychains+=("$kc")
done < <(security list-keychains -d user)
tmp_files=()
cleanup() {
  if [ ${#tmp_files[@]} -gt 0 ]; then rm -rf "${tmp_files[@]}"; fi
  security list-keychains -d user -s "${orig_keychains[@]}" 2>/dev/null || true
  security delete-keychain "$KEYCHAIN" 2>/dev/null || true
}
trap cleanup EXIT

cert="$(mktemp -t frbr)"; tmp_files+=("$cert")
printf '%s' "$MACOS_CERTIFICATES_P12" | base64 -D > "$cert"
key_file="$(mktemp -t frbr)"; tmp_files+=("$key_file")
printf '%s' "$APPLE_API_KEY" | base64 -D > "$key_file"

# ── Stage payload from the portable archive ────────────────────────
staging="dist/pkg-staging"
root="dist/pkg-root"
tmp_files+=("$staging" "$root")
rm -rf "$staging"; mkdir -p "$staging"
tar -xzf "$archive" -C "$staging"

# The pkg installs to /usr/local/freshbreath (binary + resources) and
# /Library/LaunchDaemons (the plist). Build a root tree mirroring those
# absolute paths; pkgbuild --install-location / lays them down at /.
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
# a GUI-less runner — if it fails, the build would hang on that prompt, so
# neither step is allowed to fail quietly.
security delete-keychain "$KEYCHAIN" 2>/dev/null || true
KEYCHAIN_PW="$(uuidgen)"
security create-keychain -p "$KEYCHAIN_PW" "$KEYCHAIN"
security set-keychain-settings -lut 21600 "$KEYCHAIN"
security unlock-keychain -p "$KEYCHAIN_PW" "$KEYCHAIN"
# Prepend ours to the search list so codesign / productbuild resolve our
# identities (and their intermediate certs). cleanup restores the original.
security list-keychains -d user -s "$KEYCHAIN" "${orig_keychains[@]}"
security import "$cert" -k "$KEYCHAIN" -P "$MACOS_CERTIFICATES_PASSWORD" \
  -T /usr/bin/codesign -T /usr/bin/productbuild -T /usr/bin/pkgbuild
security set-key-partition-list \
  -S apple-tool:,apple:,codesign:,productbuild:,pkgbuild: \
  -s -k "$KEYCHAIN_PW" "$KEYCHAIN" >/dev/null

# Resolve the two identities by name. A .p12 carrying both certs yields one
# "Developer ID Application: …" (for the binary) and one "Developer ID
# Installer: …" (for the package).
app_identity=$(security find-identity -p codesigning -v "$KEYCHAIN" \
  | grep -m1 'Developer ID Application' | sed 's/.*"\(.*\)".*/\1/' || true)
installer_identity=$(security find-identity -v "$KEYCHAIN" \
  | grep -m1 'Developer ID Installer' | sed 's/.*"\(.*\)".*/\1/' || true)
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

# ── Build the component pkg ─────────────────────────────────────────
# The identifier is constant across versions — that receipt is what makes a
# newer pkg upgrade an older one in place. --version must be bare numeric:
# drop the leading v and any pre-release/build suffix (v1.2.0-rc1 → 1.2.0) so
# upgrade ordering still holds; only a non-tag describe (a bare sha, "dev")
# falls back to 0.0.0.
PKG_VERSION="${VERSION#v}"
PKG_VERSION="${PKG_VERSION%%[-+]*}"
if ! printf '%s' "$PKG_VERSION" | grep -qE '^[0-9]+(\.[0-9]+)*$'; then
  echo "→ version '$VERSION' isn't a clean pkg version; coercing to 0.0.0" >&2
  PKG_VERSION="0.0.0"
fi

# Flat packages run postinstall on both fresh installs and upgrades.
scripts_dir="$(mktemp -d -t frbr)"; tmp_files+=("$scripts_dir")
cp scripts/macos/postinstall "$scripts_dir/postinstall"
chmod 0755 "$scripts_dir/postinstall"

component="dist/freshbreath-${VERSION}-component.pkg"
tmp_files+=("$component")
pkgbuild \
  --root "$root" \
  --identifier "$PKG_ID" \
  --version "$PKG_VERSION" \
  --scripts "$scripts_dir" \
  --install-location / \
  "$component"

# ── Wrap in a distribution pkg, arch-locked and signed ──────────────
# hostArchitectures makes Installer refuse a Mac of the wrong arch up front,
# instead of laying down a binary that launchd would crash-loop forever.
# productbuild signs with a trusted timestamp, which notarization requires.
dist_xml="$(mktemp -t frbr)"; tmp_files+=("$dist_xml")
cat > "$dist_xml" <<EOF
<?xml version="1.0" encoding="utf-8"?>
<installer-gui-script minSpecVersion="2">
  <title>Fresh Breath</title>
  <options hostArchitectures="$host_arch" customize="never" require-scripts="false"/>
  <choices-outline>
    <line choice="default"><line choice="$PKG_ID"/></line>
  </choices-outline>
  <choice id="default"/>
  <choice id="$PKG_ID" visible="false"><pkg-ref id="$PKG_ID"/></choice>
  <pkg-ref id="$PKG_ID" version="$PKG_VERSION" onConclusion="none">$(basename "$component")</pkg-ref>
</installer-gui-script>
EOF

out="dist/freshbreath-${VERSION}-macos-${arch}-setup.pkg"
echo "→ productbuild + sign as: $installer_identity"
productbuild --distribution "$dist_xml" --package-path dist \
  --sign "$installer_identity" "$out"

# ── Notarize + staple ───────────────────────────────────────────────
echo "→ notarytool submit (can take a few minutes)…"
xcrun notarytool submit "$out" \
  --key "$key_file" --key-id "$APPLE_API_KEY_ID" --issuer "$APPLE_API_ISSUER" \
  --wait

echo "→ stapler staple"
xcrun stapler staple "$out"
xcrun stapler validate "$out"
spctl -a -t install -v "$out"

echo "✓ $out"
