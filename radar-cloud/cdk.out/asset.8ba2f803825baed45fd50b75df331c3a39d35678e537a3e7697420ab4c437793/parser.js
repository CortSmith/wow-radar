"use strict";
// Port of go-radar/internal/{parser,ruleset}. Pure functions: loaded by worker.js (importScripts) and by node (require).
// Layout: f[0]=event f[1..4]=src guid,name,flags,raid f[5..8]=dst; then event prefix params, then (advanced logging)
// a 19 field "info unit" block: 0 guid,1 owner,2 hp,3 maxhp,...,14 X,15 Y,16 uiMapID,17 facing,18 level.
(function (g) {
  const ADV = 19;
  const KIND_OTHER = 0, KIND_PLAYER = 1, KIND_NPC = 2, KIND_PET = 3;

  function kindOf(guid) {
    if (guid.startsWith("Player-")) return KIND_PLAYER;
    if (guid.startsWith("Creature-") || guid.startsWith("Vehicle-")) return KIND_NPC;
    if (guid.startsWith("Pet-")) return KIND_PET;
    return KIND_OTHER;
  }
  const isMine = (flags) => (flags & 0x1) !== 0;
  function reactionOf(fl) {
    if (fl & 0x11) return "friendly";
    if (fl & 0x40) return "hostile";
    if (fl & 0x20) return "neutral";
    return "unknown";
  }

  // "M/D/YYYY HH:MM:SS.mmm[+-H[:MM]]" -> unix ms, NaN if bad (no offset = local time of the browser).
  function parseTime(s) {
    const sp = s.indexOf(" ");
    if (sp < 0) return NaN;
    const d = s.slice(0, sp).split("/");
    if (d.length !== 3) return NaN;
    let clock = s.slice(sp + 1), off = 0, has = false;
    const k = Math.max(clock.lastIndexOf("+"), clock.lastIndexOf("-"));
    if (k > 0) {
      const o = clock.slice(k);
      clock = clock.slice(0, k);
      const parts = o.slice(1).split(":");
      const h = Number(parts[0]), m = parts[1] ? Number(parts[1]) : 0;
      if (!isFinite(h) || !isFinite(m)) return NaN;
      off = (o[0] === "-" ? -1 : 1) * (h * 3600 + m * 60);
      has = true;
    }
    const dot = clock.indexOf(".");
    const secs = dot < 0 ? clock : clock.slice(0, dot), frac = dot < 0 ? "" : clock.slice(dot + 1);
    const hms = secs.split(":");
    if (hms.length !== 3) return NaN;
    const mo = +d[0], dd = +d[1], y = +d[2], hh = +hms[0], mm = +hms[1], ss = +hms[2];
    if (!(mo >= 1 && mo <= 12 && dd >= 1 && dd <= 31 && hh >= 0 && hh <= 23 && mm >= 0 && mm <= 59 && ss >= 0 && ss <= 60 && y > 0)) return NaN;
    const ms = frac ? parseInt(frac.slice(0, 3).padEnd(3, "0"), 10) : 0;
    if (has) return Date.UTC(y, mo - 1, dd, hh, mm, ss, ms) - off * 1000;
    return new Date(y, mo - 1, dd, hh, mm, ss, ms).getTime();
  }

  // Splits on commas honouring double quotes ("" escapes a quote); null when malformed.
  function splitCSV(s) {
    if (s.indexOf('"') < 0) return s.split(",");
    const out = [];
    let pos = 0;
    for (;;) {
      if (pos < s.length && s[pos] === '"') {
        let b = "", closed = false;
        pos++;
        while (pos < s.length) {
          const c = s[pos];
          if (c === '"') {
            if (s[pos + 1] === '"') { b += '"'; pos += 2; continue; }
            pos++; closed = true; break;
          }
          b += c; pos++;
        }
        if (!closed) return null;
        out.push(b);
        if (pos >= s.length) return out;
        if (s[pos] !== ",") return null;
        pos++;
        continue;
      }
      const next = s.indexOf(",", pos);
      if (next < 0) { out.push(s.slice(pos)); return out; }
      out.push(s.slice(pos, next));
      pos = next + 1;
    }
  }

  const isEmptyGuid = (x) => x === "" || x === "0000000000000000" || x === "nil";
  function parseHex(s) { const v = parseInt(String(s).replace(/^0[xX]/, ""), 16); return isFinite(v) ? v >>> 0 : 0; }
  function unitAt(f, i) {
    let guid = f[i], name = f[i + 1];
    if (isEmptyGuid(guid)) guid = "";
    if (name === "nil") name = "";
    return { guid, name, flags: parseHex(f[i + 2]) };
  }
  function prefixLen(t) {
    if (t.startsWith("SWING_")) return 0;
    if (t.startsWith("ENVIRONMENTAL_")) return 1;
    if (t.startsWith("SPELL_") || t.startsWith("RANGE_") || t === "DAMAGE_SHIELD" || t === "DAMAGE_SPLIT" || t === "DAMAGE_SHIELD_MISSED") return 3;
    return -1;
  }
  function isDamage(t) {
    if (t === "DAMAGE_SHIELD" || t === "DAMAGE_SPLIT") return true;
    return t.endsWith("_DAMAGE") && !t.endsWith("_LANDED");
  }
  function parseInfo(f, s) {
    if (s + ADV > f.length || kindOf(f[s]) === KIND_OTHER) return null;
    const in_ = { guid: f[s], owner: "", hp: 0, maxHp: 0, hasHp: false, x: 0, y: 0, mapId: 0, level: 0, hasPos: false };
    if (!isEmptyGuid(f[s + 1])) in_.owner = f[s + 1];
    const hp = parseFloat(f[s + 2]), mx = parseFloat(f[s + 3]);
    if (isFinite(hp) && isFinite(mx) && f[s + 2] !== "" && f[s + 3] !== "") { in_.hp = hp; in_.maxHp = mx; in_.hasHp = true; }
    const x = parseFloat(f[s + 14]), y = parseFloat(f[s + 15]), mp = Number(f[s + 16]);
    if (isFinite(x) && isFinite(y) && f[s + 16] !== "" && Number.isInteger(mp)) { in_.x = x; in_.y = y; in_.mapId = mp; in_.hasPos = true; }
    in_.level = parseInt(f[s + 18], 10) || 0;
    return in_;
  }

  // Returns an event object or null for unparsable lines.
  function parseLine(line) {
    if (line.charCodeAt(line.length - 1) === 13) line = line.slice(0, -1);
    const i = line.indexOf("  ");
    if (i < 0) return null;
    const t = parseTime(line.slice(0, i));
    if (!isFinite(t)) return null;
    const f = splitCSV(line.slice(i + 2));
    if (!f || !f.length || f[0] === "") return null;
    const ev = { t, type: f[0], src: null, dst: null, info: null, amount: 0, hasAmount: false, isDamage: false, isHeal: false, zone: "", map: null, advanced: null, unconscious: false };
    switch (ev.type) {
      case "COMBAT_LOG_VERSION":
        for (let k = 1; k + 1 < f.length; k++) if (f[k] === "ADVANCED_LOG_ENABLED") ev.advanced = f[k + 1] === "1";
        return ev;
      case "ZONE_CHANGE":
        if (f.length > 2) ev.zone = f[2];
        return ev;
      case "MAP_CHANGE":
        if (f.length >= 7) {
          const id = parseInt(f[1], 10), a = parseFloat(f[3]), b = parseFloat(f[4]), c = parseFloat(f[5]), d = parseFloat(f[6]);
          if ([id, a, b, c, d].every(isFinite)) ev.map = { id, name: f[2], x0: Math.min(a, b), x1: Math.max(a, b), y0: Math.min(c, d), y1: Math.max(c, d) };
        }
        return ev;
    }
    if (f.length < 9) return ev;
    ev.src = unitAt(f, 1);
    ev.dst = unitAt(f, 5);
    if (ev.type === "UNIT_DIED" && f.length > 9 && f[9] === "1") ev.unconscious = true;
    const prefix = prefixLen(ev.type), start = 9 + prefix;
    if (prefix >= 0) ev.info = parseInfo(f, start);
    else {
      for (let k = 9; k + ADV <= f.length; k++) {
        const n = parseInfo(f, k);
        if (n && n.hasPos) { ev.info = n; break; }
      }
    }
    ev.isDamage = isDamage(ev.type);
    ev.isHeal = ev.type === "SPELL_HEAL" || ev.type === "SPELL_PERIODIC_HEAL";
    if (prefix >= 0 && (ev.isDamage || ev.isHeal)) {
      const idx = start + (ev.info ? ADV : 0);
      if (idx < f.length && f[idx] !== "") {
        const v = Number(f[idx]);
        if (isFinite(v)) { ev.amount = v; ev.hasAmount = true; }
      }
    }
    return ev;
  }

  // ---- Namer: raw "First-Last-PvP-US" -> "First Last"; shared realm suffix detected once >= 6 distinct names agree ----
  const MIN_NAMES = 6;
  function commonSuffix(names) {
    if (!names.length) return "";
    const lists = names.map((n) => n.split("-"));
    let shortest = Infinity;
    for (const l of lists) if (l.length < shortest) shortest = l.length;
    let best = [];
    for (let k = 1; k < shortest; k++) {
      const tail = lists[0].slice(-k);
      if (!lists.every((l) => l.slice(-k).every((tk, j) => tk === tail[j]))) break;
      best = tail;
    }
    return best.length ? "-" + best.join("-") : "";
  }
  class Namer {
    constructor(realmSuffix) {
      this.suffix = ""; this.locked = false; this.seen = new Set(); this.names = [];
      if (realmSuffix) { this.suffix = realmSuffix[0] === "-" ? realmSuffix : "-" + realmSuffix; this.locked = true; }
    }
    observe(raw) {
      if (this.locked || !raw || this.seen.has(raw)) return;
      this.seen.add(raw); this.names.push(raw);
      if (this.names.length < MIN_NAMES) return;
      const s = commonSuffix(this.names);
      if (s) { this.suffix = s; this.locked = true; this.names = null; this.seen = null; }
    }
    display(raw) {
      if (this.suffix && raw.endsWith(this.suffix)) raw = raw.slice(0, -this.suffix.length);
      return raw.split("-").join(" ");
    }
  }

  // ---- ruleset ----
  const RULESETS = [["normal", "Normal"], ["pvp", "PvP"], ["rp", "RP"], ["rppvp", "RP-PvP"]];
  const DEFAULT_RS = "pvp";
  function parseRuleset(s) {
    switch (String(s).toLowerCase().trim()) {
      case "normal": return "normal";
      case "pvp": return "pvp";
      case "rp": return "rp";
      case "rppvp": case "rp-pvp": case "rp_pvp": case "rp pvp": return "rppvp";
    }
    return "";
  }
  // Realm token of a raw name "First-Last-PvP-US": only the trailing three '-' parts after the first are considered.
  function detectFromRawName(raw) {
    const parts = raw.split("-");
    const lo = Math.max(1, parts.length - 3);
    for (let i = parts.length - 1; i >= lo; i--) {
      if (i - 1 >= 1 && parts[i].toLowerCase() === "pvp" && parts[i - 1].toLowerCase() === "rp") return "rppvp";
      const id = parseRuleset(parts[i]);
      if (id) return id;
    }
    return "";
  }

  const api = { ADV, KIND_OTHER, KIND_PLAYER, KIND_NPC, KIND_PET, kindOf, isMine, reactionOf, parseTime, splitCSV, parseLine, Namer, RULESETS, DEFAULT_RS, parseRuleset, detectFromRawName };
  g.RadarParser = api;
  if (typeof module !== "undefined" && module.exports) module.exports = api;
})(typeof self !== "undefined" ? self : globalThis);
