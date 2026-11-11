/**
 * Contact parsing and merging: validates one contact of an uploaded ping and merges it into the stored
 * contact. Pure functions only (no I/O), which is what makes pings idempotent and easy to test.
 */
import { cleanFixes } from './trails.mjs';
import { clamp, int, round1, text, isFiniteNumber, isPlainObject } from './validate.mjs';
import {
  GUID_PATTERN,
  REACTIONS,
  TIMESTAMP_MAX_MS,
  COUNTER_MAX,
  KILLING_BLOW_COUNT_MAX,
  DAMAGE_SUM_MAX,
  HP_MAX,
  COORD_MAX,
  MAP_ID_MAX,
  LEVEL_MAX,
  NAME_MAX_CHARS,
  ZONE_NAME_MAX_CHARS,
  PAST_NAMES_INPUT_MAX,
  PAST_NAMES_KEPT_MAX,
} from './config.mjs';

/** Counters that accumulate across pings (each ping carries deltas, the stored value is the running total). */
const ADDITIVE_FIELDS = [
  'events',
  'srcEvents',
  'dmgTotal',
  'dmgHits',
  'dmgEvents',
  'hpSum',
  'hpSamples',
  'kb',
  'assists',
];
/** Floating-point additive fields are rounded after each addition so repeated merges stay stable. */
const DECIMAL_FIELDS = new Set(['dmgTotal', 'hpSum']);
/** Fields compared to decide whether a merge actually changed anything. */
const COMPARED_FIELDS = [
  'name',
  'reaction',
  'level',
  'maxHp',
  'x',
  'y',
  'mapId',
  'zone',
  'posTime',
  'firstSeen',
  'lastSeen',
  'fixes',
  'lb',
  ...ADDITIVE_FIELDS,
];

/**
 * Validates the counter deltas of one contact.
 * @param {unknown} rawDelta
 * @returns {import('./types.mjs').ContactDelta & Object<string, number|boolean>}
 */
function parseDelta(rawDelta) {
  const source = isPlainObject(rawDelta) ? rawDelta : {};
  const delta = {
    events: int(source.events, 0, COUNTER_MAX),
    srcEvents: int(source.srcEvents, 0, COUNTER_MAX),
    dmgHits: int(source.dmgHits, 0, COUNTER_MAX),
    dmgEvents: int(source.dmgEvents, 0, COUNTER_MAX),
    hpSamples: int(source.hpSamples, 0, COUNTER_MAX),
    kb: int(source.kb, 0, KILLING_BLOW_COUNT_MAX),
    assists: int(source.assists, 0, KILLING_BLOW_COUNT_MAX),
    dmgTotal: round1(clamp(source.dmgTotal, 0, DAMAGE_SUM_MAX)),
    hpSum: round1(clamp(source.hpSum, 0, DAMAGE_SUM_MAX)),
    maxHp: round1(clamp(source.maxHp, 0, HP_MAX)),
  };
  delta.hasChanges = ADDITIVE_FIELDS.some(field => delta[field] > 0);
  return delta;
}

/**
 * Validates one contact of a ping.
 * @param {*} raw Untrusted JSON.
 * @returns {import('./types.mjs').IncomingContact|null} null when the guid is missing or malformed.
 */
export function parseContact(raw) {
  if (!raw || typeof raw.guid !== 'string' || !GUID_PATTERN.test(raw.guid)) return null;
  const lastSeen = int(raw.lastSeen, 0, TIMESTAMP_MAX_MS);
  const hasPosition = [raw.x, raw.y, raw.mapId].every(isFiniteNumber);
  return {
    guid: raw.guid,
    name: text(raw.name, NAME_MAX_CHARS),
    reaction: REACTIONS.includes(raw.reaction) ? raw.reaction : 'unknown',
    level: int(raw.level, 0, LEVEL_MAX),
    pastNames: Array.isArray(raw.pastNames)
      ? raw.pastNames
          .slice(0, PAST_NAMES_INPUT_MAX)
          .map(name => text(name, NAME_MAX_CHARS))
          .filter(Boolean)
      : [],
    hasPosition,
    x: hasPosition ? round1(clamp(raw.x, -COORD_MAX, COORD_MAX)) : undefined,
    y: hasPosition ? round1(clamp(raw.y, -COORD_MAX, COORD_MAX)) : undefined,
    mapId: hasPosition ? int(raw.mapId, 0, MAP_ID_MAX) : undefined,
    zone: text(raw.zone, ZONE_NAME_MAX_CHARS),
    posTime: int(raw.posTime, 0, TIMESTAMP_MAX_MS, lastSeen),
    firstSeen: int(raw.firstSeen, 0, TIMESTAMP_MAX_MS, lastSeen),
    lastSeen,
    delta: parseDelta(raw.d),
    fixes: cleanFixes(Array.isArray(raw.fixes) ? raw.fixes : []),
  };
}

