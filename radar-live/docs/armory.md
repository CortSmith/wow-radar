# Armory sync (radar-armory)

> **PROVISIONAL API SHAPE.** radar-armory talks to Blizzard's Battle.net **WoW Classic Profile API**. Blizzard's documentation
> page is rendered by JavaScript and could not be read, so every endpoint path, the namespace, the guild roster call and the
> response field names are community-sourced and **UNVERIFIED**. They live in one file,
> `internal/mod/armoryclient/mapping.go`, and every path, the namespace, region, locale and token URL can be changed with
> `RADAR_ARMORY_*` settings without rebuilding. Expect to adjust them on the first real run. See "What is provisional" below.

## What it is
`radar-armory` is a separate one-shot program. Once a night at **00:00 (the laptop's local time)** systemd starts it. It asks
radar-db which characters to look at (the "seeds"), fetches each one from the armory (level, class, race, faction, guild, gear,
talents, health, power, PvP progress, ...) and sends the result to radar-db in batches of at most 100. Then it exits. It is not
part of the website and not part of radar-ingest; it talks to radar-db only through the loopback **internal** listener
(`/internal/v1/armory/*`), never to the database file.

If the laptop was off at midnight, the missed run starts at the next boot (`Persistent=true`). A run stops by itself after
`RADAR_ARMORY_MAX_RUNTIME` (default 6 h) or `RADAR_ARMORY_MAX_REQUESTS`, whichever comes first, and picks up where it stopped
when started again the same local day.

## How the armory is authenticated (client credentials)
Battle.net uses OAuth2 **client credentials**: a *client id* and a *client secret* identify your application, and the program
exchanges them at the token URL (default `https://oauth.battle.net/token`) for a short-lived **bearer token**. In this project:

| What | Variable (path variant) | File on the laptop |
|---|---|---|
| Battle.net client id | `WOW_ARMORY_CLIENT_ID` (`WOW_ARMORY_CLIENT_ID_FILE`) | `/etc/radar-live/secrets/armory-client-id` |
| Battle.net client **secret** | `WOW_ARMORY_API_KEY` (`WOW_ARMORY_API_KEY_FILE`) | `/etc/radar-live/secrets/armory-api-key` |

Both are treated as secrets. Token handling: the token is requested at the first call of a run, kept **in memory only** (never
written to disk, never logged), refreshed when about 90 % of its lifetime (`expires_in`) is used, and refreshed once more if the
armory answers 401 to a call. Blizzard tokens typically last about 24 hours (UNVERIFIED); a run is far shorter than that. Several
parallel fetches share one token request.

### Reference OAuth request (verified)
This is the token request verified against the real token endpoint on 2026-11-12, and the one radar-armory sends
(`internal/mod/armoryclient`, pinned by `TestTokenRequestMatchesReferenceCurl`). It follows Blizzard's
[client credentials guide](https://community.developer.battle.net/documentation/guides/using-oauth/client-credentials-flow):

```sh
curl -u 'CLIENT_ID:CLIENT_SECRET' -d grant_type=client_credentials https://oauth.battle.net/token
```

POST, HTTP Basic auth with the client id as user and the secret as password, form body exactly
`grant_type=client_credentials`. The token (about 24 h) is then sent as `Authorization: Bearer TOKEN`. The token URL is
`https://oauth.battle.net/token` for US, EU and APAC and `https://oauth.battlenet.com.cn/token` for CN; there is no
per-region token host. To test your own files without typing the values (nothing is echoed or put in an argument):

```sh
sudo sh -c 'printf "user = \"%s:%s\"\n" "$(tr -d "[:space:]" </etc/radar-live/secrets/armory-client-id)" "$(tr -d "[:space:]" </etc/radar-live/secrets/armory-api-key)" |
  curl -s -o /dev/null -K - -d grant_type=client_credentials -w "token: HTTP %{http_code}\n" https://oauth.battle.net/token'
```

`HTTP 200` here but `exit=2` "rejected the client credentials" from radar-armory means radar-armory is not using these
files: look for a `radar-armory: warning: WOW_ARMORY_API_KEY is set directly and overrides ...` line in the journal and
remove the plain `WOW_ARMORY_API_KEY` / `WOW_ARMORY_CLIENT_ID` lines (the ones without `_FILE`) from `radar.env`.

### Get a client id and secret
1. Sign in at the Battle.net developer portal (`develop.battle.net`) with the Battle.net account that should own the app.
2. Open "API Access" (or "Clients") and create a client. The name is free; the redirect URL is not used by this program (client
   credentials have no user login), enter any https URL you control if the form insists.
3. Copy the **client id** and the **client secret**. The secret is shown once; if you lose it, generate a new one.
4. Type both into their files with the recipe below. Do not paste them into chats, tickets, scripts or the repo.

## Where the seeds come from (and why the first night is small)
The armory has no "list all characters" call, so the crawl is driven by what radar-db already knows. radar-db serves the seeds
at `GET /internal/v1/armory/seeds` (ingest key, internal listener only):

1. **Known contacts first**: every player contact seen by radar-ingest (all rulesets, de-duplicated). The realm is taken from the
   `Name-Realm` part of the raw name; a contact without a realm uses `RADAR_ARMORY_DEFAULT_REALM`, and if that is unset the seed
   is skipped and counted as `skipped`.
2. **Guilds from stored armory data**: every distinct guild that appears on a character already stored in `armory_characters`.
   Each guild seed is expanded through the **guild roster** call, and the members are fetched like any other character.

Consequence, the **first night**: no armory data is stored yet, so there are no guild seeds. The first run fetches only the known
contacts and stores them (with their guild names). The **second night** (or a second manual start) then finds those guilds, expands
their rosters and fetches the members. Coverage grows night by night as new contacts and guilds are discovered; it never grows
beyond people who share a guild with, or were seen as, a contact. A seed name that fails client-side validation (for example
characters or guilds with `/`, `?` or other unsafe characters) is **skipped and counted** in `skipped`, never sent to the armory,
and does not turn the run into a partial one. Real fetch failures (network, 5xx, bad JSON) count as `failed`.

## What is stored
In the radar-db SQLite file: `armory_characters` (latest state per realm + name, gear/talents/stats/PvP as size-capped JSON),
`armory_snapshots` (one compact row per character per day, pruned after 30 days), `armory_batches` (replay protection) and
`armory_runs` (the last runs, used to resume). No client id, secret, token, or request or response bodies are stored.
Read it through `GET /api/v1/armory/character?realm=&name=` (view key). Characters that are hidden or unknown are stored with
that status; their old details are kept.

## Enable it
1. Install (or upgrade) with `sudo sh install.sh`. It installs `radar-armory`, `radar-armory.service`, `radar-armory.timer`
   and creates `/etc/radar-live/secrets` (0750 root:radar). The timer is **not enabled** yet.
2. Put the client id and the client secret into their files with the hidden-typing recipe below.
3. Edit `/etc/radar-live/radar.env` (mode 0640, `sudo -e`): `WOW_ARMORY_CLIENT_ID_FILE`, `WOW_ARMORY_API_KEY_FILE` and
   `RADAR_ARMORY_DB_KEY` are already there on a fresh install; on an older install copy those lines from
   `/etc/radar-live/radar.env.example`. `RADAR_ARMORY_DB_KEY` must be one of `RADAR_INGEST_KEYS`. Uncomment and set
   `RADAR_ARMORY_DEFAULT_REALM` (the realm slug, lowercase with hyphens) so contacts without a realm are used, and
   `RADAR_ARMORY_REGION` if you are not on `us`.
4. Run `sudo sh install.sh` again, or `sudo systemctl enable --now radar-armory.timer`. `install.sh` enables the timer only when
   **both** secret files exist and the db key is set; otherwise it prints what is missing.

### Type the client id and secret without showing them
Run this in a normal terminal on the laptop (bash). Nothing is echoed, nothing lands in the shell history, nothing is an argument
of a program, and each file is created private before anything is written to it. Do it once for the id and once for the secret:

```sh
read -rs -p 'Battle.net client id (hidden): ' ARMORY_ID; echo
printf '%s' "$ARMORY_ID" | sudo sh -c 'umask 077; cat > /etc/radar-live/secrets/armory-client-id'
unset ARMORY_ID
sudo chown root:radar /etc/radar-live/secrets/armory-client-id
sudo chmod 0640 /etc/radar-live/secrets/armory-client-id

read -rs -p 'Battle.net client secret (hidden): ' ARMORY_KEY; echo
printf '%s' "$ARMORY_KEY" | sudo sh -c 'umask 077; cat > /etc/radar-live/secrets/armory-api-key'
unset ARMORY_KEY
sudo chown root:radar /etc/radar-live/secrets/armory-api-key
sudo chmod 0640 /etc/radar-live/secrets/armory-api-key
```

`WOW_ARMORY_CLIENT_ID_FILE` and `WOW_ARMORY_API_KEY_FILE` (paths only, in `radar.env`) point at those files; the program reads
each once, trims whitespace and keeps it in memory as a redacted secret. The plain variables `WOW_ARMORY_CLIENT_ID` and
`WOW_ARMORY_API_KEY` (environment only) also work and win if both are set, but are not recommended here because `radar.env` is
also read by radar-db and radar-ingest. Never use `echo NAME=...`, never paste a secret into a chat, a ticket or a commit.

### Verify
```sh
systemctl list-timers radar-armory.timer       # NEXT should be the coming 00:00
systemd-analyze calendar '*-*-* 00:00:00'      # how systemd reads the schedule (host time zone: timedatectl)
sudo systemctl start radar-armory              # run it now (the shell waits until it ends)
journalctl -u radar-armory -n 30 --no-pager    # the run summary
```
The summary is one line of counts, never a secret or a character name:
`radar-armory: status=ok run=r... enumerated=.. fetched=.. stored=.. skipped=.. failed=.. duration=.. exit=0`.
`skipped` counts seeds without a usable realm, seeds that failed validation, and characters whose data was already stored.
`sudo sh /usr/local/share/doc/radar-live/verify-hardening.sh` checks the unit, the timer and the secret files' permissions.

## Exit codes and how failures show
| Code | Status | Meaning |
|---|---|---|
| exit 0 | ok | everything enumerated was stored or skipped |
| exit 1 | partial or interrupted | some characters or rosters failed, a seed or database call failed after retries, the runtime/request budget ended the run, or SIGTERM arrived. The next run resumes. |
| exit 2 | failed | the armory refused the client credentials (token endpoint 400/401, or 401 again after a token refresh), radar-db refused the db key (401/403), or a setting is wrong (missing client id, secret or db key, bad value) |

The unit deliberately has **no** `SuccessExitStatus`: exit 1 and 2 leave it `failed`, visible in `systemctl --failed` and
`systemctl status radar-armory`, so a partial night is not mistaken for a good one. There is no automatic restart and no
`OnFailure=` alert: the alert hook `RADAR_ALERT_CMD` belongs to radar-db and radar-ingest, which radar-armory does not embed.
Check `systemctl status radar-armory` now and then, or wire your own `OnFailure=` drop-in.

`TimeoutStartSec` of the service is 6h 30min, the default `RADAR_ARMORY_MAX_RUNTIME` (6h) plus 30 minutes. If you raise the
runtime, raise the timeout with a drop-in (`sudo systemctl edit radar-armory`).

## Schedule and the backup
The timer fires at 00:00. The nightly backup (`backup.cron`) runs at 03:17, so the two do not start together. A long armory
run (up to 6 h) can overlap the backup. That is safe: the backup is a consistent `VACUUM INTO` copy that works while radar-db
is running, and writes from radar-armory arrive through radar-db like pings. Both only share the disk, so on a very weak
laptop either lower `RADAR_ARMORY_RATE`/`RADAR_ARMORY_MAX_RUNTIME` or move the timer with a drop-in
(`OnCalendar=` empty line, then a new `OnCalendar=`), keeping the 00:00 default otherwise.

## Settings
All in `/etc/radar-live/radar.env`, documented in `radar.env.example`; only the secrets and `RADAR_ARMORY_DB_KEY` are needed.
- Region and endpoints: `RADAR_ARMORY_REGION` (default `us`), `RADAR_ARMORY_BASE_URL` (default `https://REGION.api.blizzard.com`,
  https only), `RADAR_ARMORY_NAMESPACE` (default `profile-classic-REGION`), `RADAR_ARMORY_ROSTER_NAMESPACE` (default: the
  namespace), `RADAR_ARMORY_LOCALE` (`en_US`), `RADAR_ARMORY_TOKEN_URL` (`https://oauth.battle.net/token`), and the paths
  `RADAR_ARMORY_SUMMARY_PATH`, `_EQUIPMENT_PATH`, `_SPECS_PATH`, `_STATS_PATH`, `_PVP_PATH`, `_ROSTER_PATH`.
- Crawl: `RADAR_ARMORY_DEFAULT_REALM`, `RADAR_ARMORY_SEED_PAGE_SIZE` (200), `RADAR_ARMORY_MAX_GUILD_MEMBERS` (1000),
  `RADAR_ARMORY_RATE` (requests/s, default 5), `RADAR_ARMORY_CONCURRENCY` (3), `RADAR_ARMORY_MAX_REQUESTS` (0 = unlimited),
  `RADAR_ARMORY_MAX_RUNTIME` (6h), `RADAR_ARMORY_DB_URL` (default `http://127.0.0.1:8789`), `RADAR_ARMORY_LOCATION` (zone for the
  same-day resume).
- Secrets: `WOW_ARMORY_CLIENT_ID(_FILE)`, `WOW_ARMORY_API_KEY(_FILE)` and `RADAR_ARMORY_DB_KEY(_FILE)`. There are no flags for secrets.

### Region and namespace caveat
The region picks the API host (`us.api.blizzard.com`, `eu...`) and the default namespace `profile-classic-REGION`. Which region
and which **namespace** the "WoW Forever" realms use (Classic Era `profile-classic1x-REGION`, Classic progression
`profile-classic-REGION`, or something else) is **unknown**. A wrong region or namespace shows up as 404 on every character, or
as characters that are all `notfound`. Try the other values with `RADAR_ARMORY_REGION` / `RADAR_ARMORY_NAMESPACE` (and
`RADAR_ARMORY_ROSTER_NAMESPACE` if the roster call needs a different one, for example a `static` namespace) on a manual run.

## What is provisional
Everything below is UNVERIFIED (community-sourced) and isolated in `internal/mod/armoryclient/mapping.go`:
- the host pattern `https://REGION.api.blizzard.com` and the token URL;
- the namespace default `profile-classic-REGION` (and the roster namespace);
- the five character paths (summary, equipment, specializations, statistics, pvp-summary) and the **guild roster** path
  `/data/wow/guild/{realmSlug}/{guildSlug}/roster`, including whether the roster call is available for these realms at all;
- the JSON field names read from those answers, and the realm and name slug rules (lowercase, hyphens);
- the token lifetime and the rate limit (Blizzard documents a per-client request quota; the default `RADAR_ARMORY_RATE=5` is a
  conservative guess).
A real run is the first verification. The unit tests use synthetic fixtures only.

## Rotate the secrets
1. Generate a new client secret at the developer portal (or create a new client and use its id and secret).
2. Run the hidden-typing recipe again for the changed file(s) (it overwrites). Nothing needs restarting: the next run reads the files.
3. `sudo systemctl start radar-armory`, check the summary says `exit=0`. Revoke the old secret at the portal.
To rotate the radar-db side, replace the key in `RADAR_INGEST_KEYS` and `RADAR_ARMORY_DB_KEY` together (see
`docs/security/hardening.md`, key rotation).

## Troubleshooting
| Symptom in `journalctl -u radar-armory` | Cause and fix |
|---|---|
| `warning: WOW_ARMORY_API_KEY is set directly and overrides ..._FILE` | A plain value in `radar.env` (often the `CHANGE_ME` placeholder or an old secret) wins over the secret file. Delete that line, keep the `_FILE` line. |
| `exit=2`, armory rejected the client credentials, 401 | First run the "Reference OAuth request" file test: if it gives 200, see the warning row above. Otherwise: Client id or secret wrong, revoked or swapped. Retype both with the recipe (the id goes in `armory-client-id`, the secret in `armory-api-key`), check `RADAR_ARMORY_TOKEN_URL` (a wrong region token host also gives 401) and that the portal client is active. |
| 403 on single characters | Not an error: the character is hidden or private and is stored with status `hidden`. A 403 on every call usually means the client has no access to that API or namespace: check the namespace and region. |
| 429 or many retries | The client's rate limit. Lower `RADAR_ARMORY_RATE` and `RADAR_ARMORY_CONCURRENCY`. The client honours `Retry-After` and pauses when 429/5xx repeat. |
| every character is `notfound` or 404 | Wrong region, namespace or realm slug: see "Region and namespace caveat"; set `RADAR_ARMORY_DEFAULT_REALM` in slug form. |
| `skipped` is high | Contacts without a realm and no `RADAR_ARMORY_DEFAULT_REALM`, or names that fail validation. Not an error. |
| nothing from guilds on night one | Expected: guild seeds appear only after characters are stored (first night section). |
| radar-db answers 507 | The database is full (`RADAR_DB_MAX_MB`) or the disk is short (`RADAR_DB_MIN_FREE_MB`). The run ends `partial` (exit 1) and resumes later. Free space, see README-xubuntu.md section 6. |
| radar-db answers 401/403 | `RADAR_ARMORY_DB_KEY` is not one of `RADAR_INGEST_KEYS`. exit 2. |
| radar-db answers 404 on `/internal/v1/armory/...` | radar-db is too old (no armory store or seeds route); upgrade with `install.sh`. |
| `exit=2` with `looks like an OAuth access token`, `CHANGE_ME placeholder`, `hold the same value`, `contains spaces` or `contains ':'` | The value is set but cannot work, so the run stops before calling Blizzard. The api-key file must hold the **client secret** from the developer portal, never the `access_token` a token request returns (radar-armory requests its own token every run). Retype the named file with the hidden-typing recipe. |
| `WOW_ARMORY_CLIENT_ID is not set` | The id file is missing or empty; see "Type the client id and secret". |
| Timer never fired | `systemctl list-timers radar-armory.timer`: if not listed, it is not enabled (see Enable it). |

## Security notes
- The client id, secret and tokens are read once, held as redacted secrets, and never appear in logs, errors, the summary line or
  the unit. See `docs/security/secrets-policy.md` (the documented exception for the names `WOW_ARMORY_API_KEY` and
  `WOW_ARMORY_CLIENT_ID`).
- Armory and token URLs are https only, certificates verified, redirects are never followed, responses are size capped.
- The service is sandboxed like radar-ingest (no capabilities, read-only system, only `/var/log/radar` writable, 128 MB cap).
  It cannot touch the database directory. Outbound network is not restricted by address; a firewall rule for the Blizzard hosts
  is optional hardening.
- Character names, guild names and API JSON are untrusted: they are validated and size capped before they reach radar-db.
- The secret files are 0640 root:radar, so radar-db and radar-ingest (user `radar`) could read them too. Stricter option: keep
  the files 0600 root:root, remove the `*_FILE` lines from `radar.env`, and add a drop-in for the service with
  `LoadCredential=armory-api-key:/etc/radar-live/secrets/armory-api-key`, `LoadCredential=armory-client-id:/etc/radar-live/secrets/armory-client-id`
  and `Environment=WOW_ARMORY_API_KEY_FILE=%d/armory-api-key`, `Environment=WOW_ARMORY_CLIENT_ID_FILE=%d/armory-client-id`.
- `install.sh` and the units have not been run on a real Linux host yet (like the rest of `deploy/`); verify on first use.
