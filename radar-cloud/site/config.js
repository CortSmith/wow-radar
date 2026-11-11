'use strict';
/**
 * config.js - single responsibility: every tunable constant of the frontend, in one place.
 *
 * Public API: Radar.config (frozen groups), see each group below. Units are given in the name or the comment
 * (_MS milliseconds, _SEC seconds, _PX CSS pixels, _YD in-game yards, _BYTES bytes).
 * Loaded by the page, by worker.js (importScripts) and by node (require); it has no dependencies.
 */
(function (g) {
  const SECOND = 1000;
  const MINUTE = 60 * SECOND;

  /** Sweep: the cloud sweep loop (sweep.js). */
  const sweep = {
    BASE_MS: 5 * SECOND, // cadence while the user is active or data keeps changing
    SLOW_MS: 30 * SECOND, // cadence after UNCHANGED_MS of silence, and the cap of the error backoff
    UNCHANGED_MS: 2 * MINUTE, // quiet period after which sweeping slows down
    // `since` is rewound by this much so rows written during the previous request are not missed
    OVERLAP_MS: 2 * SECOND,
    JITTER_RATIO: 0.1, // +-10 % random spread so many open tabs do not sweep in lock-step
    BACKOFF_BASE: 2, // errors multiply BASE_MS by BACKOFF_BASE ** consecutive errors
  };

  /** Ping: upload of parsed contacts to the cloud (ledger.js, ping.js, tail.js, console-settings.js). */
  const ping = {
    MAX_CONTACTS_PER_REQ: 500, // players per POST /api/upload
    MAX_FIXES_PER_REQ: 40000, // position fixes per POST /api/upload (a single player is never split)
    SLICE_BYTES: 4 * 1024 * 1024, // file bytes handed to the worker per message
    HEAD_BYTES: 65536, // the first 64 KB of a log identify it (ledger fingerprint)
    MAX_PENDING_FIXES: 150000, // flush early when the engine holds this many unsent fixes
    LIVE_SLICES_PER_SWEEP: 4, // live tail reads at most this many slices per sweep so the UI stays responsive
    HASH_ALGORITHM: 'SHA-256', // Web Crypto digest used for file fingerprints
    FINGERPRINT_HEX: 32, // hex chars of the SHA-256 kept as ledger id
    SETTINGS_MAX_BYTES: 7500, // PUT /api/settings payload cap; oldest calibrations are dropped first
  };

  /** Auto-upload interval of the live tail, in seconds (the UI input is clamped to this range). */
  const pingInterval = { DEFAULT_SEC: 60, MIN_SEC: 5, MAX_SEC: 3600 };

  /** Timers of the UI. */
  const timing = {
    LIVE_TICK_MS: SECOND, // live tail sweep + snapshot refresh
    BOUNTY_REFRESH_MS: MINUTE, // bounty leaderboard refresh while logged in
    REDRAW_MS: 2 * SECOND, // ages and stale state advance even without new data
    SEARCH_DEBOUNCE_MS: 250, // typing pause before GET /api/players?q=
    SETTINGS_SAVE_DEBOUNCE_MS: 1500, // PUT /api/settings coalescing
    PROGRESS_HIDE_MS: 1500, // progress bar stays visible this long after an upload
  };

  /** Row limits of the lists (keeps the DOM small). */
  const limits = {
    LIST_ROWS: 400,
    SEARCH_RESULTS: 50,
    HISTORY_ROWS: 500,
    BOUNTY_ROWS: 50,
    TRACK_BUCKETS: 4, // fade steps of the drawn track
  };

  /** Cloud snapshot / bounty rules. */
  const cloud = {
    STALE_MS: 24 * 60 * MINUTE, // a stored player this far behind the newest one is shown as stale
    KILLING_BLOW_POINTS: 10, // bounty score of one killing blow
    ASSIST_POINTS: 0, // assists are listed but not scored
  };

  /** Rule-sets: id -> label. "auto" is a UI-only choice (follow the detected realm type). */
  const rulesets = {
    LIST: [
      ['normal', 'Normal'],
      ['pvp', 'PvP'],
      ['rp', 'RP'],
      ['rppvp', 'RP-PvP'],
    ],
    DEFAULT_ID: 'pvp',
    AUTO_ID: 'auto',
  };

  /** Radar geometry (blips.js). */
  const geometry = {
    ZOOM_MAX: 12, // maximum zoom of the fixed map frame
    FRAME_PAD_PX: 26, // room for the percent axis labels around the frame
    FRAME_Y_OFFSET_PX: 4, // frame sits slightly lower to leave room for the top labels
    DEFAULT_IMAGE_W: 1002,
    DEFAULT_IMAGE_H: 668, // aspect ratio used until a map image is known
    CAL_NUDGE_PCT: 0.5, // Shift+arrow moves the map by this many frame percent
    CAL_SCALE_STEP: 1.005, // Shift +/- scales the map by 0.5 % per press
    RING_STEPS: [
      [1500, 500],
      [600, 200],
      [200, 100],
    ], // [range above (yd), ring spacing (yd)]; below all: RING_MIN_YD
    RING_MIN_YD: 25,
    WHEEL_ZOOM: 1.2, // fixed-frame zoom factor per wheel notch
    WHEEL_RANGE: 1.15, // radar range factor per wheel notch
    TRACK_ALPHA_MIN: 0.3,
    TRACK_ALPHA_SPAN: 0.6,
    RANGE_DEFAULT_YD: 300,
  };

  /** Canvas drawing (canvas.js): sizes in CSS px, colors as CSS color strings. */
  const draw = {
    PIP_R: 5,
    PIP_ME_R: 7,
    PIP_CULL_PX: 20,
    HIT_PAD_PX: 5,
    RING_ME_PAD: 3,
    RING_SEL_PAD: 5,
    RING_HP_PAD: 1.5,
    HP_LINE_PX: 2,
    APPROX_DASH: [3, 2],
    IMAGE_ALPHA_FIXED: 0.9,
    IMAGE_ALPHA_FREE: 0.55,
    GRID_STEP_ZOOM: [
      [6, 2],
      [3, 5],
    ],
    GRID_STEP_DEFAULT: 10, // [min zoom, grid step in frame percent]
    TRACK_DASH: [2, 5],
    TRACK_LINE_PX: 1.5,
    TRACK_DOT_R: 2.5,
    TRACK_LAST_R: 4,
    TRACK_LAST_RING_R: 6.5,
    TRACK_HIT_PAD_PX: 3,
    TRACK_ALPHA_MIN: 0.45,
    TRACK_ALPHA_SPAN: 0.55,
    TRACK_CULL_PX: 10,
    BOUNTY_LINE_PX: 1.5,
    BOUNTY_RING_PAD: 3.5,
    BOUNTY_MARK_OFFSET: 9,
    BOUNTY_MARK_W: 3,
    BOUNTY_MARK_H: 3.5,
    LABEL_FONT_FRAME: '10px sans-serif',
    LABEL_FONT_RING: '11px sans-serif',
    LABEL_FONT_NORTH: '12px sans-serif',
    TIP_OFFSET_PX: 12,
    TIP_MAX_W_PX: 200,
    DRAG_MIN_PX: 2,
    LINE_PX: 1,
    RING_EXTENT: 1.5, // rings are drawn out to 1.5 x the radar range
    AXIS_LABEL_TOP_PX: 5,
    AXIS_LABEL_LEFT_PX: 4,
    AXIS_LABEL_BASELINE_PX: 3,
    RING_LABEL_DX: 3,
    RING_LABEL_DY: 2,
    NORTH_X_OFFSET_PX: 4,
    NORTH_Y_PX: 14,
    HP_ARC_START: -Math.PI / 2,
    // by reaction; mirrors the CSS --radar-* colors
    PIP_COLOR: { friendly: '#3b82f6', hostile: '#ef4444', neutral: '#ef4444', unknown: '#ef4444', stale: '#4b5563' },
    COLOR: {
      me: '#fff',
      selected: '#fde047',
      hp: '#22c55e',
      bounty: '#f5b301',
      grid: 'rgba(148,163,184,.25)',
      axisLabel: '#6b7280',
      frame: '#2f3b52',
      ring: '#232b39',
      ringLabel: '#4a5568',
      north: '#6b7280',
    },
  };

  /** Engine (tracker.js, analyzer.js, engine.js): live blips, bounty and history accumulation. */
  const engine = {
    STALE_MS: 5 * MINUTE, // no event for this long -> pip is "stale"
    EXPIRE_MS: 15 * MINUTE, // ... and forgotten after this long (the player's own unit never expires)
    APPROX_MAX_AGE_MS: 2 * MINUTE, // a nearby unit inherits our position only while our own fix is this fresh
    APPROX_WEIGHT: 0.3, // weight of an inherited (approximate) fix in the position estimate
    // an exact fix older than a newer approx fix by more than this loses to the approx one
    APPROX_WINDOW_MS: 30 * SECOND,
    FIX_RING: 60, // fixes kept per live unit
    ESTIMATE_N: 15,
    ESTIMATE_GAP_MS: 30 * SECOND, // estimate window: last N fixes with gaps below this
    ESTIMATE_DECAY: 0.9,
    ESTIMATE_MIN_DT_SEC: 1.5,
    ESTIMATE_MAX_SPEED: 30,
    ESTIMATE_MIN_SPEED: 0.5, // yd/s
    ESTIMATE_MAX_LOOKAHEAD_SEC: 10,
    ESTIMATE_PROJECTED_WEIGHT: 1.5,
    ESTIMATE_MIN_SPREAD_YD: 8,
    ESTIMATE_SPEED_FIXES: 4,
    DUP_FIX_MS: 500, // identical consecutive fixes closer than this are dropped
    MIN_FIX_DISTANCE_YD: 2,
    MIN_FIX_INTERVAL_MS: 5 * SECOND, // history dedupe (same map, closer than both)
    MAX_TRACK: 5000, // stored fixes per player and rule-set
    MAX_PAST_NAMES: 10,
    PRUNE_EVERY_EVENTS: 50000, // expire old units this often while replaying a big file
    SUFFIX_CHECK_EVERY: 512,
    ASSIST_WINDOW_MS: 15 * SECOND,
    KILLING_BLOW_WINDOW_MS: 5 * SECOND,
    DUPLICATE_DEATH_MS: 3 * SECOND,
    MAX_HITS: 64,
    MAX_VICTIMS: 256,
    MAX_KNOWN: 4096,
    MAX_OWNERS: 4096,
    MAX_LAST: 512,
    MAX_MAPS: 512, // analyzer memory caps
  };

  /** Combat log parser (parser.js). */
  const parser = {
    ADV_FIELDS: 19, // size of the advanced-logging "info unit" block
    NAMER_MIN_NAMES: 6, // distinct names that must agree before the realm suffix is trusted
    FIELD_SRC: 1,
    FIELD_DST: 5,
    FIELD_PARAMS: 9, // indexes in a split log line: source unit, destination unit, first event param
    INFO_OWNER: 1,
    INFO_HP: 2,
    INFO_MAX_HP: 3,
    INFO_X: 14,
    INFO_Y: 15,
    INFO_MAP: 16,
    INFO_LEVEL: 18, // indexes in an info block
    MAP_CHANGE_FIELDS: 7, // MAP_CHANGE,id,name,x0,x1,y0,y1
    PREFIX_NONE: 0,
    PREFIX_ENVIRONMENTAL: 1,
    PREFIX_SPELL: 3, // event prefix parameter counts
    DATE_PARTS: 3,
    CLOCK_PARTS: 3,
    MS_DIGITS: 3, // M/D/Y, H:M:S, fractional digits kept
    MONTHS_MAX: 12,
    DAY_MAX: 31,
    HOUR_MAX: 23,
    MINUTE_MAX: 59,
    SECOND_MAX: 60, // 60 allows a leap second
    CARRIAGE_RETURN: 13, // char code stripped from CRLF lines
    HEX_RADIX: 16,
    DECIMAL_RADIX: 10,
    REALM_TAIL_PARTS: 3, // trailing "-" parts of a raw name inspected for the realm type
  };

  /** Time zones (time-zone.js): the display zone and the zone applied to log lines that carry no offset. */
  const timeZone = {
    FALLBACK_ZONE: 'UTC', // used when no valid IANA zone is known (engine default, unusable host zone)
    FORMAT_LOCALE: 'en-US', // locale whose short zone names read 'EST' / 'GMT+5:30' / 'UTC'
    PROBE_MS: 24 * 60 * MINUTE, // wall-clock resolution probes the offset this far before and after the local time
    NAME_MAX_CHARS: 64, // longest IANA zone name accepted (mirrors the server limit)
    OPTION_UTC_LABEL: 'UTC', // the one zone offered besides Intl.supportedValuesOf('timeZone')
  };

  /** Unit conversions and scales shared by every module (no bare 100 / 1000 / 60 in the code). */
  const units = {
    PCT_FULL: 100, // frame percent: 0..100 across the chart
    MS_PER_SEC: SECOND,
    SEC_PER_MIN: 60,
    MIN_PER_HOUR: 60,
    SEC_PER_HOUR: 3600,
    HOURS_PER_DAY: 24,
    BYTES_PER_MB: 1048576,
    ROUND_1DP: 10, // scale for rounding to one decimal place
  };

  const config = {
    units,
    sweep,
    ping,
    pingInterval,
    timing,
    limits,
    cloud,
    rulesets,
    geometry,
    draw,
    engine,
    parser,
    timeZone,
  };
  const ns = g.Radar || (g.Radar = {});
  ns.config = config;
  if (typeof module !== 'undefined' && module.exports) module.exports = config;
})(typeof self !== 'undefined' ? self : globalThis);
