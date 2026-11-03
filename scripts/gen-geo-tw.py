#!/usr/bin/env python3
"""Regenerate internal/geo/tw_ranges.txt from the DB-IP Lite country CSV.

Usage: scripts/gen-geo-tw.py dbip-country-lite-YYYY-MM.csv.gz

Source: https://db-ip.com/db/download/ip-to-country-lite (CC BY 4.0,
"IP Geolocation by DB-IP", https://db-ip.com). Only the TW ranges are kept,
merged where adjacent, so the embedded file stays small.
"""
import gzip, ipaddress, os, re, sys

src = sys.argv[1]
m = re.search(r"(\d{4}-\d{2})", os.path.basename(src))
ver = m.group(1) if m else "unknown"
rows = []
with gzip.open(src, "rt") as f:
    for line in f:
        a, b, cc = line.strip().split(",")
        if cc == "TW":
            rows.append((ipaddress.ip_address(a), ipaddress.ip_address(b)))
merged = []
for a, b in sorted(rows, key=lambda r: (r[0].version, int(r[0]))):
    if merged and merged[-1][0].version == a.version and int(merged[-1][1]) + 1 >= int(a):
        if int(b) > int(merged[-1][1]):
            merged[-1] = (merged[-1][0], b)
    else:
        merged.append((a, b))
out = os.path.join(os.path.dirname(__file__), "..", "internal", "geo", "tw_ranges.txt")
with open(out, "w") as f:
    f.write("# TW IP ranges from DB-IP Lite country %s. IP Geolocation by DB-IP (https://db-ip.com), CC BY 4.0.\n" % ver)
    for a, b in merged:
        f.write("%s %s\n" % (a, b))
print("wrote %d ranges (from %d rows)" % (len(merged), len(rows)))
