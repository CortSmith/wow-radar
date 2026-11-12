# World map placement table — discovery notes (stage 14, task W0)

STATUS: Discovery complete. Read-only on game files; no PNGs or `maps/world/*` written. Confirmed against the
local WoW Forever install at `C:\Program Files (x86)\World of Warcraft` (build `WOW-70888patch1.60.1_Retail`,
product `wowf`).

## What I tried

1. **Community TACT listfile.** Downloaded
   `https://github.com/wowdev/wow-listfile/releases/latest/download/community-listfile.csv` directly with `curl`
   (WebFetch wasn't needed/used — it summarizes via a small model and isn't suited to grepping a 150MB CSV; a
   plain download + local grep was more reliable). It fetched fine (2,277,757 lines). Grepped it for
   `dbfilesclient/uimap.db2`, `dbfilesclient/uimapassignment.db2`, `dbfilesclient/worldmapcontinent.db2`, and
   `dbfilesclient/map.db2`, case-insensitively. Got one candidate FDID per name:
   - `UiMap.db2` -> **1957206**
   - `UiMapAssignment.db2` -> **1957219**
   - `WorldMapContinent.db2` -> 1343594
   - `Map.db2` -> 1349477

2. **Independent verification against the local install.** Wrote a throwaway script (kept only in the session
   scratchpad, not committed) that imports `Casc`, `Encoding`, `find_build_config`, `build_info`, `find_files`,
   and `DB2` straight from `extract_maps.py`, resolves the root file the same way `main()` does, looks up each
   candidate FDID via `find_files(root, {}, {fdid: label})`, and for any hit, resolves the content key through
   `Encoding` and reads it from the local `Casc` store, then parses it with the existing `DB2` (WDC5) reader.
   For each table it printed row count / field count / record size, then the first rows' `id`/`rel`/raw `vals`.
   A second and third pass dumped the raw per-field bit layout (`fsi`) and the raw record bytes so wide/packed
   fields (which the generic `DB2._bits()` returns as one large opaque integer, not as floats) could be manually
   reinterpreted as IEEE-754 floats at the right byte offsets.

## What I found

### UiMap.db2 — FDID 1957206 — CONFIRMED

