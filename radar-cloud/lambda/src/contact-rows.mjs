/**
 * Wire shapes of contacts: stored item to JSON row, for the full list and for the slimmer zone-index rows.
 */
import { guidFromSk } from './keys.mjs';
import { round1 } from './validate.mjs';

/** Ratio rounded to one decimal; 0 when there is nothing to divide by. */
const average = (sum, count) => (count ? round1((sum || 0) / count) : 0);

/**
 * Full row (all counters and derived averages).
 * @param {import('./types.mjs').Contact} item
 */
export function toContactRow(item) {
  return {
    guid: guidFromSk(item.SK),
    name: item.name || '',
    pastNames: item.pastNames || [],
    reaction: item.reaction,
    level: item.level || 0,
    events: item.events || 0,
    srcEvents: item.srcEvents || 0,
    dmgTotal: item.dmgTotal || 0,
    dmgHits: item.dmgHits || 0,
    dmgEvents: item.dmgEvents || 0,
    hpSum: item.hpSum || 0,
    hpSamples: item.hpSamples || 0,
    maxHp: item.maxHp || 0,
    kb: item.kb || 0,
    assists: item.assists || 0,
    avgDamagePerHit: average(item.dmgTotal, item.dmgHits),
    avgDamagePerEvent: average(item.dmgTotal, item.srcEvents),
    avgHp: average(item.hpSum, item.hpSamples),
    firstSeen: item.firstSeen,
    lastSeen: item.lastSeen,
    lastX: item.x,
    lastY: item.y,
    lastMap: item.mapId,
    lastZone: item.zone,
    posTime: item.posTime,
    fixes: item.fixes || 0,
  };
}

/**
 * Slim row for sector (zone-idx) reads; counters are not projected into the index, so they are not here.
 * @param {Object} item Projected GSI item.
 */
export function toSectorRow(item) {
  return {
    guid: guidFromSk(item.SK),
    name: item.name || '',
    reaction: item.reaction,
    level: item.level || 0,
    maxHp: item.maxHp || 0,
    firstSeen: item.firstSeen,
    lastSeen: item.lastSeen,
    lastX: item.x,
    lastY: item.y,
    lastMap: item.mapId,
    lastZone: item.zone,
    posTime: item.posTime,
    ls: item.ls,
  };
}
