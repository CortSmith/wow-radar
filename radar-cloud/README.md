# Radar Cloud

A hosted version of the local Go radar (`../go-radar`). Same look and the same dynamic features, but your
data lives in your own AWS account so you can reach it from any browser. It is built to cost almost nothing
(estimated **$0.3 to $1 a month** for ~5 active accounts; see [PLAN.md](PLAN.md) for the cost model).

**Status:** unit/integration tests and `cdk synth` pass; the local server runs. The UI has not yet been
opened in a real browser and nothing has been deployed to AWS. See "Known limits" below.

## How it works

```
Browser ──> CloudFront ──┬─ default      : private S3 bucket  (static page, scripts, maps, ads)
                         └─ /api/*       : Lambda Function URL ──> DynamoDB (one table)
```

- **Parsing happens in your browser** (a Web Worker). Raw combat logs are never uploaded or stored. Only
  small per-player summaries are sent.
- **The page is a static shell** (`site/index.html`); all character data is filled in from JSON.
- **Live radar is local.** In Chrome/Edge the page watches your WoW `Logs` folder (File System Access API)
  and updates every second with no cloud calls. Firefox/Safari fall back to manual upload plus the cloud view.
- **Cloud view polls every 5 s**, scoped to what you are looking at, to keep database reads minimal.

### What is stored (per account, per rule-set)

| Data     | Notes                                                                                                                                        |
| -------- | -------------------------------------------------------------------------------------------------------------------------------------------- |
| Players  | name, past names, reaction, level, max HP, last position/zone, first/last seen, event and damage counters, HP samples, killing blows/assists |
| Tracks   | position fixes per player (deduped 2 yd / 5 s, rounded to 0.1 yd, capped at 5000 per player)                                                 |
| Maps     | zone rectangles seen in the logs                                                                                                             |
| Settings | rule-set, map calibration, UI prefs, display time zone (max 8 KB)                                                                            |
| Ledger   | per-log-file watermark so a log is never counted twice                                                                                       |

Rule-sets are `normal`, `pvp`, `rp`, `rppvp`. Each keeps its own players, tracks and bounty.
Class and race are not in the combat log, so they are not stored. "Hostile" means hostile **to the person who
recorded the log**, not necessarily the other faction.

### Time zones

