# web/ — site JavaScript sources

The public site's JS is authored here and bundled into
`internal/site/static/js/` with [esbuild](https://esbuild.github.io/). **The
built output is committed** because CI (and the Go build) has no Node toolchain;
`internal/site/static/*` is embedded into the server binary via `go:embed`.

## Sources

- `src/main.js` — the whole enhancement layer: copy-to-clipboard (+ toast),
  scroll reveals, the scroll-scrubbed "story" connector, the looping privacy
  animation, the sticky CTA reveals, and the **hero explainer** — a GSAP
  timeline that drives an inline SVG (6 captioned steps on a two-computers-plus-cloud scene, clickable step dots,
  play/pause/replay). Bundles **GSAP + ScrollTrigger**. There is no second
  bundle: the hero needs no WebGL/canvas and no `unsafe-eval`.

## Rebuild

Install the pinned libraries under `web/node_modules` (not committed here):

| package  | version |
|----------|---------|
| esbuild  | 0.28.2  |
| gsap     | 3.15.0  |

```sh
# from the repo root, after `cd web && npm install` (default LIBS=web/node_modules):
scripts/build-site.sh

# or point at your own install (must match the versions above):
LIBS=/abs/path/to/node_modules scripts/build-site.sh
```

To install those versions yourself (do **not** commit `node_modules`):

```sh
cd web && npm install   # uses the pinned versions in package.json
LIBS="$PWD/node_modules" ../scripts/build-site.sh
```

The script emits `internal/site/static/js/main.js` and refreshes
`internal/site/static/js/LICENSES.txt` (GSAP notice). Commit those.

## Constraints

- Strict CSP: `script-src 'self'` only — no inline scripts, no `unsafe-eval`.
  Don't introduce `new Function` or `eval`.
- The page is fully usable with no JS: every illustration has a static inline
  SVG, the hero falls back to a 6-panel storyboard, and copy text is
  selectable. Keep it that way.
- Budget: total JS ≤ ~350 KB is the aim; GSAP + ScrollTrigger is the only
  dependency, so the single bundle stays well under that.
