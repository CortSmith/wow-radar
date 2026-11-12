// Radar geometry (no DOM). The fixed frame maps a chart rectangle to 0..100 (left = high y, top = high x).
const FULL = 100;
const HALF = 50;
export const ZOOM_MAX = 40;

/** World (x,y) -> frame percent [px,py] for a chart {x0,x1,y0,y1}. */
export function worldToPct(chart, x, y) {
  const dx = chart.x1 - chart.x0;
  const dy = chart.y1 - chart.y0;
  if (!dx || !dy) return [HALF, HALF];
  return [((chart.y1 - y) / dy) * FULL, ((chart.x1 - x) / dx) * FULL];
}

/**
 * Stage 14 W3a: image-pixel (x,y) -> frame percent [px,py] for an image of size w x h. Plain pixel
 * space, no axis swap/flip (unlike worldToPct, which is for zone charts in game-world coordinates) -
 * a continent PNG's own x/y already run top-left to bottom-right like any other image.
 */
export function imageToPct(w, h, x, y) {
  if (!w || !h) return [HALF, HALF];
  return [(x / w) * FULL, (y / h) * FULL];
}

/**
 * Stage 14 W3a: projector for a continent-pixel-space image (w x h) onto a screen frame, given a
 * pan/zoom view - the same shape as scene.js's fixedProjector, but for plain image pixels instead of
 * a zone chart's world coordinates, so continent rectangles and PNGs can reuse the same pan/zoom view
 * {z,cx,cy} and the same clampView/panFrom/zoomAt helpers above.
 */
export function continentProjector(w, h, frame, view) {
  const span = FULL / view.z;
  return (x, y) => {
    const [px, py] = imageToPct(w, h, x, y);
    return [frame.x + ((px - (view.cx - span / 2)) / span) * frame.w, frame.y + ((py - (view.cy - span / 2)) / span) * frame.h];
  };
}

/** Largest rect with the given aspect inside w x h minus pad, centred. */
export function fitFrame(w, h, aspect, pad) {
  const aw = w - pad * 2;
  const ah = h - pad * 2;
  let fw = aw;
  let fh = aw / aspect;
  if (fh > ah) {
    fh = ah;
    fw = ah * aspect;
  }
  return { x: (w - fw) / 2, y: (h - fh) / 2, w: fw, h: fh };
}

/** Clamp zoom >= 1 and the view centre so the view stays inside the map. */
export function clampView(v) {
  const z = Math.max(1, Math.min(ZOOM_MAX, v.z));
  const half = HALF / z;
  return { z, cx: Math.max(half, Math.min(FULL - half, v.cx)), cy: Math.max(half, Math.min(FULL - half, v.cy)) };
}

/** Pan by (dx,dy) screen px from the view `from`. */
export function panFrom(from, frame, dx, dy) {
  return clampView({
    z: from.z,
    cx: from.cx - dx * (FULL / (frame.w * from.z)),
    cy: from.cy - dy * (FULL / (frame.h * from.z)),
  });
}

/** Zoom by factor keeping the frame point under the cursor (mx,my) fixed. */
export function zoomAt(view, frame, mx, my, factor) {
  const nz = Math.max(1, Math.min(ZOOM_MAX, view.z * factor));
  const fx = (mx - frame.x) / frame.w;
  const fy = (my - frame.y) / frame.h;
  const px = view.cx - HALF / view.z + fx * (FULL / view.z);
  const py = view.cy - HALF / view.z + fy * (FULL / view.z);
  return clampView({ z: nz, cx: px - fx * (FULL / nz) + HALF / nz, cy: py - fy * (FULL / nz) + HALF / nz });
}

/** Free (no chart) projection: centre in world units, range in yards = half the shorter side. North up, east right. */
export function freeProjection({ w, h, center, rangeYd }) {
  const scale = Math.min(w, h) / 2 / rangeYd;
  return (x, y) => [w / 2 - (y - center.y) * scale, h / 2 - (x - center.x) * scale];
}

/** Nearest point (with sx, sy) within radius of (mx,my), or null. */
export function hitTest(points, mx, my, radius) {
  let best = null;
  let bestD = radius * radius;
  for (const p of points) {
    const d = (p.sx - mx) ** 2 + (p.sy - my) ** 2;
    if (d <= bestD) {
      best = p;
      bestD = d;
    }
  }
  return best;
}

/**
 * Stage 11 U5: hit-tests both the live pips and the drawn trail (history) points with the same
 * radius, so hovering a historical dot shows a tooltip too. Pips win ties: any pip within radius
 * is returned before the trail is even tested, so a pip sitting exactly on top of a historical fix
 * always shows as the live pip, never the history point underneath it.
 */
export function hitTestScene(pips, trail, mx, my, radius) {
  return hitTest(pips, mx, my, radius) || hitTest(trail, mx, my, radius);
}
