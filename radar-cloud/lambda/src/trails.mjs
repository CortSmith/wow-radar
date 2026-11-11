/**
 * Trails: each contact's history of fixes, stored as fixed-size chunk items. Covers fix cleaning,
 * appending a ping's fixes to the newest chunk, trimming old chunks past the cap, and GET /api/track.
 */
import { readAll, readEdge, readItem, dropItems } from './db.mjs';
import {
  trailPartition,
  trailPrefix,
  trailSk,
  trailSeq,
  contactPartition,
  contactSk,
  CHART_PREFIX,
  mapIdFromSk,
} from './keys.mjs';
import { resolveRuleset } from './console-settings.mjs';
import { clamp, int, round1, isFiniteNumber, requireGuid, parseOptionalInteger } from './validate.mjs';
import {
  TIMESTAMP_MAX_MS,
  MAP_ID_MAX,
  COORD_MAX,
  TRAIL_FIXES_PER_CONTACT_MAX,
  TRAIL_CHUNK_FIXES,
  TRAIL_CAP_FIXES,
  TRAIL_TRIM_ITERATIONS_MAX,
  FIX_DEDUPE_DISTANCE_YD,
  FIX_DEDUPE_WINDOW_MS,
} from './config.mjs';

/** @typedef {import('./types.mjs').Fix} Fix */

/**
 * Validates and normalises the fixes of one contact in a ping: drops junk, clamps, rounds, sorts by time.
 * @param {unknown[]} rawFixes Entries shaped `[t, mapId, x, y, kind]` where kind is 'approx' or 1 for approximate.
 * @returns {Fix[]}
 */
export function cleanFixes(rawFixes) {
  const cleaned = [];
  for (const raw of rawFixes.slice(0, TRAIL_FIXES_PER_CONTACT_MAX)) {
    if (!Array.isArray(raw) || raw.length < 4) continue;
    const [t, mapId, x, y, kind] = raw;
    if (![t, mapId, x, y].every(isFiniteNumber)) continue;
    cleaned.push([
      int(t, 0, TIMESTAMP_MAX_MS),
      int(mapId, 0, MAP_ID_MAX),
      round1(clamp(x, -COORD_MAX, COORD_MAX)),
      round1(clamp(y, -COORD_MAX, COORD_MAX)),
      kind === 'approx' || kind === 1 ? 1 : 0,
    ]);
  }
  return cleaned.sort((a, b) => a[0] - b[0]);
}

/** A fix is redundant when it is on the same map, nearly in place and nearly simultaneous. */
function isRedundantFix(previous, fix) {
  return (
    previous[1] === fix[1] &&
    Math.abs(fix[0] - previous[0]) < FIX_DEDUPE_WINDOW_MS &&
    Math.hypot(previous[2] - fix[2], previous[3] - fix[3]) < FIX_DEDUPE_DISTANCE_YD
  );
}

/**
 * Watermark filter: only fixes strictly newer than the stored last fix survive, so replaying a ping
 * (or retrying after a crash) can never duplicate points.
 */
function selectNewFixes(fixes, lastStoredFix) {
  let previous = lastStoredFix || null;
  const fresh = [];
  for (const fix of fixes) {
    if (lastStoredFix && fix[0] <= lastStoredFix[0]) continue;
    if (previous && isRedundantFix(previous, fix)) continue;
    fresh.push(fix);
    previous = fix;
  }
  return fresh;
}

/** Fills the newest chunk up to TRAIL_CHUNK_FIXES, then starts new ones; returns the chunk items to write. */
function packIntoChunks(partition, guid, fixes, newestChunk) {
  const chunks = [];
  let seq = newestChunk ? trailSeq(newestChunk) : 0;
  let current = newestChunk && newestChunk.f.length < TRAIL_CHUNK_FIXES ? newestChunk.f.slice() : null;
  const flush = () => {
    chunks.push({ PK: partition, SK: trailSk(guid, seq), f: current, n: current.length });
    current = null;
  };
  for (const fix of fixes) {
    if (!current) {
      current = [];
      seq++;
    }
    current.push(fix);
    if (current.length >= TRAIL_CHUNK_FIXES) flush();
  }
  if (current) flush();
  return chunks;
}

