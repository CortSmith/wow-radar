# go-radar stage 1: become a radar-live client (same page, same features, own window + settings)

STATUS: PLAN ONLY. Nothing here has been executed. This is go-radar's first plan file (no prior docs/plans here); it is written to
pair with radar-live's `docs/plans/stage-11-radar-ux.md` and `stage-14-world-map.md`, which this plan assumes land first or in parallel
so there is one JS feature set to share instead of two to maintain.

## 0. Reading the request back (confirm this interpretation before starting, since it's a real fork in design)
"Update the local app to behave in the exact same way as the website we are building, except it pulls data from the database we've
created in radar-live instead of viewing it from the ui." Interpreted as: **go-radar's embedded page should become the radar-live page
itself** (the same `site/js` feature modules — multi-select tracking, full greyed list, history tooltips, world map, armory tooltip
rows once it lands), not go-radar's own separate `web/app.js`/`index.html`. The "except... instead of viewing it from the ui" half
means go-radar does not open a browser tab pointed at a hosted radar-live URL and does not screen-scrape/embed that page as a remote
iframe — it talks straight to radar-live's existing JSON read API (`/api/v1/sweep`, `/api/v1/contacts`, `/api/v1/trail`, soon
`/api/v1/profile(s)`, `/api/v1/world-counts` if that lands) with a view key, exactly like the website's own JS already does, just
pointed at whichever radar-live server the user configures instead of being baked into one deployment. If this reading is wrong,
say so before task G1 starts — it's the one decision everything else is built on.

## 1. Why this is a real architecture change, not a UI skin
Today go-radar is a **self-contained** app: `internal/tailer` follows the combat log directly, `internal/parser`+`internal/tracker`
build live state from raw log lines, `internal/history`/`internal/bounty`/`internal/dataset` keep their own on-disk history/ledger, and
`internal/server` serves a **separate**, hand-written UI (`web/index.html` + `web/app.js`, ~700 lines, polling `/api/state`,
`/api/players`, `/api/track`, `/api/bounty`, `/api/ruleset`, SSE `/api/events`) over those local structures. radar-live is a wholly
different pipeline (radar-ingest parses logs -> radar-db stores -> radar-live's `site/js` polls radar-db's read API). The two UIs have
grown independently and already differ (go-radar has a bounty ledger and reverse-log-analysis; radar-live doesn't, yet). Making
go-radar "behave exactly like the website" means retiring go-radar's own page and log-parsing path **for the radar-live-connected
mode**, and becoming a thin native shell around the same `site/js` code pointed at a remote (or same-LAN) radar-live server.

## 2. What stays, what goes, what's added
- **Stays:** the native window (`cmd/radar/window_windows.go`, go-webview2), the browser fallback, the single-executable packaging,
  the gear-icon gated settings this task adds, and go-radar's own maps folder (same zone PNGs already bundled — no need to fetch them
  over the network every launch; radar-live's static page already works this way too).
- **Goes (for this mode only — see section 6 on keeping the old mode as a fallback):** `internal/tailer`, `internal/parser`,
  `internal/tracker`, `internal/history`, `internal/bounty`, `internal/dataset`, and go-radar's own `internal/server` JSON API +
  `web/app.js`/`web/index.html` are **no longer the source of truth for the live view**. go-radar stops reading the combat log itself.
- **Added:** `internal/liveclient` (a small Go HTTP client for radar-live's read API: base URL, view key, retry/backoff — mirrors what
  `internal/client` already is on the radar-live side, read-only, no new protocol), a vendored/shared copy of radar-live's `site/js`
  feature modules served by go-radar's own embedded static server (so the exact same code runs, not a reimplementation — see section 3),
  and the new settings window (section 4).

## 3. How the page gets shared without maintaining two copies
- `site/js` is pure client-side JS that only ever talks to its own origin's `/api/v1/*` (see `site/js/client.js`, `site/js/config.js`).
  go-radar's embedded Go HTTP server (replacing `internal/server`'s current handlers in this mode) serves the **same static files**
  (`site/index.html`, `site/js/*`, `site/style.css`) `go:embed`-ed from a **vendored copy that lives in this repo** (not a live network
  fetch of radar-live's own static files — go-radar must work with no network reachable except the configured radar-live API host), and
  **reverse-proxies `/api/v1/*`** straight through to the configured radar-live base URL + view key, so `site/js/client.js`'s existing
  `GET /api/v1/...` calls work completely unmodified, no "pretend API" to maintain in Go.
- A small build-time (not runtime) step copies radar-live's `site/` into go-radar's `web/live/` (a plain file copy via a Makefile/script
  target, run manually or in CI — NOT a live symlink/network mount, so go-radar still builds to one standalone executable with
  `go:embed`). Document the sync step clearly (`docs/sync-site.md` or a `scripts/sync-site.ps1`) so it's not forgotten when radar-live's
  page changes; a CI check (go test) can fail the build if the vendored copy's content hash drifts from `../radar-live/site` so this
  isn't silently stale — add that check as a task (G6).
- The reverse proxy adds the view key as radar-live's API expects (header or query param — check `internal/client`'s existing key
  handling on the radar-live side and match it exactly, do not invent a second auth mechanism) and nothing else; it does not fabricate,
  cache, or rewrite responses, so armory/world-map/profile features already built against that API work in go-radar for free the
  moment they ship on radar-live, with zero go-radar changes needed for each new site feature.
