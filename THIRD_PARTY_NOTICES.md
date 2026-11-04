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

All fonts below are licensed under the SIL Open Font License, Version 1.1.
The full licence text is in [`LICENSES/OFL-1.1.txt`](LICENSES/OFL-1.1.txt)
(also shipped with the site as `internal/site/static/OFL.txt`). The files here
are subsets made with fonttools: only the glyphs the pages need are kept. The
full families are available from the upstream projects.

| Font | Files | Copyright | Licence |
|---|---|---|---|
| Instrument Sans (Regular, SemiBold) | `internal/site/static/fonts/instrument-sans.woff2`, `internal/site/static/fonts/instrument-sans-sb.woff2` | Copyright 2022 The Instrument Sans Project Authors (https://github.com/Instrument/instrument-sans) | SIL OFL 1.1 |
| Instrument Sans SemiBold, embedded subset | `docs/assets/one-to-one.svg`, `docs/assets/one-to-many.svg` (base64 WOFF2 inside each SVG, renamed "BB Sans") | Copyright 2022 The Instrument Sans Project Authors (https://github.com/Instrument/instrument-sans) | SIL OFL 1.1 |
| Fraunces | `internal/site/static/fonts/fraunces.woff2` | Copyright 2018 The Fraunces Project Authors (https://github.com/undercasetype/Fraunces) | SIL OFL 1.1 |
| Noto Sans CJK TC (Noto Sans TC), version 2.004 | `internal/site/static/fonts/noto-sans-tc.woff2` | © 2014-2021 Adobe (http://www.adobe.com/). Noto is a trademark of Google Inc. | SIL OFL 1.1 |
| Noto Serif CJK TC (Noto Serif TC), version 2.003 | `internal/site/static/fonts/noto-serif-tc.woff2` | © 2017-2024 Adobe (http://www.adobe.com/). Noto is a trademark of Google Inc. | SIL OFL 1.1 |

Copyright lines are taken from each project's `OFL.txt` (Instrument Sans,
Fraunces) or from the name table of the matching upstream release (Noto CJK,
https://github.com/notofonts/noto-cjk). The Google Fonts copy of the Noto Sans
TC licence also lists the Reserved Font Name "Source"; the subsets here do not
use it, and no Reserved Font Name is used by any file in this repository.
