// Canvas painting for a scene built by scene.js. The only file that touches the 2D context.
import { trackColorClass } from './trackcolors.js';

const PIP_RADIUS_PX = 5;
const SELECT_RING_PX = 9;
const HOVER_RING_PX = 11;
const GRID_STEPS = 10;
const UNTRACKED_ALPHA = 0.35;
const TRACK_RING_PX = 8;
const DEFAULTS = {
  bg: '#0a0d12', line: '#262d3a', friendly: '#3b82f6', hostile: '#ef4444', neutral: '#f59e0b', stale: '#4b5563', text: '#e6e9ef',
  'track-1': '#332288', 'track-2': '#88ccee', 'track-3': '#44aa99', 'track-4': '#117733',
  'track-5': '#999933', 'track-6': '#882255', 'track-7': '#aa4499', 'track-8': '#ddcc77',
};

/** Reads the theme colours from CSS custom properties (falls back to the defaults). */
export function readColors(win, root) {
  const out = { ...DEFAULTS };
  if (!win || !win.getComputedStyle || !root) return out;
  const style = win.getComputedStyle(root);
  const keys = [
    ['friendly', '--friendly'], ['hostile', '--hostile'], ['neutral', '--world-neutral'], ['stale', '--stale'], ['line', '--line'], ['text', '--text'],
    ['track-1', '--track-1'], ['track-2', '--track-2'], ['track-3', '--track-3'], ['track-4', '--track-4'],
    ['track-5', '--track-5'], ['track-6', '--track-6'], ['track-7', '--track-7'], ['track-8', '--track-8'],
  ];
  for (const [key, cssVar] of keys) {
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

/**
 * Stage 11 U4b: when a tracked set is active (hasTracked), non-tracked pips are drawn faded (hit-testing
 * still works off the same sx/sy, labels were never drawn on the canvas so there's nothing to hide), and
 * tracked pips get a ring in their stable per-guid track colour (trackcolors.js) so the radar matches the
 * list's track-colour swatches.
 */
function paintPips(g, pips, colors, hasTracked) {
  for (const p of pips) {
    const faded = hasTracked && !p.tracked;
    g.globalAlpha = faded ? UNTRACKED_ALPHA : 1;
    g.beginPath();
    g.arc(p.sx, p.sy, PIP_RADIUS_PX, 0, Math.PI * 2);
    g.fillStyle = colors[p.color] || colors.hostile;
    g.fill();
    if (hasTracked && p.tracked) {
      g.strokeStyle = colors[trackColorClass(p.guid)] || colors.text;
      g.lineWidth = 2;
      g.beginPath();
      g.arc(p.sx, p.sy, TRACK_RING_PX, 0, Math.PI * 2);
      g.stroke();
    }
    g.globalAlpha = 1;
    if (!p.selected) continue;
    g.strokeStyle = colors.text;
    g.lineWidth = 2;
    g.beginPath();
    g.arc(p.sx, p.sy, SELECT_RING_PX, 0, Math.PI * 2);
    g.stroke();
  }
}

/**
 * Stage 11 U5: highlights the hovered dot with a slightly larger ring than the selection ring, and for
 * a hovered trail (history) point also draws thin lines to its immediate chronological neighbours
 * (scene.trail is already time-ordered) so the path direction is obvious. hover: the resolved point
 * from tooltip.js's resolveHoverPoint ({sx,sy,isTrail,prev,next}), or null for no hover.
 */
function paintHover(g, hover, colors) {
  if (!hover) return;
  g.strokeStyle = colors.text;
  g.lineWidth = 2;
  g.beginPath();
  g.arc(hover.sx, hover.sy, HOVER_RING_PX, 0, Math.PI * 2);
  g.stroke();
  if (!hover.isTrail) return;
  g.lineWidth = 1;
  g.beginPath();
  if (hover.prev) {
    g.moveTo(hover.prev.sx, hover.prev.sy);
    g.lineTo(hover.sx, hover.sy);
  }
  if (hover.next) {
    g.moveTo(hover.sx, hover.sy);
    g.lineTo(hover.next.sx, hover.next.sy);
  }
  g.stroke();
}

/** Paints one frame. img: a loaded image for the map background or null. hover: see paintHover. */
export function paintScene(g, scene, size, colors, img, hover = null) {
  g.clearRect(0, 0, size.w, size.h);
  g.fillStyle = colors.bg;
  g.fillRect(0, 0, size.w, size.h);
  if (scene.fixed) paintFrame(g, scene, colors, img);
  else paintRings(g, size, colors);
  paintTrail(g, scene.trail, colors);
  paintPips(g, scene.pips, colors, scene.hasTracked);
  paintHover(g, hover, colors);
}
