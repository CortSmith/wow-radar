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

function paintFrame(g, scene, colors, img) {
  const f = scene.frame;
  if (img) g.drawImage(img, f.x, f.y, f.w, f.h);
  g.strokeStyle = colors.line;
  g.lineWidth = 1;
  g.strokeRect(f.x, f.y, f.w, f.h);
  g.beginPath();
  for (let i = 1; i < GRID_STEPS; i++) {
    g.moveTo(f.x + (f.w * i) / GRID_STEPS, f.y);
    g.lineTo(f.x + (f.w * i) / GRID_STEPS, f.y + f.h);
    g.moveTo(f.x, f.y + (f.h * i) / GRID_STEPS);
    g.lineTo(f.x + f.w, f.y + (f.h * i) / GRID_STEPS);
  }
  g.globalAlpha = 0.35;
  g.stroke();
  g.globalAlpha = 1;
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
