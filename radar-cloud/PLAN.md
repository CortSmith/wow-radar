# radar-cloud — Plan, Goal & Decision Log

> Handoff file for future sessions. **On compaction, preserve: Decisions, Problems/Risks, and Cost/Time metrics sections verbatim.**

## GOAL

Cheapest-possible AWS CDK app hosting a web version of the local WoW radar app (`../go-radar/web`, must look identical):

1. Upload WoW combat logs, parse them, store only needed per-player info, no duplicates.
2. Login with username + password + API key. API keys are admin-issued and must be active; **max 5 accounts per key**.
3. Static `index.html` shell of generic UI elements; character data is injected from JSON.
4. Static ads on the left side.
5. Minimize cost in every layer (target < $1/month for ~5 accounts).

## STATUS (update each session)

- [x] Explored existing app (summary below)
- [x] Plan + cost analysis (3 review passes)
- [x] Frontend (`site/`) built by subagent: JS passes `node --check` (VS Code's bundled Node), parser smoke-tested on 4 sample lines; NEVER rendered in a browser or tested against the backend. Maps converted to WebP q85: 15 MB (was 88 MB). Deviations from original UI: removed Range/Follow me/Fixed map/rule-set/bounty/live polling/tracks; added login overlay, Upload panel, Logout; "stale" = >24h before newest sighting; client-side search; players with no position fix are not uploaded; ads column hidden <900px.
- [x] WRITTEN, UNVERIFIED (no Node here): `lib/radar-stack.ts`, `bin/app.ts`, `lambda/index.mjs`, `scripts/create-key.mjs`, package.json/cdk.json/tsconfig
- [ ] `npm install`, `cdk synth`, local handler test with mocked DynamoDB
- [ ] Deploy (needs user's AWS creds + budget alert email) — **ask user first**

## EXISTING APP FACTS (from exploration)

- UI: `go-radar/web/{index.html 4KB, style.css 6.5KB, app.js 40KB}` vanilla JS, canvas radar, 300px right panel, dark theme (`#0e1117` bg, `#161b24` panel, `#3b82f6` friendly, `#ef4444` hostile, font 13px Segoe UI).
- Log line: `M/D/YYYY HH:MM:SS.mmm[±H]  RECORD`. Advanced block gives X, Y, mapID, level, HP/maxHP. `MAP_CHANGE` gives map rect (x0,x1,y0,y1) → coords normalized to 0–100 frame. Class/race are NOT in the parser. Players = `Player-` GUIDs. Reaction from flags (0x10 friendly, 0x40 hostile…).
- `maps/` = 76 PNGs, **88 MB**, plus `index.json`.
- Original history dedupes tracks (≤2yd/<5s), caps 5000 fixes/player. Bounty system / rule-sets exist (NOT ported).

## ARCHITECTURE (final)

```
Browser ──> CloudFront (PriceClass_100, free tier)
             ├─ default: S3 (private, OAC)  index.html, app.js, parser.js, style.css, ads/, maps/
             └─ /api/*  : Lambda Function URL (arm64, node22, 256MB, no API Gateway)
                              └─ DynamoDB single table (on-demand)
Parsing happens IN THE BROWSER (Web Worker). Only deduped player summaries are uploaded.
```

Single table `PK/SK`:

| PK                 | SK        | Data                                                                 |
| ------------------ | --------- | -------------------------------------------------------------------- |
| `K#sha256(apikey)` | `KEY`     | `active`, `cnt` (accounts)                                           |
| `U#username`       | `PROFILE` | scrypt hash+salt, keyHash, createdAt                                 |
| `P#username`       | `G#guid`  | name, reaction, level, maxHp, x, y, mapId, zone, firstSeen, lastSeen |
| `P#username`       | `M#mapId` | name, x0,x1,y0,y1                                                    |
| `CFG`              | `SECRET`  | random HMAC secret (lazy-created; avoids $0.40/mo Secrets Manager)   |

Auth: stateless HMAC-signed token (7d). Signup = DynamoDB transaction: put user (not exists) + key `cnt<5 AND active` increment. Login checks password + key matches user + key active.
Dedupe: client Map by GUID; server BatchGet existing → merge in JS (firstSeen=min, lastSeen=max, newest position wins, maxHp=max) → BatchWrite **only changed** items. All merges idempotent, so re-uploading a log or overlapping logs is harmless and costs only reads.
API: `POST /api/signup|login`, `GET /api/players`, `POST /api/upload` (≤1000 players/req, body < 6MB Function URL limit).
Ads: static `<aside id=ads>` fed by `ads/ads.json`, plain image links, no third-party scripts.
Ops: `scripts/create-key.mjs` mints keys; AWS Budget ($5, email via CDK context) — first two budgets free; Lambda logs 1-week retention; DynamoDB table RETAIN.

## DECISIONS & WHY

1. **Client-side parsing** over S3→Lambda parsing: logs are 100s of MB; Lambda compute + S3 storage + egress would dominate cost. Browser parse = $0, raw logs never stored.
2. **Lambda Function URL behind CloudFront** over API Gateway HTTP API ($1/M req) — free. Auth type NONE (CloudFront OAC+POST needs client-signed payload hash, painful); app does its own token auth.
3. **DynamoDB on-demand** over provisioned: no idle cost, no throttle tuning; storage <25GB free.
4. **Dedupe by read-merge-write-if-changed**, not ADD counters: counters double-count on re-upload. Consequence: event/damage totals dropped (History columns trimmed to Name, Level, Reaction, Max HP, Last seen, Zone).
5. **No per-player tracks stored** (largest storage/write driver). Only last position kept. Feature loss vs local app; revisit only if user wants it (cap e.g. 20 fixes/player).
6. **Node built-in `crypto.scrypt`** for passwords; no dependencies → no bundling step, Lambda uses runtime-provided AWS SDK v3.
7. **Secret in DynamoDB** instead of Secrets Manager/SSM SecureString (CDK can't create SecureString; SM is $0.40/mo).
8. **No custom domain / Route53 / WAF / ACM** ($0.50/mo zone, $5+/mo WAF). Use `*.cloudfront.net`. Optional later.
9. **Maps → WebP** (frontend agent attempts with Pillow) to cut S3/egress and load time; fetched lazily per zone, cached 1yr immutable.
10. ARM64 Lambda (20% cheaper), 256MB, 1-week log retention, no X-ray, no PITR, no versioning on bucket.
11. Keys are admin-minted (script), stored hashed; users cannot create keys.

## REVIEW PASSES (summary)

- **Pass 1**: S3 upload→Lambda parse→DynamoDB with API Gateway ≈ $1–3/mo, Lambda minutes heavy. Rejected.
- **Pass 2**: Browser parse + HTTP API + DynamoDB ≈ $0.10 but API GW adds $1/M req after free year. Replace with Function URL.
- **Pass 3**: Found counter double-count bug on re-upload → idempotent merge; found Secrets Manager $0.40 → DynamoDB secret; found PNG maps 88MB → WebP; found track storage dominates → dropped. Settled.

## PROBLEMS / RISKS (carry forward)

- **Node.js/npm and Python are NOT installed/usable on this machine** → nothing syntax-checked; `npm install`/`cdk synth` never run; maps can't be converted to WebP here (cost table assumes WebP; PNG = 88 MB, still ~free). Install Node 20+, AWS CLI, run `cdk bootstrap`.
- Unverified CDK API usage: `origins.S3BucketOrigin.withOriginAccessControl` (needs aws-cdk-lib >= 2.156), `FunctionUrlOrigin`, `logRetention` (deprecated; adds a helper Lambda — consider explicit LogGroup).
- `scripts/create-key.mjs` needs `@aws-sdk/client-dynamodb` + `@aws-sdk/lib-dynamodb` installed locally (add to devDependencies).
- **Client-trusted data**: a malicious logged-in user can upload fake players. Acceptable (data is per-account, private). Server still validates field types/lengths and caps 1000/req.
- **Brute force**: no rate limit on login beyond scrypt cost (~80 ms). Mitigation idea: failed-attempt counter item with TTL (a few writes/mo). Not yet built.
- **Function URL is publicly reachable directly** (bypasses CloudFront). Cost-abuse exposure is bounded by Lambda free tier then ~$0.20/M req; Budget alert is the safety net. Reserved concurrency not set (new accounts may have low unreserved limits).
- Original UI JS was only smoke-tested in goja, never rendered → frontend clone may carry latent bugs; **visually verify in a browser**.
- Original used live polling/SSE; cloud version is snapshot-only (last known positions). Not "live radar".
- `/api/players` returns all players each load (10k players ≈ 3MB ≈ 375 RRU). Add gzip in Lambda (or paginate) if it grows.
- Pricing figures below are from memory of AWS list prices — re-verify before committing.
- Map images are extracted from WoW game data (README says personal use only): hosting them publicly may be a licensing issue; consider restricting site to logged-in users or removing from public bucket. **Raise with user.**

## COST ESTIMATES (assumption: 5 accounts, 20 uploads/mo, 10k players each, 500 page loads/mo)

| Layer                 | Usage                                                                                      | Monthly cost                                    |
| --------------------- | ------------------------------------------------------------------------------------------ | ----------------------------------------------- |
| CloudFront            | ~3 GB out, ~50k req (free tier: 1 TB, 10M req, permanent)                                  | **$0.00**                                       |
| S3                    | ~30–90 MB storage (WebP vs PNG), few k GETs                                                | **~$0.01**                                      |
| Lambda (arm64, 256MB) | ~5k invocations, ~100 ms avg (free: 1M req, 400k GB-s)                                     | **$0.00**                                       |
| Function URL          | included                                                                                   | **$0.00**                                       |
| DynamoDB writes       | first-time 50k items ≈ 50k WRU × $0.625/M ≈ $0.03 one-off; steady state only changed items | **~$0.01**                                      |
| DynamoDB reads        | uploads BatchGet 200k×0.5 RRU + page loads ~190k RRU × $0.125/M                            | **~$0.03**                                      |
| DynamoDB storage      | ~15 MB (free to 25 GB)                                                                     | **$0.00**                                       |
| CloudWatch Logs       | 1-wk retention, tiny                                                                       | **~$0.00–0.02**                                 |
| Budgets               | first 2 free                                                                               | **$0.00**                                       |
| **Total**             |                                                                                            | **≈ $0.05–0.20 / month** (worst realistic < $1) |

## TIME / LATENCY ESTIMATES

| Layer                                            | Estimate                                                            |
| ------------------------------------------------ | ------------------------------------------------------------------- |
| Static page (CloudFront cached)                  | 20–60 ms; map PNG→WebP 1002×668 ~100–300 KB each                    |
| Browser log parse (streaming Worker)             | ~20–50 MB/s → 500 MB log ≈ 10–25 s                                  |
| Upload batches (1000 players, gzip-less ~300 KB) | ~0.5–2 s each; 10k players ≈ 5–15 s sequential (can run 3 parallel) |
| Lambda cold start (node22 arm64, no deps)        | ~150–300 ms; warm ~5–20 ms + DDB 5–15 ms/op                         |
| scrypt login/signup                              | ~60–100 ms                                                          |
| `GET /api/players` 10k items                     | ~300–700 ms query pages + render                                    |
| Deploy (`cdk deploy`, first time)                | ~5–8 min (CloudFront dominates)                                     |

## FILE MAP (target)

```
radar-cloud/
  PLAN.md  package.json  cdk.json  tsconfig.json
  bin/app.ts          lib/radar-stack.ts
  lambda/index.mjs (router) + lambda/src/*.mjs (modules, see lambda/ARCHITECTURE.md)    scripts/create-key.mjs
  site/ index.html style.css app.js parser.js ads/ maps/
```

## NEXT STEPS FOR A FUTURE SESSION

1. Check `site/` contents; run `node --check` on JS; open in browser (mock `/api` or deploy) and compare to `go-radar` UI.
2. Finish/verify CDK + Lambda; `npm i && npx cdk synth`.
3. Write a quick local test of Lambda handler with an in-memory DynamoDB stub (signup cap of 5, inactive key rejected, re-upload no-op).
4. Ask user: AWS profile/region, budget email, map-hosting licensing stance, whether tracks/event counters matter.
5. `cdk deploy`, run `node scripts/create-key.mjs`, smoke test signup→upload→view.

---

# SCOPE CHANGE v2 (user request: "support ALL dynamic data of the Go app") — SUPERSEDES decisions 4 and 5 above

Go-app dynamic features to cover: live radar (pips, stale 5m/expire 15m, approx fixes weight 0.3, estimate, me, follow, range), per-player history aggregates (events, srcEvents, dmg total/hits/events, hp sum/samples, maxHp, pastNames, first/last seen, fixes), tracks (dedupe 2yd/5s, cap 5000/player, round 0.1yd), search + track display, bounty (killing blows x10, assists 0, death dedupe, analyze), rule-sets (normal/pvp/rp/rppvp auto-detected from realm token; separate history+bounty each), settings (map calibration etc.), map picker/noImage. (Python-only Heatmap: not ported.)

## V2 DECISIONS

12. **Live view stays 100% in the browser** ($0): ported tracker.go runs in a Web Worker, tails the log via File System Access API (`showDirectoryPicker`, poll `file.slice(offset)` ~1s; Chromium/Edge only). Firefox/Safari fall back to manual upload + cloud snapshot view.
13. **Exactly-once counters via ingest ledger** (replaces "no counters"): fingerprint `fp` = hash(first 64KB + file name); ledger item `PK=L#user#rs, SK=fp` holds `lastTs` watermark. Client asks `GET /api/ledger`, parses only events with ts > watermark, sends additive deltas `d`; each batch has `batchId`; player item stores `lb` (last batchId) so a retried batch is skipped per player. Known minor risk: crash between player writes and ledger update is safe because of `lb`.
14. **Tracks stored as chunk items** `PK=T#user#rs, SK=guid#seq` (≤~300 fixes/chunk, ~12KB max, typically ~1KB); per-player cap 5000 fixes by deleting oldest chunk; read only on search (`GET /api/track`). Keeps `GET /api/players` small.
15. **Rule-set** = suffix on partition keys (`P#user#rs`, `T#user#rs`, `L#user#rs`); `rs` query/body param; default from `/api/settings`.
16. **Settings** item `PK=U#user, SK=SETTINGS` (≤8 KB JSON: ruleset, per-map calibration, UI prefs).
17. Bounty is derived from per-player `kb`/`assists` counters (no separate death-id list; the watermark gives exactly-once). "Analyze logs" = `POST /api/reset` (clears bounty/all for rs + ledgers) then client re-parses chosen logs.

## API v2 CONTRACT (all JSON, Bearer token, `rs` ∈ normal|pvp|rp|rppvp)

- `POST /api/signup|login` (unchanged) ; `GET /api/me` → {username, settings, rulesets:[…]}
- `GET|PUT /api/settings` (PUT body = JSON ≤8KB)
- `GET /api/ledger?rs=&fp=` → {lastTs} (0 if none)
- `POST /api/upload` {rs, fp, from, to, batchId, final, players:[{guid,name,pastNames?,reaction,level,x,y,mapId,zone,posTime,firstSeen,lastSeen, d:{events,srcEvents,dmgTotal,dmgHits,dmgEvents,hpSum,hpSamples,maxHp,kb,assists}, fixes:[[t,mapId,x,y,kind]]}], maps:[{id,name,x0,x1,y0,y1}]} → {received,changed}. ≤500 players/req. `d` counters are ADDED (maxHp=max); firstSeen=min; lastSeen/pos newest-wins; ledger lastTs set to `to` when `final`.
- `GET /api/players?rs=&q=&mapId=&reaction=` → {players:[history rows incl. aggregates + lastX,lastY,lastMap,lastZone,posTime,fixes count], maps}
- `GET /api/track?rs=&guid=&map=` → {fixes:[{t,mapId,zone,x,y,kind}]}
- `GET /api/bounty?rs=` → {entries:[{guid,name,kb,assists,score}]} ; `GET /api/bounty/player?rs=&guid=` → entry + rank
- `POST /api/reset` {rs, what:'bounty'|'all'}

## V2 COST DELTA

| Item                                                                                                                   | Estimate                                                                                                                |
| ---------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------- |
| Track storage (≤250k fixes/acct ≈ 10 MB JSON) ×5                                                                       | ~50 MB → free tier                                                                                                      |
| Full first ingest (10k players + 250k fixes)                                                                           | ~20k WRU ≈ $0.01 one-off                                                                                                |
| Live auto-upload every 5 min, ~50 active players (~3 WRU each)                                                         | ~43k WRU/day ≈ $0.03/day while playing 24h; ~3 h/day ≈ **$0.10/mo per active user** (main cost driver; knob = interval) |
| Reads (ledger + BatchGet per upload, track/search queries)                                                             | < $0.05/mo                                                                                                              |
| Lambda invocations (live uploads, ≤ ~9k/mo/user)                                                                       | inside 1M free                                                                                                          |
| **New total (5 heavy users)**                                                                                          | **≈ $0.3–1.0 / month** (was $0.05–0.20)                                                                                 |
| Latency: live view is local (≈ real-time, 1 s poll); track fetch ≈ 100–300 ms; ledger+upload batch of 500 ≈ 0.5–1.5 s. |

## V2 RISKS

- Chromium-only live tailing. Settings/ledger fingerprints depend on first 64KB being stable (WoW logs append-only: OK).
- Client-computed counters are trusted (per-account private data).
- If the live-upload interval is shortened, write cost scales linearly.

---

# SCOPE CHANGE v3 — 5-second updates scoped to what the user is viewing, minimal DB reads

Read-cost hierarchy (cheapest first): **specific player > zone (+ faction group) > whole account**.

## V3 DECISIONS

18. **Single poll endpoint** `GET /api/poll?rs=&scope=player|zone|all&guid=&mapId=&grp=friendly|hostile|other|any&since=<serverMs>&v=<ver>&track=0|1&tsince=<ms>`.
    Returns `{changed:false, v, now}` when nothing new, else `{changed:true, v, now, players:[delta rows]}` (player scope: `{player, fixes}`). `since` = the `now` returned by the previous response (server clock, no skew). `since=0` = initial full load of that scope.
19. **player scope = one GetItem** (0.5 RRU eventually-consistent); it is its own change gate (`lastSeen > since`). Track tail (`track=1`) = Query newest chunk only, `Limit 1`, `ScanIndexForward=false`, only when `lastSeen` changed.
20. **zone scope**: gate = GetItem on version item `PK=V#user#rs, SK=VER` with ProjectionExpression of `v` and `z<mapId>` (0.5 RRU). If `z<mapId> > since` → Query GSI **`zone-idx`** (PK `zk`=`user#rs#mapId#grp`, SK `ls`=lastSeen number) with `ls > since`: returns only players changed since last poll. grp ∈ friendly|hostile|other(neutral+unknown); `any` = 3 queries. Projection INCLUDE [name, reaction, level, maxHp, x, y, mapId, zone, firstSeen, lastSeen, posTime].
21. **all scope**: gate = version item `v`; only if changed do the full base-table Query (existing /api/players path).
22. **Upload writes** one extra UpdateItem on the version item per upload (`ADD v 1`, `SET z<mapId> = :now` for each map touched). Player items carry `zk` and `ls` attributes (set whenever position known; REMOVE when not).
23. **Client poll loop**: 5 s `setTimeout` chain (no overlap); pauses when `document.hidden`; backs off to 30 s after 2 min unchanged, snaps back to 5 s on change/visibility/user input; scope switches with the UI (search selection → player; map selected → zone+grp from Blue/Red checkboxes; else all). Local live mode (File System Access tailing) is unaffected (1 s, local, free).
24. Players who leave a zone are not "removed" by the zone delta; client ages them out with the existing stale/expire logic.
25. Freshness of cloud data is bounded by the **uploader interval** (default 60 s, min 5 s, configurable); 5 s polling only helps when another session is uploading.

## V3 COST

| Item                                                                         | Estimate                                                                                                                                                    |
| ---------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Steady poll (any scope)                                                      | 0.5 RRU / 5 s = 8,640 RRU/day ≈ **$0.001/day ≈ $0.03/mo per always-open viewer**                                                                            |
| Changed-poll extras                                                          | GSI Query on delta only (~0.5–2 RRU); player GetItem 0.5 RRU                                                                                                |
| GSI write amplification                                                      | +1 WRU per changed player with position (≈ +30–50% write cost)                                                                                              |
| Version item                                                                 | +1 WRU per upload                                                                                                                                           |
| Lambda invocations                                                           | 17,280/day/viewer ≈ 518k/mo; 5 always-on viewers ≈ 2.6M/mo → ~1.6M over free 1M ≈ **$0.32/mo** (visibility pause + 30 s back-off typically cuts this 3–10x) |
| CloudFront                                                                   | req inside permanent 10M free tier                                                                                                                          |
| **New total (5 heavy users, always-open tabs)**                              | **≈ $0.5–1.5 / month**; typical with back-off ≈ $0.3–0.8                                                                                                    |
| Latency: poll unchanged ≈ 15–40 ms warm (GetItem ~5 ms); changed ≈ 30–80 ms. |

## V3 RISKS

- GSI is eventually consistent (typically <1 s lag): a delta can miss a write landing between `now` and the Query; client re-requests with `since = now - 2000` overlap and dedupes by guid.
- 5 s + 5 always-on tabs is the cost driver; no server push (WebSocket/API GW would cost more).

---

# STATUS UPDATE (end of session 1)

Implemented v1+v2+v3. **Proven by tests (portable Node 22.23.4):** `npm test` = 22 API tests (in-memory DDB stub incl. GSI w/ INCLUDE projection, read-op counting) + 12 CDK assertion tests + 97 frontend tests (parser, tracker, engine, ingest, poller, radar, shell w/ fake DOM); `cdk synth` clean; `npm run smoke` = 17 black-box checks against the local server.
**Local imitation of production:** `npm run dev` (or `dev:sim` = demo data + simulated live uploader every 5 s) → http://127.0.0.1:8787 ; real `site/`, real `lambda/index.mjs` (+ `lambda/src/`), stub DynamoDB persisted to `dev/.data.json`, simulated latency, 6 MB cap, S3-style 403, per-request `reads=/writes=` log. Keys: `dev-key-0001` (active), `dev-inactive-key`; demo login `demo / demo-password-1`. `node dev/make-sample-log.cjs [--append]` makes a fake combat log for upload/live-tail testing.
**Measured (local):** unchanged poll = 1 DDB read op; player poll = 1 GetItem (+1 Query only with track tail); zone/all initial load = 1 gate + queries; grp=hostile never queries friendly.

## OPEN PROBLEMS / NOT PROVEN

- **UI never rendered in a real browser** (canvas, real Web Worker, File System Access API untested). First thing next session: open `npm run dev:sim`, eyeball vs `go-radar` UI.
- Real DynamoDB behaviour (throttling, transaction semantics, GSI lag) untested; stub only. Real deploy not done.
- Portable Node lives in the session scratchpad (temp) — reinstall Node 22 LTS for real use. Windows PowerShell 5.1 `Set-Content -Encoding utf8` writes a BOM → breaks JSON (caused one failure); use WriteAllText w/o BOM.
- Deviations: gate uses server write time `ut` (not log time); same-ms watermark events can be lost; neutral/unknown skipped when only Red ticked; moved players not announced in old zone; <64 KB live logs defer upload; fixes lack zone; `reset bounty` also deletes ledgers (client always uses `all`).
- CDK: invalidation now `/*` (fixed — new script files were not in the old path list).

---

# STATUS UPDATE (end of session 1, final)

**Refactor/readability loop (target: independent reviewer score >= 95% on a 20-item rubric):** round 1 = 80%, round 2 = 90%, round 3 = **95% (target met, loop stopped)**. Optional leftovers from the reviewer: replace ~43 name-echo @param descriptions in site/ (e.g. '@param {Object} f fix') with real units/shapes and extend scripts/lint.mjs to reject them; delete unused PollScope typedef (site/types.js:102,112); rename single-letter params V/o/k/f in canvas.js, blips.js, engine-protocol.js; reword 'polls' to 'sweeps' in README.md (:22, :58, :62, :65, :176-177). To re-score: re-run the strict reviewer (rubric items: modules, headers, function length <=40 / nesting <=3, named constants, JSDoc on exported+non-trivial functions with real information, naming per NAMING.md, error catalogue, central keys, WHY comments, CDK tidy, dev/scripts, package.json, tests, no lambda deps, style, security, docs accuracy, HTML/CSS, globals, NAMING compliance) and fix anything under 5.
**Verified green at last check (portable Node 22):** `npm test` = 22 API + 12 CDK + 121 frontend; `npx tsc --noEmit` clean; `cdk synth` 0 warnings; `npm run lint` (incl. doc-filler check) + `npm run check:docs` + `npx prettier --check .` pass; `node dev/smoke.mjs` 17/17 against `node dev/server.mjs --demo --fresh`.
**Naming:** see NAMING.md (radar vocabulary; wire names frozen: /api/* paths incl. /api/poll, JSON fields, DynamoDB keys, env vars TABLE/MAX_ACCOUNTS_PER_KEY, outputs SiteUrl/TableName).
**Deploy caution:** construct/stack ids were renamed (stack `RadarStation`, ContactsTable, RelayFunction, HullBucket, Skyline, FuelGauge). Never deployed, so no migration issue unless an older version is deployed first (the table is RETAIN).
**Still unproven:** UI never opened in a real browser; real AWS (DynamoDB throttling/GSI lag/transactions) untested; nothing deployed. Portable Node is in the session scratchpad (temp): install Node 22 LTS for real use. Run locally without npm: `run-local.bat [--sim] [--fresh]`.
**Open questions for the user:** map-image licensing (public hosting of extracted game art), AWS region + budget email at deploy time.

---

# NOTE: time zones

Stored, uploaded and API times are UTC epoch-millisecond instants (wire format unchanged: `firstSeen`, `lastSeen`, `posTime`, fix `t`, ledger `lastTs`, `since` stay numbers). The browser engine normalises each log line to a UTC instant at parse time: a line's own offset (`-5`, `+5:30`) wins; a line without one is read in the operator's `timezone` setting (IANA name in `/api/settings`, default: the browser's zone), first occurrence for an ambiguous DST hour, pre-change offset for a nonexistent one. `site/time-zone.js` also gives `toUtcIso` (UTC with an explicit `+00:00`, display/export only) and `formatInZone` (every on-screen time, with a short offset label). Local time is display-only. `dev/make-sample-log.cjs --no-offset` exercises the fallback path. Not done: bounty rows carry no timestamps, so the bounty panel has nothing to convert; a live engine keeps the zone it had when each line was parsed (changing the zone later does not re-read earlier lines).
