# Stage 2: parsing and extraction modules

Read `../../PLAN.md` and `stage-1-core.md` (Results section: exact `domain` types) first. Work only in
`internal/mod/logparse`, `internal/mod/contactx`, `internal/mod/bountyx` (and their testdata). Do not touch other dirs.

## Goal
Turn raw combat-log lines into the per-player details we care about, as `domain.Ping`s ready for the store.

## Deliverables
1. `logparse` (module name `logparse`): `ParseLine(line string, opts) (Event, error)`. CSV with `"` quoting, timestamp
   `M/D/YYYY HH:MM:SS.mmm[+-H[:MM]]  RECORD` -> **UTC epoch ms** (own offset wins; else `opts.FallbackZone`, a `*time.Location`).
   Event: name, src/dst (guid, raw name, flags, raid flags), prefix params, advanced block (19 fields: infoGUID, ownerGUID, curHP,
   maxHP, ..., X, Y, mapID, facing, level; block sits between prefix and suffix params and describes the *info unit*), damage amount,
   `Unconscious` flag on UNIT_DIED. Special events: `COMBAT_LOG_VERSION`, `ZONE_CHANGE`, `MAP_CHANGE` (x=(a,b), y=(c,d)).
   Name cleanup (strip realm suffix, `-` -> space), `DetectRealmToken`, ruleset detection from the owner's raw name
   (`domain.Ruleset`). Reaction from flags. Port behaviour from `../go-radar/internal/parser` and `../go-radar/internal/ruleset`
   (read them; **rewrite and test freshly, do not import**). Unknown/garbled lines return a typed error, never panic. Line cap 1 MiB.
2. `contactx` (module `contactx`): a stateful `Extractor` per source: `Feed(Event)`, `Flush(now) domain.Ping-part`. Tracks exact fixes,
   approximate fixes ("me" last fix < 120 s old, kind `approx`), level, maxHp, hp samples, past names (<=10), events/srcEvents,
   damage totals/hits/events (credit `*_DAMAGE` source; ignore `*_LANDED` duplicates), "me" detection (flag 0x1 on Player GUID),
   players only by default (option `TrackNPCs`), trail dedupe (2 yd / 5 s, 0.1 yd rounding), chart rectangles from MAP_CHANGE.
   Flush returns **deltas since the last flush** (additive counters) and resets them; bounded memory (cap tracked contacts, evict least
   recently seen).
3. `bountyx` (module `bountyx`): `Analyzer` implementing the rules in PLAN.md section 3 (victims = friendly `Player-` GUIDs; UNIT_DIED
   (not when Unconscious=1), PARTY_KILL, SPELL_INSTAKILL; 3 s duplicate window; killing blow window 5 s; assist window 15 s; bounded
   per-victim ring 64 hits / 256 victims; pet/totem owner credit via advanced-block owner GUID or SPELL_SUMMON; NPC kills counted
   only as `npcDeaths`). Emits `domain.Death` records with stable ids (sha1 first 10 bytes hex of victim GUID + ms + source name) so
   the store can dedupe; `Flush` returns kb/assist deltas keyed by guid.
4. Each package registers itself with the core via a `New()` returning a `core.Module` that adds a Parser to the `Parsers` socket
   (check the real socket signature in stage-1 Results). Removing a package must not break the others (the architecture test).
5. Synthetic test fixtures in `testdata/` (no real names): a small zone walk, a fight with damage, a death with killing blow and
   assists, a feign death, a pet kill, realm-suffixed names, offset and no-offset timestamps, malformed lines.

## TDD order
logparse timestamps -> CSV split -> advanced block/prefix layouts -> special events -> names/realm/ruleset -> contactx fixes/counters ->
flush deltas/idempotent re-flush -> trails -> bountyx windows/dedupe -> module wiring.

## Done when
gofmt/vet/test green, >= 85% coverage per package, README-style package comments, STATUS.md line added, Results section appended here.
