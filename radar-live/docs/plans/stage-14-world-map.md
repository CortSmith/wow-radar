# Stage 14: world map in "All Zones" (continent chips, cross-zone player path, click-to-navigate)

STATUS: PLAN ONLY. Nothing here has been executed. Builds on stage 11 (`stage-11-radar-ux.md`, multi-select/tracking) — this stage is
easiest once U1-U3 land (tracked players, chips) but the world-map view itself can start in parallel since it's mostly additive.

## 1. What the user asked for
1. A world map (continents) shown when the scope is "All Zones". No such art exists yet — pull it from the game files first, the same way
   `extract_maps.py` already pulls zone art.
2. Each zone on the world map shows two chips: a count of friendly contacts and a count of hostile/neutral contacts in that zone, sized by
   count ("larger chip" = bigger for more players).
3. Searching/tracking a player draws lines connecting the zones they were seen in, **in chronological order**, across the whole world (not
   just within one zone, which is all the current trail does today).
4. Clicking a zone's dot/chip on the world map navigates into that zone's own radar view, with the Blue/Red checkboxes pre-set to match
   which chip (friendly/hostile) was clicked.

## 2. What exists today and what's missing
- `maps/` already has continent-level art from `extract_maps.py`: `Azeroth.png`, `Kalimdor.png`, `EasternKingdoms.png`,
  `Expansion01.png` (Outland), `Northrend.png` — these are continent *images* but the script has no notion of placing each zone's chart
  rectangle inside the continent image (zones are each extracted as their own standalone calibrated image, calibrated for local coordinates
  only). There is **no single "world" composite and no zone-placement table** (continent id + x/y offset/scale per zone) — this is the
  "pull the world map first" the user means, and section 3.1 covers it.
- `domain.Chart` calibrates one zone's local coordinates; there's no cross-zone/continent coordinate space in the DB or wire format today.
- `state.trail` (merge.js/scene.js) only ever holds fixes for the single selected player and only within `mapId === current view's mapId`
  for drawing (`scene.js`: `trail.filter(f => f.mapId === mapId)`). A world-spanning path needs the *other* maps' fixes too, grouped by zone
  and ordered by time, not filtered down to one map.
- "All Zones" scope today (`sectorOf` returning `{scope:'all'}`) just means "no zone/player filter" — there's no notion of a world view;
  `chooseViewMap` always lands on one concrete `mapId` (busiest live zone). A genuine world view is a new mode, not a `mapId`.

## 3. Design

