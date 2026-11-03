# Third-party notices

## IP geolocation: DB-IP Lite (country)

`internal/geo/tw_ranges.txt` is the Taiwan (TW) subset of the DB-IP
"IP to Country Lite" database, 2026-10 edition.

IP Geolocation by DB-IP, https://db-ip.com

Licensed under the Creative Commons Attribution 4.0 International License
(CC BY 4.0), https://creativecommons.org/licenses/by/4.0/. Changes: only the
TW rows are kept and they are written as "start end" lines. The site footer on
/en and /zh carries the attribution link.

It is used only to pick the default language of https://botobot.app/ (English,
or Traditional Chinese for Taiwan). The lookup runs inside the server; no
address is sent anywhere or stored. Regenerate with
`scripts/gen-geo-tw.py dbip-country-lite-YYYY-MM.csv.gz`.

## Fonts

The self-hosted web fonts in `internal/site/static/fonts/` are under the SIL
Open Font License 1.1; see `internal/site/static/OFL.txt`.
