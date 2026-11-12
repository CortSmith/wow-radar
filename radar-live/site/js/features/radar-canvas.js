// The radar: canvas, tooltip, range/map/fixed-frame controls, pan/zoom, click-to-select.
import { buildScene } from '../scene.js';
import { paintScene, readColors } from '../draw.js';
import { clampView, panFrom, zoomAt, hitTest, hitTestScene } from '../projection.js';
import { fmtAge } from '../format.js';
import { tooltipModel, resolveHoverPoint, hoverKeyFor } from '../tooltip.js';

const DEFAULT_VIEW = { z: 1, cx: 50, cy: 50 };
const HIT_RADIUS_PX = 10;
const WHEEL_ZOOM = 1.2;
const RANGE = { min: 40, max: 3000, step: 10, initial: 300 };
const TIP_OFFSET_PX = 12;

export default {
  name: 'radar-canvas',
  init(ctx) {
    const { MSG, state } = ctx;
    const wrap = ctx.mount('radar');
    if (!wrap) return;
    const canvas = ctx.el('canvas', { attrs: { 'aria-label': MSG.title } });
    const tip = ctx.el('div', { className: 'tip', hidden: true });
    const empty = ctx.el('div', { className: 'empty muted', text: MSG.noData });
    wrap.appendChild(canvas);
    wrap.appendChild(tip);
    wrap.appendChild(empty);

    const ui = { view: { ...DEFAULT_VIEW }, fixed: true, bg: true, rangeYd: RANGE.initial, scene: null, img: null, imgUrl: '', hoverKey: null };
    buildControls(ctx, ui);
    wireInput(ctx, canvas, tip, ui);

    const colors = readColors(ctx.win, ctx.doc.documentElement || null);
    function draw() {
      if (wrap.hidden) return; // world view is showing: nothing to paint, and a hidden canvas has no size
      const size = { w: canvas.clientWidth || 800, h: canvas.clientHeight || 600 };
      syncImage(ctx, ui);
      const image = ui.bg && ui.img && ui.img.complete ? { w: ui.img.naturalWidth, h: ui.img.naturalHeight } : null;
      ui.scene = buildScene({ state, now: ctx.now(), size, view: ui.view, opts: { fixed: ui.fixed, rangeYd: ui.rangeYd }, image });
      empty.hidden = ui.scene.pips.length > 0;
      const g = canvas.getContext('2d');
      if (!g) return;
      const dpr = (ctx.win && ctx.win.devicePixelRatio) || 1;
      // Resizing a canvas reallocates its pixel buffer, so only do it when the size really changed.
      const pw = Math.round(size.w * dpr);
      const ph = Math.round(size.h * dpr);
      if (canvas.width !== pw) canvas.width = pw;
      if (canvas.height !== ph) canvas.height = ph;
      g.setTransform(dpr, 0, 0, dpr, 0, 0);
      paintScene(g, ui.scene, size, colors, image ? ui.img : null, resolveHoverPoint(ui.scene, ui.hoverKey));
    }
    ui.draw = draw;
    ui.fmtTip = (hit) => tooltipModel(hit, tipCtx(ctx, fmtAge)).join('\n');
    for (const ev of ['sweep', 'reset', 'selection', 'filters', 'view', 'charts', 'tick', 'worldView']) ctx.bus.on(ev, draw);
    if (ctx.win) ctx.win.addEventListener('resize', draw);
    draw();
  },
};

/** The small plain object tooltipModel()'s pure ctx parameter needs, built from the page ctx. */
function tipCtx(ctx, fmtAge) {
  return { MSG: ctx.MSG, fmt: ctx.fmt, now: ctx.now, fmtAge, fmtTime: ctx.fmtTime, state: ctx.state };
}

/** Loads the zone image named by state.mapImage (optional; failures just leave the background empty). */
function syncImage(ctx, ui) {
  const want = ctx.state.mapImage ? ctx.state.mapImage.url : '';
  if (want === ui.imgUrl) return;
  ui.imgUrl = want;
  ui.img = null;
  if (!want) return;
  const img = ctx.doc.createElement('img');
  img.addEventListener('load', () => ui.draw && ui.draw());
  img.src = want;
  ui.img = img;
}

function checkbox(ctx, label, hint, checked, onChange) {
  const input = ctx.el('input', { type: 'checkbox', checked, on: { change: (e) => onChange(e.target.checked) } });
  return ctx.el('label', { title: hint }, input, label);
}

