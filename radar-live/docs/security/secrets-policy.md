# Secrets policy (binding for all stages)

**Rule:** the repository never contains secrets, auth tokens, API keys, passwords, personal information, real player names or real
combat-log content. Anything that would hold such a value is read from **environment variables specific to the application**
(the `RADAR_` prefix, one variable per value), never from a file in the repo and never from a default in code.

## What counts
Ingest keys, view keys, session/UI tokens, passwords, certificates and private keys, tunnel tokens (Cloudflare etc.), real
account/character names, real IP addresses or hostnames of the operator's machines, e-mail addresses, anything copied from a real
combat log or a real database, personal paths such as `C:\Users\<name>\...`.

## How it works
- Secret settings are read **only** from env vars (or from a file outside the repo whose *path* is given by an env var, e.g.
  `RADAR_VIEW_KEYS_FILE`; the file is created on the host by `install.sh`, mode 0600/0640, never in git).
- Names: `RADAR_VIEW_KEYS`, `RADAR_INGEST_KEYS`, `RADAR_DB_KEY` (relay -> db), `RADAR_INGEST_KEY` (client/feeder -> ingest),
  `RADAR_CLIENT_*` for the desktop client, `RADAR_TUNNEL_TOKEN` for tunnels. Flags that would take a secret on the command line are
  not offered (command lines leak through process lists); the env var is the only way. Non-secret settings may still use flags.
- **Documented exception (stage 15, radar-armory):** the Battle.net credentials use user-mandated names without the `RADAR_` prefix:
  `WOW_ARMORY_API_KEY` (holds the OAuth2 client secret) and `WOW_ARMORY_CLIENT_ID` (the client id, also treated as a secret), with the
  path variants `WOW_ARMORY_API_KEY_FILE` and `WOW_ARMORY_CLIENT_ID_FILE`. These two are the only such names. The short-lived bearer token
  obtained from them lives in memory only and is never stored or logged. Every other armory setting follows the
  rule: `RADAR_ARMORY_DB_KEY` (the ingest-role key radar-armory sends to radar-db; must be one of `RADAR_INGEST_KEYS`) and
  `RADAR_ARMORY_DB_KEY_FILE`, plus the non-secret `RADAR_ARMORY_*` settings. As for all secrets: env or `*_FILE` path only, no flag
  (`--api-key`, `--client-id`, `--client-secret`, `--db-key` are refused), values redacted everywhere, never in `radar.env.example`
  (only the file paths), files `/etc/radar-live/secrets/armory-client-id` and `/etc/radar-live/secrets/armory-api-key` mode 0640
  root:radar created on the host by hand (docs/armory.md). The repo scan flags a long value assigned to `WOW_ARMORY_API_KEY` (its
  key/secret/token rule); a plain `WOW_ARMORY_CLIENT_ID=<value>` is not a key-shaped name and is covered by the deploy test that
  forbids assigning either variable in `radar.env.example`, not by the scanner.
- Example configuration lives in `deploy/radar.env.example` with **placeholders only** (`CHANGE_ME`), never working values.
  `.env`-style files are git-ignored (see `.gitignore`); only `*.example` files are tracked.
- Keys are generated on the host (`radar-db keygen`), shown once, stored hashed in memory only, never written to logs.
- The desktop client keeps the ingest key out of its settings file: it is read from `RADAR_CLIENT_INGEST_KEY` (or typed into the
  UI for the current run only and held in memory; there is no "remember key" on disk). Non-secret settings (server URL, log path,
  rule-set) may be saved in the per-user settings file outside the repo.
- Tests use obviously fake values (`test-key-0000000000000000`), generated in the test, never real ones. Test fixtures use synthetic
  names only.
- Logs, error messages, status endpoints and crash output must never include a key, token, header value or full environment dump.

## Enforcement
- `.gitignore` blocks env/key/db files. Task 8.13 adds a repo scan test (`internal/mod/guard/secrets_test.go`) that fails on key-like
  strings, private-key blocks, `Authorization`/`X-*-Key` literals with values, personal Windows paths, and e-mail addresses in tracked
  files; plus `scripts/scan-secrets.ps1|sh` for use before every commit.
- Reviewers (the lead's pass at the end of each stage) check diffs against this file.
