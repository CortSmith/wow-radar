# Stage 5: radar-ingest binary (streamapi, relay)

Read `../../PLAN.md` (contract section 4 is binding), `STATUS.md`, Results of stages 1 and 2 (Event, Extractor, Analyzer API).
Work only in `internal/mod/streamapi`, `internal/mod/relay`, `cmd/radar-ingest`.

## Deliverables
1. `streamapi` module (ingest side, `RoleIngest` auth via the same keys scheme; ingest has its own copy of the small key-check
   config via core Auth, it must NOT import the `keys` module; if the ingest binary needs keys it lists the `keys` module in its own
   `modules.go`):
   - `POST /v1/stream?source=&rs=`: reads the (chunked, long-lived) body line by line with a 1 MiB line cap and a read idle timeout
     (`--stream-idle`, default 120 s), feeds each line through `logparse` via `host.Parsers.Feed`, flushes extractor/analyzer output
     every `--flush-interval` (default 2 s) as a `domain.Ping` to `host.Sinks.Publish`; replies with a summary
     `{lines, events, contacts, rejected, durationMs}` when the client closes.
   - `POST /v1/lines?source=&rs=`: one batch (`text/plain` or `{"lines":[...]}`, max lines/bytes configurable), same pipeline, flush at end.
   - `GET /v1/sources`: live connections `{source, rs, since, lines, linesPerSec, lastEvent}` (RoleView or RoleIngest).
   - A per-source session owns its own Extractor/Analyzer (state isolated; cap on concurrent sources `--max-sources`, default 16).
   - Ruleset: `rs` param, else detected from the log (stage 2 detection) and held per session; fallback `--default-ruleset`.
2. `relay` module: a `core.Sink` that batches pings and POSTs them to `--db-url` (`/internal/v1/ping`, `X-Ingest-Key`, timeout 10 s) with
   retry + exponential backoff and jitter, a **bounded** buffer (`--relay-buffer-pings`, drop-oldest + counter), idempotent `batchId`
   per ping (uuid-ish, reused on retry), graceful drain on shutdown (up to `shutdown-timeout`). It only ever dials the configured URL.
3. `cmd/radar-ingest`: `main.go` + `modules.go` (logparse, contactx, bountyx, streamapi, relay [, keys]); default listen `127.0.0.1:8788`.
4. Tests: httptest for both endpoints with synthetic logs from stage 2 testdata; a fake db server (`httptest`) asserting what the relay
   sends (batchId stable on retry, drop-oldest when the buffer is full, backoff with fake clock); end-to-end test wiring a real
   radar-db host (memstore) in-process: stream a synthetic log in, sweep out. Removal tests: no bountyx -> stream still works with no
   kb deltas; no relay -> pings are only published to sinks (and the stream still returns a summary).

## Done when
gofmt/vet/test green, >= 85% coverage per package, linux cross-compile OK, Results appended here, STATUS.md line added.

## Results (Stage 5 complete)

**Built.** `internal/mod/streamapi`, `internal/mod/relay`, `cmd/radar-ingest` (`main.go`, `modules.go`, `defaults.go`), `cmd/radar-ingest/e2e`
(in-process ingest -> relay -> test db handler + memstore; lives in its own dir because only a file named `modules.go` may import modules under `cmd`).
gofmt/vet clean, tests green with `-count=3`, `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./cmd/radar-ingest` OK.

| Package | Coverage |
|---|---|
| streamapi | 97.9% |
| relay | 95.8% |
| cmd/radar-ingest | 85.7% (e2e package 100%) |

**Config keys (flag / `RADAR_<KEY>`).** streamapi: `stream-idle` 120s, `max-sources` 16, `stream-max-bytes` 4 GiB per stream, `lines-max-lines` 20000,
`lines-max-bytes` 8 MiB. relay: `db-url` (required), `db-key` (sent as X-Ingest-Key), `relay-buffer-pings` 5000, `relay-buffer-bytes` 64 MiB,
`relay-batch-pings` 50, `relay-timeout` 10s, `relay-backoff-min` 500ms, `relay-backoff-max` 30s. Ingest auth keys come from the `keys` module
(`ingest-keys`, `view-keys`, ...). Ingest-only defaults (`defaults.go`, applied below flag/env/env file): `listen=127.0.0.1:8788`,
`contactx-flush=2s`, `bountyx-flush=2s`, `max-body=1 TiB`.

**Behaviour.** streamapi never builds pings: per line it calls `host.Parsers.Feed(RawLine{Source, Text, Ruleset})` and registers an observer parser to count
events/distinct players/last event per source. Summary `{source, lines, events, contacts, rejected, durationMs, ended}`; `ended` is
`eof|idle|max-bytes|shutdown|error`. 409 `source_busy` for a duplicate source, 429 `too_many_sources`, 413 for over-cap batches. `GET /v1/sources` accepts
a view or ingest key and returns `{sources:[{source, rs, since, lines, linesPerSec, lastEvent}]}` (times in epoch ms). relay: oldest-dropped bounded buffer
(count and bytes), one POST per ping (batch = up to `relay-batch-pings` per pass), batchId kept (cx-/bx- ids; `rl-` id only if a ping has none), transient
(network, 408, 429, 5xx) retried with exponential backoff + jitter, other 4xx/3xx dropped and counted `Rejected`, no redirects, drain on Stop up to the shutdown timeout.

**Deviations / notes**
- `domain.RawLine` gained optional `Ruleset` (override). logparse's parser copies it into `Event.Ruleset` (3 lines + test); this is the only edit outside scope; contactx/bountyx/logparse/domain tests stay green.
- No `--default-ruleset` on ingest: without `rs` the ruleset is the detected realm token, else `contactx-ruleset` / `bountyx-ruleset` (stage 2 settings).
- The global `max-body` middleware would cap a stream at 8 MiB, so the ingest binary defaults it to 1 TiB and streamapi enforces `stream-max-bytes` itself.
- `/v1/lines` cannot force an immediate flush (no socket for it); pings follow within `contactx-flush` (2 s). The e2e test polls the store.
- Module order in `modules.go` matters: `relay` first so it stops last and drains the final flushes of contactx/bountyx.
- `GET /v1/sources` is open if the view role has no keys, even when ingest keys are set (Auth socket cannot express "either role" otherwise).
- Names in contacts keep the realm suffix (contactx module does not set `RealmSuffix`); stage 2 matter.
- Removal tests: no bountyx (stream + contacts fine, no bounty rows), no relay (pings reach other sinks), no parsers (stream still returns a summary).
