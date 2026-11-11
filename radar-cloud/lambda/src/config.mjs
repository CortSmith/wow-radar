/**
 * Single home for every tunable number, limit and pattern used by the API.
 * Nothing in here has side effects except reading the two environment variables.
 * Naming follows NAMING.md: SCREAMING_SNAKE_CASE with a unit suffix.
 */

// ---------------------------------------------------------------- environment (frozen names)
/** DynamoDB table name, injected by the CDK stack. */
export const TABLE_NAME = process.env.TABLE;
/** How many operators (accounts) one beacon (API key) may create. */
export const BEACON_MAX_OPERATORS = +(process.env.MAX_ACCOUNTS_PER_KEY || 5);

// ---------------------------------------------------------------- units
/** Bytes in a kilobyte (for messages and byte limits). */
export const BYTES_PER_KB = 1024;
/** Bytes in a megabyte. */
export const BYTES_PER_MB = BYTES_PER_KB * 1024;
/** Milliseconds in a day. */
export const DAY_MS = 24 * 60 * 60 * 1000;
/** Multiplier that makes Math.round keep one decimal place. */
export const ROUND1_SCALE = 10;

// ---------------------------------------------------------------- rulesets
/** The rule-sets a contact partition can belong to. */
export const RULESETS = ['normal', 'pvp', 'rp', 'rppvp'];
/** Ruleset used when neither the request nor the saved settings name one. */
export const DEFAULT_RULESET = 'normal';

// ---------------------------------------------------------------- HTTP
/** Function URLs cap payloads at 6 MB; stay below it so the Lambda answers 413 itself. */
export const BODY_LIMIT_BYTES = 5.5 * BYTES_PER_MB;
/** Zip-bomb guard: a gzip request body may not inflate beyond this. */
export const INFLATED_BODY_LIMIT_BYTES = 32 * BYTES_PER_MB;
/** Responses smaller than this are not worth the CPU of gzip. */
export const GZIP_RESPONSE_MIN_BYTES = 2048;

// ---------------------------------------------------------------- pass (session token) and credentials
/** How long a pass stays valid. */
export const PASS_TTL_MS = 7 * DAY_MS;
/** Random bytes in the HMAC signing secret. */
export const SIGNING_SECRET_BYTES = 32;
/** Random bytes in a per-operator scrypt salt. */
export const SCRYPT_SALT_BYTES = 16;
/** Length of the derived scrypt key. */
export const SCRYPT_KEY_BYTES = 32;
/** Shortest accepted username. */
export const USERNAME_MIN_CHARS = 3;
/** Longest accepted username. */
export const USERNAME_MAX_CHARS = 32;
/** Allowed username characters and length. */
export const USERNAME_PATTERN = new RegExp(`^[A-Za-z0-9_.-]{${USERNAME_MIN_CHARS},${USERNAME_MAX_CHARS}}$`);
/** Shortest accepted password. */
export const PASSWORD_MIN_CHARS = 8;
/** Longest accepted password (also the cap applied before hashing at log-in). */
export const PASSWORD_MAX_CHARS = 128;
/** Shortest accepted beacon (API key). */
export const BEACON_KEY_MIN_CHARS = 8;
/** Longest accepted beacon (API key). */
export const BEACON_KEY_MAX_CHARS = 128;

// ---------------------------------------------------------------- DynamoDB access
/** BatchGetItem accepts at most 100 keys per call. */
export const BATCH_GET_MAX_KEYS = 100;
/** BatchWriteItem accepts at most 25 requests per call. */
export const BATCH_WRITE_MAX_REQUESTS = 25;
/** Unprocessed batch items are retried this many times, backing off exponentially. */
export const RETRY_ATTEMPTS_MAX = 6;
/** First retry delay; doubles each attempt (50, 100, 200 ... ms). */
export const RETRY_BASE_MS = 50;
/** Name of the zone GSI (must match the CDK stack). */
export const ZONE_INDEX = 'zone-idx';
/** A sector query that returns a page this large stops paging (bounded cost per sweep). */
export const SECTOR_PAGE_ROWS_MAX = 5000;
/** Digits in the zero-padded trail chunk sequence number. */
export const TRAIL_SEQ_DIGITS = 6;