- Found in root, content key resolves locally (no CDN fetch needed), parses cleanly as WDC5.
- 60 rows, 15 fields, 8-byte records (small table — this build only carries the classic-era zone/continent set,
  not retail's full ~1700+ rows, which is expected for the WoW Forever/Classic product).
- Field layout (from `fsi`, confirmed by cross-checking against `UIMAP` dict ids already in `extract_maps.py`):
  - `vals[1]` = the row's own UiMapID (13-bit field at bit offset 32)
  - `vals[2]` = ParentUiMapID (12-bit field at bit offset 45)
  - `vals[5]` = Type, a 3-bit enum at bit offset 60, matching Blizzard's known `UiMapType` enum exactly:
    `0=COSMIC, 1=WORLD, 2=CONTINENT, 3=ZONE, ..., 6=ORPHAN`.
- Decoded hierarchy (own id -> parent -> type), with names filled in from the existing `UIMAP` dict where the id
  is already known to this script:

  ```
  947  (type=1 WORLD)      parent=0      — almost certainly "Azeroth"
  1414 (type=2 CONTINENT)  parent=947    — Kalimdor (all 20 Kalimdor-side zones have parent=1414)
  1415 (type=2 CONTINENT)  parent=947    — Eastern Kingdoms (all EK-side zones have parent=1415)
  1411 Durotar             parent=1414
  1412 Mulgore             parent=1414
  1413 The Barrens         parent=1414
  1416 Alterac Mountains   parent=1415
  ... (every other UIMAP zone id lines up under 1414 or 1415 exactly as expected)
  1459/1460/1461 (type=6 ORPHAN)  — small sub-insets (e.g. a zone's instance-portal overlay)
  1463/1464 (type=2 CONTINENT) parent=0  — likely Outland/Northrend-equivalent top nodes (not yet named, not
     blocking — same mechanism applies)
  ```

  This is a clean, exact match: every zone id already hardcoded in `UIMAP` resolves to a `ZONE`-type row whose
  `ParentUiMapID` is a `CONTINENT`-type row, which is exactly the hierarchy task W0 needed to confirm exists.

### UiMapAssignment.db2 — FDID 1957219 — CONFIRMED

- Found in root, content key resolves locally, parses cleanly as WDC5.
- 61 rows, 11 fields, 48-byte records. Every row carries a `rel` (relationship-table) value equal to a UiMapID —
  i.e. one assignment row per UiMap node, matching 1:1 against the zone/continent ids above.
- The record's field at bit offset 128, 192 bits wide (24 bytes), is a packed blob that `DB2`'s generic bit
  extractor returns as one large opaque integer (expected and documented as a known limitation of the minimal
  reader — it has no concept of float fields). Reinterpreting those 24 raw bytes as six little-endian `float`s
  gives a clean, sane region box for every row:

  `[AreaMin.X, AreaMin.Y, AreaMin.Z, AreaMax.X, AreaMax.Y, AreaMax.Z]`

  where `Z` is always exactly `-1000000.0` / `+1000000.0` (an "unbounded" sentinel) and `X`/`Y` are real
  world-yard coordinates in the parent map's own coordinate system — **not** normalized 0..1 UV coordinates.
  Sample decoded rows (`rel` = UiMapID):

  ```
  rel=1429 Elwynn Forest:        X -10254.2 .. -7939.6   Y -1935.4 .. 1535.4
  rel=1436 Westfall:             X -11733.3 .. -9400.0   Y  -483.3 .. 3016.7
  rel=1415 (Eastern Kingdoms, CONTINENT): X -16000.0 .. 7466.6   Y -19199.9 .. 16000.0
  rel=1414 (Kalimdor, CONTINENT, from the earlier zone-row's own "parent box" sibling entry):
                                  X -11733.3 .. 12799.9   Y -19733.2 .. 17066.6
  ```

  Checked containment explicitly: every sampled zone's `[AreaMin.X..AreaMax.X, AreaMin.Y..AreaMax.Y]` box sits
  entirely inside its continent's box, in the same coordinate units (confirmed for Elwynn Forest/Westfall inside
  Eastern Kingdoms, and spot-checked others). This is exactly the placement data task W0 needed: a zone's
  rectangle inside its continent's world map, expressed in a shared coordinate space, so normalized placement is
  a straight linear remap:

  ```
  norm_x = (zone.minX - continent.minX) / (continent.maxX - continent.minX)   (same for maxX, Y)
  ```

  which then multiplies out to pixel x/y/w/h against whatever pixel size the continent's world-map PNG ends up
  being.