- This also means go-radar's window simply navigates to `http://127.0.0.1:<local-port>/` same as it (mostly) does today — only the
  handler behind that URL changes in this mode.

## 4. Settings/config window (gear icon)
- A small gear-wheel icon, fixed top-right corner of the page (new tiny CSS + a few lines of markup added to the **vendored** page via
  a go-radar-specific overlay file injected alongside the shared `site/js`, NOT by editing radar-live's own `site/index.html` — see
  section 5 on how to add go-radar-only UI without forking the shared files). Click toggles an in-page overlay panel (a `<dialog>` or a
  simple fixed-position div with a backdrop) — not a second OS window; it opens and closes within the same webview, which is simpler,
  keeps one process/one window, and matches "open and close" without needing inter-window state sync. (If a true separate OS window is
  wanted instead, flag it — doable with a second `webview2.New` instance, more plumbing, not the default here.)
- Fields in the settings panel, each a labeled input: **radar-live base URL** (e.g. `https://your-domain` or `http://192.168.x.x:8787`),
  **view key** (password-style input, with a "remember" checkbox — mirrors radar-live's own `view-key.js` feature exactly: memory-only
  unless the box is ticked, same `prefs`/localStorage pattern, same wording), **ruleset** (reuses the existing Auto/Normal/PvP/RP/RP-PvP
  segmented control already in `web/index.html` today, or the radar-live equivalent if one exists by the time this ships), **poll
  interval** (if the user wants it configurable; radar-live's page already has a fixed `SWEEP_INTERVAL_MS` in `config.js` — default to
  that, expose only if there's a real reason to), **maps folder** (local path, same as today's `--maps`/`WOW_MAPS`), and a **Save and
  reconnect** button that applies the new base URL/key to the running reverse proxy without restarting the app (swap the live client's
  config atomically, then force a sweep).
