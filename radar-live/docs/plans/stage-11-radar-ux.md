# Stage 11: radar page UX (multi-select, full roster, history tooltips) + armory in the tooltip

STATUS: PLAN ONLY. Nothing here has been executed. Same rules as every stage (TDD, one Sonnet/medium agent per task, lead review with
gofmt/vet/go test/node tests/`scripts/scan-secrets.ps1`, agents never commit). Stage 10 (`stage-10-armory.md`) supplies the armory data; this
stage consumes it. Tasks U1-U3 do NOT depend on the armory and can ship first.

## 1. What the user asked for
1. Add the WoW armory module (stage 10) and show armory details in the mouse-hover tooltip.
2. The same tooltip on the **historical** dots of a tracked character's trail, not only the newest dot.
3. Always show the **entire player list** despite a search; players outside the search are greyed out with an indicator.
4. Search and track **several players at once**, removing each one individually.

## 2. What the code does today (why the changes are not trivial)
- `state.player` is a single `{guid,name}`. `sectorOf` (`js/sector.js`) turns it into `scope=player`, and `applySectorChange` (`js/core.js`)
  **clears `state.contacts` and `state.trail`** whenever the sector changes. So selecting someone empties the list and the radar of everyone else.
  That is the direct cause of requirement 3.
- The tooltip (`radar-canvas.js` `tipText`) reads `p.contact` of the current pips only. Trail points (`scene.trail`, `draw.js paintTrail`) are drawn
  but never hit-tested, and trail fixes carry no name, level, or profile (requirement 2).
- Search (`features/search.js`) picks one contact; the box shows that one name with an x. Escape / the x call `deselect()`.

## 3. Design

### 3.1 State: a set of tracked players (U1)
- Replace `state.player` with `state.tracked`: an ordered `Map<guid, {guid,name}>` (cap `TRACK_MAX`, default 8, a config constant).
  Keep `state.player` as a derived getter = the **focused** player (last added or last clicked) so existing features keep working unchanged
  (`history-panel`, `bounty-panel`, `view-model`, `track-overlay`). Removing the module's multi-select must leave single-select behaviour intact.
- `ctx` API: `addPlayer(p)`, `removePlayer(guid)`, `clearPlayers()`, `focusPlayer(guid)`; `setPlayer(p)` = clear + add (compat); `deselect()` = clear.
  All emit `selection` through `changed()`.
- Clicking a dot or list row toggles that player in the set (click again removes it). Escape clears the whole set (documented). A search result adds.

### 3.2 Two data streams, so the list is never emptied (U2)
Today one sweep serves both the list and the radar. Split by purpose:
- **Roster sweep** (always): `scope=zone|all` exactly as the no-selection view works today. Fills `state.contacts` and is **never cleared by a
  selection**; it is only cleared by a ruleset or zone change. This is what the list and the dots show.
- **Tracked sweeps** (only when `tracked` is non-empty): one `scope=player&guid=&trail=1` request per tracked guid, results go to
  `state.trackedData: Map<guid,{contact, trail}>`. Cap concurrency (`<= TRACK_MAX`, sequential with the same abort/backoff scheduler).
  `mergeContacts` for the tracked contact also updates the roster row so it stays fresh even if it left the zone window.
- Server impact: none expected. If the all/zone sweep hides someone the tracked sweep still finds (zone mismatch), the list shows them from
  `state.trackedData` (pinned rows, as `visibleContacts` already does for one guid). Confirm in U2 that the 60 s polling budget and the rate limit
  (readapi per-key limiter) tolerate `1 + TRACK_MAX` requests per cycle; if not, add `GET /api/v1/sweep?guids=a,b,c` (<=8) to readapi as task U2b
  (frozen wire names stay, new param only, validated through `internal/validate`).
- `sectorKey` loses the player component; it gains a separate `trackKey` so removing one player does not discard the others.

