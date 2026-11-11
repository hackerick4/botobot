#!/usr/bin/env bash
# Rebuild or check the Traditional Chinese font subset the site serves
# (internal/site/static/fonts/noto-sans-tc.woff2, family "Noto Sans TC Sub").
# Every CJK character on /zh and /en must be in the subset, or browsers fall
# back to a system font for that character (mixed shapes). Run after changing
# site text; CI has no fontTools, so this is a manual step.
#
#   scripts/font-subset.sh          # rebuild from Noto Sans CJK TC Regular
#   scripts/font-subset.sh --check  # list characters missing from the subset
#
# Needs python3 with fonttools + brotli (e.g. a venv: pip install fonttools brotli)
# and /usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc (face 3 = TC). OFL.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
S="$ROOT/internal/site/static"
PY="${PYTHON:-python3}"
SRC="${SRC:-/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc}"
OUT="$S/fonts/noto-sans-tc.woff2"
U="$("$PY" - "$S" "$OUT" "${1:-}" <<'PYEOF'
import glob, sys
from fontTools.ttLib import TTFont
s, out, mode = sys.argv[1], sys.argv[2], sys.argv[3]
have = set(TTFont(out).getBestCmap())
txt = "".join(open(f, encoding="utf-8").read() for f in [s + "/index.html", s + "/en.html"] + sorted(glob.glob(s + "/js/*.js")))
need = {ord(c) for c in txt if ord(c) >= 0x2E80 and ord(c) <= 0xFFFF or ord(c) in (0x2014, 0x2015, 0x2026)}
miss = sorted(need - have)
if mode == "--check":
    print("missing %d: %s" % (len(miss), "".join(map(chr, miss))), file=sys.stderr)
    sys.exit(1 if miss else 0)
print(",".join("U+%04X" % x for x in sorted(have | need)))
PYEOF
)"
[ "${1:-}" = "--check" ] && exit 0
"$PY" -m fontTools.subset "$SRC" --font-number=3 --unicodes="$U" --flavor=woff2 \
  --layout-features='ccmp,locl,vert,vrt2' --no-hinting --desubroutinize --output-file="$OUT"
echo "wrote $OUT ($(wc -c <"$OUT") bytes)"
