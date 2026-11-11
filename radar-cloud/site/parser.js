'use strict';
/**
 * parser.js - single responsibility: turn WoW combat-log text into event objects, and decode names and rule-sets.
 *
 * Port of go-radar/internal/{parser,ruleset}. Pure functions, no DOM: loaded by worker.js (importScripts), the page
 * and node.
 * Public API (Radar.parser): parseLine(line, fallbackZone) -> LogEvent|null, parseTime(text, fallbackZone) -> UTC unix
 * ms (offset and zone rules live in time-zone.js), splitCSV, kindOf, isMine, reactionOf,
 * Namer (raw "First-Last-Realm" -> display name), parseRuleset, detectFromRawName, RULESETS, DEFAULT_RS, KIND_*, ADV.
 *
 * Line layout: f[0]=event f[1..4]=src guid,name,flags,raid f[5..8]=dst; then event prefix params, then (advanced
 * logging)
 * a 19 field "info unit" block: 0 guid,1 owner,2 hp,3 maxhp,...,14 X,15 Y,16 uiMapID,17 facing,18 level.
 */
(function (g) {
  const dep = (name, file) =>
    typeof require !== 'undefined' && typeof module !== 'undefined' ? require(file) : g.Radar[name];
  const CFG = dep('config', './config.js');
  const TZ = dep('timeZone', './time-zone.js');
  const PC = CFG.parser;
  const ADV = PC.ADV_FIELDS;
  const KIND_OTHER = 0,
    KIND_PLAYER = 1,
    KIND_NPC = 2,
    KIND_PET = 3;

  function kindOf(guid) {
    if (guid.startsWith('Player-')) return KIND_PLAYER;
    if (guid.startsWith('Creature-') || guid.startsWith('Vehicle-')) return KIND_NPC;
    if (guid.startsWith('Pet-')) return KIND_PET;
    return KIND_OTHER;
  }
  const isMine = flags => (flags & 0x1) !== 0;
  function reactionOf(fl) {
    if (fl & 0x11) return 'friendly';
    if (fl & 0x40) return 'hostile';
    if (fl & 0x20) return 'neutral';
    return 'unknown';
  }

  /**
   * "M/D/YYYY HH:MM:SS.mmm[+-H[:MM]]" to unix ms (UTC instant); see time-zone.js for the offset and DST rules.
   * @param {string} s log timestamp
   * @param {string} [fallbackZone] IANA zone for lines without an offset; omitted means the browser's own zone
   * @returns {number} unix ms, NaN when malformed
   */
  const parseTime = (s, fallbackZone) => TZ.parseLogTimestamp(s, fallbackZone);

  /**
   * Read a double-quoted CSV field starting at the opening quote ("" is an escaped quote).
   * @param {string} s
   * @param {number} pos index of the opening quote
   * @returns {{value: string, next: number}|null} next = index after the closing quote; null when unterminated
   */
  function readQuoted(s, pos) {
    let value = '';
    pos++;
    while (pos < s.length) {
      const c = s[pos];
      if (c !== '"') {
        value += c;
        pos++;
        continue;
      }
      if (s[pos + 1] === '"') {
        value += '"';
        pos += 2;
        continue;
      }
      return { value, next: pos + 1 };
    }
    return null;
  }

  /**
   * Split on commas honouring double quotes.
   * @param {string} s
   * @returns {string[]|null} fields, or null when malformed
   */
  function splitCSV(s) {
    if (s.indexOf('"') < 0) return s.split(',');
    const out = [];
    let pos = 0;
    for (;;) {
      if (s[pos] === '"') {
        const quoted = readQuoted(s, pos);
        if (!quoted) return null;
        out.push(quoted.value);
        if (quoted.next >= s.length) return out;
        if (s[quoted.next] !== ',') return null;
        pos = quoted.next + 1;
        continue;
      }
      const next = s.indexOf(',', pos);
      if (next < 0) {
        out.push(s.slice(pos));
        return out;
      }
      out.push(s.slice(pos, next));
      pos = next + 1;
    }
  }

  const isEmptyGuid = x => x === '' || x === '0000000000000000' || x === 'nil';
  function parseHex(s) {
    const v = parseInt(String(s).replace(/^0[xX]/, ''), PC.HEX_RADIX);
    return isFinite(v) ? v >>> 0 : 0;
  }
  function unitAt(f, i) {
    let guid = f[i],
      name = f[i + 1];
    if (isEmptyGuid(guid)) guid = '';
    if (name === 'nil') name = '';
    return { guid, name, flags: parseHex(f[i + 2]) };
  }
  /**
   * Number of event-prefix parameters before the info block (swing 0, environmental 1, spell/range/shield 3); -1 for
   * events without a known prefix.
   * @param {number} t time in unix ms
   * @returns {*} the computed value
   */
  function prefixLen(t) {
    if (t.startsWith('SWING_')) return PC.PREFIX_NONE;
    if (t.startsWith('ENVIRONMENTAL_')) return PC.PREFIX_ENVIRONMENTAL;
    if (
      t.startsWith('SPELL_') ||
      t.startsWith('RANGE_') ||
      t === 'DAMAGE_SHIELD' ||
      t === 'DAMAGE_SPLIT' ||
      t === 'DAMAGE_SHIELD_MISSED'
    )
      return PC.PREFIX_SPELL;
    return -1;
  }
  function isDamage(t) {
    if (t === 'DAMAGE_SHIELD' || t === 'DAMAGE_SPLIT') return true;
    return t.endsWith('_DAMAGE') && !t.endsWith('_LANDED');
  }
  /**
   * Decode the advanced-logging info block starting at s (hp, owner, position in world yards, map id, level); null
   * when it is not a unit block.
   * @param {Object} f fix
   * @param {Object} s the s
   * @returns {*} the computed value
   */
  function parseInfo(f, s) {
    if (s + ADV > f.length || kindOf(f[s]) === KIND_OTHER) return null;
    const in_ = { guid: f[s], owner: '', hp: 0, maxHp: 0, hasHp: false, x: 0, y: 0, mapId: 0, level: 0, hasPos: false };
    if (!isEmptyGuid(f[s + 1])) in_.owner = f[s + 1];
    const hp = parseFloat(f[s + PC.INFO_HP]),
      mx = parseFloat(f[s + PC.INFO_MAX_HP]);
    if (isFinite(hp) && isFinite(mx) && f[s + PC.INFO_HP] !== '' && f[s + PC.INFO_MAX_HP] !== '') {
      in_.hp = hp;
      in_.maxHp = mx;
      in_.hasHp = true;
    }
    const x = parseFloat(f[s + PC.INFO_X]),
      y = parseFloat(f[s + PC.INFO_Y]),
      mp = Number(f[s + PC.INFO_MAP]);
    if (isFinite(x) && isFinite(y) && f[s + PC.INFO_MAP] !== '' && Number.isInteger(mp)) {
      in_.x = x;
      in_.y = y;
      in_.mapId = mp;
      in_.hasPos = true;
    }
    in_.level = parseInt(f[s + PC.INFO_LEVEL], PC.DECIMAL_RADIX) || 0;
    return in_;
  }

  /**
   * Event object with every field at its neutral value, so consumers never test for missing properties.
   * @param {number} t time in unix ms
   * @param {string} type the type
   */
  const emptyEvent = (t, type) => ({
    t,
    type,
    src: null,
    dst: null,
    info: null,
    amount: 0,
    hasAmount: false,
    isDamage: false,
    isHeal: false,
    zone: '',
    map: null,
    advanced: null,
    unconscious: false,
  });

  /**
   * MAP_CHANGE,id,name,x0,x1,y0,y1 -> the chart rectangle (corners may come in either order).
   * @param {Object} f fix
   * @returns {*} the computed value
   */
  function parseChart(f) {
    if (f.length < PC.MAP_CHANGE_FIELDS) return null;
    const id = parseInt(f[1], 10),
      a = parseFloat(f[3]),
      b = parseFloat(f[4]),
      c = parseFloat(f[5]),
      d = parseFloat(f[6]);
    if (![id, a, b, c, d].every(isFinite)) return null;
    return { id, name: f[2], x0: Math.min(a, b), x1: Math.max(a, b), y0: Math.min(c, d), y1: Math.max(c, d) };
  }

  /**
   * Events that carry no units: logging version, zone and chart changes. Returns true when ev was a meta event.
   * @param {Object} ev parsed log event
   * @param {Object} f fix
   * @returns {*} the computed value
   */
  function fillMetaEvent(ev, f) {
    switch (ev.type) {
      case 'COMBAT_LOG_VERSION':
        for (let k = 1; k + 1 < f.length; k++) if (f[k] === 'ADVANCED_LOG_ENABLED') ev.advanced = f[k + 1] === '1';
        return true;
      case 'ZONE_CHANGE':
        if (f.length > 2) ev.zone = f[2];
        return true;
      case 'MAP_CHANGE':
        ev.map = parseChart(f);
        return true;
    }
    return false;
  }

  /**
   * Events without a fixed prefix (e.g. UNIT_DIED): scan for the first info block that carries a position.
   * @param {Object} f fix
   * @returns {*} the computed value
   */
  function scanInfoWithPosition(f) {
    for (let k = PC.FIELD_PARAMS; k + ADV <= f.length; k++) {
      const n = parseInfo(f, k);
      if (n && n.hasPos) return n;
    }
    return null;
  }

  /**
   * Damage/heal amount sits right after the (optional) info block.
   * @param {Object} ev parsed log event
   * @param {Object} f fix
   * @param {Object} idx the idx
   */
  function fillAmount(ev, f, idx) {
    if (idx >= f.length || f[idx] === '') return;
    const v = Number(f[idx]);
    if (isFinite(v)) {
      ev.amount = v;
      ev.hasAmount = true;
    }
  }

  /**
   * Returns an event object or null for unparsable lines; event.t is a UTC instant in unix ms.
   * @param {string} line the line
   * @param {string} [fallbackZone] IANA zone for a timestamp without an offset (see time-zone.js)
   * @returns {*} the computed value
   */
  function parseLine(line, fallbackZone) {
    if (line.charCodeAt(line.length - 1) === PC.CARRIAGE_RETURN) line = line.slice(0, -1);
    const i = line.indexOf('  ');
    if (i < 0) return null;
    const t = parseTime(line.slice(0, i), fallbackZone);
    if (!isFinite(t)) return null;
    const f = splitCSV(line.slice(i + 2));
    if (!f || !f.length || f[0] === '') return null;
    const ev = emptyEvent(t, f[0]);
    if (fillMetaEvent(ev, f) || f.length < PC.FIELD_PARAMS) return ev;
    ev.src = unitAt(f, PC.FIELD_SRC);
    ev.dst = unitAt(f, PC.FIELD_DST);
    if (ev.type === 'UNIT_DIED' && f.length > PC.FIELD_PARAMS && f[PC.FIELD_PARAMS] === '1') ev.unconscious = true;
    const prefix = prefixLen(ev.type),
      start = PC.FIELD_PARAMS + prefix;
    ev.info = prefix >= 0 ? parseInfo(f, start) : scanInfoWithPosition(f);
    ev.isDamage = isDamage(ev.type);
    ev.isHeal = ev.type === 'SPELL_HEAL' || ev.type === 'SPELL_PERIODIC_HEAL';
    if (prefix >= 0 && (ev.isDamage || ev.isHeal)) fillAmount(ev, f, start + (ev.info ? ADV : 0));
    return ev;
  }

  // ---- Namer: raw "First-Last-PvP-US" -> "First Last"; shared realm suffix detected once >= 6 distinct names agree
  // ----
  const MIN_NAMES = CFG.parser.NAMER_MIN_NAMES;
  /**
   * Longest "-Part-Part" tail shared by all raw names (the realm), or "" when they share none.
   * @param {Array} names the names
   * @returns {*} the computed value
   */
  function commonSuffix(names) {
    if (!names.length) return '';
    const lists = names.map(n => n.split('-'));
    let shortest = Infinity;
    for (const l of lists) if (l.length < shortest) shortest = l.length;
    let best = [];
    for (let k = 1; k < shortest; k++) {
      const tail = lists[0].slice(-k);
      if (!lists.every(l => l.slice(-k).every((tk, j) => tk === tail[j]))) break;
      best = tail;
    }
    return best.length ? '-' + best.join('-') : '';
  }
  class Namer {
    /**
     * Start unlocked, or locked to a known realm suffix such as "-PvP-US".
     * @param {Object} realmSuffix the realm suffix
     */
    constructor(realmSuffix) {
      this.suffix = '';
      this.locked = false;
      this.seen = new Set();
      this.names = [];
      if (realmSuffix) {
        this.suffix = realmSuffix[0] === '-' ? realmSuffix : '-' + realmSuffix;
        this.locked = true;
      }
    }
    /**
     * Learn a raw player name; once enough agree the realm suffix is locked.
     * @param {string} raw the raw
     */
    observe(raw) {
      if (this.locked || !raw || this.seen.has(raw)) return;
      this.seen.add(raw);
      this.names.push(raw);
      if (this.names.length < MIN_NAMES) return;
      const s = commonSuffix(this.names);
      if (s) {
        this.suffix = s;
        this.locked = true;
        this.names = null;
        this.seen = null;
      }
    }
    /**
     * Display name: realm suffix removed, dashes turned into spaces.
     * @param {string} raw the raw
     * @returns {*} the computed value
     */
    display(raw) {
      if (this.suffix && raw.endsWith(this.suffix)) raw = raw.slice(0, -this.suffix.length);
      return raw.split('-').join(' ');
    }
  }

  // ---- ruleset ----
  const RULESETS = CFG.rulesets.LIST;
  const DEFAULT_RS = CFG.rulesets.DEFAULT_ID;
  /**
   * Normalise user or log spellings (rp-pvp, rp_pvp, RP PvP) to a ruleset id; "" when unknown.
   * @param {Object} s the s
   * @returns {*} the computed value
   */
  function parseRuleset(s) {
    switch (String(s).toLowerCase().trim()) {
      case 'normal':
        return 'normal';
      case 'pvp':
        return 'pvp';
      case 'rp':
        return 'rp';
      case 'rppvp':
      case 'rp-pvp':
      case 'rp_pvp':
      case 'rp pvp':
        return 'rppvp';
    }
    return '';
  }
  /**
   * Realm token of a raw name "First-Last-PvP-US": only the trailing three '-' parts after the first are considered.
   * @param {string} raw the raw
   * @returns {*} the computed value
   */
  function detectFromRawName(raw) {
    const parts = raw.split('-');
    const lo = Math.max(1, parts.length - PC.REALM_TAIL_PARTS);
    for (let i = parts.length - 1; i >= lo; i--) {
      if (i - 1 >= 1 && parts[i].toLowerCase() === 'pvp' && parts[i - 1].toLowerCase() === 'rp') return 'rppvp';
      const id = parseRuleset(parts[i]);
      if (id) return id;
    }
    return '';
  }

  const api = {
    ADV,
    KIND_OTHER,
    KIND_PLAYER,
    KIND_NPC,
    KIND_PET,
    kindOf,
    isMine,
    reactionOf,
    parseTime,
    splitCSV,
    parseLine,
    Namer,
    RULESETS,
    DEFAULT_RS,
    parseRuleset,
    detectFromRawName,
  };
  (g.Radar || (g.Radar = {})).parser = api;
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
})(typeof self !== 'undefined' ? self : globalThis);
