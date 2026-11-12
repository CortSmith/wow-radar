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

## Results (Stage 2 complete)

**Built.** `internal/domain/event.go` (+ `event_test.go`), `internal/mod/logparse`, `internal/mod/contactx`, `internal/mod/bountyx`.
`gofmt -l` empty, `go vet` clean, `go test` green incl. the core architecture test (modules import only `core` and `domain`).

| Package | Coverage |
|---|---|
| logparse | 98.3% |
| contactx | 95.9% |
| bountyx | 95.5% |
| domain (whole package) | 98.6% |

**Exported API**
- `domain`: `Event` (`Source, TimeMs, Name, Src, Dst EventUnit, Info *EventInfo, Amount, HasAmount, IsDamage, IsHeal,
  Unconscious, Zone, Chart *Chart, AdvancedEnabled *bool, Ruleset`), `EventUnit` (`GUID, Raw, Flags, RaidFlags`; `Kind/IsPlayer/IsMine/Reaction`),
  `EventInfo`, `RawLine{Source, Text}`, `GUIDKind`/`KindOf`, `ReactionFromFlags`, `Namer` (`NewNamer, Observe, Suffix, Display`).
- `logparse`: `ParseLine(line, Options{FallbackZone, Source}) (domain.Event, error)`, `DetectRealmToken`, `ErrBadLine`, `ErrLineTooLong`,
  `MaxLineBytes`, `New()` module (setting `logparse-zone`, default UTC). Parser accepts `domain.RawLine`, feeds `domain.Event` back to `host.Parsers`.
- `contactx`: `NewExtractor(Options{TrackNPCs, DisableApprox, ApproxMaxAge, MaxContacts, RealmSuffix})`, `Feed(Event)`,
  `Flush(now) Part{Ruleset, Contacts []PingContact, Charts []Chart}`, `Me()`, `Tracked()`; `New()` module (settings `contactx-flush` 10 s,
  `contactx-ruleset`, `contactx-track-npcs`).
- `bountyx`: `NewAnalyzer(Options{RealmSuffix})`, `Feed(Event)`, `Flush() Part{Ruleset, Deaths []Death, Credits map[guid]Credit{Raw,Name,KB,Assists,LastMs}}`,
  `Stats()`; `New()` module (settings `bountyx-flush`, `bountyx-ruleset`).

**Wiring.** Streaming consumer: per received line call `host.Parsers.Feed(ctx, domain.RawLine{Source, Text})`. logparse parses and re-feeds the
`domain.Event`; contactx and bountyx keep one Extractor/Analyzer per `Event.Source` and, from their `Start` loops, publish `domain.Ping`s to
`host.Sinks` every flush interval (ruleset = detected realm token else the module setting; unique `cx-`/`bx-` batch ids; players only; split at 500 contacts).
`Stop` does a final flush. Direct use without the socket: `ParseLine` -> `Extractor.Feed` / `Analyzer.Feed` -> `Flush`.

**Deviations / decisions**
- The shared event type needed more than a struct: `Namer`, `KindOf`, `ReactionFromFlags` also live in `domain/event.go` (contactx and bountyx cannot import logparse).
- `Event.Ruleset` is set by logparse from the log owner's raw name; sources without a token use the module setting.
- `bountyx.Flush()` takes no time argument. Bounty credits arrive as pings whose contacts hold only kb/assist deltas, hostile reaction, first/lastSeen = death time.
- Sink failure drops that flush's deltas (the relay sink in stage 5 owns buffering and retry); `Flush` returns the first sink error.
- A death whose killer is a friendly player (or friendly pet) gives no credit to anyone, as in go-radar. Valid-but-short records (<9 fields) parse to a name-only Event.
- `-race` not run (no cgo).

## Results (realm suffix fix)

- `domain.Namer` now counts trailing-token runs per distinct name and locks the longest run shared by >= 6 distinct players, so a minority with another suffix no longer blocks detection (it keeps dashes-as-spaces display). Below the threshold nothing is stripped.
- New settings `contactx-realm-suffix` / `bountyx-realm-suffix` (default empty = auto-detect) are passed to the Extractor / Analyzer.
- contactx: `name` is the display name, `raw` the original; when detection changes the display name, the previously published one is added once to `pastNames`.
- bountyx: credit names are resolved at Flush, so they follow a later detection.
- Tests added in domain, contactx and bountyx; gofmt, vet and `go test ./internal/... -cover` pass (incl. the core architecture test).
