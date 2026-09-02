#!/usr/bin/env bash
set -euo pipefail

# ── Config from env (set by mise tasks or CI) ──────────────────────
# Required: GOOS, GOARCH, VERSION, COMMIT
# Optional: CC (defaults per platform below)
VERSION=${VERSION:-$GIT_VERSION}
COMMIT=${COMMIT:-$GIT_COMMIT}

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
# -tags sqlite_fts5: full-text search is compiled in, not loaded. Named here
# as well as in mise.toml's GOFLAGS so a release binary can't lose it by
# being built outside mise.
go build -tags sqlite_fts5 -ldflags "-X main.version=$VERSION -X main.commit=$COMMIT" -o "dist/${GOOS}-${arch}-$binary" ./cmd/freshbreath

# ── Package ────────────────────────────────────────────────────────
staging="dist/staging"
mkdir -p "$staging"
mv "dist/${GOOS}-${arch}-$binary" "$staging/$binary"
cp README.md "$staging/README.txt"
cp -r web skills "$staging/"

# Swap the control panel into dist mode: the working tree loads app.js (JSX)
# via in-browser babel for edit-and-refresh development; packaged dists get
# the pre-compiled app and no babel payload. The compiled app stays a module
# script so it executes strictly after /frbr.js (also a module) — a classic
# script would race it and the app would boot before __HOMESLICE_CONFIG/FrBr
# exist, silently skipping the stored admin token.
node scripts/compile-control.mjs
mv dist/app.compiled.js "$staging/web/control/app.compiled.js"
# Note: no `sed -i` here — BSD sed (macOS) requires a backup suffix after -i,
# which would swallow the first -e as its argument. Redirect + mv is portable.
sed \
  -e '/Dev mode:/d' \
  -e '/babel-standalone-7.29.0.min.js/d' \
  -e 's|<script type="text/babel" src="/control/app.js"></script>|<script type="module" src="/control/app.compiled.js"></script>|' \
  "$staging/web/control.html" > "$staging/web/control.html.tmp"
mv "$staging/web/control.html.tmp" "$staging/web/control.html"
grep -q 'app.compiled.js' "$staging/web/control.html" || { echo "✗ control.html swap failed"; exit 1; }
rm -rf "$staging/web/control/app.js" "$staging/web/control/vendor/babel-standalone-7.29.0.min.js"

if [ "$GOOS" = "windows" ]; then
  archive="freshbreath-${VERSION}-${GOOS}-${arch}.zip"
  echo "→ Packaging $archive"
  (cd "$staging" && zip -r "../$archive" .)
else
  if [ "$GOOS" = "darwin" ]; then
    archive="freshbreath-${VERSION}-macos-${arch}.tar.gz"
  else
    archive="freshbreath-${VERSION}-${GOOS}-${arch}.tar.gz"
  fi
  echo "→ Packaging $archive"
  tar -czf "dist/$archive" -C "$staging" .
fi

rm -rf "$staging"
echo "✓ dist/$archive"