- The WORLD-type row (947, "Azeroth") has **two** assignment rows, one per constituent continent (Kalimdor's and
  Eastern Kingdoms' terrain coordinate spaces) — i.e. the world-level UiMap node is assigned once per continent
  it spans, each with that continent's own box. Not a blocker, just means a lookup by `rel` alone isn't 1:1 for
  world-type nodes (only matters if the "Azeroth" combined world image is used directly instead of the two
  continent images — see recommendation below).

### WorldMapContinent.db2 — FDID guess 1343594 — NOT FOUND in this build's root

Expected/not a blocker: `WorldMapContinent.db2` is the legacy pre-Legion table that the modern `UiMap` +
`UiMapAssignment` pair replaced. Its absence here is consistent with this build already being on the modern
UiMap-based system, which is what we actually need.

### Map.db2 — FDID guess 1349477 — found in root but BLTE-encrypted, not decodable here

Found in root and the content key resolves, but the CASC-level decode hit BLTE mode `E` (encrypted block) which
`extract_maps.py`'s `blte_decode()` deliberately doesn't support. Not needed for the placement feature: the
candidate "MapID" field inside `UiMapAssignment` decoded as a constant `0` in every sampled row anyway (not used
to disambiguate anything we care about), so `Map.db2` is not on the critical path for W1.

## Recommended path forward

**(a) These FDIDs work — proceed to implement the real extractor in W1 with this schema.**

Concretely, for W1:
- Read `UiMap.db2` (FDID 1957206) to build the id -> (parent, type) tree; a zone's continent is its
  `ParentUiMapID` chain walked up to the nearest `type=2 (CONTINENT)` ancestor (already matches `UIMAP` ids 1:1
  with the existing script's zones).
- Read `UiMapAssignment.db2` (FDID 1957219); for a given UiMapID, take its row's 192-bit region field, reinterpret
  the 24 raw bytes as 6 little-endian floats, and use `[X.min, Y.min, X.max, Y.max]` (ignore the Z sentinel pair).
- For each zone, look up its own box and its continent's box (by the continent's UiMapID — 1414/1415 found here;
  presumably 1416-ish ids similarly for Outland/Northrend, not yet enumerated but same mechanism, trivial to
  extend), and normalize into 0..1, then scale by the continent PNG's actual pixel width/height.
- **Caveat for W1 to handle, not yet resolved here:** the continent PNGs already produced by `extract_maps.py`'s
  existing zone-art pathway (`Kalimdor.png`, `EasternKingdoms.png`, etc., built via `stitch()`) are cropped to the
  per-zone constant `MAP_W, MAP_H = 1002, 668`, which was calibrated for *zone* tile sheets. Continent-level art
  may legitimately have a different native tile count/crop (confirm via the same `UiMapXMapArt`
  width/height/offset fields already read by `overlay_plan()`, rather than assuming 1002x668 applies to
  continents too) before trusting pixel-space placement math.
- The `DB2` class needs a small, explicit extension (not attempted here, per the no-write constraint on this
  task) to read genuine float fields rather than returning wide bitfields as opaque integers — straightforward
  since the byte layout is now known exactly, but it's new code for W1, not a change made in this discovery pass.
- No CDN fallback is needed for either table — both are already present in local CASC storage.

This means the stage-14 plan's fallback path ("hand-authored placement JSON", option (b) in the plan's open
questions) is **not needed** — the client's own data already contains exactly the placement table the feature
requires.

## Appendix: FDIDs confirmed for W1

| Table | FDID | Status |
|---|---|---|
| `UiMap.db2` | 1957206 | confirmed, local, WDC5, 60 rows |
| `UiMapAssignment.db2` | 1957219 | confirmed, local, WDC5, 61 rows |
| `WorldMapContinent.db2` | 1343594 (listfile guess) | not present in this build — expected, unused |
| `Map.db2` | 1349477 (listfile guess) | present but encrypted — not needed |

## 2026-11-12 — combined "Azeroth" world map: disambiguation attempt FAILED, no composite built

Task: replace the two-continent `maps/world/index.json` (separate Kalimdor/Eastern Kingdoms canvases, each in its
own local world-yard frame) with one combined canvas showing both continents correctly positioned relative to
each other. Hypothesis to test: uiMapID 947 ("Azeroth", the WORLD-type parent of both continents) has, besides
its two known duplicate `UiMapAssignment` rows (noted above), a disambiguating field that would let us pick
"947's row that places Kalimdor inside Azeroth's own canvas" vs. "the row that places Eastern Kingdoms", distinct
from each continent's own self-frame region (1414/1415, used today for zone placement).

**Method.** Wrote a throwaway script (scratchpad only, not committed) reusing `Casc`/`Encoding`/`open_game`/
`find_files`/`DB2` from `extract_maps.py` to dump every `UiMapAssignment.db2` row whose `rel` is 1414, 1415, or
947 — full `vals[]` field list plus `DB2.floats_at(raw, 16, 6)` — and separately dumped every field (not just the
region) for the same ids, and did the same for `UiMap.db2`.

**Findings:**

1. Confirmed exactly 61 rows total in `UiMapAssignment.db2`; **only `rel=947` has more than one row** (2 rows).
   1414 and 1415 each still have exactly their one known self-frame row.
