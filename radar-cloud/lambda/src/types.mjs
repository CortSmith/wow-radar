/**
 * Schema documentation: JSDoc typedefs for every item shape stored in DynamoDB and for the wire objects
 * that carry them. This module has no runtime code; import types with
 * `@typedef {import('./types.mjs').Contact} Contact`. Key formats live in keys.mjs.
 */

/**
 * One position fix: `[t, mapId, x, y, kind]` where kind is 0 (exact) or 1 (approximate).
 * Chronological inside a trail, coordinates rounded to 0.1.
 * @typedef {[number, number, number, number, 0|1]} Fix
 */

/**
 * Stored contact (a tracked player). Key: P#<user>#<ruleset> / G#<guid>.
 * @typedef {Object} Contact
 * @property {string} PK
 * @property {string} SK
 * @property {string} name Newest known name.
 * @property {string[]} [pastNames] Previous names, newest last, capped.
 * @property {'friendly'|'hostile'|'neutral'|'unknown'} reaction
 * @property {number} level
 * @property {number} maxHp
 * @property {number} [x] Last known position (absent until a position is seen).
 * @property {number} [y]
 * @property {number} [mapId]
 * @property {string} [zone]
 * @property {number} [posTime] Game-log time of the last position.
 * @property {number} firstSeen Game-log time, minimum over all pings.
 * @property {number} lastSeen Game-log time, maximum over all pings.
 * @property {number} events Additive counters (all numbers, running totals): events, srcEvents, dmgTotal, dmgHits,
 *   dmgEvents, hpSum, hpSamples, kb, assists.
 * @property {number} fixes Number of fixes currently stored in the trail.
 * @property {string} [lb] Id of the last ping applied (replay guard).
 * @property {number} ut Server time of the last write (per-contact sweep gate).
 * @property {string} [zk] zone-idx partition key; present only while a position is known.
 * @property {number} [ls] zone-idx sort key (server time of the last write); present with zk.
 */

/**
 * Stored trail chunk. Key: T#<user>#<ruleset> / <guid>#<seq>.
 * @typedef {Object} TrailChunk
 * @property {string} PK
 * @property {string} SK
 * @property {Fix[]} f Up to TRAIL_CHUNK_FIXES fixes.
 * @property {number} n Fix count (lets trimming avoid reading f).
 */

/**
 * Stored chart (map calibration rectangle). Key: P#<user>#<ruleset> / M#<mapId>.
 * @typedef {Object} Chart
 * @property {string} PK
 * @property {string} SK
 * @property {string} name
 * @property {number} x0
 * @property {number} x1
 * @property {number} y0
 * @property {number} y1
 */

/**
 * Stored ledger entry: how far a log file has been ingested. Key: L#<user>#<ruleset> / <fingerprint>.
 * @typedef {Object} LedgerItem
 * @property {number} lastTs Watermark; only ever moves forward.
 */

/**
 * Stored tripwire (version item). Key: V#<user>#<ruleset> / VER.
 * @typedef {Object} Tripwire
 * @property {number} v Counter bumped on every change; clients compare it to skip full loads.
 * @property {number} [z] Dynamic attributes z<mapId> hold the last write time of that map.
 */

/**
 * Stored beacon (API key). Key: K#<sha256> / KEY.
 * @typedef {Object} Beacon
 * @property {boolean} active
 * @property {number} [cnt] Operators created with this beacon.
 */

/**
 * Stored operator (account). Key: U#<username> / PROFILE.
 * @typedef {Object} Operator
 * @property {string} salt Hex scrypt salt.
 * @property {string} hash Hex scrypt hash of the password.
 * @property {string} kh sha256 of the beacon the operator signed up with.
 * @property {number} created
 */

/**
 * Parsed per-contact counter deltas of one ping.
 * @typedef {Object} ContactDelta
 * @property {boolean} hasChanges True when any additive counter is > 0.
 * @property {number} maxHp
 */

/**
 * A ping entry after validation (see contact-merge.mjs parseContact).
 * @typedef {Object} IncomingContact
 * @property {string} guid
 * @property {boolean} hasPosition
 * @property {ContactDelta} delta
 * @property {Fix[]} fixes
 */

/**
 * Handler context built by the router.
 * @typedef {Object} RequestContext
 * @property {string} [user] Authenticated username (absent on public routes).
 * @property {Object<string,string>} query Query string parameters.
 * @property {*} body Parsed JSON body (POST/PUT only).
 * @property {Object} event Raw Lambda Function URL event.
 */

export {};
