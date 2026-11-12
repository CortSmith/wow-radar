# site/ architecture

Static page, no build step, no dependencies, no external requests. ES modules only. Strict-CSP ready
(`default-src 'self'`): no inline script/style/handlers, text only via `textContent`.

## View it

```
node site/dev-server.mjs          # http://localhost:8080/  demo API + maps from ../../maps
VIEW_KEY=abc node site/dev-server.mjs   # API answers 401 until the key prompt is filled in
node --test site/test/*.test.js   # tests (Windows: C:\Program Files\nodejs\node.exe)
```
In the Go binary: the `static` module (`internal/mod/static`) serves `site/embed.go`'s embedded copy; `--site-dir <dir>`
serves from disk instead.

## Modules

| File | Responsibility |
|---|---|
| `js/config.js` | constants (poll period, overlap, stale/gone times, API base, maps folder); only `?maps=` accepted from the URL |
| `js/messages.js` | every user-visible string |
| `js/core.js` | host: state, bus, `registerFeature`, mounts, sweep orchestration, API client wiring |
| `js/scheduler.js` | 60 s loop: chained timeouts, debounce, abort, backoff (5 s doubling to 5 min), visibility pause |
| `js/client.js` | fetch wrapper, `X-View-Key`, `ApiError`, 401 hook |
| `js/sector.js` | sector selection -> sweep query; sector identity |
| `js/merge.js`, `js/staleness.js` | delta merge by guid, trail merge, live/stale/gone |
| `js/view-model.js`, `js/projection.js`, `js/scene.js`, `js/draw.js` | what is drawn: map choice, filters, geometry, scene, canvas painting |
| `js/format.js`, `js/charts.js`, `js/dom.js` | formatting + time zone, map-image lookup, safe DOM builder |
| `js/features.js` | the one list of features (one dynamic-import line each) |
| `js/features/*.js` | `status`, `view-key`, `ruleset-switch`, `chart-picker`, `group-filter`, `time-zone`, `radar-canvas`, `track-overlay`, `search`, `bounty-panel`, `contact-list`, `history-panel` |
| `js/main.js` | browser boot |
| `dev-demo.mjs`, `dev-server.mjs` | demo backend + static server for development (not embedded) |

Feature contract: `{name, init(ctx), onSweep(result, ctx), destroy()}`. `ctx` exposes `state`, `bus`, `el`, `mount(name)`,
`api`, `setPlayer/deselect/setZone/setRuleset/setFilters`, `refreshNow`, `fmtTime`. Mount points are the `data-mount` attributes of
`index.html`: `header-status header-controls notice radar side-top side-panels side-list below footer`. A feature whose
mount is missing does nothing. To remove a feature delete its file and its line in `features.js`.

## Sector / API map

| Page input | Request |
|---|---|
| nothing selected | `GET /api/v1/sweep?scope=all&since=` |
| zone picked (+Blue/Red) | `scope=zone&mapId=&grp=friendly\|hostile\|any` |
| player picked | `scope=player&guid=&trail=1` and once `GET /api/v1/trail?guid=` |
| ruleset (not Default) | `rs=` on every call |
| search box (250 ms) | `GET /api/v1/contacts?q=&limit=50` |
| bounty panel | `GET /api/v1/bounty?limit=50` each sweep while open |
| zone list / map frames | `GET /api/v1/charts` on start, ruleset change, every 10th sweep |
| zone images (optional) | `maps/index.json` (`{zones:{key:{name,file,w,h,revealed}}}`) + image files, served read-only at `/maps/` from `RADAR_MAPS_DIR` (never embedded or committed); `charts.js mapImageFor` accepts plain `.webp`/`.png` names only; a 404 index leaves the grid and a muted "no map image" note |

`since` is 0 on the first call of a sector and `lastNow - 5000` afterwards. Changing ruleset, scope, guid, map or group
discards the loaded rows and starts again at `since=0`. Stale after 5 min, dropped from the live view after 15 min (the
history table keeps loaded rows). Only user-driven changes trigger an early sweep (300 ms debounce); the 60 s timer
restarts after each sweep.

## View key storage (threat note)

The view key is held in memory only by default, so closing the tab forgets it. The key prompt has an unchecked
"Remember on this device" box; only when ticked is the key written to `localStorage` (`radarLive.viewKey`), where any script
running on this origin could read it (the strict CSP, no inline script and the XSS tests are what keep that script from existing).
Saving again with the box unticked erases a remembered key. No cookies are read or written; the key travels only in the
`X-View-Key` header. Tests: `site/test/boot.test.js` (key prompt tests) and `site/test/csp.test.js` (page parses clean under the CSP).

## Deselecting
`ctx.deselect()` is `setPlayer(null)` when something is selected, so the sector falls back to the zone/all scope, rows reset and
the next sweep (300 ms debounce) restarts at `since=0`. Every path calls it: click the selected list row or blip again, click
empty canvas space, the search `x` button, and Escape (the host listens on the document, emits `escape` so the search box
wipes itself, then deselects).