// ---------------------------------------------------------------- input clamping (bounds for untrusted numbers)
/** Upper bound for any timestamp we accept (year 2096 in ms). */
export const TIMESTAMP_MAX_MS = 4e12;
/** Upper bound for event/sample counters in one ping. */
export const COUNTER_MAX = 1e7;
/** Upper bound for killing blows and assists in one ping. */
export const KILLING_BLOW_COUNT_MAX = 1e6;
/** Upper bound for damage and hp sums in one ping. */
export const DAMAGE_SUM_MAX = 1e12;
/** Upper bound for a hit-point value. */
export const HP_MAX = 1e9;
/** Upper bound (absolute) for a map coordinate. */
export const COORD_MAX = 1e7;
/** Upper bound for a map id. */
export const MAP_ID_MAX = 1e6;
/** Upper bound for a character level. */
export const LEVEL_MAX = 200;
/** Longest stored player name. */
export const NAME_MAX_CHARS = 48;
/** Longest stored zone name. */
export const ZONE_NAME_MAX_CHARS = 64;
/** Past names accepted from one ping entry. */
export const PAST_NAMES_INPUT_MAX = 20;
/** Past names kept on a stored contact (newest). */
export const PAST_NAMES_KEPT_MAX = 10;
/** Upper bound for the tripwire counter a client echoes back. */
export const CLIENT_TRIPWIRE_MAX = 1e15;

// ---------------------------------------------------------------- pings (uploads)
/** Most contacts in one ping. */
export const PING_MAX_CONTACTS = 500;
/** Most charts in one ping. */
export const PING_MAX_CHARTS = 300;
/** Most fixes in one ping, over all contacts. */
export const PING_MAX_FIXES = 20000;
/** Longest ping (batch) id. */
export const PING_BATCH_ID_MAX_CHARS = 64;
/** Allowed ping id characters and length. */
export const PING_BATCH_ID_PATTERN = new RegExp(`^[\\w.:-]{1,${PING_BATCH_ID_MAX_CHARS}}$`);
/** Player GUIDs as the game prints them; also used to validate query input. */
export const GUID_PATTERN = /^Player-[\w-]{1,40}$/;
/** Fingerprint of a combat-log file (ledger key). */
export const FINGERPRINT_PATTERN = /^[\w.-]{1,128}$/;
/** How many contacts have their trails read/extended concurrently. */
export const TRAIL_PARALLEL_COUNT = 10;

// ---------------------------------------------------------------- trails
/** Fixes per stored trail chunk item. */
export const TRAIL_CHUNK_FIXES = 300;
/** Most fixes kept per contact; older chunks are dropped beyond this. */
export const TRAIL_CAP_FIXES = 5000;
/** Safety valve on the "drop oldest chunks" loop. */
export const TRAIL_TRIM_ITERATIONS_MAX = 50;
/** Fixes accepted per contact in one ping. */
export const TRAIL_FIXES_PER_CONTACT_MAX = 2000;
/** A new fix is skipped when it is closer than this to the previous one ... */
export const FIX_DEDUPE_DISTANCE_YD = 2;
/** ... and newer by less than this, on the same map. */
export const FIX_DEDUPE_WINDOW_MS = 5000;

// ---------------------------------------------------------------- console settings
/** Largest saved settings document. */
export const CONSOLE_SETTINGS_MAX_BYTES = 8 * BYTES_PER_KB;
/** Longest accepted `timezone` setting (an IANA name such as America/Argentina/ComodRivadavia). */
export const TIMEZONE_MAX_CHARS = 64;
/** Shape of an IANA zone name: letters, digits, _ + - and / separators; rejects offsets like "+05:00" and junk. */
export const TIMEZONE_PATTERN = /^[A-Za-z][A-Za-z0-9_+-]*(\/[A-Za-z0-9_+-]+)*$/;

// ---------------------------------------------------------------- bounty
/** Bounty score per killing blow. */
export const BOUNTY_POINTS_PER_KILLING_BLOW = 10;
/** Bounty score per assist. */
export const BOUNTY_POINTS_PER_ASSIST = 0;

// ---------------------------------------------------------------- sweeps (wire path /api/poll)
/** What a sweep can look at. */
export const SECTORS = ['player', 'zone', 'all'];
/** Reaction groups; "other" holds neutral and unknown contacts. */
export const REACTION_GROUPS = ['friendly', 'hostile', 'other'];
/** Reactions a contact can have. */
export const REACTIONS = ['friendly', 'hostile', 'neutral', 'unknown'];
/** Sector-zone group value meaning "all reaction groups". */
export const ANY_GROUP = 'any';