2. Full field layout of `UiMapAssignment.db2` (`fsi`): field0 (2 packed floats, always `0.0,0.0` for every row
   sampled), field1 (2 packed floats, always `1.0,1.0`), field2 = the known 6-float region blob, field3
   (incrementing per-row id, e.g. 46724/46725 for 1414/1415, 46784/46785 for the two 947 rows — just row
   insertion order, not a reference), field4 (duplicates the row's own `rel` UiMapID — not independently useful),
   field5 (always 0, a common-table default — looked like a candidate `AreaID`/`MapID` link but is 0 for every
   row checked), field6 (palette-backed int; initially looked like a clean 0/1 discriminator matching 1414→1,
   1415→0 and the two 947 rows→1/0 respectively — **but a full-table dump showed field6's value distribution
   overall is `{0: 29, 1: 26, 2991: 2, 30: 1, 489: 1, 529: 1, 2997: 1}`**, i.e. it is a generic, mostly-sparse
   field (almost certainly `AreaID`) that happens to be 0 or 1 for most rows by coincidence, not a real
   continent-matching key. No field in this table reliably pairs a 947 row to a specific continent.
3. Decoded regions for the two `rel=947` rows (ignoring the always-`±1000000` Z sentinel):
   - 947 row A: X `-16000.0 .. 6933.3`, Y `-7466.7 .. 8000.0`
   - 947 row B: X `-12800.0 .. 12266.7`, Y `-9600.0 .. 6933.3`

   These two boxes **overlap heavily in both X and Y** (common X range roughly `-12800..6933`, common Y range
   roughly `-7466..6933`) — regardless of which row is assigned to which continent, swapping the labels cannot
   fix this, since overlap is a property of the two boxes themselves. A correct "where does each continent sit
   in the combined Azeroth canvas" pair of rects should **not** overlap (continents 1414/1415 don't overlap in
   reality). This fails the sanity check the task set out to apply, so these two rows are not "one placement rect
   per continent" — more likely two unrelated sub-regions of the Azeroth world node (e.g. two different
   zoom/viewport presets), not geography.
4. Cross-checked `UiMap.db2` itself for a hidden offset/scale/placement field (in case the transform lives there
   instead of in `UiMapAssignment`): its record is only 8 bytes (64 bits) per row, and every field beyond
   `id`/`ownUiMapID`/`ParentUiMapID`/`Flags`/`Type` is a common-table default of 0 for 947/1414/1415. No
   placement data lives there either.
5. Also checked `UiMapXMapArt.db2` (FDID 1957217, used elsewhere for overlay art lookup) for 947/1414/1415: it is
   a 2-field table, `{ownUiMapID -> ArtID}` only (`947->2141`, `1414->2142`, `1415->2140`). No width/height/offset
   fields live here either — that data (when present) lives in `WorldMapOverlay`/`WorldMapOverlayTile`, which are
   per-zone exploration-overlay tables, not continent-placement tables.
6. As an additional sanity check on the task's own stated premise (that Kalimdor's and Eastern Kingdoms' self
   frames are in genuinely different local coordinate systems, not a shared world frame): their self-frame boxes
   are `1414: X -11733.3..12799.9, Y -19733.2..17066.6` and `1415: X -16000.0..7466.6, Y -19199.9..16000.0`. These
   two boxes also overlap almost completely and EK's range nests almost entirely inside Kalimdor's range. Treating
   them as already-shared coordinates (i.e. skipping 947 entirely and just taking the union of the two self-frame
   boxes) would place Eastern Kingdoms *inside* Kalimdor's footprint rather than beside it — confirming the task's
   premise that the two self frames are **not** directly combinable, and ruling out that shortcut too.

**Conclusion: no confirmed, non-guessed disambiguation exists in this build's `UiMap.db2` / `UiMapAssignment.db2`
/ `UiMapXMapArt.db2` for "where does continent X sit inside the Azeroth (947) world canvas."** Per the task's own
stop condition, the composite was **not built** — doing so would require guessing an axis/offset/scale with no
real data backing it, which is exactly what was to be avoided.