### 3.3 Search: chips instead of one box value (U3)
- The search box stays a type-ahead (debounced, same endpoint). Picking a result **adds a chip** below/inside the box and clears the text so the
  next name can be typed immediately. Enter on a single exact match also adds it.
- Each chip shows a colour swatch (the track colour, below), the name and an `x` button that calls `removePlayer(guid)`. A "Clear all" link
  appears when there are two or more chips. Chips are built with `textContent` only (XSS tests extended), keyboard reachable (Backspace on empty
  input removes the last chip, `aria-label` on each x).
- Each tracked player gets a stable **track colour** from a small colour-blind-safe palette (reuses `draw.js readColors`; added as CSS tokens
  in `style.css`, dark-mode safe). The swatch in the chip, the list row border, the dot ring and the trail line all use it.

### 3.4 Full list with greying (U4)
- `contact-list.js` renders the roster (all contacts, existing group/stale filters still apply). When `tracked` is non-empty:
  - tracked rows: normal weight, track-colour marker, pinned to the top in chip order, with a small `x` to remove;
  - non-tracked rows: **greyed** (`.dim`: reduced opacity + muted text, still clickable to add). A header line states
    `Tracking 2 of 143 - others greyed` (MSG key, so the wording is in one place) and a "Show only tracked" checkbox hides the greyed rows for
    people who want the old behaviour (default OFF, remembered in `prefs`).
  - The indicator is not colour-only (also a text line and an `aria-label`/`title` "not in your search"), for accessibility.
- The radar does the same: non-tracked dots are drawn faded (alpha ~0.35, no labels), tracked dots at full strength with their track colour ring;
  hit-testing keeps working on faded dots so the tooltip and click-to-add still work.
- Search results already showing a tracked player are marked "tracked".

### 3.5 Tooltip on every dot, including history (U5)
- `scene.js` already returns `trail` points with `sx,sy`. Add `hitTest` over `[...pips, ...trailPoints]` (pips win ties within the radius).
  A hovered trail point shows: `Name - zone`, the **time of that fix** (absolute in the chosen time zone + "x ago"), the fix `kind` (exact / approx),
  coordinates, and, if known at that time, level/reaction/state. Trail fixes carry only `{t,mapId,x,y,kind}` today, so name/zone come from the
  tracked player's contact (`trackedData`), and "state" is omitted for history (we do not store historical state). No wire change needed.
- Hover highlights the dot (larger ring) and, for a trail point, draws a thin line to the neighbouring fixes so the path direction is obvious.
- Tooltip text is rebuilt by one pure function `tooltipModel(target, ctx)` returning rows, so it is unit-testable and the renderer stays dumb.
  The tip element stays `textContent`-only; multi-line via separate row elements (no HTML strings).

### 3.6 Armory details in the tooltip (A5 of stage 10, merged here as U6)
Needs stage 10 tasks A0-A4 done (domain `Profile`, `GET /api/v1/profile(s)`). Until then the tooltip simply has no armory rows.
- New removable feature `js/features/profile-panel.js` owns a `state.profiles: Map<guidKey, Profile>` cache and exposes `ctx.profileFor(guid)`.
  `radar-canvas` asks `ctx.profileFor` through an optional hook; if the feature is absent nothing breaks (same socket pattern as other features).
