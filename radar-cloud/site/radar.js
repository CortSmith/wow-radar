"use strict";
// DOM-free radar geometry and filtering (frame, zoom/pan, projection, follow, visibility, track grouping).
(function (g) {
  const C = typeof require !== "undefined" && typeof module !== "undefined" ? require("./core.js") : g.RadarCore;
  const ZMAX = 12;
  const COLORS = { friendly: "#3b82f6", hostile: "#ef4444", neutral: "#ef4444", unknown: "#ef4444", stale: "#4b5563" };
  const IDENT = () => ({ dx: 0, dy: 0, sx: 1, sy: 1 });

  const colorOf = (p) => (p.state === "stale" ? COLORS.stale : COLORS[p.reaction] || COLORS.unknown);

  // Image frame inside the canvas (w x h in CSS px) for a map image of zi.w x zi.h.
  function frameRect(w, h, zi) {
    const pad = 26, ar = ((zi && zi.w) || 1002) / ((zi && zi.h) || 668), aw = w - pad * 2, ah = h - pad * 2;
    let fw = aw, fh = aw / ar;
    if (fh > ah) { fh = ah; fw = ah * ar; }
    return { x: (w - fw) / 2, y: (h - fh) / 2 + 4, w: fw, h: fh };
  }
  // Zoom z >= 1, view centre (cx,cy) in frame percent; the view never leaves the map (0..100).
  function clampView(fz) {
    fz.z = Math.max(1, Math.min(ZMAX, fz.z));
    const half = 50 / fz.z;
    fz.cx = Math.max(half, Math.min(100 - half, fz.cx));
    fz.cy = Math.max(half, Math.min(100 - half, fz.cy));
    return fz;
  }
  function panFrom(from, f, z, dx, dy) {
    const fz = { z, cx: from.cx - dx * (100 / (f.w * z)), cy: from.cy - dy * (100 / (f.h * z)) };
    return clampView(fz);
  }
  // Zoom keeping the frame percent under the cursor fixed.
  function zoomAt(fz, f, mx, my, factor) {
    const half = 50 / fz.z;
    const px = fz.cx - half + ((mx - f.x) / f.w) * (100 / fz.z), py = fz.cy - half + ((my - f.y) / f.h) * (100 / fz.z);
    const nz = Math.max(1, Math.min(ZMAX, fz.z * factor)), nh = 50 / nz;
    return clampView({ z: nz, cx: px - ((mx - f.x) / f.w) * (100 / nz) + nh, cy: py - ((my - f.y) / f.h) * (100 / nz) + nh });
  }
  // Calibration nudges (Shift+arrows / +/- / 0). Returns the new calibration or null for an unhandled key.
  function calKey(cal, k) {
    const c = Object.assign(IDENT(), cal), st = 0.5;
    if (k === "ArrowLeft") c.dx -= st; else if (k === "ArrowRight") c.dx += st;
    else if (k === "ArrowUp") c.dy -= st; else if (k === "ArrowDown") c.dy += st;
    else if (k === "+" || k === "=") { c.sx *= 1.005; c.sy *= 1.005; }
    else if (k === "_" || k === "-") { c.sx /= 1.005; c.sy /= 1.005; }
    else if (k === ")" || k === "0") return IDENT();
    else return null;
    return c;
  }

  // Projection for the current mode. o: {w,h,fixed (fit frame active),m (map rect),cal,zi,fz,me (pip|null),follow,cam,range,viewId}
  // Mutates o.fz when following in a zoomed fixed frame. Returns {fit, toScreen, f, ox, oy, z, scale, c}.
  function projection(o) {
    if (o.fixed) {
      const f = frameRect(o.w, o.h, o.zi), fz = o.fz;
      if (o.follow && fz.z > 1 && o.me && o.me.posKind !== "none" && o.me.mapId === o.viewId) {
        const mp = C.worldToPct(o.m, o.me.x, o.me.y, o.cal); fz.cx = mp[0]; fz.cy = mp[1];
      }
      clampView(fz);
      const z = fz.z, half = 50 / z, ox = fz.cx - half, oy = fz.cy - half;
      const toScreen = (x, y) => { const p = C.worldToPct(o.m, x, y, o.cal); return [f.x + ((p[0] - ox) * z / 100) * f.w, f.y + ((p[1] - oy) * z / 100) * f.h]; };
      return { fit: true, f, z, ox, oy, scale: 1, toScreen, c: { x: 0, y: 0 }, range: 0 };
    }
    const c = centre(o), scale = Math.min(o.w, o.h) / 2 / o.range;
    // standard orientation: X is north (up), Y is west (left)
    const toScreen = (x, y) => [o.w / 2 - (y - c.y) * scale, o.h / 2 - (x - c.x) * scale];
    return { fit: false, c, scale, range: o.range, toScreen, w: o.w, h: o.h };
  }
  function centre(o) {
    if (o.follow && o.me && o.me.posKind !== "none" && (!o.viewId || o.me.mapId === o.viewId)) return { x: o.me.x, y: o.me.y };
    return o.cam;
  }
  function ringStep(range) { return range > 1500 ? 500 : range > 600 ? 200 : range > 200 ? 100 : 25; }
  // Wheel in range mode.
  function rangeWheel(value, min, max, deltaY) { return Math.max(min, Math.min(max, Math.round(value * (deltaY > 0 ? 1.15 : 1 / 1.15)))); }

  // Visibility filters (flags: {stale, showBlue, showRed}); track = searched player (hides every other dot).
  function visible(p, flags, track) {
    if (track && p.guid !== track.guid) return false;
    if (p.me) return flags.stale || p.state !== "stale";
    if (!(p.reaction === "friendly" ? flags.showBlue : flags.showRed)) return false;
    return flags.stale || p.state !== "stale";
  }
  const onMap = (p, viewId) => p.posKind !== "none" && (!viewId || p.mapId === viewId);

  // Which map is viewed: user choice or the game's (live) / busiest (cloud) map.
  function viewId(S, chosen) { return chosen !== null && chosen !== undefined ? chosen : S ? S.mapId : 0; }

  // Track fixes on the viewed map and the 4-step fade buckets used by the original.
  function trackBuckets(n, buckets) {
    const out = [];
    for (let b = 0; b < buckets; b++) {
      const lo = Math.max(1, Math.floor((b * n) / buckets)), hi = Math.floor(((b + 1) * n) / buckets);
      if (hi > lo) out.push({ lo, hi, alpha: 0.3 + (0.6 * (b + 1)) / buckets });
    }
    return out;
  }
  const isApprox = (f) => f.kind === "approx" || f.kind === "a" || f.kind === 1;

  const api = { ZMAX, COLORS, colorOf, frameRect, clampView, panFrom, zoomAt, calKey, projection, centre, ringStep, rangeWheel, visible, onMap, viewId, trackBuckets, isApprox };
  g.RadarGeo = api;
  if (typeof module !== "undefined" && module.exports) module.exports = api;
})(typeof self !== "undefined" ? self : globalThis);