Plan B (use the pre-extracted `maps/Azeroth.png`, confirmed to already exist locally, 1002x668, produced by the
normal `extract_maps.py` zone pipeline via the `("Azeroth","Azeroth")` `ZONES` entry; sampled pixels at several
points are non-blank, so it likely is Blizzard's own pre-composed world image) does not escape this problem
either: placing the 46 zones onto that image correctly still requires each zone's rect in 947's own pixel space,
i.e. the exact same unresolved continent-in-947 placement transform. So Plan B was not attempted either — it
would need the identical missing data.

**Recommendation for a future session:** the only way forward without guessing is either (a) find a different
source for the continent-in-world placement rects — e.g. a newer/non-Classic build's `UiMapAssignment.db2` may
carry extra rows per continent with a real usable key (this WoW Forever/Classic-era build's table is small, 61
rows, and may simply not carry the modern retail client's full placement data), or (b) accept a *visual-only*,
explicitly-labeled hand-calibrated offset (not derived from game data, and documented as such) if the product
decision is that an approximate combined map is acceptable. Neither was done here since both require either new
data or an explicit, acknowledged guess, and the task asked to stop rather than force either.

No files were changed by this session: `extract_maps.py`, `maps/world/index.json`, and `maps/world/*.png` are
untouched. Only this notes file was appended to.

## 2026-11-12 (follow-up) — re-examined the UiMap tree itself, then checked for a legacy Lua placement; neither yields a usable combined-world rect

Task: before accepting the prior session's conclusion, re-verify from scratch that uiMapID 947 really is the only/
nearest COSMIC- or WORLD-type ancestor of both continents (rather than trusting that assumption), and if so, fall
back to searching the client's own WorldMapFrame Lua/XML for a hardcoded legacy placement.

**Part 1 — full UiMap.db2 dump + full parent-chain walk (CONFIRMED, this session, independently re-derived).**

Wrote a throwaway script (scratchpad only, not committed) reusing `open_game`/`find_files`/`DB2` from
`extract_maps.py` to dump every row of `UiMap.db2` (FDID 1957206) as `(id, parent, type)` and to walk 1414's and
1415's full parent chains to the root (not stopping at the first CONTINENT ancestor).

- Dumped all 60 rows. **Exactly one row has `type` 0 or 1**: `id=947, parent=0, type=1 (WORLD)`. No `type=0`
  (COSMIC) row exists anywhere in this table at all.
- Full parent chain for 1414 (Kalimdor): `1414 -> parent 947 (type=2 CONTINENT) -> parent 947 is type=1 (WORLD),
  parent 0 -> 0 is the "no parent" sentinel, not a real node`.
- Full parent chain for 1415 (Eastern Kingdoms): identical shape, `1415 -> 947 -> 0`.
- Common ancestors of 1414 and 1415: `{947, 0}` — and `0` is not a real UiMap row (it's the terminator value used
  when `ParentUiMapID` is unset), so **947 is genuinely the only real common ancestor**. There is no sibling
  COSMIC/WORLD node anywhere in this table, and no alternate path.
- Re-dumped 947's own row plainly, making no assumption: `947 -> parent=0, type=1 (WORLD)`. The original premise
  ("947 is WORLD-type, parent of both continents") is **confirmed correct, not a mistake** — the problem was never
  947's identity, it's that its two `UiMapAssignment` rows don't decode into a valid non-overlapping placement
  pair (re-confirmed below, same floats as the prior session got).
- For completeness, re-pulled the two `rel=947` `UiMapAssignment` rows in this session and got byte-for-byte the
  same floats as the prior session's writeup: row A `X -16000.0..6933.3, Y -7466.7..8000.0`; row B
  `X -12800.0..12266.7, Y -9600.0..6933.3`. Still heavily overlapping, still not a usable per-continent placement
  pair. Also confirmed: no OTHER id in the common-ancestor set (`{947, 0}`) has any `UiMapAssignment` row besides
  947's own two (0 isn't a real id, so there's nothing to look up for it).

**Conclusion for Part 1: no new path found. The prior session's conclusion stands, independently re-derived from
scratch in this session rather than trusted on faith.** 947 is confirmed the sole WORLD-type ancestor of both
continents; there is no distinct COSMIC/WORLD node or sibling path to check instead; and 947's own
`UiMapAssignment` data still doesn't yield a non-overlapping placement pair for Kalimdor vs. Eastern Kingdoms.

**Part 2 — searched for a legacy Lua/XML WorldMapFrame placement (performed, since Part 1 found nothing).**

Built a `wanted` dict of path-name hashes (reusing `name_hash`/`find_files` exactly as `extract_maps.py` already
does for `.blp` art paths) for several candidate legacy/modern WorldMap file paths and looked them up in this
build's root via `find_files(root, wanted, {})`.

- Found (3 of 7 candidates): `Interface\AddOns\Blizzard_WorldMap\Blizzard_WorldMap.lua` (54,005 bytes),
  `Interface\AddOns\Blizzard_WorldMap\Blizzard_WorldMap.xml` (5,034 bytes), and
  `Interface\AddOns\Blizzard_MapCanvas\Blizzard_MapCanvas.lua` (40,930 bytes). All three were present locally (no
  CDN fetch needed) and decoded cleanly via the normal `Fetcher.get` -> `blte_decode` path.
- Not found: `Blizzard_WorldMap.toc`, `Interface\FrameXML\WorldMapFrame.lua`, `Interface\FrameXML\WorldMapFrame.xml`,
  `Blizzard_WorldMap_PingFrame.lua` — consistent with this being the modern map-canvas-based client (no legacy
  FrameXML world map exists in this build at all).
- Searched the three found files' decoded text for `Kalimdor`, `Eastern Kingdoms`, `Azeroth`, `CONTINENT`,
  `SetTexCoord`, and the literal `947` (947 checked specifically since that's the WORLD-type uiMapID in question).
  **Zero matches for any of these in any of the three files.** Spot-read the first ~60 lines of
  `Blizzard_WorldMap.lua` to sanity-check the file is genuine, readable Lua and not binary/garbled — it is: normal
  frame-mixin code (`WorldMapMixin`, `SetupTitle`, `Minimize`/`Maximize`, gamepad nav-bar handling), entirely
  generic UI plumbing with no map-specific geography or ids hardcoded anywhere in it.

**Conclusion for Part 2: no legacy or hardcoded continent-placement data exists in this client's Lua/XML either.**
This build's world map implementation is fully data-driven through `C_Map`/the `UiMap`+`UiMapAssignment` DB2
tables this session already dumped — there is no fallback hardcoded rect to recover.

**Overall assessment (confidence-labeled):**

- **Confident/re-verified independently:** 947 is the one and only WORLD-type UiMap node in this build, and the
  sole common ancestor of 1414/1415; its two `UiMapAssignment` rows are real, correctly-decoded floats, and they
  do overlap (this is a property of the data itself, not a labeling mix-up). The WorldMap addon's Lua/XML contains
  no hardcoded placement data of any kind for 947, 1414, or 1415.
- **Not found despite two independent search strategies:** any game-data-backed answer to "where does Kalimdor
  sit relative to Eastern Kingdoms on a combined canvas." Both the UiMap-table path (Part 1, this session's
  re-derivation + the prior session's deeper table-by-table audit) and the client-code path (Part 2) are now
  exhausted for this build.
- **Still true, unchanged from the prior session's recommendation:** proceeding requires either (a) a different
  build's `UiMapAssignment.db2` with richer per-continent placement rows (untested — this WoW Forever/Classic-era
  build only has 61 rows total), or (b) an explicitly-labeled, hand-calibrated visual approximation, acknowledged
  as not derived from game data. This session did not attempt either, consistent with the task's stop condition.

No files were changed by this session besides this notes file: `extract_maps.py`, `maps/world/index.json`, and
`maps/world/*.png` remain untouched.

## 2026-11-12 (later) — built the approximate composite (product decision: ship it, clearly labeled)

Per the product decision recorded above ("accept a visual-only, explicitly-labeled hand-calibrated offset if the
product decision is that an approximate combined map is acceptable"), `extract_maps.py --world` now emits a
single combined `maps/world/World.png` (2400x698) with both continents' 46 zones remapped into it, instead of the
two separate continent canvases. The two continents' SIZE relative to each other is real data (the ratio between
their own `UiMapAssignment` self-frame world-yard boxes - Kalimdor ended up ~4.55% larger than Eastern Kingdoms in
the composite); the GAP between them (350px, Kalimdor left / Eastern Kingdoms right) is a hand-chosen visual
approximation, not derived from game data, exactly as this file documents above. The output JSON records this via
top-level `"placement": "approximate"` and `"note"` fields so nothing later mistakes the layout for game-data-
derived. The old `maps/world/Kalimdor.png` / `EasternKingdoms.png` were deleted as part of this change.

## 2026-11-12 (follow-up 2) — EasternKingdoms art bug found, then World.png replaced with a real screenshot
Separately from the placement question: `Interface\WorldMap\EasternKingdoms\` in this build does NOT hold real
terrain art - opening the extracted `maps\EasternKingdoms.png` showed only decorative UI border/frame chrome, not
a landmass (confirmed visually, not just by the "non-transparent pixel" sanity check used earlier, which passed
by coincidence since the chrome is a similar brown/tan colour to real terrain). The real Eastern Kingdoms terrain
ships under the OLD pre-"two continents" lore name "Azeroth" instead (`Interface\WorldMap\Azeroth\`, uiMapID 947's
own art) - `maps\Azeroth.png` already correctly shows the labeled "EASTERN KINGDOMS" landmass. Fixed two places:
`maps\index.json`'s `"easternkingdoms"` entry now points its `"file"` at `Azeroth.png`; `extract_maps.py` gained
a `CONTINENT_ART_OVERRIDE = {"Eastern Kingdoms": "Azeroth"}` so `--world` mode reads the correct source too. This
is a real, confirmed bug fix, independent of the placement-approximation question above.

After re-running `--world --force` with that fix, the user supplied a real screenshot of the client's own in-game
combined-continent WorldMapFrame view (both continents drawn together on one sheet, with UI chrome: title text,
a coordinate readout, menu/help buttons, flight-point icons, and a player position arrow). At the user's request,
that screenshot was hand-cleaned (UI chrome removed/inpainted via clone-patch + feathered blending, done directly
in-session with PIL, not generated by `extract_maps.py`) and now IS `maps\world\World.png` (837x556), replacing
the synthetic two-panel composite entirely. This means the background art's continent SIZE/SHAPE/RELATIVE
POSITION is now 100% authentic Blizzard art (real screenshot), not synthetic - strictly better than the composite.
Zone marker positions are still an approximation (no game data places them against this specific screenshot
either): each zone's already-correct fractional position within its own continent's native 1002x668 extracted
frame was remapped onto a HAND-ESTIMATED landmass bounding box within the screenshot (Kalimdor ~(70,40,260,450),
Eastern Kingdoms ~(545,40,230,450) in the 837x556 image), verified by plotting all 46 markers and visually
confirming every one lands on its correct landmass in a plausible relative position. Recorded in index.json's
`"note"` field. See the WARNING comment above `extract_world()` in `extract_maps.py`: re-running `--world --force`
would silently overwrite this hand-prepared World.png with the inferior synthetic one - back it up first.

## 2026-11-12 (follow-up 3) — World.png replaced again, now with wowforeveratlas.com's atlas image
The user supplied a direct URL to a clean, pre-made combined-continent map:
https://wowforeveratlas.com/media/forever-maps/azeroth.webp (1002x668, no UI chrome at all - a purpose-built
atlas image, not a client screenshot, so no cleanup/inpainting was needed this time). This REPLACES the
hand-cleaned-screenshot version from follow-up 2 as `maps\world\World.png`. Zone placement uses the same method
as before (each zone's known fractional position within its own continent's native 1002x668 extracted frame,
remapped onto a hand-estimated landmass bounding box within this new image - Kalimdor ~(95,70,270,495), Eastern
Kingdoms ~(655,55,265,510)), re-verified by plotting all 46 markers; all land correctly on their landmass.
`index.json`'s `"note"` field updated to credit the real source and describe the approximation. Still subject to
the WARNING above `extract_world()` in `extract_maps.py`: `--world --force` would overwrite this with the
inferior synthetic composite.
