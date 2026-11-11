"use strict";
// DOM-free cloud poll loop for GET /api/poll (scope v3): player < zone(+grp) < all. Timers, clock, random and visibility are injected.
(function (g) {
  const C = typeof require !== "undefined" && typeof module !== "undefined" ? require("./core.js") : g.RadarCore;
  const BASE_MS = 5000, SLOW_MS = 30000, UNCHANGED_MS = 120000, OVERLAP_MS = 2000, JITTER = 0.1;

  // Chooses what to poll from the UI state. Returns null when nothing visible needs data.
  // ui: {rs, track:{guid,fixes}|null, viewMapId:number|null, showBlue, showRed}
  function pollScope(ui) {
    if (ui.track) return { scope: "player", rs: ui.rs, guid: ui.track.guid, track: ui.track.fixes !== undefined ? 1 : 0 };
    if (ui.viewMapId !== null && ui.viewMapId !== undefined) {
      if (!ui.showBlue && !ui.showRed) return null;
      const grp = ui.showBlue && ui.showRed ? "any" : ui.showBlue ? "friendly" : "hostile";
      return { scope: "zone", rs: ui.rs, mapId: ui.viewMapId, grp };
    }
    return { scope: "all", rs: ui.rs };
  }
  const scopeKey = (s) => [s.rs, s.scope, s.guid || "", s.mapId || "", s.grp || "", s.track || ""].join("|");

  function normalizeRow(r) {
    const o = Object.assign({}, r);
    if (o.x !== undefined && o.lastX === undefined) { o.lastX = o.x; o.lastY = o.y; o.lastMap = o.mapId; o.lastZone = o.zone; }
    return o;
  }
  // Merges delta rows into a Map(guid -> row); newer lastSeen wins field-wise, unknown fields are kept.
  function mergeRows(map, rows) {
    let n = 0;
    for (const r0 of rows || []) {
      if (!r0 || !r0.guid) continue;
      const r = normalizeRow(r0), old = map.get(r.guid);
      if (old && old.lastSeen > r.lastSeen) continue;
      map.set(r.guid, old ? Object.assign({}, old, r) : r);
      n++;
    }
    return n;
  }
  // Merges track fixes (sorted by t, deduped on t/x/y).
  function mergeFixes(have, add) {
    if (!add || !add.length) return have;
    const seen = new Set(have.map((f) => f.t + "|" + f.x + "|" + f.y));
    const out = have.slice();
    for (const f of add) { const k = f.t + "|" + f.x + "|" + f.y; if (!seen.has(k)) { seen.add(k); out.push(f); } }
    out.sort((a, b) => a.t - b.t);
    return out;
  }

  class Poller {
    // deps: {api(path), getScope() -> scope|null, onResponse(resp, scope, initial), onError(err), setTimeout, clearTimeout, now(), random(), hidden(), getTrackTail() -> tsince}
    constructor(d) {
      this.d = d; this.timer = null; this.inflight = false; this.running = false; this.paused = false;
      this.key = null; this.lastNow = 0; this.v = ""; this.interval = BASE_MS; this.lastChange = d.now(); this.errors = 0; this.again = false; this.requests = [];
    }
    start() { if (this.running) return; this.running = true; this.lastChange = this.d.now(); this.schedule(0); }
    stop() { this.running = false; this.clear(); }
    clear() { if (this.timer !== null) { this.d.clearTimeout(this.timer); this.timer = null; } }
    jitter(ms) { return Math.max(0, Math.round(ms * (1 + (this.d.random() * 2 - 1) * JITTER))); }
    schedule(ms) {
      this.clear();
      if (!this.running) return;
      if (this.d.hidden()) { this.paused = true; return; }
      this.paused = false;
      this.timer = this.d.setTimeout(() => { this.timer = null; this.tick(); }, ms === 0 ? 0 : this.jitter(ms));
    }
    // User input / scope change / visibility: back to the fast cadence. immediate=true polls right away.
    poke(immediate) {
      if (!this.running) return;
      this.interval = BASE_MS; this.lastChange = this.d.now();
      if (this.inflight) { this.again = true; return; }
      if (immediate || this.paused || this.timer === null) this.schedule(immediate ? 0 : BASE_MS); else this.schedule(BASE_MS);
    }
    onVisibility() { if (this.d.hidden()) { this.clear(); this.paused = true; } else if (this.running) { this.poke(true); } }
    reset() { this.key = null; this.lastNow = 0; this.v = ""; }
    buildPath(sc) {
      const key = scopeKey(sc);
      if (key !== this.key) { this.key = key; this.lastNow = 0; this.v = ""; }
      const q = { rs: sc.rs, scope: sc.scope, guid: sc.guid, mapId: sc.mapId, grp: sc.grp, since: this.lastNow ? Math.max(0, this.lastNow - OVERLAP_MS) : 0, v: this.v };
      if (sc.scope === "player") { q.track = sc.track ? 1 : 0; if (sc.track && this.d.getTrackTail) q.tsince = this.d.getTrackTail() || 0; }
      return "/api/poll?" + C.qs(Object.assign({}, q, { since: q.since, track: q.track, tsince: q.tsince }));
    }
    async tick() {
      if (!this.running || this.inflight) return;
      if (this.d.hidden()) { this.paused = true; return; }
      const sc = this.d.getScope();
      if (!sc) { this.schedule(this.interval); return; }
      this.inflight = true;
      let delay;
      try {
        const path = this.buildPath(sc);
        this.requests.push(path);
        const initial = this.lastNow === 0;
        const r = await this.d.api(path);
        this.errors = 0;
        if (r && r.now) this.lastNow = r.now;
        if (r && r.v !== undefined) this.v = String(r.v);
        if (r && r.changed) { this.interval = BASE_MS; this.lastChange = this.d.now(); this.d.onResponse(r, sc, initial); }
        else if (this.d.now() - this.lastChange >= UNCHANGED_MS) this.interval = SLOW_MS;
        delay = this.interval;
      } catch (e) {
        this.errors++;
        delay = Math.min(SLOW_MS, BASE_MS * Math.pow(2, this.errors));
        if (this.d.onError) this.d.onError(e);
      }
      this.inflight = false;
      if (this.again) { this.again = false; this.interval = BASE_MS; delay = BASE_MS; }
      this.schedule(delay);
    }
  }

  const api = { BASE_MS, SLOW_MS, UNCHANGED_MS, OVERLAP_MS, pollScope, scopeKey, normalizeRow, mergeRows, mergeFixes, Poller };
  g.RadarPoller = api;
  if (typeof module !== "undefined" && module.exports) module.exports = api;
})(typeof self !== "undefined" ? self : globalThis);
