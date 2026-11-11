'use strict';
/**
 * engine.js - single responsibility: per-ruleset accumulation of parsed log events into upload deltas (pings).
 *
 * Feeds log text to the tracker (live blips) and the analyzer (bounty) and records contact history, trails and bounty
 * credit per ruleset, gated by the ledger watermarks. flush() hands the deltas out and advances the watermarks.
 * Public API (Radar.engine): Engine(opts): setConfig, begin, feedText, pending, flush, snapshot, rulesetInfo;
 * addFix(trail, fix) -> boolean. The worker message handler lives in engine-protocol.js. No DOM.
 */
(function (g) {
  const dep = (name, file) =>
    typeof require !== 'undefined' && typeof module !== 'undefined' ? require(file) : g.Radar[name];
  const P = dep('parser', './parser.js');
  const { Tracker, round1 } = dep('tracker', './tracker.js');
  const { Analyzer } = dep('analyzer', './analyzer.js');
  const CFG = dep('config', './config.js');
  const TZ = dep('timeZone', './time-zone.js');
  const E = CFG.engine;

  const MIN_FIX_DISTANCE = E.MIN_FIX_DISTANCE_YD;
  const MIN_FIX_INTERVAL = E.MIN_FIX_INTERVAL_MS;
  const MAX_TRACK = E.MAX_TRACK;
  const MAX_PAST = E.MAX_PAST_NAMES;
  const roundCoord = round1;
  /** @returns {Object} zeroed per-contact counters */
  const newD = () => ({
    events: 0,
    srcEvents: 0,
    dmgTotal: 0,
    dmgHits: 0,
    dmgEvents: 0,
    hpSum: 0,
    hpSamples: 0,
    maxHp: 0,
    kb: 0,
    assists: 0,
  });

  /**
   * Adds fix f to a chronologically sorted list with the Go dedupe (<2yd on the same map and <5s from its predecessor).
   * @param {Object} tr trail (sorted fix tuples)
   * @param {Object} f fix
   * @returns {*} the computed value
   */
  function addFix(tr, f) {
    let i = tr.length;
    if (i && tr[i - 1][0] > f[0]) {
      let lo = 0,
        hi = tr.length;
      while (lo < hi) {
        const m = (lo + hi) >> 1;
        if (tr[m][0] > f[0]) hi = m;
        else lo = m + 1;
      }
      i = lo;
    }
    if (i > 0) {
      const prev = tr[i - 1];
      if (
        prev[1] === f[1] &&
        Math.abs(f[0] - prev[0]) < MIN_FIX_INTERVAL &&
        Math.hypot(prev[2] - f[2], prev[3] - f[3]) < MIN_FIX_DISTANCE
      )
        return false;
    }
    tr.splice(i, 0, f);
    if (tr.length > MAX_TRACK) tr.splice(0, tr.length - MAX_TRACK);
    return true;
  }

  class Engine {
    /**
     * opts: {clock: fn, realmSuffix, timezone (IANA zone for log lines without an offset; default UTC)}
     * @param {Object} opts options
     */
    constructor(opts) {
      opts = opts || {};
      this.mode = 'auto';
      this.manual = '';
      this.detected = '';
      this.hint = '';
      this.buckets = {};
      this.wm = {};
      this.bountyOnly = false;
      this.sourceId = 'log';
      this.tracker = new Tracker({ realmSuffix: opts.realmSuffix || '', history: sm => this.record(sm) }, opts.clock);
      this.analyzer = new Analyzer(d => this.credit(d));
      this.carry = '';
      this.lines = 0;
      this.bad = 0;
      this.lastMe = '';
      this.nApplied = 0;
      this.lastSfx = '';
      this.maxT = 0;
      this.zone = TZ.isValidZone(opts.timezone) ? opts.timezone : CFG.timeZone.FALLBACK_ZONE;
      this.fileFirstT = 0;
      this.fileLastT = 0;
    }
    /**
     * Set the rule-set mode (auto/manual), the manual choice and the zone for log lines without an offset (an invalid
     * zone name is ignored).
     * @param {Object} c the c
     */
    setConfig(c) {
      if (c.mode) this.mode = c.mode === 'manual' ? 'manual' : 'auto';
      if (c.manual !== undefined) this.manual = P.parseRuleset(c.manual);
      if (TZ.isValidZone(c.timezone)) this.zone = c.timezone;
    }
    /**
     * The ruleset new data is recorded under.
     * @returns {*} the computed value
     */
    activeRs() {
      if (this.mode === 'manual' && this.manual) return this.manual;
      return this.detected || this.hint || this.manual || P.DEFAULT_RS;
    }
    /**
     * Ruleset status sent to the page: active, mode, detected.
     * @returns {*} the computed value
     */
    rulesetInfo() {
      return { active: this.activeRs(), mode: this.mode, detected: this.detected || this.hint || null };
    }
    /**
     * Starts a new log file: resets the analyzer windows and sets the per-rule-set watermarks (ledger lastTs).
     * @param {Object} o options
     */
    begin(o) {
      this.analyzer.setFile(o.file || 'log');
      this.analyzer.reset();
      this.wm = Object.assign({}, o.wm || {});
      this.bountyOnly = !!o.bountyOnly;
      this.carry = '';
      this.fileName = o.file || '';
      this.fileFirstT = 0;
      this.fileLastT = 0;
    }
    /**
     * The per-ruleset accumulator, created on demand.
     * @param {string} rs ruleset id
     * @returns {*} the computed value
     */
    bucket(rs) {
      return this.buckets[rs] || (this.buckets[rs] = { players: new Map(), maxT: 0, fixes: 0 });
    }
    /**
     * Whether an event is newer than the ledger watermark (otherwise it was already uploaded).
     * @param {string} rs ruleset id
     * @param {number} t time in unix ms
     * @returns {*} the computed value
     */
    gate(rs, t) {
      return t > (this.wm[rs] || 0);
    }
    /**
     * The contact entry of a bucket, created on demand.
     * @param {Object} b bucket or batch
     * @param {string} guid unit guid
     * @param {number} t time in unix ms
     * @returns {*} the computed value
     */
    entry(b, guid, t) {
      let e = b.players.get(guid);
      if (!e) {
        e = {
          guid,
          raw: '',
          name: '',
          pastNames: [],
          reaction: '',
          level: 0,
          x: 0,
          y: 0,
          mapId: 0,
          zone: '',
          posTime: -1,
          firstSeen: t,
          lastSeen: t,
          d: newD(),
          fixes: [],
        };
        b.players.set(guid, e);
      }
      return e;
    }
    /** Keep the previous names of a contact that renamed (bounded). @param {Object} e entry */
    keepPastNames(e) {
      for (const n of [e.raw, e.name]) if (n && e.pastNames.indexOf(n) < 0) e.pastNames.push(n);
      if (e.pastNames.length > MAX_PAST) e.pastNames.splice(0, e.pastNames.length - MAX_PAST);
    }
    /** Apply names, reaction, level and seen-times of a sample. @param {Object} e entry @param {Object} sm sample */
    applyIdentity(e, sm) {
      if (sm.name && sm.name !== e.raw) {
        if (e.raw) this.keepPastNames(e);
        e.raw = sm.name;
      }
      if (sm.display) e.name = sm.display;
      if (sm.reaction) e.reaction = sm.reaction;
      if (sm.level > 0) e.level = sm.level;
      if (sm.t < e.firstSeen) e.firstSeen = sm.t;
      if (sm.t > e.lastSeen) e.lastSeen = sm.t;
    }
    /** Add the sample's counters (events, damage, hp). @param {Object} d counters @param {Object} sm sample */
    applyCounters(d, sm) {
      d.events++;
      if (sm.asSource) {
        d.srcEvents++;
        d.dmgEvents++;
      }
      if (sm.hasDamage) {
        d.dmgHits++;
        d.dmgTotal += sm.damage;
      }
      if (!sm.hasHp) return;
      d.hpSamples++;
      d.hpSum += sm.hp;
      if (sm.maxHp > d.maxHp) d.maxHp = sm.maxHp;
    }
    /** Last exact position and trail of a sample. @param {Object} b bucket @param {Object} e entry @param {Object} sm
     * sample */
    applyPosition(b, e, sm) {
      if (!sm.hasPos) return;
      if (!sm.approx && sm.t >= e.posTime) {
        e.x = sm.x;
        e.y = sm.y;
        e.mapId = sm.mapId;
        e.posTime = sm.t;
        if (sm.zone) e.zone = sm.zone;
      }
      const fix = [sm.t, sm.mapId, roundCoord(sm.x), roundCoord(sm.y), sm.approx ? 'approx' : 'exact'];
      if (addFix(e.fixes, fix)) b.fixes++;
    }
    /** Record one history sample into the active ruleset's bucket (skipped before the ledger watermark). @param
     * {Object} sm */
    record(sm) {
      if (!sm.guid || this.bountyOnly) return;
      const rs = this.activeRs();
      if (!this.gate(rs, sm.t)) return;
      const b = this.bucket(rs);
      const e = this.entry(b, sm.guid, sm.t);
      if (sm.t > b.maxT) b.maxT = sm.t;
      this.applyIdentity(e, sm);
      this.applyCounters(e.d, sm);
      this.applyPosition(b, e, sm);
    }
    /**
     * Give bounty credit (killing blow, assists) for one death.
     * @param {Object} d response data
     */
    credit(d) {
      const rs = this.activeRs();
      if (!this.gate(rs, d.t)) return;
      if (!d.killer && !d.assists.length) return;
      const b = this.bucket(rs);
      if (d.t > b.maxT) b.maxT = d.t;
      const one = (pt, key) => {
        if (!pt.guid) return;
        const e = this.entry(b, pt.guid, d.t);
        if (!e.name && (pt.display || pt.raw)) {
          e.name = pt.display || pt.raw;
          e.raw = pt.raw;
        }
        if (!e.reaction) e.reaction = 'hostile';
        e.d[key]++;
        if (d.t > e.lastSeen) e.lastSeen = d.t;
        if (d.t < e.firstSeen) e.firstSeen = d.t;
      };
      if (d.killer) one(d.killer, 'kb');
      for (const a of d.assists) one(a, 'assists');
    }
    /**
     * Raw name of the operator character mentioned by an event, or "".
     * @param {Object} ev parsed log event
     * @returns {*} the computed value
     */
    mineName(ev) {
      for (const u of [ev.src, ev.dst])
        if (u && u.name && P.kindOf(u.guid) === P.KIND_PLAYER && P.isMine(u.flags)) return u.name;
      return '';
    }
    /**
     * Parse one log line and apply it to the tracker and analyzer.
     * @param {string} line the line
     */
    feedLine(line) {
      if (!line) return;
      // Everything the tracker/analyzer act on names a Player- guid (pet owners included); skip the rest cheaply.
      const i0 = line.indexOf('  ');
      if (i0 < 0) {
        this.bad++;
        return;
      }
      const c = line.indexOf(',', i0 + 2);
      const type = line.slice(i0 + 2, c < 0 ? line.length : c);
      const meta = type === 'MAP_CHANGE' || type === 'ZONE_CHANGE' || type === 'COMBAT_LOG_VERSION';
      if (!meta && line.indexOf('Player-', i0) < 0) {
        this.lines++;
        return;
      }
      const ev = P.parseLine(line, this.zone);
      this.lines++;
      if (!ev) {
        this.bad++;
        return;
      }
      if (ev.t > this.maxT) this.maxT = ev.t;
      if (!this.fileFirstT || ev.t < this.fileFirstT) this.fileFirstT = ev.t;
      if (ev.t > this.fileLastT) this.fileLastT = ev.t;
      const me = this.mineName(ev);
      if (me && me !== this.lastMe) {
        this.lastMe = me;
        const id = P.detectFromRawName(me);
        if (id) this.detected = id;
      }
      this.tracker.apply(this.sourceId, ev);
      if (++this.nApplied % E.SUFFIX_CHECK_EVERY === 1 || !this.lastSfx) {
        const sfx = this.tracker.namer.suffix;
        if (sfx !== this.lastSfx) {
          this.lastSfx = sfx;
          const id = P.detectFromRawName('x-' + sfx.replace(/^-/, ''));
          if (id) this.hint = id;
        }
      }
      this.analyzer.apply(ev);
      if (this.nApplied % E.PRUNE_EVERY_EVENTS === 0) this.tracker.prune(this.tracker.clock());
    }
    /**
     * Feeds text that may end mid-line; the partial tail is kept for the next call.
     * @param {string} text text
     * @param {boolean} final this is the last flush
     */
    feedText(text, final) {
      text = this.carry + text;
      let start = 0,
        nl;
      while ((nl = text.indexOf('\n', start)) >= 0) {
        this.feedLine(text.slice(start, nl));
        start = nl + 1;
      }
      this.carry = text.slice(start);
      if (final && this.carry) {
        this.feedLine(this.carry);
        this.carry = '';
      }
    }
    /**
     * Count contacts and fixes waiting for a flush.
     * @returns {*} the computed value
     */
    pending() {
      let players = 0,
        fixes = 0;
      for (const b of Object.values(this.buckets)) {
        players += b.players.size;
        fixes += b.fixes;
      }
      return { players, fixes };
    }
    /**
     * Moves the accumulated deltas out: [{rs, from, to, players:[...], maps:[...]}]. Watermarks advance to `to`.
     * @returns {*} the computed value
     */
    flush() {
      const out = [];
      const maps = [];
      for (const s of this.tracker.srcs.values())
        for (const m of s.maps.values()) maps.push({ id: m.id, name: m.name, x0: m.x0, x1: m.x1, y0: m.y0, y1: m.y1 });
      for (const rs of Object.keys(this.buckets)) {
        const b = this.buckets[rs];
        const players = [];
        for (const e of b.players.values()) {
          if (this.bountyOnly && !e.d.kb && !e.d.assists) continue;
          const delta = {
            guid: e.guid,
            name: e.name || e.raw.split('-').join(' '),
            reaction: e.reaction || 'unknown',
            level: e.level,
            firstSeen: e.firstSeen,
            lastSeen: e.lastSeen,
            d: e.d,
          };
          if (e.pastNames.length) delta.pastNames = e.pastNames;
          if (e.posTime >= 0) {
            delta.x = e.x;
            delta.y = e.y;
            delta.mapId = e.mapId;
            delta.zone = e.zone;
            delta.posTime = e.posTime;
          }
          if (this.bountyOnly) delta.fixes = [];
          else delta.fixes = e.fixes;
          players.push(delta);
        }
        if (players.length) out.push({ rs, from: this.wm[rs] || 0, to: b.maxT, players, maps });
        if (b.maxT > (this.wm[rs] || 0)) this.wm[rs] = b.maxT;
      }
      this.buckets = {};
      return out;
    }
    /**
     * Build the Snapshot shown by the radar.
     * @returns {*} the computed value
     */
    snapshot() {
      const s = this.tracker.snapshot();
      s.ruleset = this.rulesetInfo();
      return s;
    }
  }

  const api = { Engine, addFix };
  (g.Radar || (g.Radar = {})).engine = api;
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
})(typeof self !== 'undefined' ? self : globalThis);
