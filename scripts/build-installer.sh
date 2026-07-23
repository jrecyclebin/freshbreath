#!/usr/bin/env bash
set -euo pipefail

# Packages the Windows service installer (.exe) for freshbreath.
#
# This task does NOT build freshbreath.exe itself — it depends on the matching
# build:windows* task, which cross-compiles the binary and emits the portable
# archive at dist/freshbreath-<version>-windows-<arch>.zip. That zip already
# contains freshbreath.exe, README.txt, web/ and skills/ — the exact payload a
# portable install ships — so the service runs byte-for-byte what the archive
# does. Here we just unzip it, drop in the vendored NSSM, and let makensis
# wrap the whole thing into a setup.exe.
#
# ── Config from env (set by mise tasks or CI) ──────────────────────
# Required: GOOS, GOARCH
# Optional: VERSION (defaults to git describe via GIT_VERSION)
# Required tools: makensis (NSIS); an extractor (unzip, else Python).
VERSION=${VERSION:-$GIT_VERSION}

if [ "$GOOS" != "windows" ]; then
  echo "build-installer.sh is Windows-only (got GOOS=$GOOS)" >&2
  exit 1
fi

case "$GOARCH" in
  amd64) arch="x64";   nssm_arch=${NSSM_ARCH:-win64} ;;
  arm64) arch="arm64"; nssm_arch=${NSSM_ARCH:-win32} ;;  # NSSM has no arm64 build; the win32 build runs under Windows-on-ARM's x86 emulation
  *)     echo "unsupported GOARCH=$GOARCH" >&2; exit 1 ;;
esac

zip="dist/freshbreath-${VERSION}-windows-${arch}.zip"
if [ ! -f "$zip" ]; then
  echo "build-installer.sh: $zip not found." >&2
  echo "  This task depends on 'mise run build:windows' to produce it; run that first." >&2
  exit 1
fi

# Extract the portable archive into staging. Prefer Info-ZIP unzip (faster,
# no Python dep); fall back to the stdlib zipfile module, which every CI
# runner and dev box has a Python for anyway.
extract_archive() {
  local archive="$1" dest="$2"
  if command -v unzip >/dev/null 2>&1; then
    unzip -o -q "$archive" -d "$dest"
  elif command -v python3 >/dev/null 2>&1; then
    python3 -m zipfile -e "$archive" "$dest/"
  elif command -v python >/dev/null 2>&1; then
    python -m zipfile -e "$archive" "$dest/"
  else
    echo "build-installer.sh: need 'unzip' or Python to extract $archive" >&2
    return 1
  fi
}

# ── Stage payload: unzip the build task's archive, then add NSSM ────
staging="dist/nsis-staging"
rm -rf "$staging"
mkdir -p "$staging"
extract_archive "$zip" "$staging"
if [ -d data ]; then
  cp -r data "$staging/data"
else
  mkdir -p "$staging/data"
fi

# NSSM, vendored under scripts/vendor/ (see scripts/vendor/nssm/README.txt).
cp "scripts/vendor/nssm/$nssm_arch/nssm.exe" "$staging/"

# ── Package installer ────────────────────────────────────────────────
archive="freshbreath-${VERSION}-windows-${arch}-setup.exe"
echo "→ Packaging $archive (from $zip)"
makensis -DVERSION="$VERSION" -DARCH="$arch" -DOUTFILE="..\\dist\\$archive" scripts/installer.nsi

rm -rf "$staging"
echo "✓ dist/$archive"
