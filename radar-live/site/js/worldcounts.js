// Stage 14 W3b: per-zone friendly/hostile contact counts for the world map's chips, plus the pure
// sqrt-area sizing helper for the chips themselves. No DOM here (see world-map.js for drawing/hover).
import { classify } from './staleness.js';

/**
 * Counts CURRENTLY-live contacts (classify() === 'live' - stale and gone contacts are excluded, same
 * "stale/gone" vocabulary as staleness.js elsewhere) per mapId, split friendly vs everything else
 * (hostile/neutral/unknown - the same two-bucket split view-model.js's colorOf uses). Contacts with no
 * usable position (posTime <= 0) or no mapId are skipped, same guard as view-model.js's toBlips/chooseViewMap.
 * Returns a Map<mapId, {friendly, hostile}>; a zone with zero of both buckets simply has no entry.
 */
export function zoneCounts(contacts, now, cfg) {
  const out = new Map();
  for (const c of contacts.values()) {
    if (!c || c.mapId == null || !(c.posTime > 0)) continue;
    if (classify(c, now, cfg) !== 'live') continue;
    const entry = out.get(c.mapId) || { friendly: 0, hostile: 0 };
    if (c.reaction === 'friendly') entry.friendly += 1;
    else entry.hostile += 1;
    out.set(c.mapId, entry);
  }
  return out;
}

const CHIP_MIN_R_PX = 4;
const CHIP_SCALE_PX = 3;

/**
 * Chip radius for a count: area (not radius) scales linearly with count, so radius scales by
 * sqrt(count) - stage-14-world-map.md 3.4. 0 (or negative/NaN) -> 0, meaning "draw no chip at all".
 * A count of 1+ always clears CHIP_MIN_R_PX so a single contact is still visible.
 */
export function chipRadius(count) {
  if (!(count > 0)) return 0;
  return CHIP_MIN_R_PX + CHIP_SCALE_PX * Math.sqrt(count);
}

/**
 * Tooltip row strings for a hovered chip (pure, no DOM - same spirit as tooltip.js's tooltipModel).
 * hit: {name, friendly, hostile} (the zone's name and both exact counts, shown together regardless of
 * which of the zone's two chips was actually hovered - stage-14-world-map.md 3.4 "shows the zone name
 * + exact counts"). ctx: {MSG, fmt}.
 */
export function chipTooltipModel(hit, ctx) {
  if (!hit) return [];
  const { MSG, fmt } = ctx;
  return [hit.name, fmt(MSG.worldChipFriendly, { n: hit.friendly }), fmt(MSG.worldChipHostile, { n: hit.hostile })];
}
