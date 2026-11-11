# Naming convention: the radar vocabulary

Everything in `radar-cloud` is named from one theme: **a radar station tracking contacts**. If you can picture
the station, you can guess the name of a thing. This file is the single source of truth; new code must follow it.

> **Frozen wire names.** Names that cross a boundary stay exactly as they are, because tests, the Go app and
> stored data depend on them: HTTP paths (`/api/poll`), JSON fields (`lastSeen`, `pips`, `mapId`), DynamoDB
> keys (`P#`, `G#`, `zk`, `ls`), env vars (`TABLE`, `MAX_ACCOUNTS_PER_KEY`) and query params (`rs`, `scope`).
> The theme applies to everything _inside_ those boundaries.

## 1. The glossary (concept to name)

| Concept in the product                      | Radar word           | Use it for                | Example names                                           |
| ------------------------------------------- | -------------------- | ------------------------- | ------------------------------------------------------- |
| A tracked player (stored record)            | **contact**          | modules, functions, types | `contacts.mjs`, `mergeContact()`, `Contact`             |
| A contact drawn on the canvas               | **blip**             | rendering code only       | `drawBlip()`, `BLIP_RADIUS_PX`                          |
| One position fix                            | **fix**              | positions                 | `Fix`, `dedupeFixes()`                                  |
| A contact's history of fixes                | **trail**            | track storage and display | `trails.mjs`, `appendTrail()`, `TRAIL_CAP_FIXES`        |
| One uploaded batch of contacts              | **ping**             | upload code               | `pings.mjs`, `Ping`, `applyPing()`, `PING_MAX_CONTACTS` |
| One sweep cycle from the page               | **sweep**            | sweeping                  | `sweep.mjs`, `runSweep()`, `SWEEP_INTERVAL_MS`          |
| What a sweep looks at (player / zone / all) | **sector**           | sweep scope               | `Sector`, `pickSector()`                                |
| The cheap "did anything change" read        | **tripwire**         | version item and gates    | `readTripwire()`, `tripTripwire()`                      |
| The zone index (GSI)                        | **zone-idx** (kept)  | docs and CDK              | `ZONE_INDEX`                                            |
| A zone map / its rectangle                  | **chart**            | maps                      | `charts.mjs`, `Chart`, `chartFor(mapId)`                |
| An account                                  | **operator**         | accounts                  | `operators.mjs`, `Operator`, `signUpOperator()`         |
| An API key                                  | **beacon**           | keys                      | `keys.mjs`, `Beacon`, `BEACON_MAX_OPERATORS`            |
| The session token                           | **pass**             | auth                      | `issuePass()`, `verifyPass()`                           |
| Per-account UI settings                     | **console**          | settings                  | `console-settings.mjs`, `readConsole()`                 |
| Log-file ingest watermark                   | **ledger**           | dedupe                    | `ledger.mjs`, `readLedger()`                            |
| A rule-set (normal/pvp/rp/rppvp)            | **ruleset** (kept)   | everywhere                | `Ruleset`, `RULESETS`                                   |
| Killing-blow ranking                        | **bounty** (kept)    | same as the Go app        | `bounty.mjs`                                            |
| The parsed combat log                       | **log**              | parser                    | `parser.js`, `LogLine`                                  |
| Time zones and timestamp conversion         | plain: **time-zone** | UTC instants vs display   | `time-zone.js`, `parseLogTimestamp()`, `formatInZone()` |
| The browser engine (worker)                 | **engine**           | parse+track in the Worker | `engine.js`, `worker.js`                                |
| Watching the WoW logs folder                | **tail**             | live mode                 | `tailLogs()`, `Tail`                                    |
| The local dev stack                         | **harbor**           | dev folder only           | `dev/` stays; log prefix `[harbor]`                     |

Rules of thumb:

- Prefer the radar word when it names the _domain_ concept. Keep plain words for plumbing (`http`, `db`,
  `config`, `keys`, `types`, `utils`).
- Never use two words for one concept. A player record is a **contact** in code, never `player`/`user`/`entity`
  (user-facing text may still say "player").
- Don't stretch the theme: if no word in the table fits, use a plain, boring, accurate name.

## 2. Casing and form

