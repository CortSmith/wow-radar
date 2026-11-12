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
| `js/trackcolors.js` | stable per-guid track colour (FNV-1a hash of guid mod 8, colour-blind-safe palette; `--track-1..8` in `style.css`) |
| `js/tooltip.js` | pure `tooltipModel(target, ctx)` → row strings, for a live pip or a historical trail point; `hoverKeyFor`/`resolveHoverPoint` re-locate a hovered point across scene rebuilds |
| `js/worldpath.js` | pure `worldPath(trail)` → chronological per-zone "visits" (consecutive same-mapId fixes collapsed), used by the world map's cross-zone lines |
| `js/worldcounts.js` | pure `zoneCounts`/`chipRadius`/`chipTooltipModel` for the world map's friendly/hostile chips |
| `js/features.js` | the one list of features (one dynamic-import line each) |
| `js/features/*.js` | `status`, `view-key`, `ruleset-switch`, `chart-picker`, `group-filter`, `time-zone`, `radar-canvas`, `track-overlay`, `search`, `bounty-panel`, `contact-list`, `history-panel`, `world-map` |
| `js/main.js` | browser boot |
| `dev-demo.mjs`, `dev-server.mjs` | demo backend + static server for development (not embedded) |

Feature contract: `{name, init(ctx), onSweep(result, ctx), destroy()}`. `ctx` exposes `state`, `bus`, `el`, `mount(name)`,
`api`, `addPlayer/removePlayer/clearPlayers/focusPlayer/setPlayer/deselect/setZone/setRuleset/setFilters/setWorldView`,
`refreshNow`, `fmtTime`. Mount points are the `data-mount` attributes of `index.html`: `header-status header-controls notice radar
world side-top side-panels side-list below footer`. A feature whose mount is missing does nothing. To remove a feature delete its
file and its line in `features.js`.

## Multi-select / tracking (stage 11)
`state.tracked` is an ordered `Map<guid,{guid,name}>` (cap `TRACK_MAX`=8 in `config.js`; oldest evicted past the cap).
`state.player` is a **derived, read-only getter** = the focused (most recently added/clicked) tracked player, so every older
feature that reads `state.player` is unaffected. `ctx.addPlayer/removePlayer/clearPlayers/focusPlayer` manage the set;
`setPlayer`/`deselect` stay as back-compat single-select wrappers. The search box (`features/search.js`) renders one chip per
tracked player (coloured via `trackcolors.js`), not a single filled box. `contact-list.js` shows the full roster always; tracked
rows sort first and get an `x`, everyone else gets `.dim` (never hidden, unless "Show only tracked" is checked) plus a
non-colour `title`/`aria-label` cue. `radar-canvas.js`/`draw.js` fade non-tracked dots (`UNTRACKED_ALPHA`) and ring tracked ones in
their track colour. The sweep is two independent streams per cycle (`core.js`): the roster sweep (`sector.js` `rosterSectorOf`,
never invalidated by tracking changes) always fills `state.contacts`; a second, per-guid `scope=player&trail=1` sweep for every
tracked guid fills `state.trackedData: Map<guid,{contact,trail}>` without touching the roster. Hover tooltips
(`radar-canvas.js`+`tooltip.js`) work on both live pips and historical trail points via `hitTestScene` (pips win ties); a trail
point's name/zone fall back through `trackedData` → `contacts` → the guid itself.

## World map (stage 14)
`maps/world/<Continent>.png` + `maps/world/index.json` (`{continents:[{id,name,file,w,h,zones:[{mapId,name,x,y,w,h}]}]}`) are
produced read-only from the local game install by `extract_maps.py --world` (see the root `README.md`), served the same way
`maps/index.json` already is (`ctx.fetchJson`, no view key). `features/world-map.js` renders one canvas per continent (its own
pan/zoom), zone outline rects, friendly/hostile "chip" circles sized by `sqrt(count)` (from `worldcounts.js`, computed off
`state.contacts` client-side — no server change), a hover tooltip per chip, and each tracked player's cross-zone path
(`worldpath.js` + the zone rects, one line per player in their track colour, thickening/arrowhead toward the most recent visit).
Clicking a chip calls the local `gotoZone(ctx, mapId, name, which)` helper: `setZone` always, `setFilters({friendly,hostile})`
only when a specific chip (not the bare zone rect) was clicked, then `setWorldView(false)` to return to the zone radar. The
`state.worldView` flag (`ctx.setWorldView`, bus event `'worldView'`) toggles the `radar`/`world` mount sections and switches
`rosterSectorOf` to `scope:'all'` even while a zone is selected (so the world map's counts reflect every zone, not just the one
underneath) — zone-view behaviour is unchanged when `worldView` is false.

## Sector / API map

| Page input | Request |
|---|---|
| nothing selected | `GET /api/v1/sweep?scope=all&since=` |
| zone picked (+Blue/Red) | `scope=zone&mapId=&grp=friendly` or `any` (Red-only sends `any`: the page hides the blue ones itself) |
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
