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
