# Stage 6: the web page (`site/`) and `internal/mod/static`

Read `../../PLAN.md` (API contract section 4 is binding) first. Reference look: `../../../go-radar/web/{index.html,style.css,app.js}` and
`../../../radar-cloud/site/*` (reuse ideas, canvas drawing, blip colours, search UI, bounty panel). Work only in `site/`,
`internal/mod/static`, and `site/test/`. Do not touch other dirs. (Stage 1 provides `internal/core`; use the real `Routes` socket.)

## Goal
A static HTML + JavaScript page that looks like the Go app (same dark theme tokens `--bg:#0e1117 --panel:#161b24 --friendly:#3b82f6
--hostile:#ef4444 --stale:#4b5563`, header controls, radar canvas, 300 px side panel with search, bounty, list, history table, footer)
and **pulls from the database server every 60 seconds** based on the user's parameters. Easy to host: no build step, no
dependencies, no external requests; works from the Go binary or any static file host.

## Behaviour
- **Sector selection** drives each sweep: searching/selecting a player -> `scope=player&guid=`; picking a map (zone) ->
  `scope=zone&mapId=&grp=` (Blue/Red checkboxes -> friendly/hostile/any); otherwise `scope=all`. Ruleset switch (Normal/PvP/RP/RP-PvP;
  there is no "Auto" detection on the page, add `Default` meaning server default) sends `rs`.
- **Sweep loop**: first load `since=0`; then every 60 s (`SWEEP_INTERVAL_MS`, configurable in `js/config.js`) with
  `since = lastNow - 5000`; merge deltas by guid; a changed sector or user input triggers an immediate sweep (debounced), then the
  60 s timer restarts; pauses while the tab is hidden and sweeps immediately on return; exponential backoff on errors (max 5 min);
  never overlaps requests (chained `setTimeout`, abort on sector change). Connection dot (ok / polling / error), "next update in Ns"
  countdown, "Update now" button.
- Stale after 5 min (dark grey), removed after 15 min from the live view (history panel/search still find them). Pips: friendly blue,
  hostile/neutral red, stale grey. Radar canvas: zone chart image (from `maps/index.json`, optional), fixed 0-100 frame, range slider,
  pan/zoom, tooltip, track drawn dotted for a selected player (`trail=1`, plus `GET /api/v1/trail`).
- Search box (250 ms debounce) -> `GET /api/v1/contacts?q=`; Bounty panel -> `GET /api/v1/bounty`; History table from loaded rows.
  Time zone chooser: display only (times are UTC epoch ms), default browser zone.
- View key: if the server answers 401, show a small key prompt; store the key in `localStorage` (wrapped in try/catch).
- Safe DOM: never `innerHTML` with data, `textContent` only. Strings in one `js/messages.js`.

## Modularity ("socketed" page)
`js/core.js` is the host: state object, event bus, `registerFeature({name, init(ctx), onSweep(result), destroy()})`, a panel registry
(features add themselves into named mount points in `index.html`), and a fetch client wrapper. Features are separate ES-module files
(`radar-canvas`, `search`, `bounty-panel`, `history-panel`, `ruleset-switch`, `chart-picker`, `time-zone`, `track-overlay`,
`ads` optional) imported only by `js/features.js` (the single list). Removing any feature file + its line in `features.js` must leave the
page working (a test boots the page with each feature removed in turn).

## Tests (`node --test`, no npm)
Pure logic modules (sweep scheduler with fake timers/clock, delta merge, sector builder, staleness, projection math, formatting, time zone
display) and a fake-DOM boot test against a stub fetch. Include a Go test in `internal/mod/static` using `httptest` that the embedded
page is served at `/`, assets have correct content types and caching headers, and `/api/*` is not shadowed.

## Deliverables
`site/index.html`, `site/style.css`, `site/js/*.js`, `site/test/*.test.js`, `site/ARCHITECTURE.md` (module table + sector/API map),
`internal/mod/static` (`go:embed`, module `static`, config `--site-dir` override to serve from disk for development),
a `site/dev-server.mjs` (tiny node static server + stub API with demo data, so the page can be viewed before the Go servers exist).
Maps: reuse `../../../maps/index.json` format; do not copy the 88 MB PNGs; the page treats charts as optional and configurable by URL.

## Done when
`node --test site/test/*.test.js` green, Go test green, Results section appended here, STATUS.md line added.
