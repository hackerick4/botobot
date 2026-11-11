#!/usr/bin/env bash
# Build the BotoBot site JS bundles with esbuild and refresh the license notice.
#
# CI has no Node, so the *output* under internal/site/static/js/ is committed.
# Run this whenever web/src/*.js changes, then commit the regenerated bundles.
#
#   LIBS=/path/to/node_modules scripts/build-site.sh
#
# Defaults to web/node_modules (gsap@3.15.0, esbuild@0.28.2). See web/README.md.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
LIBS="${LIBS:-$ROOT/web/node_modules}"
OUT="$ROOT/internal/site/static/js"

if [ ! -d "$LIBS" ]; then
  echo "error: node_modules not found at $LIBS (set LIBS=...)" >&2
  exit 1
fi

ESBUILD="$LIBS/.bin/esbuild"
[ -x "$ESBUILD" ] || ESBUILD="npx --prefix $LIBS esbuild"

mkdir -p "$OUT"

common=(--bundle --format=esm --minify --target=es2020 --legal-comments=none)

echo "• main.js (GSAP + ScrollTrigger + orchestration)"
NODE_PATH="$LIBS" $ESBUILD "$ROOT/web/src/main.js" \
  "${common[@]}" \
  --outfile="$OUT/main.js"

# The hero is now a GSAP-driven inline SVG; there is no second (PixiJS) bundle.
# Remove any stale hero.js from a previous build so it is not served/embedded.
rm -f "$OUT/hero.js"

# ---- third-party license notices --------------------------------------------
LIC="$OUT/LICENSES.txt"
{
  echo "Third-party licenses for the JavaScript bundled into this site."
  echo "Bundle: main.js (GSAP + ScrollTrigger)."
  echo
  echo "================================================================"
  echo "GSAP 3.15.0 (GreenSock) — Standard 'No Charge' license"
  echo "================================================================"
  echo
  if [ -f "$LIBS/gsap/LICENSE.md" ]; then
    cat "$LIBS/gsap/LICENSE.md"
  else
    echo "GSAP and its ScrollTrigger plugin are (c) GreenSock, Inc."
    echo "Used under GreenSock's Standard 'No Charge' license."
    echo "Full terms: https://gsap.com/standard-license"
  fi
} > "$LIC"

echo
echo "built:"
for f in "$OUT/main.js"; do
  sz=$(wc -c < "$f")
  gz=$(gzip -c "$f" | wc -c)
  printf "  %-28s %7d B  (%6d B gzip)\n" "$(basename "$f")" "$sz" "$gz"
done
