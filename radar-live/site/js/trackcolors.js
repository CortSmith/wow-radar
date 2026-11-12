// Stage 11 U3: a stable per-guid "track colour" shared by every feature that marks a tracked player
// (search chips here; the roster list and radar dots/trails in later tasks U4/U5).
//
// Stability strategy: the colour is a PURE function of the guid string (a small 32-bit hash mod the
// palette size), not an insertion-order assignment remembered in a Map. Insertion order would need one
// Map shared across independently-initialised features (search.js, contact-list.js, radar-canvas.js)
// to agree, which would couple those modules together and break the moment one of them renders before
// the others have seen the same guids in the same order. A pure hash needs no shared state: any module
// that imports trackColorIndex/trackColorClass gets the same answer for the same guid, for as long as
// that guid stays tracked, with zero coordination.
//
// Palette: 8 colours from Paul Tol's "muted" qualitative scheme (a published colour-blind-safe palette),
// with the entries closest to this app's existing blue/red/grey/gold tokens dropped, so track colours
// never get confused with friendly/hostile/stale/bounty swatches. CSS custom properties --track-1..
// --track-8 (style.css, under :root) hold the actual colours; this module only picks the INDEX.

export const TRACK_COLOR_COUNT = 8;

/** Deterministic 32-bit hash (FNV-1a). Same string in -> same number out, always. */
function hash32(s) {
  let h = 0x811c9dc5;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return h >>> 0;
}

/** 0..TRACK_COLOR_COUNT-1, stable for a given guid (falsy guid always maps to 0). */
export function trackColorIndex(guid) {
  const s = String(guid || '');
  return s ? hash32(s) % TRACK_COLOR_COUNT : 0;
}

/** CSS class suffix for this guid's track colour, e.g. "track-3" (1-based, matches --track-1.. --track-8). */
export function trackColorClass(guid) {
  return `track-${trackColorIndex(guid) + 1}`;
}
