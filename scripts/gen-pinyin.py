#!/usr/bin/env python3
"""Generate internal/cli/pinyindata/{syllables.txt,table.bin} for handle slugs.

Source: pypinyin 0.53.0 (MIT License, https://github.com/mozillazg/python-pinyin),
file pypinyin/pinyin_dict.json. For each code point in U+4E00..U+9FFF the first
(most common) reading is kept, tone marks removed, ü written as "v".
Usage: python3 scripts/gen-pinyin.py /path/to/pypinyin/pinyin_dict.json
"""
import json, sys, struct, unicodedata, os

LO, HI = 0x4E00, 0x9FFF
src = json.load(open(sys.argv[1], encoding="utf8"))

def plain(p):
    p = p.replace("ü", "v").replace("ǖ", "v").replace("ǘ", "v").replace("ǚ", "v").replace("ǜ", "v")
    p = unicodedata.normalize("NFD", p)
    p = "".join(c for c in p if not unicodedata.combining(c))
    p = p.lower()
    return p if p.isascii() and p.isalpha() else ""

readings = {}
for k, v in src.items():
    cp = int(k)
    if LO <= cp <= HI:
        s = plain(v.split(",")[0])
        if s:
            readings[cp] = s
syl = sorted(set(readings.values()))
idx = {s: i + 1 for i, s in enumerate(syl)}
out = os.path.join(os.path.dirname(__file__), "..", "internal", "cli", "pinyindata")
with open(os.path.join(out, "syllables.txt"), "w") as f:
    f.write("\n".join(syl) + "\n")
with open(os.path.join(out, "table.bin"), "wb") as f:
    for cp in range(LO, HI + 1):
        f.write(struct.pack("<H", idx.get(readings.get(cp), 0)))
print(len(readings), "chars,", len(syl), "syllables")
