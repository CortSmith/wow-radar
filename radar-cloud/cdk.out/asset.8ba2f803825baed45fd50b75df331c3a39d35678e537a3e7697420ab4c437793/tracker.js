"use strict";
// Port of go-radar/internal/{tracker,history,bounty,clock}. Loaded by worker.js (importScripts) and by node (require).
//  - Tracker: live pips (stale 5m / expire 15m, approx fixes, estimate, me) -> snapshot shaped like the Go /api/state.
//  - Engine: feeds log lines to the tracker + a delta accumulator (history aggregates, tracks, bounty) per rule-set.
(function (g) {
  const P = typeof require !== "undefined" && typeof module !== "undefined" ? require("./parser.js") : g.RadarParser;

  const APPROX_WEIGHT = 0.3, APPROX_WINDOW = 30000, FIX_RING = 60, ESTIMATE_N = 15, ESTIMATE_GAP = 30000;
  const round1 = (v) => Math.round(v * 10) / 10;

  // ---------------- Tracker ----------------
  function estimate(fixes, now, n, gap) {
    if (!fixes.length) return null;
    const mp = fixes[fixes.length - 1].mapId;
    const win = [];
    for (let i = fixes.length - 1; i >= 0; i--) {
      const f = fixes[i];
      if (f.mapId !== mp || (win.length > 0 && win[win.length - 1].t - f.t > gap)) break;
      win.push(f);
      if (win.length >= n) break;
    }
    const pts = [];
    let decay = 1, exact = 0;
    for (const f of win) {
      let w = APPROX_WEIGHT;
      if (f.kind === "exact") { w = 1; exact++; }
      pts.push({ x: f.x, y: f.y, w: w * decay });
      decay *= 0.9;
    }
    if (exact >= 2) {
      const ex = win.filter((f) => f.kind === "exact");
      const a = ex[0], b = ex[Math.min(4, ex.length - 1)];
      const dt = (a.t - b.t) / 1000;
      if (dt >= 1.5) {
        let vx = (a.x - b.x) / dt, vy = (a.y - b.y) / dt, sp = Math.hypot(vx, vy);
        const maxSpeed = 30;
        if (sp > maxSpeed) { vx = vx * maxSpeed / sp; vy = vy * maxSpeed / sp; sp = maxSpeed; }
        if (sp >= 0.5) {
          let tp = (now - a.t) / 1000;
          if (tp < 0) tp = 0;
          if (tp > 10) tp = 10;
          pts.push({ x: a.x + vx * tp, y: a.y + vy * tp, w: 1.5 });
        }
      }
    }
    let ws = 0, mx = 0, my = 0;
    for (const p of pts) { ws += p.w; mx += p.x * p.w; my += p.y * p.w; }
    mx /= ws; my /= ws;
    let v = 0;
    for (const p of pts) v += p.w * ((p.x - mx) * (p.x - mx) + (p.y - my) * (p.y - my));
    let spread = Math.sqrt(v / ws / 2);
    if (spread < 8) spread = 8;
    return { x: round1(mx), y: round1(my), spread: round1(spread), mapId: mp, n: win.length, exact };
  }

  class Tracker {
    // cfg: {trackNPCs, disableApprox, realmSuffix, staleMs, expireMs, approxMaxAgeMs, history(sample)}; clock: () => ms
    constructor(cfg, clock) {
      cfg = cfg || {};
      this.cfg = Object.assign({ trackNPCs: false, disableApprox: false, realmSuffix: "", staleMs: 300000, expireMs: 900000, approxMaxAgeMs: 120000, history: null }, cfg);
      this.clock = clock || Date.now;
      this.units = new Map(); this.srcs = new Map(); this.primary = ""; this.namer = new P.Namer(this.cfg.realmSuffix);
      this.events = 0; this.n = 0;
    }
    source(id) {
      let s = this.srcs.get(id);
      if (!s) { s = { id, meGuid: "", meFix: null, zone: "", mapId: 0, maps: new Map(), advanced: null, events: 0 }; this.srcs.set(id, s); }
      return s;
    }
    tracked(guid) {
      switch (P.kindOf(guid)) {
        case P.KIND_PLAYER: return true;
        case P.KIND_NPC: case P.KIND_PET: return this.cfg.trackNPCs;
      }
      return false;
    }
    apply(id, ev) {
      const s = this.source(id);
      this.primary = id; this.events++; s.events++;
      switch (ev.type) {
        case "COMBAT_LOG_VERSION": s.advanced = ev.advanced; return;
        case "ZONE_CHANGE": if (ev.zone) s.zone = ev.zone; return;
        case "MAP_CHANGE":
          if (ev.map) { s.maps.set(ev.map.id, ev.map); s.mapId = ev.map.id; if (!s.zone) s.zone = ev.map.name; }
          return;
      }
      const parts = [];
      const add = (p) => {
        if (!p.guid || !this.tracked(p.guid)) return;
        for (const q of parts) if (q.guid === p.guid) return;
        parts.push(p);
      };
      if (ev.src) add({ guid: ev.src.guid, name: ev.src.name, flags: ev.src.flags, hasFlags: true });
      if (ev.dst) add({ guid: ev.dst.guid, name: ev.dst.name, flags: ev.dst.flags, hasFlags: true });
      if (ev.info) add({ guid: ev.info.guid, name: "", flags: 0, hasFlags: false });
      for (const p of parts) this.touch(s, p, ev.t);

      let infoPos = "";
      if (ev.info && this.tracked(ev.info.guid)) {
        const u = this.units.get(ev.info.guid);
        if (ev.info.hasHp) { u.hp = ev.info.hp; u.maxHp = ev.info.maxHp; u.hasHp = true; }
        if (ev.info.level > 0) u.level = ev.info.level;
        if (ev.info.hasPos) {
          const f = { t: ev.t, x: ev.info.x, y: ev.info.y, kind: "exact", mapId: ev.info.mapId };
          this.setExact(u, f);
          infoPos = u.guid;
          if (u.guid === s.meGuid) { s.meFix = f; s.mapId = f.mapId; }
        }
      }
      const approxFix = new Map();
      if (!this.cfg.disableApprox && s.meFix && Math.abs(ev.t - s.meFix.t) < this.cfg.approxMaxAgeMs) {
        for (const p of parts) {
          const u = this.units.get(p.guid);
          if (u.kind !== P.KIND_PLAYER || p.guid === s.meGuid || p.guid === infoPos) continue;
          const f = { t: ev.t, x: s.meFix.x, y: s.meFix.y, kind: "approx", mapId: s.meFix.mapId };
          u.approx = f; this.pushFix(u, f); approxFix.set(p.guid, f);
        }
      }
      if (ev.isDamage && ev.hasAmount && ev.src && this.tracked(ev.src.guid)) {
        const u = this.units.get(ev.src.guid);
        u.dmgTotal += ev.amount; u.dmgHits++;
      }
      if (this.cfg.history) {
        for (const p of parts) {
          const u = this.units.get(p.guid);
          const sm = { guid: p.guid, name: u.raw || p.name, display: "", reaction: "", level: u.level, t: ev.t, asSource: !!ev.src && p.guid === ev.src.guid, zone: s.zone,
            hasHp: false, hp: 0, maxHp: 0, hasDamage: false, damage: 0, hasPos: false, approx: false, x: 0, y: 0, mapId: 0 };
          if (sm.name) sm.display = this.namer.display(sm.name);
          if (u.hasFlags) sm.reaction = P.reactionOf(u.flags);
          if (ev.info && ev.info.guid === p.guid) {
            sm.hasHp = ev.info.hasHp; sm.hp = ev.info.hp; sm.maxHp = ev.info.maxHp;
            if (ev.info.hasPos) { sm.hasPos = true; sm.x = ev.info.x; sm.y = ev.info.y; sm.mapId = ev.info.mapId; }
          }
          const af = approxFix.get(p.guid);
          if (af && !sm.hasPos) { sm.hasPos = true; sm.approx = true; sm.x = af.x; sm.y = af.y; sm.mapId = af.mapId; }
          if (ev.isDamage && ev.hasAmount && ev.src && p.guid === ev.src.guid) { sm.hasDamage = true; sm.damage = ev.amount; }
          this.cfg.history(sm);
        }
      }
    }
    touch(s, p, ts) {
      let u = this.units.get(p.guid);
      if (!u) {
        u = { guid: p.guid, raw: "", flags: 0, hasFlags: false, kind: P.kindOf(p.guid), first: ts, last: ts, events: 0, hp: 0, maxHp: 0, hasHp: false, level: 0,
          exact: null, approx: null, fixes: [], dmgTotal: 0, dmgHits: 0, source: s.id };
        this.units.set(p.guid, u);
      }
      if (p.name) { u.raw = p.name; if (u.kind === P.KIND_PLAYER) this.namer.observe(p.name); }
      if (p.hasFlags) {
        u.flags = p.flags; u.hasFlags = true;
        if (u.kind === P.KIND_PLAYER && P.isMine(p.flags)) s.meGuid = p.guid;
      }
      if (ts > u.last) u.last = ts;
      u.source = s.id; u.events++;
      return u;
    }
    setExact(u, f) { u.exact = f; this.pushFix(u, f); }
    pushFix(u, f) {
      const n = u.fixes.length;
      if (n > 0) {
        const l = u.fixes[n - 1];
        if (l.kind === f.kind && l.x === f.x && l.y === f.y && f.t - l.t < 500) return;
      }
      u.fixes.push(f);
      if (u.fixes.length > FIX_RING) u.fixes.splice(0, u.fixes.length - FIX_RING);
    }
    meSet() {
      const m = new Set();
      for (const s of this.srcs.values()) if (s.meGuid) m.add(s.meGuid);
      return m;
    }
    prune(now) {
      const me = this.meSet();
      for (const [g, u] of this.units) if (!me.has(g) && now - u.last > this.cfg.expireMs) this.units.delete(g);
    }
    primaryMeRaw() {
      const s = this.srcs.get(this.primary);
      const u = s && s.meGuid ? this.units.get(s.meGuid) : null;
      return u ? u.raw : "";
    }
    pip(u, now, isMe) {
      const age = now - u.last;
      const p = { guid: u.guid, raw: u.raw, name: this.namer.display(u.raw) || u.guid, kind: u.kind === P.KIND_PLAYER ? "player" : u.kind === P.KIND_PET ? "pet" : "npc",
        reaction: u.hasFlags ? P.reactionOf(u.flags) : "unknown", state: age > this.cfg.staleMs ? "stale" : "active", me: isMe,
        hp: u.hp, maxHp: u.maxHp, hasHp: u.hasHp, level: u.level, x: 0, y: 0, mapId: 0, posKind: "none", weight: 0,
        lastEvent: u.last, ageSec: age / 1000, events: u.events, damageTotal: u.dmgTotal, damageHits: u.dmgHits, source: u.source, est: null };
      if (u.exact && (!u.approx || u.approx.t - u.exact.t <= APPROX_WINDOW)) { p.x = u.exact.x; p.y = u.exact.y; p.mapId = u.exact.mapId; p.posKind = "exact"; p.weight = 1; }
      else if (u.approx) { p.x = u.approx.x; p.y = u.approx.y; p.mapId = u.approx.mapId; p.posKind = "approx"; p.weight = APPROX_WEIGHT; }
      p.est = estimate(u.fixes, now, ESTIMATE_N, ESTIMATE_GAP);
      return p;
    }
    snapshot() {
      const now = this.clock();
      this.prune(now);
      const me = this.meSet();
      const snap = { now, zone: "", mapId: 0, me: null, pips: [], maps: {}, advanced: null, realmSuffix: this.namer.suffix, sources: [],
        stats: { events: this.events, units: this.units.size, sources: this.srcs.size }, staleSec: this.cfg.staleMs / 1000, expireSec: this.cfg.expireMs / 1000 };
      for (const [id, s] of this.srcs) {
        snap.sources.push(id);
        for (const [mid, m] of s.maps) snap.maps[String(mid)] = { name: m.name, x0: m.x0, x1: m.x1, y0: m.y0, y1: m.y1 };
      }
      snap.sources.sort();
      const units = Array.from(this.units.values());
      units.sort((a, b) => (a.last !== b.last ? b.last - a.last : a.guid < b.guid ? -1 : 1));
      for (const u of units) snap.pips.push(this.pip(u, now, me.has(u.guid)));
      const ps = this.srcs.get(this.primary);
      if (ps) {
        if (ps.zone) snap.zone = ps.zone;
        if (ps.mapId) snap.mapId = ps.mapId;
        snap.advanced = ps.advanced;
        const u = this.units.get(ps.meGuid);
        if (u) snap.me = this.pip(u, now, true);
      }
      return snap;
    }
  }

  // ---------------- Bounty analyzer (port of bounty/analyzer.go) ----------------
  const ASSIST_WINDOW = 15000, KILLING_BLOW_WINDOW = 5000, DUPLICATE_DEATH_WINDOW = 3000;
  const MAX_HITS = 64, MAX_VICTIMS = 256, MAX_KNOWN = 4096, MAX_OWNERS = 4096, MAX_LAST = 512;
  const isFriendlyPlayer = (u) => !!u && P.kindOf(u.guid) === P.KIND_PLAYER && P.reactionOf(u.flags) === "friendly";

  class Analyzer {
    constructor(onDeath) { this.onDeath = onDeath; this.namer = new P.Namer(""); this.file = null; this.reset(); }
    reset() { this.wins = new Map(); this.known = new Map(); this.owners = new Map(); this.last = new Map(); this.zone = ""; this.maps = new Map(); }
    setFile(name) { if (name === this.file) return; this.file = name; this.reset(); }
    apply(ev) {
      switch (ev.type) {
        case "ZONE_CHANGE": if (ev.zone) this.zone = ev.zone; return;
        case "MAP_CHANGE": if (ev.map) { if (this.maps.size > 512) this.maps = new Map(); this.maps.set(ev.map.id, ev.map.name); } return;
        case "COMBAT_LOG_VERSION": return;
      }
      if (!ev.src) return;
      this.learn(ev);
      if (ev.type === "UNIT_DIED") {
        if (ev.unconscious) return;
        this.death(ev, null);
      } else if (ev.type === "PARTY_KILL" || ev.type === "SPELL_INSTAKILL") this.death(ev, ev.src);
      else if (ev.isDamage) this.hit(ev);
    }
    learn(ev) {
      for (const u of [ev.src, ev.dst]) {
        if (P.kindOf(u.guid) !== P.KIND_PLAYER) continue;
        if (u.name) this.namer.observe(u.name);
        const r = P.reactionOf(u.flags);
        if (r === "unknown" && u.flags === 0) continue;
        if (this.known.size >= MAX_KNOWN) { let i = 0; for (const k of this.known.keys()) { this.known.delete(k); if (++i >= MAX_KNOWN / 2) break; } }
        this.known.set(u.guid, { raw: u.name, hostile: r === "hostile", friendly: r === "friendly" });
      }
      if (ev.info && ev.info.owner && P.kindOf(ev.info.guid) !== P.KIND_PLAYER) this.setOwner(ev.info.guid, ev.info.owner);
      if (ev.type === "SPELL_SUMMON" && P.kindOf(ev.src.guid) === P.KIND_PLAYER && ev.dst.guid) this.setOwner(ev.dst.guid, ev.src.guid);
    }
    setOwner(pet, owner) {
      if (this.owners.size >= MAX_OWNERS) { let i = 0; for (const k of this.owners.keys()) { this.owners.delete(k); if (++i >= MAX_OWNERS / 2) break; } }
      this.owners.set(pet, owner);
    }
    hit(ev) {
      if (!isFriendlyPlayer(ev.dst)) return;
      const [credit, friend] = this.creditOf(ev.src);
      const h = { t: ev.t, credit, friend, hasPos: false, posOf: "", x: 0, y: 0, mapId: 0 };
      if (ev.info && ev.info.hasPos) { h.hasPos = true; h.posOf = ev.info.guid; h.x = ev.info.x; h.y = ev.info.y; h.mapId = ev.info.mapId; }
      let w = this.wins.get(ev.dst.guid);
      if (!w) {
        if (this.wins.size >= MAX_VICTIMS) {
          let oldest = null, oldT = 0;
          for (const [gd, ww] of this.wins) if (oldest === null || ww.touched < oldT) { oldest = gd; oldT = ww.touched; }
          this.wins.delete(oldest);
        }
        w = { hits: [], touched: ev.t };
        this.wins.set(ev.dst.guid, w);
      }
      w.touched = ev.t;
      w.hits = w.hits.filter((o) => ev.t - o.t <= ASSIST_WINDOW);
      w.hits.push(h);
      if (w.hits.length > MAX_HITS) w.hits.splice(0, w.hits.length - MAX_HITS);
    }
    // Who gets bounty credit for damage from src: [hostile player guid or "", fromFriendlyPlayer].
    creditOf(src) {
      switch (P.kindOf(src.guid)) {
        case P.KIND_PLAYER: {
          const r = P.reactionOf(src.flags);
          return r === "hostile" ? [src.guid, false] : ["", r === "friendly"];
        }
        case P.KIND_PET: case P.KIND_NPC: {
          const owner = this.owners.get(src.guid);
          if (!owner) return ["", false];
          const k = this.known.get(owner);
          if (k) return k.hostile ? [owner, false] : ["", k.friendly];
        }
      }
      return ["", false];
    }
    participant(guid) {
      const raw = (this.known.get(guid) || {}).raw || "";
      return { guid, raw, display: raw ? this.namer.display(raw) : "" };
    }
    death(ev, pkSrc) {
      const v = ev.dst;
      if (!isFriendlyPlayer(v)) return;
      const lt = this.last.get(v.guid);
      if (lt !== undefined && Math.abs(ev.t - lt) <= DUPLICATE_DEATH_WINDOW) return;
      if (this.last.size >= MAX_LAST) this.last = new Map();
      this.last.set(v.guid, ev.t);
      let hits = [];
      const w = this.wins.get(v.guid);
      if (w) { hits = w.hits.filter((h) => { const d = ev.t - h.t; return d >= 0 && d <= ASSIST_WINDOW; }); this.wins.delete(v.guid); }
      let killer = "", friendlyKill = false;
      if (pkSrc) [killer, friendlyKill] = this.creditOf(pkSrc);
      else if (hits.length) {
        let last = hits[hits.length - 1];
        for (const h of hits) if (h.t >= last.t) last = h;
        if (ev.t - last.t <= KILLING_BLOW_WINDOW) { killer = last.credit; friendlyKill = last.friend; }
      }
      const d = { t: ev.t, victimGuid: v.guid, victimName: this.namer.display(v.name), zone: this.zone, killer: null, assists: [], hasPos: false, x: 0, y: 0, mapId: 0 };
      if (!friendlyKill) {
        if (killer) d.killer = this.participant(killer);
        const seen = new Set([killer, ""]);
        for (const h of hits) if (!seen.has(h.credit)) { seen.add(h.credit); d.assists.push(this.participant(h.credit)); }
      }
      let best = null;
      for (const h of hits) {
        if (!h.hasPos) continue;
        const own = killer !== "" && h.posOf === killer, bestOwn = best !== null && killer !== "" && best.posOf === killer;
        if (best === null || (own && !bestOwn) || (own === bestOwn && h.t >= best.t)) best = h;
      }
      if (best) { d.hasPos = true; d.x = best.x; d.y = best.y; d.mapId = best.mapId; const n = this.maps.get(best.mapId); if (n) d.zone = n; }
      this.onDeath(d);
    }
  }

  // ---------------- Engine: tracker + per-rule-set delta accumulation ----------------
  const MIN_FIX_DISTANCE = 2, MIN_FIX_INTERVAL = 5000, MAX_TRACK = 5000, MAX_PAST = 10;
  const roundCoord = (v) => Math.round(v * 10) / 10;
  const newD = () => ({ events: 0, srcEvents: 0, dmgTotal: 0, dmgHits: 0, dmgEvents: 0, hpSum: 0, hpSamples: 0, maxHp: 0, kb: 0, assists: 0 });

  // Adds fix f to a chronologically sorted list with the Go dedupe (<2yd on the same map and <5s from its predecessor).
  function addFix(tr, f) {
    let i = tr.length;
    if (i && tr[i - 1][0] > f[0]) {
      let lo = 0, hi = tr.length;
      while (lo < hi) { const m = (lo + hi) >> 1; if (tr[m][0] > f[0]) hi = m; else lo = m + 1; }
      i = lo;
    }
    if (i > 0) {
      const p = tr[i - 1];
      if (p[1] === f[1] && Math.abs(f[0] - p[0]) < MIN_FIX_INTERVAL && Math.hypot(p[2] - f[2], p[3] - f[3]) < MIN_FIX_DISTANCE) return false;
    }
    tr.splice(i, 0, f);
    if (tr.length > MAX_TRACK) tr.splice(0, tr.length - MAX_TRACK);
    return true;
  }

  class Engine {
    // opts: {clock: fn, realmSuffix}
    constructor(opts) {
      opts = opts || {};
      this.mode = "auto"; this.manual = ""; this.detected = ""; this.hint = "";
      this.buckets = {}; this.wm = {}; this.bountyOnly = false;
      this.sourceId = "log";
      this.tracker = new Tracker({ realmSuffix: opts.realmSuffix || "", history: (sm) => this.record(sm) }, opts.clock);
      this.analyzer = new Analyzer((d) => this.credit(d));
      this.carry = ""; this.lines = 0; this.bad = 0; this.lastMe = ""; this.nApplied = 0; this.lastSfx = ""; this.maxT = 0;
    }
    setConfig(c) {
      if (c.mode) this.mode = c.mode === "manual" ? "manual" : "auto";
      if (c.manual !== undefined) this.manual = P.parseRuleset(c.manual);
    }
    activeRs() {
      if (this.mode === "manual" && this.manual) return this.manual;
      return this.detected || this.hint || this.manual || P.DEFAULT_RS;
    }
    rulesetInfo() { return { active: this.activeRs(), mode: this.mode, detected: this.detected || this.hint || null }; }
    // Starts a new log file: resets the analyzer windows and sets the per-rule-set watermarks (ledger lastTs).
    begin(o) {
      this.analyzer.setFile(o.file || "log");
      this.analyzer.reset();
      this.wm = Object.assign({}, o.wm || {});
      this.bountyOnly = !!o.bountyOnly;
      this.carry = ""; this.fileName = o.file || "";
    }
    bucket(rs) { return this.buckets[rs] || (this.buckets[rs] = { players: new Map(), maxT: 0, fixes: 0 }); }
    gate(rs, t) { return t > (this.wm[rs] || 0); }
    entry(b, guid, t) {
      let e = b.players.get(guid);
      if (!e) {
        e = { guid, raw: "", name: "", pastNames: [], reaction: "", level: 0, x: 0, y: 0, mapId: 0, zone: "", posTime: -1, firstSeen: t, lastSeen: t, d: newD(), fixes: [] };
        b.players.set(guid, e);
      }
      return e;
    }
    record(sm) {
      if (!sm.guid || this.bountyOnly) return;
      const rs = this.activeRs();
      if (!this.gate(rs, sm.t)) return;
      const b = this.bucket(rs), e = this.entry(b, sm.guid, sm.t);
      if (sm.t > b.maxT) b.maxT = sm.t;
      if (sm.name && sm.name !== e.raw) {
        if (e.raw) {
          const past = [e.raw, e.name];
          for (const n of past) if (n && e.pastNames.indexOf(n) < 0) e.pastNames.push(n);
          if (e.pastNames.length > MAX_PAST) e.pastNames.splice(0, e.pastNames.length - MAX_PAST);
        }
        e.raw = sm.name;
      }
      if (sm.display) e.name = sm.display;
      if (sm.reaction) e.reaction = sm.reaction;
      if (sm.level > 0) e.level = sm.level;
      if (sm.t < e.firstSeen) e.firstSeen = sm.t;
      if (sm.t > e.lastSeen) e.lastSeen = sm.t;
      const d = e.d;
      d.events++;
      if (sm.asSource) { d.srcEvents++; d.dmgEvents++; }
      if (sm.hasDamage) { d.dmgHits++; d.dmgTotal += sm.damage; }
      if (sm.hasHp) { d.hpSamples++; d.hpSum += sm.hp; if (sm.maxHp > d.maxHp) d.maxHp = sm.maxHp; }
      if (sm.hasPos && !sm.approx && sm.t >= e.posTime) {
        e.x = sm.x; e.y = sm.y; e.mapId = sm.mapId; e.posTime = sm.t;
        if (sm.zone) e.zone = sm.zone;
      }
      if (sm.hasPos) {
        if (addFix(e.fixes, [sm.t, sm.mapId, roundCoord(sm.x), roundCoord(sm.y), sm.approx ? "approx" : "exact"])) b.fixes++;
      }
    }
    credit(d) {
      const rs = this.activeRs();
      if (!this.gate(rs, d.t)) return;
      if (!d.killer && !d.assists.length) return;
      const b = this.bucket(rs);
      if (d.t > b.maxT) b.maxT = d.t;
      const one = (pt, key) => {
        if (!pt.guid) return;
        const e = this.entry(b, pt.guid, d.t);
        if (!e.name && (pt.display || pt.raw)) { e.name = pt.display || pt.raw; e.raw = pt.raw; }
        if (!e.reaction) e.reaction = "hostile";
        e.d[key]++;
        if (d.t > e.lastSeen) e.lastSeen = d.t;
        if (d.t < e.firstSeen) e.firstSeen = d.t;
      };
      if (d.killer) one(d.killer, "kb");
      for (const a of d.assists) one(a, "assists");
    }
    mineName(ev) {
      for (const u of [ev.src, ev.dst]) if (u && u.name && P.kindOf(u.guid) === P.KIND_PLAYER && P.isMine(u.flags)) return u.name;
      return "";
    }
    feedLine(line) {
      if (!line) return;
      // Everything the tracker/analyzer act on names a Player- guid (pet owners included); skip the rest cheaply.
      const i0 = line.indexOf("  ");
      if (i0 < 0) { this.bad++; return; }
      const c = line.indexOf(",", i0 + 2);
      const type = line.slice(i0 + 2, c < 0 ? line.length : c);
      const meta = type === "MAP_CHANGE" || type === "ZONE_CHANGE" || type === "COMBAT_LOG_VERSION";
      if (!meta && line.indexOf("Player-", i0) < 0) { this.lines++; return; }
      const ev = P.parseLine(line);
      this.lines++;
      if (!ev) { this.bad++; return; }
      if (ev.t > this.maxT) this.maxT = ev.t;
      const me = this.mineName(ev);
      if (me && me !== this.lastMe) {
        this.lastMe = me;
        const id = P.detectFromRawName(me);
        if (id) this.detected = id;
      }
      this.tracker.apply(this.sourceId, ev);
      if (++this.nApplied % 512 === 1 || !this.lastSfx) {
        const sfx = this.tracker.namer.suffix;
        if (sfx !== this.lastSfx) { this.lastSfx = sfx; const id = P.detectFromRawName("x-" + sfx.replace(/^-/, "")); if (id) this.hint = id; }
      }
      this.analyzer.apply(ev);
      if (this.nApplied % 50000 === 0) this.tracker.prune(this.tracker.clock());
    }
    // Feeds text that may end mid-line; the partial tail is kept for the next call.
    feedText(text, final) {
      text = this.carry + text;
      let start = 0, nl;
      while ((nl = text.indexOf("\n", start)) >= 0) { this.feedLine(text.slice(start, nl)); start = nl + 1; }
      this.carry = text.slice(start);
      if (final && this.carry) { this.feedLine(this.carry); this.carry = ""; }
    }
    pending() {
      let players = 0, fixes = 0;
      for (const b of Object.values(this.buckets)) { players += b.players.size; fixes += b.fixes; }
      return { players, fixes };
    }
    // Moves the accumulated deltas out: [{rs, from, to, players:[...], maps:[...]}]. Watermarks advance to `to`.
    flush() {
      const out = [];
      const maps = [];
      for (const s of this.tracker.srcs.values()) for (const m of s.maps.values()) maps.push({ id: m.id, name: m.name, x0: m.x0, x1: m.x1, y0: m.y0, y1: m.y1 });
      for (const rs of Object.keys(this.buckets)) {
        const b = this.buckets[rs];
        const players = [];
        for (const e of b.players.values()) {
          if (this.bountyOnly && !e.d.kb && !e.d.assists) continue;
          const p = { guid: e.guid, name: e.name || e.raw.split("-").join(" "), reaction: e.reaction || "unknown", level: e.level, firstSeen: e.firstSeen, lastSeen: e.lastSeen, d: e.d };
          if (e.pastNames.length) p.pastNames = e.pastNames;
          if (e.posTime >= 0) { p.x = e.x; p.y = e.y; p.mapId = e.mapId; p.zone = e.zone; p.posTime = e.posTime; }
          if (this.bountyOnly) p.fixes = []; else p.fixes = e.fixes;
          players.push(p);
        }
        if (players.length) out.push({ rs, from: this.wm[rs] || 0, to: b.maxT, players, maps });
        if (b.maxT > (this.wm[rs] || 0)) this.wm[rs] = b.maxT;
      }
      this.buckets = {};
      return out;
    }
    snapshot() {
      const s = this.tracker.snapshot();
      s.ruleset = this.rulesetInfo();
      return s;
    }
  }

  const api = { Tracker, Analyzer, Engine, estimate, addFix };
  g.RadarTracker = api;
  if (typeof module !== "undefined" && module.exports) module.exports = api;
})(typeof self !== "undefined" ? self : globalThis);