### 3.1 Get the world map art and per-zone placement (new; extends extract_maps.py)
- Add a mode to `extract_maps.py` (or a sibling `extract_world_map.py` reusing its CASC/table-reading code) that reads the game's
  **WorldMapContinent** / **UiMap** hierarchy (the same DBC/DB2-style tables already touched for `UIMAP`/overlay FDIDs) to get, for each
  zone, which continent it belongs to and its placement rectangle within that continent's world image (offset + scale). This is exactly the
  data the in-game world map UI uses to place zone boundaries, so it should exist in the client's own map tables — confirm the concrete
  table/FDID during implementation (read-only, local install, same as today's script; falls back to the CDN fetch path already written).
- Output: `maps/world/<continent>.png` (the big composite, reusing the already-extracted `Azeroth.png` etc. if they already are the right
  composite, else building one from `WorldMapContinent` art) plus `maps/world/index.json`:
  ```json
  { "continents": [
      { "id": "kalimdor", "name": "Kalimdor", "file": "Kalimdor.png", "w": 2048, "h": 1536,
        "zones": [ { "mapId": 1440, "name": "Ashenvale", "x": 812, "y": 240, "w": 180, "h": 150 }, ... ] },
      ... (Eastern Kingdoms, Outland, Northrend; "Azeroth" world image may itself just be the Kalimdor+EK composite - confirm in A0-style
      discovery before assuming a 5th continent image is wanted) ] }
  ```
  `mapId` here is the same id already used everywhere else in the wire format (`domain.Chart.MapID` / combat log MAP_CHANGE ids), so no new
  server-side id scheme is introduced — placement is purely a static page-side asset, not a server concern.
- Safe to re-run / skip-if-exists, same philosophy as today's script; read-only on game files; stays personal-use (not redistributed).
- **Task W0** (gate, like A0 in stage 10): confirm the table names/FDIDs for continent placement actually exist in this client build before
  committing to the JSON shape above; this is a short, scriptable discovery pass, not a design risk, but must happen before W1-W4 lock
  the asset format.

### 3.2 Server: nothing new required for counts (confirm, don't assume)
- Zone-level friendly/hostile counts are just a count of `state.contacts` (page-side) or, if the "All Zones" world sweep needs server
  aggregation because not all zones' contacts are loaded client-side today, a new read-only endpoint:
  `GET /api/v1/world-counts?rs=` -> `{ zones: [{mapId, friendly, hostile}, ...] }`, computed from the existing store (no new storage,
  one query/aggregation in `readapi`, validated, rate-limited, reuses existing `grp` semantics for what counts as friendly vs hostile/neutral
  per the existing `groupParam` comment in `sector.js`). **Task W1** decides client-side-only vs this endpoint based on how many zones have
  live contacts at once; start client-side (cheaper, no server change) and only add W1b (the endpoint) if a world sweep without it is too
  heavy or incomplete (today's `scope=all` sweep already returns all contacts across zones — check whether it already carries `mapId` per
  contact, which it does per `domain`'s model, so client-side aggregation from the existing `scope=all` sweep is likely sufficient and W1b
  may not be needed at all).

### 3.3 Cross-zone trail for a tracked player
- Today's trail wire format (`GET /api/v1/trail`) already returns fixes with `mapId` per fix (`scene.js` filters by `mapId === current`),
  so the data needed for a world path already exists — nothing to change server-side.
- **Task W2:** a pure function `worldPath(trail)` -> ordered list of `{mapId, enteredAt, leftAt, lastFix}` (consecutive fixes on the same
  `mapId` collapse into one "visit"; order is already chronological since fixes are time-ordered). The world map draws one line per
  tracked player connecting each visited zone's placement point (center of that zone's rectangle from `world/index.json`), in the order of
  `enteredAt`, with small timestamp labels and an arrowhead or growing line thickness to show direction (same track-colour from stage 11 if
  that lands first; else the existing selected-player colour).
