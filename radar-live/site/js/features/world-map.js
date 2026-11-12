// World view (stage 14): continent art + one pie-chart dot per zone, pan + zoom (task W3a). Each zone is a
// single position point (px/py in the continent's own pixel space; legacy rect-only zones fall back to
// their rectangle's centre). The dot is a pie of the zone's live friendly / neutral / hostile contacts,
// sized by total count, with a hover tooltip of the exact counts (W3b). Tracked players' cross-zone world
// paths draw on top of the pies (W4).
// click-to-navigate (W5): clicking a pie goes to that zone; the slice clicked picks the filter (friendly
// -> Blue only, hostile or neutral -> Red only, since Red covers both); a click that misses every pie
// does nothing, and a drag never navigates.
// a header-controls toggle with the single-zone radar, both ways (W6): ctx.setWorldView flips
// state.worldView, mountToggle()/mountVisibility() below wire the button + hidden-section swap, and
// gotoZone (W5) switches back to zone view automatically on click. Loads
// maps/world/index.json (same mechanism chart-picker.js already uses for maps/index.json: ctx.fetchJson,
// a same-site optional JSON file under ctx.config.MAPS_BASE_URL) and draws one canvas per continent.
import { clampView, panFrom, zoomAt, continentProjector, fitFrame } from '../projection.js';
import { readColors } from '../draw.js';
import { zoneCounts, zoneTotal, chipRadius, chipTooltipModel, pieSlices, sliceAt } from '../worldcounts.js';
import { worldPath } from '../worldpath.js';
import { trackColorClass } from '../trackcolors.js';

const DEFAULT_VIEW = { z: 1, cx: 50, cy: 50 };
const WHEEL_ZOOM = 1.2;
const FRAME_PAD_PX = 12;
const WORLD_INDEX_PATH = 'world/';
const CHIP_HIT_PAD_PX = 3; // slack added to a pie's own radius for the hover/click hit-test
const PIE_SEPARATOR = '#0a0d12'; // thin line between slices (the canvas background colour)
const CLICK_MOVE_PX = 4; // same mouseup-vs-drag threshold as radar-canvas.js's wireInput

export default {
  name: 'world-map',
  init(ctx) {
    const wrap = ctx.mount('world');
    if (!wrap) return;
    const empty = ctx.el('div', { className: 'empty muted', text: ctx.MSG.noWorldMap, hidden: true });
    const list = ctx.el('div', { className: 'worldList' });
    wrap.appendChild(empty);
    wrap.appendChild(list);
    wrap.appendChild(buildLegend(ctx));

    // W6: a simple header-controls toggle between this world view and the single-zone radar. Purely
    // a view-mode flag (ctx.setWorldView) - never touches player/tracked/zone/filters.
    mountToggle(ctx);
    mountVisibility(ctx, wrap);

    (async () => {
      let index = null;
      try {
        index = await ctx.fetchJson(ctx.config.MAPS_BASE_URL + WORLD_INDEX_PATH + ctx.config.MAPS_INDEX_FILE);
      } catch {
        index = null; // the world map is optional: no asset folder yet just means an empty section
      }
      const continents = (index && Array.isArray(index.continents)) ? index.continents : [];
      empty.hidden = continents.length > 0;
      for (const continent of continents) mountContinent(ctx, list, continent);
    })();
  },
};

/**
 * W6: header-controls button toggling ctx.state.worldView, same mount point and shape as
 * group-filter.js/ruleset-switch.js's own controls. Label flips between the two MSG strings and tracks
 * state.worldView via the 'worldView' bus event setWorldView emits (whatever flipped it - this button,
 * gotoZone, or anything else).
 */
function mountToggle(ctx) {
  const host = ctx.mount('header-controls');
  if (!host) return;
  const btn = ctx.el('button', {
    type: 'button',
    text: ctx.state.worldView ? ctx.MSG.worldViewOff : ctx.MSG.worldViewOn,
    on: { click: () => ctx.setWorldView(!ctx.state.worldView) },
  });
  const sync = () => { btn.textContent = ctx.state.worldView ? ctx.MSG.worldViewOff : ctx.MSG.worldViewOn; };
  ctx.bus.on('worldView', sync);
  // W7: setZone now also flips worldView (picking a real zone exits it, clearing back to "All Zones"
  // restores it) as part of its own single changed('selection') emission, not a separate 'worldView'
  // one - so the button label needs to track 'selection' too, not just the manual-toggle event.
  ctx.bus.on('selection', sync);
  host.appendChild(btn);
}

