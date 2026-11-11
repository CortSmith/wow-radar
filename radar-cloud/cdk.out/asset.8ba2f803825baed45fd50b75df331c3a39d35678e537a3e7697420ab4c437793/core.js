"use strict";
// DOM-free logic shared by app.js and the node tests: auth, api client, ledger/batch upload, render helpers.
(function (g) {
  const RULESET_IDS = ["normal", "pvp", "rp", "rppvp"];
  const MAX_PLAYERS_PER_REQ = 500, MAX_FIXES_PER_REQ = 40000;

  // ---------- small helpers ----------
  function fmtAge(sec) {
    sec = Math.max(0, Math.round(sec));
    if (sec < 60) return sec + "s";
    const m = Math.floor(sec / 60);
    if (m < 60) return m + "m";
    const h = Math.floor(m / 60);
    return h < 48 ? h + "h" + (m % 60) + "m" : Math.floor(h / 24) + "d";
  }
  const esc = (s) => String(s).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
  const normName = (s) => String(s).toLowerCase().replace(/[^a-z0-9]/g, "");
  const num = (v) => (v ? (Math.round(v * 10) / 10).toString() : "-");
  const fmtPct = (p) => p[0].toFixed(1) + ", " + p[1].toFixed(1);
  const mb = (n) => (n / 1048576).toFixed(1);

  // world -> frame percent (X counted from the left via Y, Y from the top via X), after per-map calibration
  function worldToPct(m, x, y, cal) {
    const c = cal || { dx: 0, dy: 0, sx: 1, sy: 1 };
    const px = ((m.y1 - y) / (m.y1 - m.y0)) * 100, py = ((m.x1 - x) / (m.x1 - m.x0)) * 100;
    return [px * c.sx + c.dx, py * c.sy + c.dy];
  }

  // Maps known from MAP_CHANGE joined with the maps index (zoneIdx: normalised name -> {file,w,h,revealed}).
  function buildView(snapMaps, zoneIdx) {
    const available = [], noImage = [];
    for (const k of Object.keys(snapMaps || {})) {
      const mi = snapMaps[k], id = Number(k);
      if (!isFinite(id) || !mi.name || mi.x0 === mi.x1 || mi.y0 === mi.y1) continue;
      const z = zoneIdx[normName(mi.name)];
      if (z) available.push({ mapId: id, name: mi.name, url: "maps/" + z.file, w: z.w, h: z.h, revealed: !!z.revealed });
      else noImage.push({ mapId: id, name: mi.name });
    }
    const cmp = (a, b) => { const la = a.name.toLowerCase(), lb = b.name.toLowerCase(); return la !== lb ? (la < lb ? -1 : 1) : a.mapId - b.mapId; };
    available.sort(cmp); noImage.sort(cmp);
    const view = { orientation: "std", zone: null, available, noImage };
    return view;
  }
  function currentZone(snap, zoneIdx) {
    const names = [snap.zone];
    const mi = snap.maps && snap.maps[String(snap.mapId)];
    if (mi && mi.name) names.unshift(mi.name);
    for (const n of names) {
      const z = n && zoneIdx[normName(n)];
      if (z) return { url: "maps/" + z.file, w: z.w, h: z.h, revealed: !!z.revealed };
    }
    return null;
  }

  // Snapshot-shaped state built from cloud history rows (non-Chromium fallback / no live source).
  const CLOUD_STALE_MS = 24 * 3600 * 1000;
  function cloudSnapshot(rows, maps, now) {
    let newest = 0;
    for (const p of rows) if (p.lastSeen > newest) newest = p.lastSeen;
    const snapMaps = {};
    for (const m of maps || []) snapMaps[String(m.id)] = { name: m.name, x0: m.x0, x1: m.x1, y0: m.y0, y1: m.y1 };
    const counts = {};
    const pips = [];
    for (const r of rows) {
      const has = r.lastX !== undefined && r.lastX !== null && r.lastMap;
      const stale = r.lastSeen < newest - CLOUD_STALE_MS;
      if (has) counts[r.lastMap] = (counts[r.lastMap] || 0) + 1;
      pips.push({ guid: r.guid, name: r.name, raw: r.name, kind: "player", reaction: r.reaction || "unknown", state: stale ? "stale" : "active", me: false,
        hp: 0, maxHp: r.maxHp || 0, hasHp: false, level: r.level || 0, x: has ? r.lastX : 0, y: has ? r.lastY : 0, mapId: has ? r.lastMap : 0,
        posKind: has ? "exact" : "none", weight: 1, lastEvent: r.lastSeen, ageSec: Math.max(0, (now - r.lastSeen) / 1000), events: r.events || 0,
        damageTotal: r.dmgTotal || 0, damageHits: r.dmgHits || 0, source: "cloud", est: null, zone: r.lastZone || "" });
    }
    let best = 0, bn = -1;
    for (const id of Object.keys(counts)) { const n = counts[id] + (snapMaps[id] ? 1e6 : 0); if (n > bn) { bn = n; best = +id; } }
    return { now, zone: "", mapId: best, me: null, pips, maps: snapMaps, advanced: null, realmSuffix: "", sources: ["cloud"], cloud: true,
      stats: { events: 0, units: pips.length, sources: 0 }, staleSec: CLOUD_STALE_MS / 1000, expireSec: 0 };
  }

  const bountyValue = (kb, assists) => kb * 10 + assists * 0; // killing blows x10, assists worth 0
  // Bounty leaderboard from /api/bounty entries: ranked by score, then assists, then name. Only scoring players.
  function rankBounty(entries) {
    const rows = (entries || []).filter((e) => e.score > 0).slice();
    rows.sort((a, b) => b.score - a.score || (b.assists || 0) - (a.assists || 0) || String(a.name).localeCompare(String(b.name)));
    rows.forEach((r, i) => { r.rank = i + 1; });
    const by = {};
    for (const r of rows) by[r.guid] = r;
    return { rows, by };
  }

  // History table row cells (original columns): Name, Avg dmg/hit, Avg dmg/event, Avg HP, Max HP, Samples, Last seen, Zone.
  function historyRow(p) {
    const hits = p.dmgHits || 0, ev = p.dmgEvents || p.srcEvents || 0, hs = p.hpSamples || 0;
    return {
      name: p.name,
      avgDmgHit: hits ? (p.dmgTotal || 0) / hits : 0,
      avgDmgEvent: ev ? (p.dmgTotal || 0) / ev : 0,
      avgHp: hs ? (p.hpSum || 0) / hs : 0,
      maxHp: p.maxHp || 0, samples: p.events || 0, lastSeen: p.lastSeen, zone: p.lastZone || p.zone || "",
    };
  }

  // Per-map track summary like Go TrackResult: {total, maps:[{mapId,zone,count}]}.
  function trackSummary(fixes) {
    const idx = new Map();
    for (const f of fixes) {
      let m = idx.get(f.mapId);
      if (!m) { m = { mapId: f.mapId, zone: "", count: 0 }; idx.set(f.mapId, m); }
      m.count++;
      if (f.zone) m.zone = f.zone;
    }
    return { total: fixes.length, maps: Array.from(idx.values()).sort((a, b) => a.mapId - b.mapId) };
  }

  // ---------- auth + api client ----------
  async function authenticate(fetchFn, o) {
    let r;
    try {
      r = await fetchFn(o.signup ? "/api/signup" : "/api/login", {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ username: o.username.trim(), password: o.password, apiKey: o.apiKey.trim() }),
      });
    } catch (e) { throw new Error("Could not reach the server."); }
    let d;
    try { d = await r.json(); } catch (e) { d = { error: "Server error (" + r.status + ")" }; }
    if (!d.token) throw new Error(d.error || "Login failed");
    return { token: d.token, username: d.username || o.username.trim() };
  }
  // Returns api(path, opt) -> parsed JSON; 401 calls onUnauthorized and throws.
  function makeApi(fetchFn, getToken, onUnauthorized) {
    return async function api(path, opt) {
      opt = Object.assign({}, opt || {});
      opt.headers = Object.assign({ Authorization: "Bearer " + getToken() }, opt.headers || {});
      const r = await fetchFn(path, opt);
      if (r.status === 401) { if (onUnauthorized) onUnauthorized(); throw new Error("Session expired, please log in again."); }
      const d = await r.json().catch(() => ({}));
      if (!r.ok) throw new Error(d.error || "Server error (" + r.status + ")");
      return d;
    };
  }
  const qs = (o) => Object.keys(o).filter((k) => o[k] !== undefined && o[k] !== null && o[k] !== "").map((k) => encodeURIComponent(k) + "=" + encodeURIComponent(o[k])).join("&");

  // ---------- ledger + batch upload ----------
  async function fingerprint(bytes, name, sha256) {
    const enc = new TextEncoder().encode("|" + name);
    const all = new Uint8Array(bytes.length + enc.length);
    all.set(bytes, 0); all.set(enc, bytes.length);
    return (await sha256(all)).slice(0, 32);
  }
  // Watermarks (lastTs) of every rule-set for one file fingerprint.
  async function loadLedger(api, fp) {
    const wm = {};
    await Promise.all(RULESET_IDS.map(async (rs) => {
      const d = await api("/api/ledger?" + qs({ rs, fp }));
      wm[rs] = Number(d && d.lastTs) || 0;
    }));
    return wm;
  }
  // Splits players into requests of <= maxPlayers players and <= maxFixes fixes (a single player is never split).
  function chunkPlayers(players, maxPlayers, maxFixes) {
    maxPlayers = maxPlayers || MAX_PLAYERS_PER_REQ; maxFixes = maxFixes || MAX_FIXES_PER_REQ;
    const out = [];
    let cur = [], fx = 0;
    for (const p of players) {
      const n = p.fixes ? p.fixes.length : 0;
      if (cur.length && (cur.length >= maxPlayers || fx + n > maxFixes)) { out.push(cur); cur = []; fx = 0; }
      cur.push(p); fx += n;
    }
    if (cur.length) out.push(cur);
    return out;
  }
  function newBatchId(fp, rs) { return fp.slice(0, 8) + "-" + rs + "-" + Date.now().toString(36) + Math.random().toString(36).slice(2, 8); }
  // Uploads one flushed batch {rs, from, to, players, maps}. Resumable: on failure the batch keeps its batchId and the index of
  // the next unsent request, so calling uploadBatch again retries with the SAME batchId (the server skips players it already has).
  async function uploadBatch(api, fp, batch, onProgress) {
    if (!batch.batchId) batch.batchId = newBatchId(fp, batch.rs);
    if (!batch.chunks) batch.chunks = chunkPlayers(batch.players);
    batch.next = batch.next || 0; batch.received = batch.received || 0; batch.changed = batch.changed || 0;
    while (batch.next < batch.chunks.length) {
      const i = batch.next, last = i === batch.chunks.length - 1;
      const d = await api("/api/upload", {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ rs: batch.rs, fp, from: batch.from, to: batch.to, batchId: batch.batchId, final: last, players: batch.chunks[i], maps: i === 0 ? batch.maps : [] }),
      });
      batch.received += d.received || 0; batch.changed += d.changed || 0;
      batch.next++;
      if (onProgress) onProgress(batch.next, batch.chunks.length, batch);
    }
    return batch;
  }
  // Outbox: batches waiting for upload. Keeps failed batches for the next attempt, drops finished ones.
  class Outbox {
    constructor() { this.items = []; }
    add(fp, batches) { for (const b of batches) this.items.push({ fp, batch: b }); }
    get length() { return this.items.length; }
    async drain(api, onProgress) {
      let received = 0, changed = 0;
      while (this.items.length) {
        const it = this.items[0];
        await uploadBatch(api, it.fp, it.batch, onProgress); // throws -> item stays at the head for the retry
        received += it.batch.received; changed += it.batch.changed;
        this.items.shift();
      }
      return { received, changed };
    }
  }

  // ---------- settings (persisted in /api/settings; calibration also in localStorage as a fallback) ----------
  const SETTINGS_MAX = 7500;
  function compactSettings(s) {
    const out = Object.assign({}, s, { cal: {} });
    const keys = Object.keys(s.cal || {});
    for (const k of keys) {
      const c = s.cal[k];
      if (!c || (c.dx === 0 && c.dy === 0 && c.sx === 1 && c.sy === 1)) continue;
      out.cal[k] = { dx: +c.dx.toFixed(2), dy: +c.dy.toFixed(2), sx: +c.sx.toFixed(4), sy: +c.sy.toFixed(4) };
    }
    let ks = Object.keys(out.cal);
    while (JSON.stringify(out).length > SETTINGS_MAX && ks.length) { delete out.cal[ks.shift()]; }
    return out;
  }

  const api = { RULESET_IDS, MAX_PLAYERS_PER_REQ, MAX_FIXES_PER_REQ, fmtAge, esc, normName, num, fmtPct, mb, worldToPct, buildView, currentZone, cloudSnapshot, rankBounty, bountyValue,
    historyRow, trackSummary, authenticate, makeApi, qs, fingerprint, loadLedger, chunkPlayers, uploadBatch, Outbox, compactSettings };
  g.RadarCore = api;
  if (typeof module !== "undefined" && module.exports) module.exports = api;
})(typeof self !== "undefined" ? self : globalThis);
