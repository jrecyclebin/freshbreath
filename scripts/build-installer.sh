#!/usr/bin/env bash
set -euo pipefail

# Builds freshbreath.exe, bundles it with NSSM, and runs makensis to produce
# a Windows installer that registers freshbreath as an auto-start service.
#
# ── Config from env (set by mise tasks or CI) ──────────────────────
# Required: GOOS=windows, GOARCH, VERSION, COMMIT
# Optional: CC, NSSM_ARCH (win64|win32, defaults per GOARCH below - NSSM has
#           no native arm64 build, so arm64 targets use the win32 build,
#           which runs fine under Windows-on-ARM's x86 emulation)
VERSION=${VERSION:-$GIT_VERSION}
COMMIT=${COMMIT:-$GIT_COMMIT}

if [ "$GOOS" != "windows" ]; then
  echo "build-installer.sh is Windows-only (got GOOS=$GOOS)" >&2
  exit 1
fi

case "$GOARCH" in
  amd64) arch="x64";  nssm_arch=${NSSM_ARCH:-win64} ;;
  arm64) arch="arm64"; nssm_arch=${NSSM_ARCH:-win32} ;;
  *)     echo "unsupported GOARCH=$GOARCH" >&2; exit 1 ;;
esac

# ── Build ──────────────────────────────────────────────────────────
echo "→ Building freshbreath.exe (GOOS=$GOOS GOARCH=$GOARCH CC=${CC:-default})"
go build -ldflags "-X main.version=$VERSION -X main.commit=$COMMIT" -o "dist/freshbreath.exe" ./cmd/freshbreath

# ── Stage payload ────────────────────────────────────────────────────
staging="dist/nsis-staging"
rm -rf "$staging"
mkdir -p "$staging"
mv "dist/freshbreath.exe" "$staging/"
cp README.md "$staging/README.txt"
cp -r web skills "$staging/"

# ── NSSM ──────────────────────────────────────────────────────────────
# Vendored under scripts/vendor/ rather than fetched at build time - see
# scripts/vendor/nssm/README.txt for why.
cp "scripts/vendor/nssm/$nssm_arch/nssm.exe" "$staging/"

# ── Package installer ────────────────────────────────────────────────
archive="freshbreath-${VERSION}-windows-${arch}-setup.exe"
echo "→ Packaging $archive"
makensis -DVERSION="$VERSION" -DARCH="$arch" -DOUTFILE="..\\dist\\$archive" scripts/installer.nsi

rm -rf "$staging"
echo "✓ dist/$archive"