- Settings persist to a small local JSON file next to the exe (reuse the same `writable(dir)`/`%APPDATA%\NearbyRadar` fallback pattern
  `internal/app/config.go`'s `ResolveHistoryPath` already uses, just for `radar-settings.json` instead of history) — **the view key
  itself follows the project's own secrets rule** (no secrets committed/logged; stored locally only if the user ticks "remember", same
  as the website's own behavior, never sent anywhere but the configured base URL).
- Opening the settings panel must not interrupt the live radar view behind it (it's an overlay, the sweep polling keeps running).

## 5. Info (i) tooltip icons next to every setting
- Each input in the settings panel gets a small circular "i" icon immediately after its label, implemented as a plain button/span
  (`aria-describedby` pointing at a tooltip element, keyboard-focusable, not just `title=` since the user asked specifically for a
  hover tooltip — `title` alone is the cheapest option and arguably satisfies "tooltip on hover", but an accessible custom tooltip
  (small `role="tooltip"` div positioned near the icon, shown on hover/focus, hidden on blur/mouseleave) is more consistent with this
  project's accessibility bar elsewhere (see radar-live's `aria-label` usage throughout `site/js/features/*`) — **recommend the custom
  tooltip, fall back to `title` only if time is short; call this out as a decision point, not a silent choice.**
- Content per field (placeholder copy — refine wording when implemented, keep it short, one or two sentences, plain language, no
  jargon the user hasn't already used elsewhere in this app):
  - **Base URL:** "The address of your radar-live server, e.g. the domain or IP:port you (or whoever hosts it) set up for port
    forwarding. Ask them if you don't have it."
  - **View key:** "A read-only key radar-live issues for viewing data. Get it from whoever administers the radar-live server
    (`docs/client.md` / `RADAR_VIEW_KEYS` on their end). Leave blank only if their server runs open (no keys required)."
  - **Remember key:** "If checked, the key is saved on this computer so you don't have to re-enter it next time. Leave unchecked on a
    shared computer."
  - **Ruleset:** "Which server rule-set's data to show. Auto follows your character's realm type automatically."
  - **Maps folder:** "Where the zone map images live. Defaults to the `maps` folder bundled next to this program; only change this if
    you moved it."
  - **Poll interval** (if exposed): "How often to ask the server for updates. Lower is more responsive but uses more bandwidth; the
    default matches the website."
- These icons/tooltips are go-radar-only chrome (the settings panel is not part of the shared `site/js`), so they live entirely in
  go-radar's own overlay layer (section 3/4), never touching the vendored radar-live files — keeps the "one shared copy of the real
  page" promise from section 3 intact.

## 6. Keeping the old mode available (don't silently delete working features)
- go-radar today has real features radar-live does not yet have (bounty ledger, reverse log analysis of old logs, `--analyze-logs`).
  Do not delete `internal/bounty`/`internal/dataset`/the old `web/` UI outright in this stage. Instead, add a **mode switch**:
  `--mode=live` (new: radar-live client, this plan) vs `--mode=local` (today's existing self-contained behavior, becomes the explicit
  default initially so nothing breaks for the user without warning, or ask the user which should be the default once this is close to
  shipping). The settings gear/panel this plan adds is itself the natural place to also expose "Local (parse my own combat log)" vs
  "Live (connect to a radar-live server)" as a top-level choice, each with its own settings sub-panel.
- Longer-term options (not this stage, just noted): port bounty/reverse-analysis into radar-live itself so "live" mode has true
  feature parity, or keep them go-radar-only indefinitely since they're local-log-specific by nature. Revisit after stage 11/14 land
  and the user has used live mode for a while.

## 7. Tasks
| # | Task | Depends on | Files |
|---|---|---|---|
| G0 | Confirm the interpretation (section 0) with the user; confirm radar-live's `site/js/client.js`/`internal/client` exact key-auth mechanism (header name or query param) to mirror | - | docs only |
| G1 | `internal/liveclient`: Go HTTP client wrapping radar-live's read API (base URL, view key, timeouts, retry/backoff) — thin, read-only, TDD with httptest fakes | G0 | `internal/liveclient` |
| G2 | Reverse-proxy handler: go-radar's embedded server proxies `/api/v1/*` to the configured radar-live base URL + key, passthrough only (no caching/rewriting), config swappable at runtime without restart | G1 | `internal/server` (new mode) or a new `internal/liveserver` |
| G3 | Vendoring: `scripts/sync-site.ps1` (or Makefile target) copies `radar-live/site/*` into `go-radar/web/live/*`; `go:embed` wiring; a Go test asserting the vendored copy's hash matches a committed manifest (fails loudly if someone edited radar-live's site without re-syncing) | G0 | `web/live/*` (generated), new sync script, `web/live_test.go` |
| G4 | Mode switch (`--mode=live\|local`, settings-panel equivalent), wiring go-radar's existing `app.Config`/`ParseFlags` and `cmd/radar/main.go` to pick the server+page per mode | G2, G3 | `internal/app/config.go`, `cmd/radar/main.go`, `internal/app/app.go` |
| G5 | Settings gear + overlay panel: markup/CSS injected alongside vendored `site/js` (go-radar-only layer, not touching radar-live's files), fields from section 4, local JSON persistence, "Save and reconnect" that swaps `liveclient` config live | G2, G3 | new `web/shell/*` (go-radar chrome), `internal/app` settings load/save |
| G6 | Info (i) tooltip icons: accessible tooltip component (custom, `role="tooltip"`, keyboard-reachable) + the per-field copy from section 5, wired into G5's panel | G5 | `web/shell/*` |
| G7 | Tests + docs: Go tests for liveclient/proxy/mode-switch; a manual-check doc for the webview2 path (same "not yet run by a human" caveat as radar-live's client); update go-radar's `README.md` with the two modes and how to point "live" mode at a radar-live server; note the vendoring step in CONTRIBUTING-equivalent docs | all | tests, `README.md`, `docs/sync-site.md` |

Order: G0 (gate) -> G1 -> G2 -> G3 (parallel with G1/G2, independent) -> G4 -> (G5, G6 together) -> G7.

## 8. Acceptance checks
- With `--mode=live --live-url=<x> --live-key=<y>`, go-radar's window shows the literal radar-live page (same multi-select chips, same
  tooltips, same world map once stage 14 ships) pulling from that server's database, with zero go-radar-specific reimplementation of
  any of those features.
- The gear icon opens/closes the settings panel without disrupting the live view; changing the base URL/key and hitting "Save and
  reconnect" switches servers without restarting the exe.
- Every setting has a visible (i) icon; hovering (and focusing via keyboard) shows its tooltip; tooltip text matches section 5.
- `--mode=local` (or whatever the chosen default is) still behaves exactly as go-radar does today — this plan adds a mode, it does not
  remove the existing one in this stage.
- A deliberate edit to `radar-live/site/js/anything.js` without re-running the sync script makes `go test ./...` in go-radar fail on
  the vendored-copy hash check, so staleness is never silent.

## 9. Risks / open questions
- Section 0's interpretation is the single biggest risk — confirm before G1.
- View-key handling must not regress radar-live's own "memory-only unless remembered" security posture just because it's now also
  typed into a native app; reuse the exact same pattern, don't loosen it for convenience.
- Vendoring by file-copy means a human (or CI) must remember to re-run the sync script; G3's hash-check test is the safety net, but
  it only fires at `go test` time, not automatically on every radar-live commit — consider a CI job across both projects later, not
  required for this stage.
- Bounty/reverse-analysis feature gap between modes (section 6) is a known, accepted limitation for this stage, not a bug to silently
  paper over.