Every stored time is a **UTC instant** (epoch milliseconds in the database and the API; there is no local time in the
data). Your browser converts each log line to UTC while parsing: a line's own offset (`-5`, `+5:30`) is used, and a
line without one is read in the **time zone chosen in the header** (default: your browser's zone). The picker only
changes how times are displayed (with a label such as `EST` or `GMT+5:30`) and takes effect immediately; hover a time
or see the upload summary for the same instant as ISO UTC with `+00:00`. `node dev/make-sample-log.cjs --no-offset`
writes a log without offsets to try the fallback.

### Duplicates

Every log file gets a fingerprint (first 64 KB plus file name). The server remembers how far into it has been
counted, and the page only sends newer events. Every batch has an id, so a retried upload is ignored.
Re-uploading a log, or uploading overlapping logs from the same session, will not double count.

### Accounts and API keys

- Sign up and log in with **username + password + API key**.
- A key must be **active**, and **at most 5 accounts** can be created per key.
- Keys are created by you (the admin) and stored hashed. Users cannot create keys.
- Passwords are hashed with scrypt. Sessions are 7-day signed tokens.
- Deactivating a key blocks new logins and sign-ups; an already-issued token keeps working until it expires.

### Ads

A static column on the left. Edit `site/ads/ads.json` (`[{ "img", "href", "alt" }]`) and drop images in
`site/ads/`. They are plain image links (`rel="sponsored noopener"`), with no third-party scripts. The column
is hidden below 900 px wide.

### Polling cost ladder

`GET /api/poll` reads the least data that answers the question:

| You are viewing                             | Unchanged poll      | Changed poll                                                |
| ------------------------------------------- | ------------------- | ----------------------------------------------------------- |
| One player                                  | 1 GetItem           | same item (+ newest track chunk if tracks shown)            |
| A zone (optionally friendly / hostile only) | 1 tiny version read | only players changed since the last poll, from a zone index |
| Everything                                  | 1 tiny version read | full account read                                           |

The page pauses when the tab is hidden and slows to every 30 s after 2 quiet minutes. Cloud data is only as
fresh as the uploader: the live session uploads every 60 s by default (minimum 5 s).

## Run it locally (no AWS, no npm)

`run-local.bat` starts a local imitation of production: the real `site/` files, the real Lambda code, and an
in-memory stand-in for DynamoDB, with simulated latency, a 6 MB request cap and S3-style 403s.

```
run-local.bat            demo data
run-local.bat --sim      demo data + a simulator that moves players and uploads every 5 s
run-local.bat --fresh    wipe saved local data first
```

Then open <http://127.0.0.1:8787> and log in with `demo` / `demo-password-1` and API key `dev-key-0001`
(`dev-inactive-key` exists to test rejection). Each request prints its database `reads=` / `writes=`.
Local data is saved to `dev/.data.json`.

It needs only a `node` executable. The launcher uses `node` from PATH, or falls back to the Node bundled with
VS Code. Otherwise install Node.js 22 LTS.

To test uploads, make a fake log and upload it in the page (or point live mode at its folder):

```
node dev/make-sample-log.cjs [out.txt]            one-shot log
node dev/make-sample-log.cjs [out.txt] --append   keeps growing, simulates a live session
```

## Tests

```
npm install     (once)
npm test        API (in-memory DynamoDB) + CDK template + frontend logic
npm run smoke   black-box checks against a running local server
```

Without npm, the API and frontend suites run with plain Node: `node --test test/api.test.mjs` and
`node --test test/site/*.test.js`. The CDK template tests need `npm install`.

## Deploy to AWS

Prerequisites: Node.js 22, the AWS CLI configured with credentials, and a one-time `npx cdk bootstrap`.

```
npm install
npx cdk deploy -c budgetEmail=you@example.com
```

`budgetEmail` is optional; it adds a $5/month budget alert (the first two budgets are free). The deploy prints
`SiteUrl` (a `*.cloudfront.net` address) and `TableName`.

Create an API key (needs the AWS SDK installed via `npm install`, and AWS credentials):

```
set TABLE=<TableName from the deploy output>
node scripts/create-key.mjs                       prints a new active key
node scripts/create-key.mjs --deactivate <key>
```

Give the key to up to 5 people; each signs up on the page with their own username and password.

## Using the page

1. Sign up or log in.
2. **Live:** choose your WoW `Logs` folder (Chrome/Edge). The radar follows the newest `WoWCombatLog*.txt`.
   New data is uploaded in the background on the interval you set.
   **Or upload:** drop a `WoWCombatLog*.txt` file; it is parsed locally and only new data is sent.
3. Use the rule-set switch, map picker, range slider, Follow me, Blue/Red/Stale filters and search exactly as
   in the desktop app. Selecting a player shows their track. The Bounty panel ranks killing blows.
   **Analyze logs** wipes the stored data for the current rule-set and re-reads the logs you pick.
4. Map calibration (Shift+arrows, Shift +/-, Shift+0) is saved to your account.

## API (all JSON, `Authorization: Bearer <token>` except sign-up/login)

| Endpoint                                                             | Purpose                                                                              |
| -------------------------------------------------------------------- | ------------------------------------------------------------------------------------ |
| `POST /api/signup`, `POST /api/login`                                | `{username, password, apiKey}` returns `{token, username}`                           |
| `GET /api/me`                                                        | account and settings                                                                 |
| `GET`/`PUT /api/settings`                                            | UI settings (8 KB max)                                                               |
| `GET /api/ledger?rs=&fp=`                                            | how far a log file has been ingested                                                 |
| `POST /api/upload`                                                   | `{rs, fp, from, to, batchId, final, players[], maps[]}`, 500 players max per request |
| `GET /api/players?rs=&q=&mapId=&reaction=`                           | player list with stats                                                               |
| `GET /api/poll?rs=&scope=player\|zone\|all&guid=&mapId=&grp=&since=` | cheap change polling                                                                 |
| `GET /api/track?rs=&guid=&map=`                                      | one player's track                                                                   |
| `GET /api/bounty?rs=`, `GET /api/bounty/player?rs=&guid=`            | bounty ranking                                                                       |
| `POST /api/reset`                                                    | `{rs, what: 'bounty' \| 'all'}`; the page always uses `all`                          |

`rs` is one of `normal`, `pvp`, `rp`, `rppvp` (defaults to your saved setting).

## Layout

```
bin/ lib/       CDK app, stack RadarStation (ContactsTable, RelayFunction, HullBucket, Skyline, optional FuelGauge)
lambda/         the API: index.mjs (router) + src/ modules, see lambda/ARCHITECTURE.md
                (no dependencies; uses the AWS SDK built into the Lambda runtime)
site/           static page: index.html, style.css, app.js + one module per responsibility
                (see site/ARCHITECTURE.md), ads/, maps/ (WebP, ~15 MB)
scripts/        create-key.mjs
dev/            local server (harbor.mjs = shared constants), demo seeding, sample-log generator, smoke test
test/           API, CDK and frontend tests
PLAN.md         decisions, cost and time estimates, risks, history
```

## Known limits

- The live radar needs Chrome or Edge. Other browsers get upload plus the cloud snapshot view.
- Not a live radar in the cloud: other sessions only see what was uploaded.
- Same-millisecond events at an upload checkpoint can be lost; live logs under 64 KB wait to upload.
- Players who leave a zone are aged out as stale, not removed by the zone poll.
- With only Red ticked the zone poll asks for `hostile`; neutral/unknown players are skipped.
- The Python app's Heatmap is not ported.
- The map images are extracted from game data; your root README says they are for personal use. Consider
  that before sharing the site publicly.
- Always-open tabs are the main cost driver (about 17k requests per day per tab at 5 s).
