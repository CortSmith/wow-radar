/**
 * Charts: zone map calibration rectangles uploaded alongside pings (parsing, change detection, wire shape).
 */
import { chartSk, mapIdFromSk } from './keys.mjs';
import { int, text, isFiniteNumber } from './validate.mjs';
import { MAP_ID_MAX, ZONE_NAME_MAX_CHARS } from './config.mjs';

const CALIBRATION_FIELDS = ['name', 'x0', 'x1', 'y0', 'y1'];

/**
 * Validates the `maps` array of a ping; malformed entries are skipped, not fatal.
 * @param {unknown[]} rawCharts
 * @param {string} partition Contact partition key.
 * @returns {import('./types.mjs').Chart[]}
 */
export function parseCharts(rawCharts, partition) {
  const charts = [];
  for (const raw of rawCharts) {
    if (![raw?.id, raw?.x0, raw?.x1, raw?.y0, raw?.y1].every(isFiniteNumber)) continue;
    charts.push({
      PK: partition,
      SK: chartSk(int(raw.id, 0, MAP_ID_MAX)),
      name: text(raw.name, ZONE_NAME_MAX_CHARS),
      x0: raw.x0,
      x1: raw.x1,
      y0: raw.y0,
      y1: raw.y1,
    });
  }
  return charts;
}

/**
 * @param {import('./types.mjs').Chart|undefined} stored
 * @param {import('./types.mjs').Chart} incoming
 * @returns {boolean} True when the chart is new or its calibration differs (worth a write).
 */
export const isChartChanged = (stored, incoming) =>
  !stored || CALIBRATION_FIELDS.some(field => stored[field] !== incoming[field]);

/** @param {import('./types.mjs').Chart} item Stored chart. */
export const toChartRow = item => ({
  id: mapIdFromSk(item.SK),
  name: item.name,
  x0: item.x0,
  x1: item.x1,
  y0: item.y0,
  y1: item.y1,
});
