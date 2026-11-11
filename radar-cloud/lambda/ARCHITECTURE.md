# lambda/ architecture

One Lambda (Function URL, payload v2) serves the whole API. `index.mjs` is a thin router; everything else is in
`src/`, one responsibility per module (every file starts with a header comment saying which). Names follow
`../NAMING.md`; HTTP paths, JSON fields, DynamoDB keys, env vars and query params are frozen wire names.

## Request flow

```
event -> index.mjs  readJsonBody (POST/PUT) -> route lookup -> verifyPass (unless public) -> handler -> jsonResponse
                    any throw -> errorResponse  ({ "error": message }, HttpError status or 500)
```

## Route to modules to keys

| Route                               | Handler module                                                            | Key items touched (formats in `src/keys.mjs`)    |
| ----------------------------------- | ------------------------------------------------------------------------- | ------------------------------------------------ |
| POST /api/signup, /api/login        | `operators.mjs`, `pass.mjs`                                               | `U#<user>/PROFILE`, `K#<hash>/KEY`, `CFG/SECRET` |
| GET /api/me, GET/PUT /api/settings  | `console-settings.mjs`                                                    | `U#<user>/SETTINGS`                              |
| POST /api/upload                    | `pings.mjs` (+ `contact-merge`, `charts`, `trails`, `tripwire`, `ledger`) | `P#`, `T#`, `V#`, `L#` partitions                |
| GET /api/players                    | `contacts.mjs` (+ `contact-rows`, `charts`)                               | `P#<user>#<rs>` (`G#`, `M#`)                     |
| GET /api/poll                       | `sweep.mjs` (+ `tripwire`)                                                | `V#/VER`, `P#/G#<guid>`, GSI `zone-idx`          |
| GET /api/track                      | `trails.mjs`                                                              | `T#<user>#<rs>/<guid>#<seq>`, `P#/G#`, `P#/M#`   |
| GET /api/ledger                     | `ledger.mjs`                                                              | `L#<user>#<rs>/<fingerprint>`                    |
| GET /api/bounty, /api/bounty/player | `bounty.mjs`                                                              | `P#<user>#<rs>/G#`                               |
| POST /api/reset                     | `reset.mjs`                                                               | `P#`, `T#`, `V#`, `L#`                           |

## Every module in src/

| Module                       | Responsibility                                                                   |
| ---------------------------- | -------------------------------------------------------------------------------- |
| `operators.mjs`              | operator sign-up and log-in, gated by a beacon                                   |
| `pass.mjs`                   | pass (session token) issue/verify, signing secret, constant-time compare         |
| `console-settings.mjs`       | settings document (incl. the optional `timezone`), /api/me, ruleset resolution   |
| `pings.mjs`                  | upload orchestration (validate, merge, write, trip the wire, advance the ledger) |
| `contact-merge.mjs`          | pure parse and merge of one contact (idempotent)                                 |
| `contact-rows.mjs`           | stored contact to wire row (full and zone-index shapes)                          |
| `contacts.mjs`               | contact list endpoint and filters                                                |
| `charts.mjs`                 | map calibration rectangles: parse, change detection, wire shape                  |
| `trails.mjs`                 | fix cleaning, chunked trail storage, trimming, /api/track                        |
| `tripwire.mjs`               | version item (change signal) and zone-idx key stamping                           |
| `sweep.mjs`                  | sweep endpoint (/api/poll): player, zone and all sectors                         |
| `ledger.mjs`                 | per-log-file ingest watermark                                                    |
| `bounty.mjs`                 | killing-blow ranking                                                             |
| `reset.mjs`                  | wipe bounty or all data of a ruleset                                             |
| `config.mjs`                 | every limit, pattern and tunable (unit-suffixed constants)                       |
| `messages.mjs`               | catalogue of all error texts (built from config limits)                          |
| `keys.mjs`                   | the only builder of PK/SK/GSI keys, with the key table                           |
| `types.mjs`                  | JSDoc typedefs of stored items                                                   |
| `db.mjs`                     | DynamoDB client and read/write/query/batch helpers                               |
| `http.mjs`, `http-error.mjs` | body parsing, JSON/gzip responses, `HttpError`                                   |
| `validate.mjs`, `utils.mjs`  | input clamping and typed query parsing; generic helpers                          |

## Design notes

- **Idempotent pings:** `lb` (last ping id) on a contact makes a replayed ping a no-op; fixes are watermarked by the
  stored last fix; unchanged contacts are not rewritten; the ledger moves last.
- **Cheap sweeps:** a sweep reads the tripwire first; only if it moved does it query. The GSI is eventually
  consistent, so clients overlap their `since` window.
- **Security:** all input is clamped/validated before use; passes are HMAC-signed and compared in constant time;
  login always hashes the password (uniform timing); beacons are stored as SHA-256; no secret or request body is logged.
- **Time is UTC.** Every stored or returned time is a UTC epoch-millisecond number (`firstSeen`, `lastSeen`, fix `t`,
  ledger `lastTs`, `since`); the server never converts zones and has no ISO format in the wire or the table. The only
  zone-related field is the display-only `timezone` in the settings document: `PUT /api/settings` validates it with
  `isTimeZoneName` (`validate.mjs`: pattern, length, `Intl.DateTimeFormat`) and answers 400 with
  `MESSAGES.settingsTimezoneInvalid`; it counts toward the 8 KB cap and may be absent (clients then use their own zone).
- The AWS SDK comes from the Lambda runtime (no runtime dependencies); tests stub `@aws-sdk/lib-dynamodb` by specifier.