function buildControls(ctx, ui) {
  const host = ctx.mount('header-controls');
  if (!host) return;
  const { MSG } = ctx;
  const val = ctx.el('span', { text: String(ui.rangeYd) });
  const range = ctx.el('input', {
    type: 'range', min: RANGE.min, max: RANGE.max, step: RANGE.step, value: String(ui.rangeYd),
    on: { input: (e) => { ui.rangeYd = Number(e.target.value); val.textContent = String(ui.rangeYd); ui.draw(); } },
  });
  ui.setRange = (v) => { ui.rangeYd = Math.max(RANGE.min, Math.min(RANGE.max, v)); range.value = String(ui.rangeYd); val.textContent = String(ui.rangeYd); };
  host.appendChild(ctx.el('label', { title: MSG.rangeHint }, `${MSG.range} `, range, val, 'y'));
  host.appendChild(checkbox(ctx, MSG.mapBg, MSG.mapBg, ui.bg, (v) => { ui.bg = v; ui.draw(); }));
  host.appendChild(checkbox(ctx, MSG.fixedMap, MSG.fixedMapHint, ui.fixed, (v) => { ui.fixed = v; ui.view = { ...DEFAULT_VIEW }; ui.draw(); }));
}

function wireInput(ctx, canvas, tip, ui) {
  let drag = null;
  const local = (e) => ({ x: e.clientX - (e.rect ? e.rect.left : canvas.getBoundingClientRect().left), y: e.clientY - canvas.getBoundingClientRect().top });
  canvas.addEventListener('mousemove', (e) => {
    const m = local(e);
    if (drag && ui.scene.fixed) {
      ui.view = panFrom(drag.view, ui.scene.frame, m.x - drag.x, m.y - drag.y);
      ui.draw();
      return;
    }
    // Stage 11 U5: tests both the pips and the drawn trail (history) points, same HIT_RADIUS_PX, pips
    // winning ties - so hovering a historical dot of a tracked player's trail shows a tooltip too.
    const hit = ui.scene ? hitTestScene(ui.scene.pips, ui.scene.trail, m.x, m.y, HIT_RADIUS_PX) : null;
    const hoverKey = hoverKeyFor(hit);
    const hoverChanged = hoverKey !== ui.hoverKey;
    ui.hoverKey = hoverKey;
    tip.hidden = !hit;
    if (hit) {
      tip.textContent = ui.fmtTip(hit);
      tip.style.left = `${m.x + TIP_OFFSET_PX}px`;
      tip.style.top = `${m.y + TIP_OFFSET_PX}px`;
    }
    if (hoverChanged) ui.draw(); // the hover ring (and a trail point's neighbour lines) only change with the hovered item
  });
  canvas.addEventListener('mousedown', (e) => {
    const m = local(e);
    drag = { x: m.x, y: m.y, view: { ...ui.view }, moved: false };
  });
  canvas.addEventListener('mouseup', (e) => {
    const m = local(e);
    const wasClick = drag && Math.hypot(m.x - drag.x, m.y - drag.y) < 4;
    drag = null;
    const hit = wasClick && ui.scene ? hitTest(ui.scene.pips, m.x, m.y, HIT_RADIUS_PX) : null;
    if (!wasClick) return;
    if (!hit) ctx.deselect();
    else if (ctx.state.player && ctx.state.player.guid === hit.guid) ctx.deselect();
    else ctx.setPlayer({ guid: hit.guid, name: hit.name });
  });
  canvas.addEventListener('mouseleave', () => { drag = null; tip.hidden = true; ui.hoverKey = null; ui.draw(); });
  canvas.addEventListener('dblclick', () => { ui.view = { ...DEFAULT_VIEW }; ui.draw(); });
  canvas.addEventListener('wheel', (e) => {
    if (e.preventDefault) e.preventDefault();
    const up = e.deltaY < 0;
    if (ui.scene && ui.scene.fixed) {
      const m = local(e);
      ui.view = clampView(zoomAt(ui.view, ui.scene.frame, m.x, m.y, up ? WHEEL_ZOOM : 1 / WHEEL_ZOOM));
    } else if (ui.setRange) {
      ui.setRange(ui.rangeYd * (up ? 1 / WHEEL_ZOOM : WHEEL_ZOOM));
    }
    ui.draw();
  });
}