/**
 * W6: shows/hides this world-map section vs the radar section per state.worldView, mirroring every
 * other `.hidden =` toggle in this codebase (e.g. search.js's chips/clear, radar-canvas.js's tip). Kept
 * here (rather than a page-level spot in core.js/main.js) since world-map.js already owns both mount
 * points' `hidden` semantics for its own empty-state note.
 */
function mountVisibility(ctx, worldWrap) {
  const radarWrap = ctx.mount('radar');
  const sync = () => {
    if (radarWrap) radarWrap.hidden = ctx.state.worldView;
    worldWrap.hidden = !ctx.state.worldView;
    ctx.redraw(); // the section that just appeared could not paint while it was hidden
  };
  ctx.bus.on('worldView', sync);
  // W7: see mountToggle's comment above - a zone pick/clear can flip worldView via 'selection' alone.
  ctx.bus.on('selection', sync);
  sync();
}

/**
 * One continent's canvas: loads its PNG, draws it + one friendly/neutral/hostile pie per zone with live
 * contacts, wires pan/zoom and pie hover/click.
 */
function mountContinent(ctx, list, continent) {
  const box = ctx.el('div', { className: 'worldContinent' });
  const label = ctx.el('div', { className: 'worldContinentName', text: continent.name || '' });
  const canvas = ctx.el('canvas', { attrs: { 'aria-label': continent.name || '' } });
  const tip = ctx.el('div', { className: 'tip', hidden: true });
  box.appendChild(label);
  box.appendChild(canvas);
  box.appendChild(tip);
  list.appendChild(box);

  const w = continent.w > 0 ? continent.w : 1;
  const h = continent.h > 0 ? continent.h : 1;
  const zones = Array.isArray(continent.zones) ? continent.zones : [];
  const colors = readColors(ctx.win, ctx.doc.documentElement || null);
  const ui = { view: { ...DEFAULT_VIEW }, img: null, chips: [] };

  if (typeof continent.file === 'string' && continent.file) {
    const img = ctx.doc.createElement('img');
    img.addEventListener('load', () => ui.draw && ui.draw());
    img.src = ctx.config.MAPS_BASE_URL + WORLD_INDEX_PATH + continent.file;
    ui.img = img;
  }

  function frameFor(size) {
    return fitFrame(size.w, size.h, w / h, FRAME_PAD_PX);
  }

  function draw() {
    if (ctx.state.worldView === false) return; // hidden section: it has no size, and the view event repaints on show
    const size = { w: canvas.clientWidth || 600, h: canvas.clientHeight || 280 };
    const frame = frameFor(size);
    const project = continentProjector(w, h, frame, ui.view);
    // Pie layout is computed unconditionally (not gated on a 2D context existing) so hover/click
    // hit-testing keeps working even when nothing can be painted yet (e.g. a canvas with no context in tests).
    const counts = zoneCounts(ctx.state.contacts, ctx.now(), ctx.config);
    ui.chips = layoutChips(zones, project, counts);
    // W4: computed unconditionally too (same reason as ui.chips above - keeps working even without a
    // 2D context, e.g. in tests).
    ui.paths = computeTrackedPaths(ctx.state.tracked, ctx.state.trackedData, zones, project);

    const g = canvas.getContext('2d');
    if (!g) return;
    const dpr = (ctx.win && ctx.win.devicePixelRatio) || 1;
    const pw = Math.round(size.w * dpr);
    const ph = Math.round(size.h * dpr);
    if (canvas.width !== pw) canvas.width = pw;
    if (canvas.height !== ph) canvas.height = ph;
    g.setTransform(dpr, 0, 0, dpr, 0, 0);
    g.clearRect(0, 0, size.w, size.h);
    if (ui.img && ui.img.complete && ui.img.naturalWidth) {
      const [ix0, iy0] = project(0, 0);
      const [ix1, iy1] = project(w, h);
      g.drawImage(ui.img, ix0, iy0, ix1 - ix0, iy1 - iy0);
    }
    for (const chip of ui.chips) paintPie(g, chip, colors);
    // W4: tracked players' world paths draw on top of the pies (stage-14-world-map.md 3.4).
    for (const path of ui.paths) {
      paintWorldPath(g, path.points, colors[trackColorClass(path.guid)] || colors.text);
    }
  }
  ui.draw = draw;
  ui.frameFor = frameFor;

  wireInput(ctx, canvas, tip, ui);
  if (ctx.win) ctx.win.addEventListener('resize', draw);
  ctx.bus.on('sweep', draw); // re-render chips whenever the roster sweep updates state.contacts
  ctx.bus.on('view', draw);
  ctx.bus.on('selection', draw); // W4: re-render paths when tracking/untracking/focusing changes state.tracked
  draw();
}

