'use strict';
/**
 * contacts.js - single responsibility: shape stored contact rows (GET /api/players, /api/bounty) into what the UI
 * shows.
 *
 * Public API (Radar.contacts):
 *  - cloudSnapshot(rows, maps, now) -> Snapshot   cloud rows as a snapshot-shaped state (no live source)
 *  - rankBounty(entries) -> {rows, by}            leaderboard ranked by score, then assists, then name
 *  - bountyValue(kb, assists) -> number
 *  - historyRow(contact) -> HistoryRow            cells of the history table
 *  - trackSummary(fixes) -> {total, maps[]}       per-chart fix counts of a trail (like Go TrackResult)
 * Wire field names (pips, lastSeen, mapId, ...) are frozen by the backend and the Go snapshot shape. No DOM.
 */
(function (g) {
  const dep = (name, file) =>
    typeof require !== 'undefined' && typeof module !== 'undefined' ? require(file) : g.Radar[name];
  const CFG = dep('config', './config.js');

  const hasFix = r => r.lastX !== undefined && r.lastX !== null && !!r.lastMap;

  /**
   * Stored row -> Blip in the Go snapshot shape.
   * @param {number} r row or response
   * @param {Object} newestSeen the newest seen
   * @param {number} now current time in unix ms
   * @returns {*} the computed value
   */
  function toBlip(r, newestSeen, now) {
    const located = hasFix(r);
    const isStale = r.lastSeen < newestSeen - CFG.cloud.STALE_MS;
    return {
      guid: r.guid,
      name: r.name,
      raw: r.name,
      kind: 'player',
      reaction: r.reaction || 'unknown',
      state: isStale ? 'stale' : 'active',
      me: false,
      hp: 0,
      maxHp: r.maxHp || 0,
      hasHp: false,
      level: r.level || 0,
      x: located ? r.lastX : 0,
      y: located ? r.lastY : 0,
      mapId: located ? r.lastMap : 0,
      posKind: located ? 'exact' : 'none',
      weight: 1,
      lastEvent: r.lastSeen,
      ageSec: Math.max(0, (now - r.lastSeen) / CFG.units.MS_PER_SEC),
      events: r.events || 0,
      damageTotal: r.dmgTotal || 0,
      damageHits: r.dmgHits || 0,
      source: 'cloud',
      est: null,
      zone: r.lastZone || '',
    };
  }

  /**
   * The chart holding most contacts; a chart with known bounds beats one without (the 1e6 bonus).
   * @param {Array} rows rows
   * @param {Object} snapMaps the snap maps
   * @returns {*} the computed value
   */
  function busiestChart(rows, snapMaps) {
    const counts = {};
    for (const r of rows) if (hasFix(r)) counts[r.lastMap] = (counts[r.lastMap] || 0) + 1;
    let best = 0,
      bestScore = -1;
    for (const id of Object.keys(counts)) {
      const score = counts[id] + (snapMaps[id] ? 1e6 : 0);
      if (score > bestScore) {
        bestScore = score;
        best = +id;
      }
    }
    return best;
  }

  /** Snapshot-shaped state built from cloud history rows (non-Chromium fallback / no live source). */
  function cloudSnapshot(rows, maps, now) {
    let newestSeen = 0;
    for (const p of rows) if (p.lastSeen > newestSeen) newestSeen = p.lastSeen;
    const snapMaps = {};
    for (const m of maps || []) snapMaps[String(m.id)] = { name: m.name, x0: m.x0, x1: m.x1, y0: m.y0, y1: m.y1 };
    const pips = rows.map(r => toBlip(r, newestSeen, now));
    return {
      now,
      zone: '',
      mapId: busiestChart(rows, snapMaps),
      me: null,
      pips,
      maps: snapMaps,
      advanced: null,
      realmSuffix: '',
      sources: ['cloud'],
      cloud: true,
      stats: { events: 0, units: pips.length, sources: 0 },
      staleSec: CFG.cloud.STALE_MS / CFG.units.MS_PER_SEC,
      expireSec: 0,
    };
  }

  const bountyValue = (kb, assists) => kb * CFG.cloud.KILLING_BLOW_POINTS + assists * CFG.cloud.ASSIST_POINTS;

  /** Bounty leaderboard from /api/bounty entries. Only scoring contacts are ranked. */
  function rankBounty(entries) {
    const rows = (entries || []).filter(e => e.score > 0).slice();
    rows.sort(
      (a, b) =>
        b.score - a.score || (b.assists || 0) - (a.assists || 0) || String(a.name).localeCompare(String(b.name)),
    );
    rows.forEach((r, i) => {
      r.rank = i + 1;
    });
    const by = {};
    for (const r of rows) by[r.guid] = r;
    return { rows, by };
  }

  /** History table cells: Name, Avg dmg/hit, Avg dmg/event, Avg HP, Max HP, Samples, Last seen, Zone. */
  function historyRow(p) {
    const hits = p.dmgHits || 0,
      ev = p.dmgEvents || p.srcEvents || 0,
      hs = p.hpSamples || 0;
    return {
      name: p.name,
      avgDmgHit: hits ? (p.dmgTotal || 0) / hits : 0,
      avgDmgEvent: ev ? (p.dmgTotal || 0) / ev : 0,
      avgHp: hs ? (p.hpSum || 0) / hs : 0,
      maxHp: p.maxHp || 0,
      samples: p.events || 0,
      lastSeen: p.lastSeen,
      zone: p.lastZone || p.zone || '',
    };
  }

  /** Per-chart trail summary: {total, maps:[{mapId,zone,count}]}. */
  function trackSummary(fixes) {
    const byMapId = new Map();
    for (const f of fixes) {
      let m = byMapId.get(f.mapId);
      if (!m) {
        m = { mapId: f.mapId, zone: '', count: 0 };
        byMapId.set(f.mapId, m);
      }
      m.count++;
      if (f.zone) m.zone = f.zone;
    }
    return { total: fixes.length, maps: Array.from(byMapId.values()).sort((a, b) => a.mapId - b.mapId) };
  }

  const api = { cloudSnapshot, rankBounty, bountyValue, historyRow, trackSummary };
  (g.Radar || (g.Radar = {})).contacts = api;
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
})(typeof self !== 'undefined' ? self : globalThis);