| Thing                                | Rule                                                               | Example                                             |
| ------------------------------------ | ------------------------------------------------------------------ | --------------------------------------------------- |
| Files and folders                    | `kebab-case`, one responsibility per file                          | `contact-merge.mjs`                                 |
| Functions                            | `camelCase`, start with a verb                                     | `mergeContact`, `readTripwire`                      |
| Predicates                           | `is`/`has`/`should`/`can` prefix                                   | `isStale`, `hasFix`                                 |
| Getters from the database            | `read…`; writers `write…`; deleters `drop…`                        | `readLedger`, `writeContacts`, `dropTrail`          |
| Pure converters                      | `to…` / `from…`                                                    | `toContactRow`, `fromContactItem`                   |
| Event handlers (UI)                  | `on` + Event                                                       | `onBlipHover`                                       |
| Classes, types, typedefs             | `PascalCase`, singular noun                                        | `Contact`, `HttpError`                              |
| Constants                            | `SCREAMING_SNAKE_CASE` with a unit suffix                          | `SWEEP_INTERVAL_MS`, `RANGE_YD`, `BODY_LIMIT_BYTES` |
| Unit suffixes                        | `_MS`, `_SEC`, `_YD`, `_PX`, `_BYTES`, `_COUNT` (or `_MAX`/`_MIN`) | `STALE_AFTER_SEC`                                   |
| Booleans                             | positive and descriptive, never `flag`/`ok2`                       | `isFinalPing`                                       |
| Collections                          | plural nouns; maps end in `By<Key>`                                | `contacts`, `contactsByGuid`                        |
| Private helpers                      | module-local, not exported; no leading underscore                  | `clampFixes`                                        |
| CSS classes / ids / custom props     | `kebab-case`; custom props `--radar-…`                             | `.blip-ring`, `#sweep-status`, `--radar-bg`         |
| DOM ids for the same concept as code | match the glossary                                                 | `#contact-list`, `#bounty-panel`                    |
| Test files                           | `<module>.test.mjs` or `.test.js`, named after the module          | `api.test.mjs`                                      |
| Test titles                          | sentence stating behaviour                                         | `'a replayed ping does not double count'`           |

## 3. Infrastructure (CDK / AWS)

| Thing          | Rule                                                                                                                                      | Example                                                   |
| -------------- | ----------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------- |
| Stack          | `RadarStation` (class `RadarStack` is fine, id `RadarStation`)                                                                            | `new RadarStack(app, 'RadarStation')`                     |
| Construct ids  | `PascalCase` noun from the glossary                                                                                                       | `ContactsTable`, `RelayFunction`, `HullBucket`, `Skyline` |
| Resource roles | **table** = `ContactsTable`; **API function** = `RelayFunction`; **bucket** = `HullBucket`; **CDN** = `Skyline`; **budget** = `FuelGauge` |                                                           |
| Physical names | do NOT hard-code (lets CloudFormation add suffixes and avoids collisions)                                                                 |                                                           |
| Tags           | `app=radar`, `layer=data                                                                                                                  | api                                                       | site | cdn` |     |
| Stack outputs  | `SiteUrl`, `TableName` stay (frozen)                                                                                                      |                                                           |

## 4. Layer map (where each word lives)

```
lambda/   index (router); src: operators, pass, console-settings, pings, contact-merge, contact-rows, contacts, charts,
          trails, tripwire, sweep, ledger, bounty, reset; plumbing: config, messages, keys, types, db, http, validate
site/     engine (parser, tracker, analyzer, engine-protocol, worker, engine-client), tail, ping (upload), sweep
          (cloud loop, with cloud-sync), blips (canvas, canvas-input), contacts, charts, ledger, console-settings,
          operator-flow, view-model, view-controller, panels (contact, bounty, history), time-zone (UTC instants in,
          operator-zone text out; no radar word fits, so the plain name is used)
lib/      RadarStack: ContactsTable, RelayFunction, HullBucket, Skyline, FuelGauge
dev/      harbor (local server, demo seeding), smoke tests
```

## 5. Checklist for a new name

1. Is it a wire name? If yes, don't rename it (section header).
2. Does a glossary word fit? Use it. Otherwise use a plain accurate word.
3. Right case, a verb for functions, a unit suffix for numeric constants.
4. Same concept anywhere else in the repo? Use the identical word.
5. Add any new domain word to the glossary table in the same change.