/**
 * A zone's single position point in its continent's pixel space: `px`/`py` when present, else (legacy
 * rect-only index) the centre of its x/y/w/h rectangle, else null (nothing usable - the zone is skipped).
 */
export function zonePoint(z) {
  if (!z) return null;
  if (Number.isFinite(z.px) && Number.isFinite(z.py)) return { x: z.px, y: z.py };
  if (z.w > 0 && z.h > 0) return { x: z.x + z.w / 2, y: z.y + z.h / 2 };
  return null;
}

/**
 * Screen-space pie layout for one continent's zones (pure given a projector + counts), reused for both
 * drawing and hit-testing. One pie per zone with at least one live contact, centred on the zone's point,
 * radius from the zone's total. Returns [{mapId, name, friendly, neutral, hostile, x, y, r}, ...].
 */
export function layoutChips(zones, project, counts) {
  const chips = [];
  for (const z of zones) {
    const pt = zonePoint(z);
    if (!pt) continue;
    const cnt = counts.get(z.mapId) || { friendly: 0, neutral: 0, hostile: 0 };
    const r = chipRadius(zoneTotal(cnt));
    if (!(r > 0)) continue;
    const [x, y] = project(pt.x, pt.y);
    chips.push({ mapId: z.mapId, name: z.name || '', friendly: cnt.friendly, neutral: cnt.neutral, hostile: cnt.hostile, x, y, r });
  }
  return chips;
}

/**
 * Paints one zone's pie on any canvas-2D-like context (no DOM, testable with a fake context). One slice
 * per non-empty bucket (pieSlices); a single bucket is a whole disc. A thin dark line separates slices.
 * chip: {x, y, r, friendly, neutral, hostile}. colors: {friendly, neutral, hostile}.
 */
export function paintPie(g, chip, colors) {
  const slices = pieSlices(chip);
  if (slices.length === 0) return;
  const multi = slices.length > 1;
  for (const s of slices) {
    g.beginPath();
    if (multi) g.moveTo(chip.x, chip.y);
    g.arc(chip.x, chip.y, chip.r, s.start, s.end);
    if (multi) g.closePath();
    g.fillStyle = colors[s.reaction];
    g.fill();
    if (multi) {
      g.strokeStyle = PIE_SEPARATOR;
      g.lineWidth = 1;
      g.stroke();
    }
  }
}

/** The small friendly / neutral / hostile colour key shown under the world canvases. */
function buildLegend(ctx) {
  const items = [['friendly', ctx.MSG.legendFriendly], ['neutral', ctx.MSG.legendNeutral], ['hostile', ctx.MSG.legendHostile]];
  const legend = ctx.el('div', { className: 'worldLegend muted' });
  for (const [kind, text] of items) {
    legend.appendChild(ctx.el('span', { className: 'worldLegendItem' },
      ctx.el('span', { className: `worldLegendSwatch ${kind}` }), text));
  }
  return legend;
}

/**
 * W5: the `ctx.setFilters` patch for a chip's reaction, per stage-14-world-map.md 3.5 - `null` for a
 * plain zone click (no chip involved), meaning "don't call setFilters at all, leave filters as they
 * are" (NOT the same as an empty patch).
 */
