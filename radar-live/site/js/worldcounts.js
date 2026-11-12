// Stage 14 W3b: per-zone friendly/neutral/hostile contact counts for the world map's pie dots, plus the
// pure sqrt-area sizing and pie-slice helpers. No DOM here (see world-map.js for drawing/hover).
import { classify } from './staleness.js';

/**
 * Counts CURRENTLY-live contacts (classify() === 'live' - stale and gone contacts are excluded, same
 * "stale/gone" vocabulary as staleness.js elsewhere) per mapId, in three buckets: friendly, neutral, and
 * hostile (hostile plus unknown/anything else). Contacts with no usable position (posTime <= 0) or no
 * mapId are skipped, same guard as view-model.js's toBlips/chooseViewMap.
 * Returns a Map<mapId, {friendly, neutral, hostile}>; a zone with no live contacts has no entry.
 */
export function zoneCounts(contacts, now, cfg) {
  const out = new Map();
  for (const c of contacts.values()) {
    if (!c || c.mapId == null || !(c.posTime > 0)) continue;
    if (classify(c, now, cfg) !== 'live') continue;
    const entry = out.get(c.mapId) || { friendly: 0, neutral: 0, hostile: 0 };
    if (c.reaction === 'friendly') entry.friendly += 1;
    else if (c.reaction === 'neutral') entry.neutral += 1;
    else entry.hostile += 1;
    out.set(c.mapId, entry);
  }
  return out;
}

/** Total live contacts across the three buckets (missing buckets count as 0). */
export function zoneTotal(cnt) {
  if (!cnt) return 0;
  return (cnt.friendly > 0 ? cnt.friendly : 0) + (cnt.neutral > 0 ? cnt.neutral : 0) + (cnt.hostile > 0 ? cnt.hostile : 0);
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

const TAU = Math.PI * 2;
const START_ANGLE = -Math.PI / 2; // 12 o'clock
const PIE_ORDER = ['friendly', 'neutral', 'hostile'];

/**
 * Pie slices for a zone's counts, clockwise from 12 o'clock in canvas angles (radians, y down), ordered
 * friendly, neutral, hostile; empty buckets are skipped. Returns [{reaction, start, end}]. A single
 * non-empty bucket yields one slice spanning the full circle (start .. start + 2PI), so callers draw a
 * whole disc rather than a degenerate wedge. A zero total yields [].
 */
export function pieSlices(counts) {
  const total = zoneTotal(counts);
  if (!(total > 0)) return [];
  const slices = [];
  let angle = START_ANGLE;
  for (const reaction of PIE_ORDER) {
    const n = counts[reaction];
    if (!(n > 0)) continue;
    const end = angle + (n / total) * TAU;
    slices.push({ reaction, start: angle, end });
    angle = end;
  }
  // Pin the last slice's end exactly onto a full turn to avoid float drift leaving a hairline gap.
  slices[slices.length - 1].end = START_ANGLE + TAU;
  return slices;
}

/**
 * Which slice a point falls in, by angle alone (clockwise from 12 o'clock around the chip's centre).
 * Distance is NOT checked: the caller has already hit-tested the circle (world-map.js's hitChip, which
 * allows a little slack outside the radius), so a point in that slack still maps to its nearest slice.
 * chip: {x, y, friendly, neutral, hostile}. Returns 'friendly' | 'neutral' | 'hostile', or null when the
 * chip has no contacts. A point exactly at the centre maps to the first slice.
 */
export function sliceAt(chip, x, y) {
  const slices = pieSlices(chip);
  if (slices.length === 0) return null;
  if (slices.length === 1) return slices[0].reaction;
  let a = Math.atan2(y - chip.y, x - chip.x) - START_ANGLE; // 0 at 12 o'clock, growing clockwise
  a = ((a % TAU) + TAU) % TAU;
  for (const s of slices) {
    if (a < s.end - START_ANGLE) return s.reaction;
  }
  return slices[slices.length - 1].reaction;
}

/**
 * Tooltip row strings for a hovered chip (pure, no DOM - same spirit as tooltip.js's tooltipModel).
 * hit: {name, friendly, neutral, hostile} (the zone's name and all three exact counts).
 * ctx: {MSG, fmt}.
 */
export function chipTooltipModel(hit, ctx) {
  if (!hit) return [];
  const { MSG, fmt } = ctx;
  return [
    hit.name,
    fmt(MSG.worldChipFriendly, { n: hit.friendly || 0 }),
    fmt(MSG.worldChipNeutral, { n: hit.neutral || 0 }),
    fmt(MSG.worldChipHostile, { n: hit.hostile || 0 }),
  ];
}
