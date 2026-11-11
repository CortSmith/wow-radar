# Radar Cloud: project charter

One document with the original ask, what was built, the rules the code follows, and how to use, build and deploy it.
Related files: [README.md](README.md) (quick reference), [NAMING.md](NAMING.md) (vocabulary, source of truth for names),
[PLAN.md](PLAN.md) (decisions, cost and time tables, risks, history), `lambda/ARCHITECTURE.md`, `site/ARCHITECTURE.md`.

## 1. The initial ask

> Create a new CDK app for the radar app in this directory where I can upload WoW combat logs, parse them and store
> only the information on players that I need from the logs, removing duplicate data. The main HTML should show
> "non-moving parts" (generic UI elements) that contain "moving" data like character information. The UI should
> include a login that takes a username/password/API key, where the API key is active, and each API key is associated
> with accounts, blocking further account creation past 5 associations. Consider cost in every facet, keeping it as
> cheap as humanly possible. Come up with a detailed plan, cost associations and time estimates for each layer, and
> review and revise several times before settling on the cheapest setup. Static ads on the left side of the page. The
> page should look exactly like the app that currently exists in this directory. Use tokens efficiently; use subagents.

Follow-up requirements, in order:

1. API support for login, per-zone character lists, player search, and hostile (Horde) / friendly (Alliance) filtering.
2. Support every dynamic-data feature of the Go app.
3. Tests for all code; prove it works before applying it.
4. 5-second updates scoped to what the user is looking at, minimizing database reads
   (specific player > zone > hostile/friendly > everything).
5. A local imitation of production; runnable without npm.
6. README updates (cloud and root); a database schema description.
7. Readability and best-practice refactor, looped to at least 95% reviewer satisfaction (reached: 95%).
8. A naming convention with a common theme (NAMING.md).
9. A hardened `run-local` script.
10. Show times in the logged-in user's time zone; store uploaded timestamps as UTC with offset.

## 2. Features developed

**Hosting and cost**

- CloudFront (PriceClass_100) in front of a private S3 bucket (static site) and a Lambda Function URL for `/api/*`.
- One on-demand DynamoDB table, RETAIN policy, one GSI (`zone-idx`). Lambda arm64, Node 22, no npm dependencies.
- Optional $5/month AWS Budget alert (`-c budgetEmail=...`). Estimated $0.3 to $1/month for about 5 active accounts.

**Accounts and keys**

- Sign up and log in with username + password + API key. A key must be active; at most 5 accounts per key
  (enforced by a DynamoDB transaction). Keys are stored as SHA-256 hashes, minted by `scripts/create-key.mjs`.
- scrypt password hashing, constant-shape login timing, 7-day HMAC-signed session passes.

**Ingest**

- Browser-side parsing in a Web Worker; raw logs never leave the machine.
- Live tailing of the WoW `Logs` folder (File System Access API, Chrome/Edge) and manual upload fallback.
- Deduplication: log fingerprint ledger with a timestamp watermark, per-batch ids (retry-safe), additive counter
  deltas, deduped and capped position trails (2 yd / 5 s, 0.1 yd rounding, 5000 per player).
- Rule-sets `normal`, `pvp`, `rp`, `rppvp`, each with separate data.

**Viewing (parity with the Go app, except the Python Heatmap)**

- Live radar canvas, map picker, range slider, Follow me, Blue/Red/Stale filters, search.
- Player history and aggregates, tracks, bounty ranking (killing blows), settings, map calibration, reset / re-analyze.
- Static left-hand ad column driven by `site/ads/ads.json`; page looks like the Go app.

**Cheap polling**

- 5 s chained polls with jitter, scoped to player / zone / all, gated by a version item so an unchanged poll costs one
  tiny read. Pauses when the tab is hidden; slows to 30 s after 2 quiet minutes.

**Time zones**

- Log timestamps converted to UTC epoch ms at parse time (own offset, else the chosen zone); displayed in the
  operator's zone with a label; ISO UTC with `+00:00` on hover and in upload summaries.

**Tooling**

- Local production imitation (`dev/server.mjs`, `run-local.bat`), demo data, live simulator, sample-log generator.
- Tests: API (23), CDK template (12), frontend logic (141), plus a 17-check smoke test. Prettier, custom lint, docs check.

## 3. Naming conventions

Full table in [NAMING.md](NAMING.md). Summary:

- **Theme: a radar station tracking contacts.** player = **contact**, drawn player = **blip**, position = **fix**,
  history = **trail**, upload batch = **ping**, poll cycle = **sweep**, poll scope = **sector**, change check =
  **tripwire**, map = **chart**, account = **operator**, API key = **beacon**, session token = **pass**,
  settings = **console**, ingest watermark = **ledger**, local dev stack = **harbor**.
- Plain words for plumbing (`http`, `db`, `config`, `keys`, `types`, `utils`). One word per concept, never two.
  Don't stretch the theme: if no word fits, use a plain accurate name.