export function filterPatchFor(which) {
  if (which === 'friendly') return { friendly: true, hostile: false };
  if (which === 'hostile' || which === 'neutral') return { friendly: false, hostile: true }; // Red covers both
  return null;
}

/**
 * W5: navigates to a zone, built on the existing ctx.setZone + ctx.setFilters (stage-14-world-map.md
 * 3.5) - kept local to this feature rather than added to core.js's ctx, since nothing else needs it.
 */
export function gotoZone(ctx, mapId, name, which) {
  ctx.setZone({ mapId, name });
  const patch = filterPatchFor(which);
  if (patch) ctx.setFilters(patch);
  // W6: clicking into a zone from the world view should actually show that zone, not leave the user
  // looking at the world map with the selection changed underneath it.
  ctx.setWorldView(false);
}

/**
 * W4: screen-space points for one player's world path, restricted to zones on THIS continent (pure,
 * reuses the same project() every other drawing in this file uses - no duplicated projection math).
 * visits: worldPath(trail) output, already chronological by enteredAt. zones: this continent's zone
 * list. A visit whose mapId has no matching zone here (zero-size rects excluded too, same guard as
 * the zone-outline loop and layoutChips above) is simply skipped - it may be on a different continent,
 * or an unmapped zone - no crash, no placeholder point. Returns ordered [{x, y, enteredAt}, ...].
 */
export function worldPathPoints(visits, zones, project) {
  const byId = new Map();
  for (const z of zones) {
    const pt = zonePoint(z);
    if (pt) byId.set(z.mapId, pt);
  }
  const points = [];
  for (const v of visits || []) {
    const pt = byId.get(v.mapId);
    if (!pt) continue;
    const [x, y] = project(pt.x, pt.y);
    points.push({ x, y, enteredAt: v.enteredAt });
  }
  return points;
}

/**
 * W4: one path per tracked player, built from their own trail (state.trackedData) - a guid with no
 * trackedData entry yet (sweep hasn't landed one), or an empty/missing trail, simply gets an empty
 * path, never a throw. Returns [{guid, points}, ...] in state.tracked's order.
 */
export function computeTrackedPaths(tracked, trackedData, zones, project) {
  const paths = [];
  for (const guid of tracked.keys()) {
    const data = trackedData.get(guid);
    const trail = data && Array.isArray(data.trail) ? data.trail : [];
    paths.push({ guid, points: worldPathPoints(worldPath(trail), zones, project) });
  }
  return paths;
}

const PATH_LINE_MIN_W = 1.5;
const PATH_LINE_MAX_W = 4;
const PATH_DOT_R_PX = 3;
const PATH_ARROW_LEN_PX = 10;
const PATH_ARROW_SPREAD = Math.PI / 7;

/**
 * W4: paints one player's path in `color` on any canvas-2D-like context - kept separate from the DOM
 * (same spirit as draw.js's paintScene/paintPips) so it's testable with a plain fake context, no
 * canvas/DOM needed. Direction is shown two ways: the connecting line grows from PATH_LINE_MIN_W to
 * PATH_LINE_MAX_W toward the most recent segment, and a filled arrowhead sits at the final point
 * pointing along the last segment. A small dot marks every visited zone's point (drawn for all points,
 * even a lone one). No-op for an empty path; a single-point path draws just its dot (nothing to connect
 * or point toward).
 */