/** Newest sighting decides name and reaction; the losing name is remembered in pastNames. */
function mergeIdentity(merged, stored, incoming) {
  const past = [...(stored?.pastNames || [])];
  const remember = name => {
    if (name && !past.includes(name)) past.push(name);
  };
  if (!stored || incoming.lastSeen >= stored.lastSeen) {
    if (stored?.name && incoming.name && stored.name !== incoming.name) remember(stored.name);
    merged.name = incoming.name || stored?.name || '';
    // an 'unknown' reaction never overwrites a known one
    merged.reaction = incoming.reaction === 'unknown' && stored?.reaction ? stored.reaction : incoming.reaction;
  } else {
    merged.name = stored.name || incoming.name;
    merged.reaction = stored.reaction;
    if (incoming.name && incoming.name !== merged.name) remember(incoming.name);
  }
  incoming.pastNames.forEach(remember);
  const kept = past.filter(name => name !== merged.name).slice(-PAST_NAMES_KEPT_MAX);
  if (kept.length) merged.pastNames = kept;
  else delete merged.pastNames;
}

/** Position moves only forward in game time, so a late-arriving old ping cannot teleport a contact back. */
function mergePosition(merged, stored, incoming) {
  if (!incoming.hasPosition) return;
  if (stored?.posTime != null && incoming.posTime < stored.posTime) return;
  Object.assign(merged, {
    x: incoming.x,
    y: incoming.y,
    mapId: incoming.mapId,
    zone: incoming.zone || stored?.zone || '',
    posTime: incoming.posTime,
  });
}

/**
 * Merges an incoming contact into the stored one.
 * Idempotent for identical input: merging the same data again yields an equal item, so the caller's
 * isSameContact check suppresses the write. Counters are only added when the delta is non-empty and the
 * ping id (`lb`) is recorded, which is what makes a replayed ping a no-op.
 * @param {import('./types.mjs').Contact|undefined} stored Existing item, if any.
 * @param {import('./types.mjs').IncomingContact} incoming
 * @param {string} pingId Batch id of the ping being applied.
 * @returns {import('./types.mjs').Contact} A new object (inputs are not mutated).
 */
export function mergeContact(stored, incoming, pingId) {
  const merged = { ...(stored || {}) };
  for (const field of ADDITIVE_FIELDS) merged[field] = stored?.[field] || 0;
  merged.firstSeen = Math.min(stored?.firstSeen ?? Infinity, incoming.firstSeen);
  merged.lastSeen = Math.max(stored?.lastSeen ?? 0, incoming.lastSeen);
  merged.maxHp = Math.max(stored?.maxHp ?? 0, incoming.delta.maxHp);
  merged.level = Math.max(stored?.level ?? 0, incoming.level);
  merged.fixes = stored?.fixes || 0;
  mergeIdentity(merged, stored, incoming);
  mergePosition(merged, stored, incoming);
  if (incoming.delta.hasChanges) {
    for (const field of ADDITIVE_FIELDS) {
      const sum = merged[field] + incoming.delta[field];
      merged[field] = DECIMAL_FIELDS.has(field) ? round1(sum) : sum;
    }
    merged.lb = pingId;
  }
  return merged;
}

/**
 * @param {Object|undefined} stored
 * @param {Object} merged
 * @returns {boolean} True when merged equals stored on every compared field (nothing to write).
 */
export function isSameContact(stored, merged) {
  return (
    Boolean(stored) &&
    COMPARED_FIELDS.every(field => stored[field] === merged[field]) &&
    JSON.stringify(stored.pastNames || []) === JSON.stringify(merged.pastNames || [])
  );
}