- Multiple tracked players (if stage 11's multi-select lands first) each get their own path in their own colour; if stage 11 hasn't landed,
  this works today with the single `state.player`/trail exactly as it already exists.

### 3.4 The world view itself
- New feature `features/world-map.js`, mounted when `state.zone === null && state.player === null` (today's definition of "All Zones") OR
  an explicit toggle (`state.worldView = true`) so the user can get back to it even while tracking someone (needed for requirement 3, since
  drawing a world path only makes sense in the world view, not inside one zone's radar).
- Renders the appropriate continent image(s) (likely all on one scrollable/zoomable canvas, reusing `projection.js`'s pan/zoom helpers
  generalized to continent space instead of one zone's chart) with each zone's rectangle holding two chips (friendly/hostile), chip area
  or radius scaled by `sqrt(count)` (so area, not radius, scales linearly with count — avoids one huge zone dwarfing the whole map; min
  size floor so a count of 1 is still visible, a count of 0 hides the chip). Hover a chip shows the zone name + exact counts (textContent
  tooltip, consistent with stage 11's tooltip work).
- Tracked players' world paths draw on top of the chips.

### 3.5 Click-to-navigate with the right checkbox state
- Clicking a chip (not just the zone dot) calls a new `ctx.gotoZone(mapId, which)` where `which` is `'friendly'|'hostile'`:
  sets `state.zone = {mapId, name}` (existing API, `setZone`), and sets filters to `{friendly: which === 'friendly', hostile: which ===
  'hostile'}` via the existing `ctx.setFilters` — reusing both existing ctx methods, no core.js change needed beyond possibly a combined
  helper for convenience. This satisfies "navigate to that zone with the corresponding checkbox checked/unchecked depending on the chip I
  selected." Clicking the zone's dot/background (not a specific chip) navigates with filters left as they are, per current behavior of
  clicking a zone elsewhere in the app.
- Stale filter is left untouched (user only specified blue/red).

## 4. Tasks
| # | Task | Depends on | Files |
|---|---|---|---|
| W0 | Discovery: confirm continent/zone placement tables exist in this client build; decide final `world/index.json` shape | - | `extract_maps.py` read, short script, write findings to `docs/plans/world-map-notes.md` |
| W1 | Extend `extract_maps.py` (or new sibling script) to emit `maps/world/<continent>.png` + `world/index.json`; run it once locally, commit the generated assets like today's zone PNGs | W0 | `extract_maps.py`, `maps/world/*` |
| W1b | (Only if W1's client-side aggregation from `scope=all` proves insufficient) `GET /api/v1/world-counts` in readapi | W1 | `internal/mod/readapi`, storetest |
| W2 | `worldPath(trail)` pure function + tests; extend `track-overlay`/a new small module to fetch trails for tracked players even while in world view | stage 11 U1-U2 helps but not required for single-player case | `js/merge.js` or new `js/worldpath.js`, tests |
| W3 | `features/world-map.js`: continent canvas, pan/zoom (reuse `projection.js`), zone rectangles, chip rendering sized by count, hover tooltip | W1, (W1b if needed) | `features/world-map.js`, `projection.js`, `style.css` |
| W4 | World paths drawn over the map for tracked player(s); per-player colour | W2, W3 | `features/world-map.js` |
| W5 | Click-to-navigate: chip click -> `gotoZone(mapId, which)`, zone-background click -> plain `setZone`; wire into `core.js` as a small helper if it reduces duplication | W3 | `core.js`, `features/world-map.js` |
| W6 | Toggle between world view and the current single-zone radar (`state.worldView`), kept in sync with existing zone/player selection so Escape/back behaves sensibly | W3-W5 | `core.js`, `main.js`, `features.js` |
| W7 | Tests (node: chip sizing math, click-to-navigate filter state, worldPath ordering/collapsing; a real-Edge check) + `site/ARCHITECTURE.md` update + README section on regenerating world assets | all | `test/*`, docs |

Order: W0 -> W1 -> (W2 parallel with W1b-if-needed) -> W3 -> (W4, W5 parallel) -> W6 -> W7.

## 5. Acceptance checks
- `maps/world/index.json` lists every continent with every zone placed inside it; regenerating is a documented, re-runnable, read-only
  script step like today's `extract_maps.py`.
- All Zones view shows continents with correctly proportioned friendly/hostile chips matching live contact counts; a zone with zero of a
  reaction shows no chip for it.
- Tracking a player who moved Elwynn -> Westfall -> Duskwood draws a 2-segment line across the Eastern Kingdoms continent image in that
  order, regardless of which zone's radar is currently open.
- Clicking the hostile chip on Duskwood opens Duskwood's zone radar with Red checked and Blue unchecked; clicking the zone itself (not a
  chip) opens it without changing the filters.
- All existing 70+ node tests and Go tests stay green; the new assets don't blow up the page's load size on the weak laptop (check total
  PNG weight, consider a lower-res world composite if the native art is large).

## 6. Risks / open questions
- Unknown exact table/FDID for continent zone-placement until W0 runs; if it doesn't exist in this client's data, fall back to a
  hand-authored placement JSON (the user manually notes rough x/y per zone against the existing continent PNGs) — W0's findings doc should
  say explicitly which path is needed.
- Chip visual density on a continent with many simultaneously-populated zones; may need a minimum zoom level or clustering — revisit after
  W3's first real render.
- World view is a new "mode" alongside zone/player scope; make sure Escape, the ruleset switch, and the time-zone/chart-picker features
  (which currently assume a concrete `mapId`) don't break when `state.worldView` is true and no single `mapId` is chosen — flag for W6.