/**
 * Plans the writes needed to append fixes to a contact's trail. Reads only the newest chunk.
 * @param {string} partition Trail partition key.
 * @param {string} guid
 * @param {Fix[]} fixes Cleaned fixes (see cleanFixes).
 * @returns {Promise<{chunks: import('./types.mjs').TrailChunk[], added: number}>}
 */
export async function planTrailAppend(partition, guid, fixes) {
  const newestChunk = await readEdge(partition, trailPrefix(guid), false);
  const lastStoredFix = newestChunk?.f?.[newestChunk.f.length - 1];
  const fresh = selectNewFixes(fixes, lastStoredFix);
  if (!fresh.length) return { chunks: [], added: 0 };
  return { chunks: packIntoChunks(partition, guid, fresh, newestChunk), added: fresh.length };
}

/**
 * Drops the oldest whole chunks while the trail is over TRAIL_CAP_FIXES.
 * Runs after the new chunks are written, so a crash only ever leaves a trail slightly over the cap.
 * @param {string} partition
 * @param {string} guid
 * @param {number} fixCount Current fix count of the trail.
 * @returns {Promise<number>} The new fix count.
 */
export async function trimTrail(partition, guid, fixCount) {
  let remaining = fixCount;
  for (let pass = 0; remaining > TRAIL_CAP_FIXES && pass < TRAIL_TRIM_ITERATIONS_MAX; pass++) {
    const oldest = await readEdge(partition, trailPrefix(guid), true);
    if (!oldest) break;
    await dropItems([oldest]);
    remaining = Math.max(0, remaining - (oldest.n ?? oldest.f?.length ?? 0));
  }
  return remaining;
}

/**
 * Converts a stored fix to its wire shape.
 * @param {Fix} fix
 * @param {string} zone Zone name to attach ('' when unknown).
 */
export function toFixRow([t, mapId, x, y, kind], zone) {
  return { t, mapId, zone, x, y, kind: kind ? 'approx' : 'exact' };
}

/** Zone name for a fix: the chart's name, else the contact's current zone when on that map. */
function zoneNameFor(mapId, chartNames, contact) {
  return chartNames.get(mapId) || (contact?.mapId === mapId ? contact.zone : '') || '';
}

/**
 * GET /api/track: a contact's whole trail (all chunks, so up to TRAIL_CAP_FIXES fixes), optionally one map.
 * `maps` always lists per-map fix counts, even when `map` filters the fixes.
 * @param {import('./types.mjs').RequestContext} context
 * @throws {HttpError} 400 for a bad ruleset, guid or map.
 */
export async function handleTrack({ user, query }) {
  const ruleset = await resolveRuleset(user, query.rs);
  const guid = requireGuid(query.guid);
  const onlyMapId = parseOptionalInteger(query.map, 'map');
  const partition = contactPartition(user, ruleset);
  const [contact, chunks, chartItems] = await Promise.all([
    readItem({ PK: partition, SK: contactSk(guid) }),
    readAll(trailPartition(user, ruleset), trailPrefix(guid)),
    readAll(partition, CHART_PREFIX),
  ]);
  const chartNames = new Map(chartItems.map(chart => [mapIdFromSk(chart.SK), chart.name]));
  const fixCountsByMap = new Map();
  const fixes = [];
  let total = 0;
  for (const fix of chunks.flatMap(chunk => chunk.f)) {
    total++;
    fixCountsByMap.set(fix[1], (fixCountsByMap.get(fix[1]) || 0) + 1);
    if (onlyMapId === undefined || fix[1] === onlyMapId)
      fixes.push(toFixRow(fix, zoneNameFor(fix[1], chartNames, contact)));
  }
  const maps = [...fixCountsByMap]
    .sort((a, b) => a[0] - b[0])
    .map(([mapId, count]) => ({ mapId, zone: chartNames.get(mapId) || '', count }));
  return { guid, name: contact?.name || '', reaction: contact?.reaction || 'unknown', total, maps, fixes };
}
