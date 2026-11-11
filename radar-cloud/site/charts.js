'use strict';
/**
 * charts.js - single responsibility: chart (zone map) math and lookup. A chart is a world rectangle {x0,x1,y0,y1}
 * plus, when known, an image from maps/index.json.
 *
 * Public API (Radar.charts):
 *  - worldToPct(chart, x, y, cal) -> [px, py] frame percent after per-chart calibration
 *  - buildView(snapMaps, zoneIdx) -> {orientation, zone, available[], noImage[]}  charts known from MAP_CHANGE joined
 * with the index
 *  - currentZone(snap, zoneIdx) -> {url,w,h,revealed}|null  image of the zone the game reports
 * No DOM.
 */
(function (g) {
  const dep = (name, file) =>
    typeof require !== 'undefined' && typeof module !== 'undefined' ? require(file) : g.Radar[name];
  const { normName } = dep('format', './format.js');
  const U = dep('config', './config.js').units;
  const IDENTITY_CAL = { dx: 0, dy: 0, sx: 1, sy: 1 };

  /** World -> frame percent (X counted from the left via Y, Y from the top via X), after per-chart calibration. */
  function worldToPct(chart, x, y, cal) {
    const c = cal || IDENTITY_CAL;
    const px = ((chart.y1 - y) / (chart.y1 - chart.y0)) * U.PCT_FULL,
      py = ((chart.x1 - x) / (chart.x1 - chart.x0)) * U.PCT_FULL;
    return [px * c.sx + c.dx, py * c.sy + c.dy];
  }

  const byNameThenId = (a, b) => {
    const la = a.name.toLowerCase(),
      lb = b.name.toLowerCase();
    return la !== lb ? (la < lb ? -1 : 1) : a.mapId - b.mapId;
  };

  /** Charts known from MAP_CHANGE joined with the maps index (zoneIdx: normalised name -> {file,w,h,revealed}). */
  function buildView(snapMaps, zoneIdx) {
    const available = [],
      noImage = [];
    for (const key of Object.keys(snapMaps || {})) {
      const info = snapMaps[key],
        mapId = Number(key);
      if (!isFinite(mapId) || !info.name || info.x0 === info.x1 || info.y0 === info.y1) continue;
      const z = zoneIdx[normName(info.name)];
      if (z) available.push({ mapId, name: info.name, url: 'maps/' + z.file, w: z.w, h: z.h, revealed: !!z.revealed });
      else noImage.push({ mapId, name: info.name });
    }
    available.sort(byNameThenId);
    noImage.sort(byNameThenId);
    return { orientation: 'std', zone: null, available, noImage };
  }

  /** Image of the zone the snapshot reports (chart name first, then zone name). */
  function currentZone(snap, zoneIdx) {
    const names = [snap.zone];
    const info = snap.maps && snap.maps[String(snap.mapId)];
    if (info && info.name) names.unshift(info.name);
    for (const n of names) {
      const z = n && zoneIdx[normName(n)];
      if (z) return { url: 'maps/' + z.file, w: z.w, h: z.h, revealed: !!z.revealed };
    }
    return null;
  }

  const api = { worldToPct, buildView, currentZone };
  (g.Radar || (g.Radar = {})).charts = api;
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
})(typeof self !== 'undefined' ? self : globalThis);
