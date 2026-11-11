'use strict';
/**
 * tracker.js - single responsibility: live view of nearby units from parsed log events (the Go "tracker").
 *
 * Public API (Radar.tracker):
 *  - Tracker(cfg, clock): apply(sourceId, event), snapshot() -> Snapshot (Go /api/state shape), prune(now)
 *  - estimate(fixes, now, n, gap) -> position estimate with spread (yards)
 *  - round1(v) -> v rounded to one decimal
 * Units go stale and expire (config.engine); units near the operator inherit the operator's position as approximate
 * fixes. No DOM; runs in the Worker, in-process and in node.
 */
(function (g) {
  const dep = (name, file) =>
    typeof require !== 'undefined' && typeof module !== 'undefined' ? require(file) : g.Radar[name];
  const P = dep('parser', './parser.js');
  const CFG = dep('config', './config.js');
  const E = CFG.engine;
  const U = CFG.units;

  const APPROX_WEIGHT = E.APPROX_WEIGHT;
  const APPROX_WINDOW = E.APPROX_WINDOW_MS;
  const FIX_RING = E.FIX_RING;
  /** @param {number} v @returns {number} v rounded to one decimal place */
  // Deliberate copy of round1 in lambda/src/validate.mjs: the static site and the Lambda asset cannot share code.
  const round1 = v => Math.round(v * U.ROUND_1DP) / U.ROUND_1DP;

  /**
   * Recent fixes of one unit on its latest map, newest first, stopping at a map change or a long gap.
   * @param {Array} fixes trail fixes
   * @param {number} n count
   * @param {number} gap maximum gap in ms
   * @returns {*} the computed value
   */
  function recentFixes(fixes, n, gap) {
    const mapId = fixes[fixes.length - 1].mapId,
      win = [];
    for (let i = fixes.length - 1; i >= 0; i--) {
      const f = fixes[i];
      if (f.mapId !== mapId || (win.length > 0 && win[win.length - 1].t - f.t > gap)) break;
      win.push(f);
      if (win.length >= n) break;
    }
    return win;
  }

  /**
   * Dead-reckoned point ahead of the newest exact fix, or null when the unit is (nearly) standing still.
   * @param {Object} win the win
   * @param {number} now current time in unix ms
   * @returns {*} the computed value
   */
  function projectedPoint(win, now) {
    const ex = win.filter(f => f.kind === 'exact');
    if (ex.length < 2) return null;
    const a = ex[0],
      b = ex[Math.min(E.ESTIMATE_SPEED_FIXES, ex.length - 1)];
    const dt = (a.t - b.t) / U.MS_PER_SEC;
    if (dt < E.ESTIMATE_MIN_DT_SEC) return null;
    let vx = (a.x - b.x) / dt,
      vy = (a.y - b.y) / dt;
    const speed = Math.hypot(vx, vy);
    if (speed > E.ESTIMATE_MAX_SPEED) {
      vx = (vx * E.ESTIMATE_MAX_SPEED) / speed;
      vy = (vy * E.ESTIMATE_MAX_SPEED) / speed;
    }
    if (Math.min(speed, E.ESTIMATE_MAX_SPEED) < E.ESTIMATE_MIN_SPEED) return null;
    const ahead = Math.max(0, Math.min(E.ESTIMATE_MAX_LOOKAHEAD_SEC, (now - a.t) / U.MS_PER_SEC));
    return { x: a.x + vx * ahead, y: a.y + vy * ahead, w: E.ESTIMATE_PROJECTED_WEIGHT };
  }

  /**
   * Weighted centre of the recent fixes (newer and exact fixes weigh more) plus its spread in yards.
   * @param {Array} fixes trail fixes
   * @param {number} now current time in unix ms
   * @param {number} n count
   * @param {number} gap maximum gap in ms
   * @returns {*} the computed value
   */
  function estimate(fixes, now, n, gap) {
    if (!fixes.length) return null;
    const win = recentFixes(fixes, n, gap);
    let decay = 1,
      exact = 0;
    const pts = win.map(f => {
      const w = f.kind === 'exact' ? 1 : APPROX_WEIGHT;
      if (f.kind === 'exact') exact++;
      const pt = { x: f.x, y: f.y, w: w * decay };
      decay *= E.ESTIMATE_DECAY;
      return pt;
    });
    const ahead = projectedPoint(win, now);
    if (ahead) pts.push(ahead);
    let ws = 0,
      mx = 0,
      my = 0;
    for (const pt of pts) {
      ws += pt.w;
      mx += pt.x * pt.w;
      my += pt.y * pt.w;
    }
    mx /= ws;
    my /= ws;
    let v = 0;
    for (const pt of pts) v += pt.w * ((pt.x - mx) * (pt.x - mx) + (pt.y - my) * (pt.y - my));
    const spread = Math.max(E.ESTIMATE_MIN_SPREAD_YD, Math.sqrt(v / ws / 2));
    return { x: round1(mx), y: round1(my), spread: round1(spread), mapId: win[0].mapId, n: win.length, exact };
  }

  /**
   * Sample with every field at its neutral value; identity fields come from the unit and the event.
   * @param {Object} source per-source state (zone)
   * @param {Object} ev parsed log event
   * @param {{guid: string, name: string}} participant unit named by the event
   * @param {Object} unit tracked unit
   * @returns {Object} sample
   */
  function emptySample(source, ev, participant, unit) {
    return {
      guid: participant.guid,
      name: unit.raw || participant.name,
      display: '',
      reaction: '',
      level: unit.level,
      t: ev.t,
      asSource: !!ev.src && participant.guid === ev.src.guid,
      zone: source.zone,
      hasHp: false,
      hp: 0,
      maxHp: 0,
      hasDamage: false,
      damage: 0,
      hasPos: false,
      approx: false,
      x: 0,
      y: 0,
      mapId: 0,
    };
  }

  /**
   * Copy hp and exact position from the event's info block when the block describes this unit.
   * @param {Object} sample mutated
   * @param {Object} ev parsed log event
   * @param {string} guid unit guid
   */
  function fillSampleFromInfo(sample, ev, guid) {
    const info = ev.info;
    if (!info || info.guid !== guid) return;
    Object.assign(sample, { hasHp: info.hasHp, hp: info.hp, maxHp: info.maxHp });
    if (info.hasPos) Object.assign(sample, { hasPos: true, x: info.x, y: info.y, mapId: info.mapId });
  }

  /**
   * Put the blip at the unit's exact fix, unless a newer approximate fix supersedes it by more than APPROX_WINDOW_MS.
   * @param {Object} blip mutated: x, y (world yards), mapId, posKind, weight
   * @param {Object} unit tracked unit with exact / approx fixes
   */
  function placeBlip(blip, unit) {
    const exactIsCurrent = unit.exact && (!unit.approx || unit.approx.t - unit.exact.t <= APPROX_WINDOW);
    if (exactIsCurrent)
      Object.assign(blip, { x: unit.exact.x, y: unit.exact.y, mapId: unit.exact.mapId, posKind: 'exact', weight: 1 });
    else if (unit.approx) {
      Object.assign(blip, {
        x: unit.approx.x,
        y: unit.approx.y,
        mapId: unit.approx.mapId,
        posKind: 'approx',
        weight: APPROX_WEIGHT,
      });
    }
  }

  class Tracker {
    /**
     * cfg: {trackNPCs, disableApprox, realmSuffix, staleMs, expireMs, approxMaxAgeMs, history(sample)}; clock: () => ms
     * @param {Object} cfg configuration
     * @param {Function} clock the clock
     */
    constructor(cfg, clock) {
      cfg = cfg || {};
      this.cfg = Object.assign(
        {
          trackNPCs: false,
          disableApprox: false,
          realmSuffix: '',
          staleMs: E.STALE_MS,
          expireMs: E.EXPIRE_MS,
          approxMaxAgeMs: E.APPROX_MAX_AGE_MS,
          history: null,
        },
        cfg,
      );
      this.clock = clock || Date.now;
      this.units = new Map();
      this.srcs = new Map();
      this.primary = '';
      this.namer = new P.Namer(this.cfg.realmSuffix);
      this.events = 0;
      this.n = 0;
    }
    /**
     * Per-source state (zone, charts, operator guid), created on demand.
     * @param {string} id element id or map id
     * @returns {*} the computed value
     */
    source(id) {
      let s = this.srcs.get(id);
      if (!s) {
        s = { id, meGuid: '', meFix: null, zone: '', mapId: 0, maps: new Map(), advanced: null, events: 0 };
        this.srcs.set(id, s);
      }
      return s;
    }
    /**
     * Whether units with this guid are tracked at all.
     * @param {string} guid unit guid
     * @returns {*} the computed value
     */
    tracked(guid) {
      switch (P.kindOf(guid)) {
        case P.KIND_PLAYER:
          return true;
        case P.KIND_NPC:
        case P.KIND_PET:
          return this.cfg.trackNPCs;
      }
      return false;
    }
    /**
     * Handles meta events; returns true when ev carried no units.
     * @param {Object} s the s
     * @param {Object} ev parsed log event
     * @returns {*} the computed value
     */
    applyMeta(s, ev) {
      switch (ev.type) {
        case 'COMBAT_LOG_VERSION':
          s.advanced = ev.advanced;
          return true;
        case 'ZONE_CHANGE':
          if (ev.zone) s.zone = ev.zone;
          return true;
        case 'MAP_CHANGE':
          if (ev.map) {
            s.maps.set(ev.map.id, ev.map);
            s.mapId = ev.map.id;
            if (!s.zone) s.zone = ev.map.name;
          }
          return true;
      }
      return false;
    }
    /**
     * Distinct tracked units named by the event (source, destination, info block), without duplicates.
     * @param {Object} ev parsed log event
     * @returns {*} the computed value
     */
    participants(ev) {
      const parts = [];
      const add = participant => {
        if (!participant.guid || !this.tracked(participant.guid) || parts.some(q => q.guid === participant.guid))
          return;
        parts.push(participant);
      };
      if (ev.src) add({ guid: ev.src.guid, name: ev.src.name, flags: ev.src.flags, hasFlags: true });
      if (ev.dst) add({ guid: ev.dst.guid, name: ev.dst.name, flags: ev.dst.flags, hasFlags: true });
      if (ev.info) add({ guid: ev.info.guid, name: '', flags: 0, hasFlags: false });
      return parts;
    }
    /**
     * Applies the advanced-logging block (hp, level, exact position); returns the guid whose exact fix was set, or "".
     * @param {Object} s the s
     * @param {Object} ev parsed log event
     * @returns {*} the computed value
     */
    applyInfo(s, ev) {
      if (!ev.info || !this.tracked(ev.info.guid)) return '';
      const u = this.units.get(ev.info.guid);
      if (ev.info.hasHp) {
        u.hp = ev.info.hp;
        u.maxHp = ev.info.maxHp;
        u.hasHp = true;
      }
      if (ev.info.level > 0) u.level = ev.info.level;
      if (!ev.info.hasPos) return '';
      const f = { t: ev.t, x: ev.info.x, y: ev.info.y, kind: 'exact', mapId: ev.info.mapId };
      this.setExact(u, f);
      if (u.guid === s.meGuid) {
        s.meFix = f;
        s.mapId = f.mapId;
      }
      return u.guid;
    }
    /**
     * Players without their own position inherit ours (they are in combat range of us). Returns Map(guid -> fix).
     * @param {Object} s the s
     * @param {Object} ev parsed log event
     * @param {Array} parts the parts
     * @param {Object} exactGuid the exact guid
     * @returns {*} the computed value
     */
    applyApproxFixes(s, ev, parts, exactGuid) {
      const out = new Map();
      if (this.cfg.disableApprox || !s.meFix || Math.abs(ev.t - s.meFix.t) >= this.cfg.approxMaxAgeMs) return out;
      for (const participant of parts) {
        const u = this.units.get(participant.guid);
        if (u.kind !== P.KIND_PLAYER || participant.guid === s.meGuid || participant.guid === exactGuid) continue;
        const f = { t: ev.t, x: s.meFix.x, y: s.meFix.y, kind: 'approx', mapId: s.meFix.mapId };
        u.approx = f;
        this.pushFix(u, f);
        out.set(participant.guid, f);
      }
      return out;
    }
    /**
     * Apply one parsed log event.
     * @param {string} id element id or map id
     * @param {Object} ev parsed log event
     */
    apply(id, ev) {
      const s = this.source(id);
      this.primary = id;
      this.events++;
      s.events++;
      if (this.applyMeta(s, ev)) return;
      const parts = this.participants(ev);
      for (const participant of parts) this.touch(s, participant, ev.t);
      const exactGuid = this.applyInfo(s, ev);
      const approxFixes = this.applyApproxFixes(s, ev, parts, exactGuid);
      if (ev.isDamage && ev.hasAmount && ev.src && this.tracked(ev.src.guid)) {
        const u = this.units.get(ev.src.guid);
        u.dmgTotal += ev.amount;
        u.dmgHits++;
      }
      if (this.cfg.history)
        for (const participant of parts)
          this.cfg.history(this.toSample(s, ev, participant, approxFixes.get(participant.guid)));
    }
    /**
     * One history sample: what the event says about unit `participant`, for the engine's per-ruleset accumulator.
     * @param {Object} source per-source state (zone)
     * @param {Object} ev parsed log event
     * @param {{guid: string, name: string}} participant unit named by the event
     * @param {Object|null} approxFix approximate fix (operator position, world yards) given to this unit, if any
     * @returns {Object} sample {guid, name, display, reaction, level, t, zone, hp..., damage..., position...}
     */
    toSample(source, ev, participant, approxFix) {
      const unit = this.units.get(participant.guid);
      const sample = emptySample(source, ev, participant, unit);
      if (sample.name) sample.display = this.namer.display(sample.name);
      if (unit.hasFlags) sample.reaction = P.reactionOf(unit.flags);
      fillSampleFromInfo(sample, ev, participant.guid);
      if (approxFix && !sample.hasPos) {
        Object.assign(sample, { hasPos: true, approx: true, x: approxFix.x, y: approxFix.y, mapId: approxFix.mapId });
      }
      if (ev.isDamage && ev.hasAmount && ev.src && participant.guid === ev.src.guid) {
        sample.hasDamage = true;
        sample.damage = ev.amount;
      }
      return sample;
    }
    /**
     * Create or refresh a unit from an event participant.
     * @param {Object} s the s
     * @param {Object} p blip or contact
     * @param {number} ts the ts
     * @returns {*} the computed value
     */
    touch(s, participant, ts) {
      let u = this.units.get(participant.guid);
      if (!u) {
        u = {
          guid: participant.guid,
          raw: '',
          flags: 0,
          hasFlags: false,
          kind: P.kindOf(participant.guid),
          first: ts,
          last: ts,
          events: 0,
          hp: 0,
          maxHp: 0,
          hasHp: false,
          level: 0,
          exact: null,
          approx: null,
          fixes: [],
          dmgTotal: 0,
          dmgHits: 0,
          source: s.id,
        };
        this.units.set(participant.guid, u);
      }
      if (participant.name) {
        u.raw = participant.name;
        if (u.kind === P.KIND_PLAYER) this.namer.observe(participant.name);
      }
      if (participant.hasFlags) {
        u.flags = participant.flags;
        u.hasFlags = true;
        if (u.kind === P.KIND_PLAYER && P.isMine(participant.flags)) s.meGuid = participant.guid;
      }
      if (ts > u.last) u.last = ts;
      u.source = s.id;
      u.events++;
      return u;
    }
    /**
     * Store an exact fix as the unit position.
     * @param {Object} u unit
     * @param {Object} f fix
     */
    setExact(u, f) {
      u.exact = f;
      this.pushFix(u, f);
    }
    /**
     * Append a fix to the unit trail ring (bounded, duplicates dropped).
     * @param {Object} u unit
     * @param {Object} f fix
     */
    pushFix(u, f) {
      const n = u.fixes.length;
      if (n > 0) {
        const l = u.fixes[n - 1];
        if (l.kind === f.kind && l.x === f.x && l.y === f.y && f.t - l.t < E.DUP_FIX_MS) return;
      }
      u.fixes.push(f);
      if (u.fixes.length > FIX_RING) u.fixes.splice(0, u.fixes.length - FIX_RING);
    }
    /**
     * Guids of the operator characters.
     * @returns {*} the computed value
     */
    meSet() {
      const m = new Set();
      for (const s of this.srcs.values()) if (s.meGuid) m.add(s.meGuid);
      return m;
    }
    /**
     * Forget units not seen for longer than the expiry (except the operator).
     * @param {number} now current time in unix ms
     */
    prune(now) {
      const me = this.meSet();
      for (const [g, u] of this.units) if (!me.has(g) && now - u.last > this.cfg.expireMs) this.units.delete(g);
    }
    /**
     * Raw name of the operator of the primary source.
     * @returns {*} the computed value
     */
    primaryMeRaw() {
      const s = this.srcs.get(this.primary);
      const u = s && s.meGuid ? this.units.get(s.meGuid) : null;
      return u ? u.raw : '';
    }
    /**
     * Build the Blip (Go "pip") for a unit.
     * @param {Object} u unit
     * @param {number} now current time in unix ms
     * @param {boolean} isMe the is me
     * @returns {*} the computed value
     */
    pip(u, now, isMe) {
      const age = now - u.last;
      const blip = {
        guid: u.guid,
        raw: u.raw,
        name: this.namer.display(u.raw) || u.guid,
        kind: u.kind === P.KIND_PLAYER ? 'player' : u.kind === P.KIND_PET ? 'pet' : 'npc',
        reaction: u.hasFlags ? P.reactionOf(u.flags) : 'unknown',
        state: age > this.cfg.staleMs ? 'stale' : 'active',
        me: isMe,
        hp: u.hp,
        maxHp: u.maxHp,
        hasHp: u.hasHp,
        level: u.level,
        x: 0,
        y: 0,
        mapId: 0,
        posKind: 'none',
        weight: 0,
        lastEvent: u.last,
        ageSec: age / U.MS_PER_SEC,
        events: u.events,
        damageTotal: u.dmgTotal,
        damageHits: u.dmgHits,
        source: u.source,
        est: null,
      };
      placeBlip(blip, u);
      blip.est = estimate(u.fixes, now, E.ESTIMATE_N, E.ESTIMATE_GAP_MS);
      return blip;
    }
    /**
     * Build the Snapshot shown by the radar.
     * @returns {*} the computed value
     */
    snapshot() {
      const now = this.clock();
      this.prune(now);
      const me = this.meSet();
      const snap = {
        now,
        zone: '',
        mapId: 0,
        me: null,
        pips: [],
        maps: {},
        advanced: null,
        realmSuffix: this.namer.suffix,
        sources: [],
        stats: { events: this.events, units: this.units.size, sources: this.srcs.size },
        staleSec: this.cfg.staleMs / U.MS_PER_SEC,
        expireSec: this.cfg.expireMs / U.MS_PER_SEC,
      };
      for (const [id, s] of this.srcs) {
        snap.sources.push(id);
        for (const [mid, m] of s.maps)
          snap.maps[String(mid)] = { name: m.name, x0: m.x0, x1: m.x1, y0: m.y0, y1: m.y1 };
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

  const api = { Tracker, estimate, round1 };
  (g.Radar || (g.Radar = {})).tracker = api;
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
})(typeof self !== 'undefined' ? self : globalThis);