- Files `kebab-case`; functions `camelCase` starting with a verb (`read…`, `write…`, `drop…`, `to…`, `from…`);
  predicates `is/has/should/can`; classes and typedefs `PascalCase` singular; constants `SCREAMING_SNAKE_CASE` with a
  unit suffix (`_MS`, `_SEC`, `_YD`, `_PX`, `_BYTES`, `_COUNT`); maps end in `By<Key>`; CSS `kebab-case`, custom props
  `--radar-…`; tests named after the module, titles are sentences stating behaviour.
- CDK: stack `RadarStation`; constructs `ContactsTable`, `RelayFunction`, `HullBucket`, `Skyline`, `FuelGauge`.
  Never hard-code physical resource names.
- **Frozen wire names never change:** API paths, JSON fields, DynamoDB key prefixes (`P#`, `G#`, `zk`, `ls`), env vars
  (`TABLE`, `MAX_ACCOUNTS_PER_KEY`), query params, stack outputs (`SiteUrl`, `TableName`).

## 4. Best practices and current rules

**Architecture**

1. Static shell + JSON: `index.html` holds no data; everything dynamic arrives from the API.
2. The Lambda has no npm dependencies (runtime AWS SDK v3 only). `index.mjs` is a thin route table; one
   responsibility per module under `lambda/src/`.
3. Frontend: one `radar` global namespace, one module per responsibility, logic modules are DOM-free. No `innerHTML`,
   no `eval`, no third-party scripts.
4. Every user-facing string lives in the messages catalogue (`lambda/src/messages.mjs`, `site/messages.js`).

**Data**

5. Times are UTC epoch milliseconds in the database and the API. Local time exists only in display code.
6. Counters change only by additive deltas tied to a `batchId`; replays must be no-ops.
7. Read the least data that answers the question; add a gate (version item, `ut`) before any wider read.
8. The page always resets with `what: 'all'` (resetting only the bounty would delete ledgers and risk double counting).
9. Reaction is relative to the recorder, not a faction.

**Quality gate (all must pass before a change is done)**

10. Every new function has a test; prove it with `npm test` (or plain `node --test`) before deploying.
11. `npm run format:check`, `npm run lint`, `npm run check:docs`, `npm run typecheck`, `npm run synth` are clean.
12. Update the matching ARCHITECTURE.md, README and PLAN.md in the same change. Record new decisions and problems in
    PLAN.md; add any new domain word to NAMING.md.
13. Keep it cheap: any change that adds a request, a read, or an always-on resource needs a cost note in PLAN.md.
14. Files are written without a BOM (PowerShell 5.1 `Set-Content -Encoding utf8` adds one and breaks JSON).

**Security**

15. Keys and passwords are never logged or stored in plain text; keys travel in the request body or header, not URLs.
16. Map images were extracted from game data and are for personal use; don't make the site public without deciding that.

## 5. Use it

Run locally (no AWS, no npm):

```
run-local.bat              demo data
run-local.bat --sim        demo data + simulated live uploader
run-local.bat --fresh      wipe saved data (dev/.data.json)
run-local.bat --port 8788 --latency 0
```

Open <http://127.0.0.1:8787>, log in as `demo` / `demo-password-1` with key `dev-key-0001`
(`dev-inactive-key` tests rejection). The launcher uses `node` from PATH or the Node bundled with VS Code.

In the page: sign up or log in; choose the WoW `Logs` folder (Chrome/Edge) for live mode, or drop a
`WoWCombatLog*.txt` to upload; use the rule-set switch, map picker, range, filters and search; select a player for a
track; open Bounty for killing blows; pick your display time zone in the header.

Make test logs: `node dev/make-sample-log.cjs [out.txt] [--append] [--no-offset]`.

## 6. Build and test

```
npm install                 once
npm run build               compile the CDK app
npm run typecheck
npm test                    api + stack + site suites
npm run smoke               black-box checks against a running local server
npm run format:check && npm run lint && npm run check:docs
```

Without npm: `node --test test/api.test.mjs` and `node --test test/site/*.test.js`.

## 7. Deploy

Prerequisites: Node 22, AWS CLI with credentials, one-time `npx cdk bootstrap`.

```
npm install
npx cdk deploy -c budgetEmail=you@example.com      (budgetEmail optional)
set TABLE=<TableName from the output>
node scripts/create-key.mjs                         prints a new active key
node scripts/create-key.mjs --deactivate <key>
```

The deploy prints `SiteUrl` (`*.cloudfront.net`). Hand a key to up to 5 people; each signs up on the page.
Renaming stack or construct ids creates a new table instead of updating the old one.

## 8. Known limits and open items

- Not yet opened in a real browser; nothing deployed; real DynamoDB behavior (throttling, GSI lag) untested.
- Same-millisecond events at an upload watermark can be lost; live logs under 64 KB wait to upload.
- Sessions stay valid up to 7 days after a key is deactivated; no login rate limiting beyond scrypt cost.
- Heatmap not ported. Live radar needs Chrome/Edge. Map-image public hosting is an open licensing question.
- Under discussion: replacing login with ingest/view API keys and a Go uploader (no code yet).
- Optional cleanups in PLAN.md: JSDoc `@param` echo lines, unused `PollScope` typedef, single-letter params.
