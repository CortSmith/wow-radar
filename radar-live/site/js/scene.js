// Builds what the radar canvas should show (pure): projected pips, trail points and the map frame.
import { chooseViewMap, toBlips } from './view-model.js';
import { worldToPct, fitFrame, freeProjection } from './projection.js';

const FRAME_PAD_PX = 12;
const FULL = 100;
const DEFAULT_ASPECT = 1.5;

function fixedProjector(chart, frame, view) {
  const span = FULL / view.z;
  return (x, y) => {
    const [px, py] = worldToPct(chart, x, y);
    return [frame.x + ((px - (view.cx - span / 2)) / span) * frame.w, frame.y + ((py - (view.cy - span / 2)) / span) * frame.h];
  };
}

function freeCenter(blips, guid) {
  const sel = blips.find((b) => b.guid === guid);
  if (sel) return { x: sel.x, y: sel.y };
  if (!blips.length) return { x: 0, y: 0 };
  const sum = blips.reduce((a, b) => ({ x: a.x + b.x, y: a.y + b.y }), { x: 0, y: 0 });
  return { x: sum.x / blips.length, y: sum.y / blips.length };
}

/** Picks the projection: fixed chart frame when a chart exists and Fixed map is on, else free range view. */
function projector({ chart, opts, size, view, image, blips, guid }) {
  if (chart && opts.fixed) {
    const aspect = image && image.w && image.h ? image.w / image.h : (chart.y1 - chart.y0) / (chart.x1 - chart.x0) || DEFAULT_ASPECT;
    const frame = fitFrame(size.w, size.h, aspect, FRAME_PAD_PX);
    return { fixed: true, frame, project: fixedProjector(chart, frame, view) };
  }
  const center = freeCenter(blips, guid);
  return { fixed: false, frame: null, project: freeProjection({ w: size.w, h: size.h, center, rangeYd: opts.rangeYd }) };
}

/**
 * state: page state; now: ms; size {w,h} CSS px; view {z,cx,cy}; opts {fixed, rangeYd}; image {w,h} or null.
 * Returns {mapId, chart, fixed, frame, pips, trail}.
 */
export function buildScene({ state, now, size, view, opts, image }) {
  const mapId = chooseViewMap(state, now);
  const blips = mapId === null ? [] : toBlips(state, now, mapId);
  const chart = state.charts.find((c) => c.mapId === mapId) || null;
  const guid = state.player ? state.player.guid : '';
  const proj = projector({ chart, opts, size, view, image, blips, guid });
  const pips = blips.map((b) => {
    const [sx, sy] = proj.project(b.x, b.y);
    return { guid: b.guid, name: b.name, sx, sy, color: b.color, status: b.status, selected: b.guid === guid, contact: b.contact };
  });
  const trail = state.trail
    .filter((f) => f.mapId === mapId)
    .map((f) => ({ ...f, sx: proj.project(f.x, f.y)[0], sy: proj.project(f.x, f.y)[1] }));
  return { mapId, chart, fixed: proj.fixed, frame: proj.frame, pips, trail };
}