- **Lazy, batched fetch:** when dots are hovered or tracked, the guid is queued; one request per 250 ms flushes up to 100 guids to
  `/api/v1/profiles?guids=`. Profiles of all tracked players are prefetched. Negative and error results are cached for the session (TTL honours
  the server's `fetchedAt`/`status`). Never fetched for hundreds of idle dots, only for hovered/tracked/listed-visible rows.
- Tooltip rows added when a profile exists (final field list is fixed by task A0): class (class-coloured), race, level, guild, faction, and
  optionally a "profile age" line. When status is `notfound` show "not in armory"; when `error`/absent show nothing (no flicker, no spinner in a tooltip).
- Optional toggle "Class colours" (A5) recolours dots by class; track colours still ring tracked dots.
- Armory-sourced strings come from a third party via our DB: `textContent` only, length-clamped in the page too, CSP unchanged.

## 4. Tasks (one agent each, TDD, files disjoint where noted)
| # | Task | Depends on | Main files |
|---|---|---|---|
| U1 | `state.tracked` + ctx API, derived `state.player`, config `TRACK_MAX`, bus events, compat tests (single-select still passes all existing node tests) | - | `core.js`, `sector.js`, `config.js`, `test/` |
| U2 | Roster sweep vs tracked sweeps; `trackedData`; sectorKey/trackKey split; scheduler runs both within one cycle; optional U2b server `guids=` | U1 | `core.js`, `sector.js`, `merge.js`, (U2b: `internal/mod/readapi`, storetest) |
| U3 | Search chips, add/remove one by one, Clear all, Backspace removes last, track colours | U1 | `features/search.js`, `style.css`, `messages.js` |
| U4 | Full list with greyed non-tracked rows, header line, "Show only tracked" toggle, faded radar dots, per-row x | U2, U3 | `features/contact-list.js`, `draw.js`, `scene.js`, `style.css` |
| U5 | Hit-test on trail points, `tooltipModel`, hover highlight, time/kind rows | U2 | `features/radar-canvas.js`, `projection.js`, `scene.js`, new `tooltip.js` |
| A0-A4 | Stage 10 armory discovery, domain/store, client, ingest module, DB API | user supplies API docs URL | see `stage-10-armory.md` |
| U6 | `profile-panel.js` feature: cache, batched lazy fetch, tooltip rows, optional class colours | U5, A4 | `features/profile-panel.js`, `tooltip.js`, `messages.js` |
| A6-A8 | Stage 10 security, deploy docs, e2e/attack tests (secrets via env/`*_FILE` only) | A3 | see stage 10 |
| U7 | Review pass: extend `xss.test.js`, `safety.test.js`, `deselect.test.js` (now clear-all), CSP test, real Edge check against a local stack (`scratchpad/cdp-*.mjs`), `site/ARCHITECTURE.md` update, README screenshots text | all | tests, docs |

Order: U1 -> (U2, U3 parallel) -> (U4, U5 parallel) -> U7a; armory track A0..A4 runs in parallel from the moment the docs URL arrives -> U6 -> U7b.
U1-U5 deliver requirements 2, 3 and 4 with no armory dependency.

## 5. Acceptance checks
- Select 3 players: list still shows everyone, 3 rows highlighted, others greyed with the header line; removing one chip updates list, dots, trails
  and requests in the next cycle; removing the last one returns to the plain roster.
- Hover any trail dot of a tracked player: tooltip shows name, fix time, kind, zone. Hover a faded non-tracked dot: normal tooltip.
- With the armory module on: tooltip shows class/race/level/guild for profiled players, nothing for unknown ones; with the module (or the
  profile-panel feature file) removed, the page still builds and works.
- Request budget: a cycle with 8 tracked players stays under the read rate limit (measured, noted in Results); profile requests are batched
  (<= 1 per 250 ms) and never repeat for cached guids.
- Escape / x behaviour, the Blue/Red filter (neutral stays visible) and the existing 70 node tests keep passing.

## 6. Risks and open questions
- Request fan-out (`1 + TRACK_MAX` per cycle) on a weak laptop: measure first, then decide on U2b.
- Trail fixes carry no historical level/state; the history tooltip shows only what is true at that time (position, time, kind).
- Track colour palette must stay distinct from blue/red reaction colours and class colours: palette choice reviewed in U3 with the dataviz checks.
- `TRACK_MAX` 8 is a guess; ask the user if they want more.
- Armory terms of use for showing data on a page, and API shape: unchanged gate A0 (needs the official docs URL; credentials never shown to Claude).
