'use strict';
/**
 * blips.js - single responsibility: DOM-free radar geometry and filtering for blips (contacts drawn on the canvas).
 *
 * Public API (Radar.blips): frameRect, clampView, panFrom, zoomAt (fixed-chart frame zoom/pan), calKey (calibration
 * keys),
 * projection(o) (world -> screen for fixed and free modes), centre, ringStep, rangeWheel, visible(blip, flags, trail),
 * onMap(blip, viewId), viewId(snapshot, chosen), trackBuckets(n, buckets) (trail fade steps), isApprox(fix),
 * colorOf(blip),
 * COLORS, ZMAX. Canvas painting itself lives in canvas.js.
 */
(function (g) {
  const dep = (name, file) =>
    typeof require !== 'undefined' && typeof module !== 'undefined' ? require(file) : g.Radar[name];
  const charts = dep('charts', './charts.js');
  const CFG = dep('config', './config.js');
  const GEO = CFG.geometry;
  const ZMAX = GEO.ZOOM_MAX;
  const COLORS = CFG.draw.PIP_COLOR;
  const PCT_FULL = CFG.units.PCT_FULL;
  const PCT_HALF = PCT_FULL / 2;
  const IDENT = () => ({ dx: 0, dy: 0, sx: 1, sy: 1 });

  const colorOf = blip => (blip.state === 'stale' ? COLORS.stale : COLORS[blip.reaction] || COLORS.unknown);

  /**
   * Image frame inside the canvas (w x h in CSS px) for a map image of zi.w x zi.h.
   * @param {Object} w the w
   * @param {Object} h the h
   * @param {Object} zi the zi
   * @returns {*} the computed value
   */
  function frameRect(w, h, zi) {
    const pad = GEO.FRAME_PAD_PX,
      ar = ((zi && zi.w) || GEO.DEFAULT_IMAGE_W) / ((zi && zi.h) || GEO.DEFAULT_IMAGE_H),
      aw = w - pad * 2,
      ah = h - pad * 2;
    let fw = aw,
      fh = aw / ar;
    if (fh > ah) {
      fh = ah;
      fw = ah * ar;
    }
    return { x: (w - fw) / 2, y: (h - fh) / 2 + GEO.FRAME_Y_OFFSET_PX, w: fw, h: fh };
  }
  /**
   * Zoom z >= 1, view centre (cx,cy) in frame percent; the view never leaves the map (0..100).
   * @param {Object} fz zoom state {z, cx, cy}
   * @returns {*} the computed value
   */
  function clampView(fz) {
    fz.z = Math.max(1, Math.min(ZMAX, fz.z));
    const half = PCT_HALF / fz.z;
    fz.cx = Math.max(half, Math.min(PCT_FULL - half, fz.cx));
    fz.cy = Math.max(half, Math.min(PCT_FULL - half, fz.cy));
    return fz;
  }
  function panFrom(from, f, z, dx, dy) {
    const fz = { z, cx: from.cx - dx * (PCT_FULL / (f.w * z)), cy: from.cy - dy * (PCT_FULL / (f.h * z)) };
    return clampView(fz);
  }
  /**
   * Zoom keeping the frame percent under the cursor fixed.
   * @param {Object} fz zoom state {z, cx, cy}
   * @param {Object} f fix
   * @param {number} mx pointer x in CSS px
   * @param {number} my pointer y in CSS px
   * @param {number} factor the factor
   * @returns {*} the computed value
   */
  function zoomAt(fz, f, mx, my, factor) {
    const half = PCT_HALF / fz.z;
    const px = fz.cx - half + ((mx - f.x) / f.w) * (PCT_FULL / fz.z),
      py = fz.cy - half + ((my - f.y) / f.h) * (PCT_FULL / fz.z);
    const nz = Math.max(1, Math.min(ZMAX, fz.z * factor)),
      nh = PCT_HALF / nz;
    return clampView({
      z: nz,
      cx: px - ((mx - f.x) / f.w) * (PCT_FULL / nz) + nh,
      cy: py - ((my - f.y) / f.h) * (PCT_FULL / nz) + nh,
    });
  }
  /**
   * Calibration nudges (Shift+arrows / +/- / 0). Returns the new calibration or null for an unhandled key.
   * @param {Object} cal calibration {dx, dy, sx, sy}
   * @param {Object} k key
   * @returns {*} the computed value
   */
  function calKey(cal, k) {
    const c = Object.assign(IDENT(), cal),
      st = GEO.CAL_NUDGE_PCT;
    if (k === 'ArrowLeft') c.dx -= st;
    else if (k === 'ArrowRight') c.dx += st;
    else if (k === 'ArrowUp') c.dy -= st;
    else if (k === 'ArrowDown') c.dy += st;
    else if (k === '+' || k === '=') {
      c.sx *= GEO.CAL_SCALE_STEP;
      c.sy *= GEO.CAL_SCALE_STEP;
    } else if (k === '_' || k === '-') {
      c.sx /= GEO.CAL_SCALE_STEP;
      c.sy /= GEO.CAL_SCALE_STEP;
    } else if (k === ')' || k === '0') return IDENT();
    else return null;
    return c;
  }

  /**
   * Projection for the current mode. o: {w,h,fixed (fit frame active),m (map rect),cal,zi,fz,me
   * (pip|null),follow,cam,range,viewId} Mutates o.fz when following in a zoomed fixed frame. Returns {fit, toScreen,
   * f, ox, oy, z, scale, c}.
   * @param {Object} o options
   * @returns {*} the computed value
   */
  function projection(o) {
    if (o.fixed) {
      const f = frameRect(o.w, o.h, o.zi),
        fz = o.fz;
      if (o.follow && fz.z > 1 && o.me && o.me.posKind !== 'none' && o.me.mapId === o.viewId) {
        const mp = charts.worldToPct(o.m, o.me.x, o.me.y, o.cal);
        fz.cx = mp[0];
        fz.cy = mp[1];
      }
      clampView(fz);
      const z = fz.z,
        half = PCT_HALF / z,
        ox = fz.cx - half,
        oy = fz.cy - half;
      const toScreen = (x, y) => {
        const p = charts.worldToPct(o.m, x, y, o.cal);
        return [f.x + (((p[0] - ox) * z) / PCT_FULL) * f.w, f.y + (((p[1] - oy) * z) / PCT_FULL) * f.h];
      };
      return { fit: true, f, z, ox, oy, scale: 1, toScreen, c: { x: 0, y: 0 }, range: 0 };
    }
    const c = centre(o),
      scale = Math.min(o.w, o.h) / 2 / o.range;
    /**
     * standard orientation: X is north (up), Y is west (left)
     * @param {number} x world x in yards
     * @param {number} y world y in yards
     * @returns {*} the computed value
     */
    const toScreen = (x, y) => [o.w / 2 - (y - c.y) * scale, o.h / 2 - (x - c.x) * scale];
    return { fit: false, c, scale, range: o.range, toScreen, w: o.w, h: o.h };
  }
  function centre(o) {
    if (o.follow && o.me && o.me.posKind !== 'none' && (!o.viewId || o.me.mapId === o.viewId))
      return { x: o.me.x, y: o.me.y };
    return o.cam;
  }
  function ringStep(range) {
    for (const [above, step] of GEO.RING_STEPS) if (range > above) return step;
    return GEO.RING_MIN_YD;
  }
  /**
   * Wheel in range mode: one notch changes the radar range by WHEEL_RANGE.
   * @param {Object} value value
   * @param {number} min the min
   * @param {number} max the max
   * @param {number} deltaY the delta y
   * @returns {*} the computed value
   */
  function rangeWheel(value, min, max, deltaY) {
    return Math.max(min, Math.min(max, Math.round(value * (deltaY > 0 ? GEO.WHEEL_RANGE : 1 / GEO.WHEEL_RANGE))));
  }

  /**
   * Visibility filters (flags: {stale, showBlue, showRed}); track = searched player (hides every other dot).
   * @param {Object} p blip or contact
   * @param {Object} flags the flags
   * @param {Object} track the track
   * @returns {*} the computed value
   */
  function visible(blip, flags, track) {
    if (track && blip.guid !== track.guid) return false;
    if (blip.me) return flags.stale || blip.state !== 'stale';
    if (!(blip.reaction === 'friendly' ? flags.showBlue : flags.showRed)) return false;
    return flags.stale || blip.state !== 'stale';
  }
  const onMap = (blip, viewId) => blip.posKind !== 'none' && (!viewId || blip.mapId === viewId);

  /**
   * Which map is viewed: user choice or the game's (live) / busiest (cloud) map.
   * @param {Object} S the s
   * @param {Object} chosen the chosen
   * @returns {*} the computed value
   */
  function viewId(S, chosen) {
    return chosen !== null && chosen !== undefined ? chosen : S ? S.mapId : 0;
  }

  /**
   * Track fixes on the viewed map and the 4-step fade buckets used by the original.
   * @param {number} n count
   * @param {Object} buckets the buckets
   * @returns {*} the computed value
   */
  function trackBuckets(n, buckets) {
    const out = [];
    for (let b = 0; b < buckets; b++) {
      const lo = Math.max(1, Math.floor((b * n) / buckets)),
        hi = Math.floor(((b + 1) * n) / buckets);
      if (hi > lo) out.push({ lo, hi, alpha: GEO.TRACK_ALPHA_MIN + (GEO.TRACK_ALPHA_SPAN * (b + 1)) / buckets });
    }
    return out;
  }
  const isApprox = f => f.kind === 'approx' || f.kind === 'a' || f.kind === 1;

  const api = {
    ZMAX,
    COLORS,
    colorOf,
    frameRect,
    clampView,
    panFrom,
    zoomAt,
    calKey,
    projection,
    centre,
    ringStep,
    rangeWheel,
    visible,
    onMap,
    viewId,
    trackBuckets,
    isApprox,
  };
  (g.Radar || (g.Radar = {})).blips = api;
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
})(typeof self !== 'undefined' ? self : globalThis);