export function paintWorldPath(g, points, color) {
  if (!Array.isArray(points) || points.length === 0) return;
  g.strokeStyle = color;
  g.fillStyle = color;
  if (points.length >= 2) {
    const segs = points.length - 1;
    for (let i = 1; i < points.length; i++) {
      const t = segs === 1 ? 1 : (i - 1) / (segs - 1);
      g.lineWidth = PATH_LINE_MIN_W + (PATH_LINE_MAX_W - PATH_LINE_MIN_W) * t;
      g.beginPath();
      g.moveTo(points[i - 1].x, points[i - 1].y);
      g.lineTo(points[i].x, points[i].y);
      g.stroke();
    }
    const a = points[points.length - 2];
    const b = points[points.length - 1];
    const ang = Math.atan2(b.y - a.y, b.x - a.x) + Math.PI; // pointing back from the final point
    g.beginPath();
    g.moveTo(b.x, b.y);
    g.lineTo(b.x + Math.cos(ang - PATH_ARROW_SPREAD) * PATH_ARROW_LEN_PX, b.y + Math.sin(ang - PATH_ARROW_SPREAD) * PATH_ARROW_LEN_PX);
    g.lineTo(b.x + Math.cos(ang + PATH_ARROW_SPREAD) * PATH_ARROW_LEN_PX, b.y + Math.sin(ang + PATH_ARROW_SPREAD) * PATH_ARROW_LEN_PX);
    g.closePath();
    g.fill();
  }
  for (const p of points) {
    g.beginPath();
    g.arc(p.x, p.y, PATH_DOT_R_PX, 0, Math.PI * 2);
    g.fill();
  }
}

/** The first chip (if any) whose circle contains (x, y), with CHIP_HIT_PAD_PX of extra slack. */
function hitChip(chips, x, y) {
  for (const c of chips) {
    const pad = c.r + CHIP_HIT_PAD_PX;
    const dx = x - c.x;
    const dy = y - c.y;
    if (dx * dx + dy * dy <= pad * pad) return c;
  }
  return null;
}

const TIP_OFFSET_PX = 10;

/**
 * Mousedown+drag pans, wheel zooms, dblclick resets - same feel as radar-canvas.js's wireInput -
 * plus (W3b) hovering a pie shows `tip` with its zone name + exact counts, same tip-div pattern
 * radar-canvas.js uses.
 */
function wireInput(ctx, canvas, tip, ui) {
  let drag = null;
  const local = (e) => ({ x: e.clientX - canvas.getBoundingClientRect().left, y: e.clientY - canvas.getBoundingClientRect().top });
  const size = () => ({ w: canvas.clientWidth || 600, h: canvas.clientHeight || 280 });
  canvas.addEventListener('mousedown', (e) => {
    const m = local(e);
    drag = { x: m.x, y: m.y, view: { ...ui.view } };
  });
  // W5: a mouseup within CLICK_MOVE_PX of mousedown is a click, not a drag - same distinction and
  // threshold as radar-canvas.js's own pan-vs-click logic. Clicking a pie goes to its zone with the
  // clicked slice's filter; a click that misses every pie is a no-op.
  canvas.addEventListener('mouseup', (e) => {
    const m = local(e);
    const wasClick = drag && Math.hypot(m.x - drag.x, m.y - drag.y) < CLICK_MOVE_PX;
    drag = null;
    if (!wasClick) return;
    const chip = hitChip(ui.chips, m.x, m.y);
    if (chip) gotoZone(ctx, chip.mapId, chip.name, sliceAt(chip, m.x, m.y));
  });
  canvas.addEventListener('mousemove', (e) => {
    const m = local(e);
    if (drag) {
      ui.view = panFrom(drag.view, ui.frameFor(size()), m.x - drag.x, m.y - drag.y);
      ui.draw();
    }
    const hit = hitChip(ui.chips, m.x, m.y);
    tip.hidden = !hit;
    if (hit) {
      tip.textContent = chipTooltipModel(hit, { MSG: ctx.MSG, fmt: ctx.fmt }).join('\n');
      tip.style.left = `${m.x + TIP_OFFSET_PX}px`;
      tip.style.top = `${m.y + TIP_OFFSET_PX}px`;
    }
  });
  canvas.addEventListener('mouseleave', () => { drag = null; tip.hidden = true; });
  canvas.addEventListener('dblclick', () => { ui.view = { ...DEFAULT_VIEW }; ui.draw(); });
  canvas.addEventListener('wheel', (e) => {
    if (e.preventDefault) e.preventDefault();
    const up = e.deltaY < 0;
    const m = local(e);
    ui.view = clampView(zoomAt(ui.view, ui.frameFor(size()), m.x, m.y, up ? WHEEL_ZOOM : 1 / WHEEL_ZOOM));
    ui.draw();
  });
}
