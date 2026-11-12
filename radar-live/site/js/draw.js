// Canvas painting for a scene built by scene.js. The only file that touches the 2D context.
const PIP_RADIUS_PX = 5;
const SELECT_RING_PX = 9;
const GRID_STEPS = 10;
const DEFAULTS = { bg: '#0a0d12', line: '#262d3a', friendly: '#3b82f6', hostile: '#ef4444', stale: '#4b5563', text: '#e6e9ef' };

/** Reads the theme colours from CSS custom properties (falls back to the defaults). */
export function readColors(win, root) {
  const out = { ...DEFAULTS };
  if (!win || !win.getComputedStyle || !root) return out;
  const style = win.getComputedStyle(root);
  for (const [key, cssVar] of [['friendly', '--friendly'], ['hostile', '--hostile'], ['stale', '--stale'], ['line', '--line'], ['text', '--text']]) {
    const v = style.getPropertyValue(cssVar).trim();
    if (v) out[key] = v;
  }
  return out;
}

const FULL_PCT = 100;
const UNZOOMED = { z: 1, cx: 50, cy: 50 };

/** The part of the map on screen, in percent of the whole map: [left, top, span]. Same maths as scene.js. */
function visibleWindow(view) {
  const span = FULL_PCT / view.z;
  return { left: view.cx - span / 2, top: view.cy - span / 2, span };
}

/** Draws only the visible crop of the map image, stretched over the frame, so the map zooms with the dots. */
function paintMapImage(g, f, view, img) {
  const w = img.naturalWidth || img.width;
  const h = img.naturalHeight || img.height;
  const win = visibleWindow(view);
  const scale = (value, whole) => (value / FULL_PCT) * whole;
  g.drawImage(img, scale(win.left, w), scale(win.top, h), scale(win.span, w), scale(win.span, h), f.x, f.y, f.w, f.h);
}

/** Grid lines every 10 percent of the map; lines outside the visible part are skipped. */
function paintGrid(g, f, view) {
  const win = visibleWindow(view);
  const step = FULL_PCT / GRID_STEPS;
  const toScreen = (pct, start, size, from) => from + ((pct - start) / win.span) * size;
  g.beginPath();
  for (let pct = step; pct < FULL_PCT; pct += step) {
    const x = toScreen(pct, win.left, f.w, f.x);
    const y = toScreen(pct, win.top, f.h, f.y);
    if (x > f.x && x < f.x + f.w) {
      g.moveTo(x, f.y);
      g.lineTo(x, f.y + f.h);
    }
    if (y > f.y && y < f.y + f.h) {
      g.moveTo(f.x, y);
      g.lineTo(f.x + f.w, y);
    }
  }
  g.globalAlpha = 0.35;
  g.stroke();
  g.globalAlpha = 1;
}

function paintFrame(g, scene, colors, img) {
  const f = scene.frame;
  const view = scene.view || UNZOOMED;
  if (img) paintMapImage(g, f, view, img);
  g.strokeStyle = colors.line;
  g.lineWidth = 1;
  g.strokeRect(f.x, f.y, f.w, f.h);
  paintGrid(g, f, view);
}

function paintRings(g, size, colors) {
  g.strokeStyle = colors.line;
  g.lineWidth = 1;
  for (const k of [0.25, 0.5, 0.75, 1]) {
    g.beginPath();
    g.arc(size.w / 2, size.h / 2, (Math.min(size.w, size.h) / 2) * k, 0, Math.PI * 2);
    g.stroke();
  }
}

function paintTrail(g, trail, colors) {
  if (!trail.length) return;
  g.strokeStyle = colors.friendly;
  g.setLineDash([2, 4]);
  g.lineWidth = 1.5;
  g.beginPath();
  trail.forEach((p, i) => (i ? g.lineTo(p.sx, p.sy) : g.moveTo(p.sx, p.sy)));
  g.stroke();
  g.setLineDash([]);
  for (const p of trail) {
    g.beginPath();
    g.arc(p.sx, p.sy, 2, 0, Math.PI * 2);
    if (p.kind === 'approx') g.stroke();
    else {
      g.fillStyle = colors.friendly;
      g.fill();
    }
  }
}

function paintPips(g, pips, colors) {
  for (const p of pips) {
    g.beginPath();
    g.arc(p.sx, p.sy, PIP_RADIUS_PX, 0, Math.PI * 2);
    g.fillStyle = colors[p.color] || colors.hostile;
    g.fill();
    if (!p.selected) continue;
    g.strokeStyle = colors.text;
    g.lineWidth = 2;
    g.beginPath();
    g.arc(p.sx, p.sy, SELECT_RING_PX, 0, Math.PI * 2);
    g.stroke();
  }
}

/** Paints one frame. img: a loaded image for the map background or null. */
export function paintScene(g, scene, size, colors, img) {
  g.clearRect(0, 0, size.w, size.h);
  g.fillStyle = colors.bg;
  g.fillRect(0, 0, size.w, size.h);
  if (scene.fixed) paintFrame(g, scene, colors, img);
  else paintRings(g, size, colors);
  paintTrail(g, scene.trail, colors);
  paintPips(g, scene.pips, colors);
}
