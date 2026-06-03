#!/usr/bin/env bash
set -euo pipefail

# ── Config from env (set by mise tasks or CI) ──────────────────────
# Required: GOOS, GOARCH, VERSION, COMMIT
# Optional: CC (defaults per platform below)

# ── Derive architecture label ──────────────────────────────────────
case "$GOARCH" in
  amd64) arch="x64" ;;
  arm64) arch="arm64" ;;
  *)     arch="$GOARCH" ;;
esac

# ── Derive binary name ────────────────────────────────────────────
if [ "$GOOS" = "windows" ]; then
  binary="freshbreath.exe"
else
  binary="freshbreath"
fi

# ── Build ──────────────────────────────────────────────────────────
echo "→ Building $binary (GOOS=$GOOS GOARCH=$GOARCH CC=${CC:-default})"
go build -ldflags "-X main.version=$VERSION -X main.commit=$COMMIT" -o "dist/${GOOS}-${arch}-$binary" .

# ── Package ────────────────────────────────────────────────────────
staging="dist/staging"
mkdir -p "$staging"
mv "dist/${GOOS}-${arch}-$binary" "$staging/$binary"
cp -r web skills "$staging/"

if [ "$GOOS" = "windows" ]; then
  archive="freshbreath-${GOOS}-${arch}.zip"
  echo "→ Packaging $archive"
  (cd "$staging" && zip -r "../$archive" .)
else
  if [ "$GOOS" = "darwin" ]; then
    archive="freshbreath-macos-${arch}.tar.gz"
  else
    archive="freshbreath-${GOOS}-${arch}.tar.gz"
  fi
  echo "→ Packaging $archive"
  tar -czf "dist/$archive" -C "$staging" .
fi

rm -rf "$staging"
echo "✓ dist/$archive"
