'use strict';
/**
 * time-zone.js - single responsibility: every conversion between log wall-clock text, UTC instants and the operator's
 * display zone.
 *
 * Stored, uploaded and API times are always UTC instants in unix milliseconds (numbers). Local time exists only for
 * display (formatInZone) and for log lines that carry no offset (parseLogTimestamp). Pure functions of their
 * arguments; the zone is always passed in. No DOM. Loaded by the page, by worker.js (importScripts) and by node.
 *
 * Public API (Radar.timeZone):
 *  - parseLogTimestamp(text, fallbackZone) -> unix ms (NaN when malformed)
 *  - toUtcIso(unixMs) -> 'YYYY-MM-DDTHH:MM:SS.mmm+00:00'
 *  - formatInZone(unixMs, zone, opts?) -> 'YYYY-MM-DD HH:MM:SS GMT-5'   opts.label === false drops the zone label
 *  - isValidZone(name), supportedZones(), hostZone(), effectiveZone(configured), offsetMs(unixMs, zone)
 *
 * Rule for a log line WITHOUT an offset (decided once, here): its wall-clock time is read in the fallback zone.
 *  - ambiguous wall time (clocks go back, the hour happens twice): the FIRST occurrence wins (the earlier instant,
 *    the offset in force before the change);
 *  - nonexistent wall time (clocks jump forward): the offset in force BEFORE the change is applied, so the time moves
 *    forward by the length of the gap (02:30 in a 02:00->03:00 gap becomes 03:30).
 * With no fallback zone at all the wall time is read in the host's own zone (legacy behaviour of parseTime).
 */
(function (g) {
  const CFG = typeof require !== 'undefined' && typeof module !== 'undefined' ? require('./config.js') : g.Radar.config;
  const PC = CFG.parser,
    U = CFG.units,
    TZ = CFG.timeZone;
  const MS_PER_MIN = U.SEC_PER_MIN * U.MS_PER_SEC;
  const MILLIS_DIGITS = PC.MS_DIGITS;

  // ---------- the offset written in a log line ----------
  /**
   * Split a clock string "HH:MM:SS.mmm[+-H[:MM]]" into the clock and its optional UTC offset.
   * @param {string} clock text after the date
   * @returns {{clock: string, offsetSec: number|null}|null} offset in seconds east of UTC (null: none given);
   *   null when the offset is not numeric
   */
  function splitUtcOffset(clock) {
    const k = Math.max(clock.lastIndexOf('+'), clock.lastIndexOf('-'));
    if (k <= 0) return { clock, offsetSec: null };
    const zone = clock.slice(k);
    const parts = zone.slice(1).split(':');
    const hours = Number(parts[0]);
    const minutes = parts[1] ? Number(parts[1]) : 0;
    if (!isFinite(hours) || !isFinite(minutes)) return null;
    const sign = zone[0] === '-' ? -1 : 1;
    return { clock: clock.slice(0, k), offsetSec: sign * (hours * U.SEC_PER_HOUR + minutes * U.SEC_PER_MIN) };
  }

  /**
   * Whether calendar and clock fields are plausible (a leap second 60 is allowed).
   * @param {number[]} f [month, day, year, hour, minute, second]
   * @returns {boolean} true when every field is in range
   */
  function isPlausibleTime(f) {
    const [mo, dd, y, hh, mm, ss] = f;
    return (
      mo >= 1 &&
      mo <= PC.MONTHS_MAX &&
      dd >= 1 &&
      dd <= PC.DAY_MAX &&
      hh >= 0 &&
      hh <= PC.HOUR_MAX &&
      mm >= 0 &&
      mm <= PC.MINUTE_MAX &&
      ss >= 0 &&
      ss <= PC.SECOND_MAX &&
      y > 0
    );
  }

  /**
   * Split "M/D/YYYY HH:MM:SS.mmm[+-H[:MM]]" into numeric fields.
   * @param {string} s log timestamp
   * @returns {{fields: number[], ms: number, offsetSec: number|null}|null} fields = [month, day, year, hour, minute,
   *   second]; null when malformed or implausible
   */
  function readFields(s) {
    const sp = s.indexOf(' ');
    if (sp < 0) return null;
    const date = s.slice(0, sp).split('/');
    if (date.length !== PC.DATE_PARTS) return null;
    const zone = splitUtcOffset(s.slice(sp + 1));
    if (!zone) return null;
    const dot = zone.clock.indexOf('.');
    const secs = dot < 0 ? zone.clock : zone.clock.slice(0, dot);
    const frac = dot < 0 ? '' : zone.clock.slice(dot + 1);
    const hms = secs.split(':');
    if (hms.length !== PC.CLOCK_PARTS) return null;
    const fields = [+date[0], +date[1], +date[2], +hms[0], +hms[1], +hms[2]];
    if (!isPlausibleTime(fields)) return null;
    const ms = frac ? parseInt(frac.slice(0, MILLIS_DIGITS).padEnd(MILLIS_DIGITS, '0'), PC.DECIMAL_RADIX) : 0;
    return { fields, ms, offsetSec: zone.offsetSec };
  }

  // ---------- Intl access (formatters are cached per zone) ----------
  const formatters = new Map();
  const knownZones = new Set();

  /**
   * Cached formatter producing numeric wall-clock parts (and the short zone name when asked).
   * @param {string} zone IANA zone name
   * @param {boolean} withName include timeZoneName 'short'
   * @returns {Intl.DateTimeFormat} formatter; throws RangeError for an unknown zone
   */
  function formatterFor(zone, withName) {
    const key = (withName ? 'n|' : 'p|') + zone;
    let f = formatters.get(key);
    if (!f) {
      const options = {
        timeZone: zone,
        hourCycle: 'h23',
        year: 'numeric',
        month: '2-digit',
        day: '2-digit',
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit',
      };
      if (withName) options.timeZoneName = 'short';
      f = new Intl.DateTimeFormat(TZ.FORMAT_LOCALE, options);
      formatters.set(key, f);
    }
    return f;
  }

  /**
   * Whether the name is an IANA zone this runtime knows ('UTC' included).
   * @param {unknown} name candidate zone name
   * @returns {boolean} true when Intl accepts it
   */
  function isValidZone(name) {
    if (typeof name !== 'string' || !name || name.length > TZ.NAME_MAX_CHARS) return false;
    if (knownZones.has(name)) return true;
    try {
      formatterFor(name, false);
    } catch (e) {
      return false;
    }
    knownZones.add(name);
    return true;
  }

  /**
   * Wall-clock parts of an instant in a zone.
   * @param {number} unixMs UTC instant in unix ms
   * @param {string} zone valid IANA zone name
   * @param {boolean} withName include the short zone name in `name`
   * @returns {Object} {year, month, day, hour, minute, second, name} as numbers (name as text)
   */
  function wallParts(unixMs, zone, withName) {
    const out = {};
    for (const p of formatterFor(zone, withName).formatToParts(unixMs)) out[p.type] = p.value;
    return {
      year: +out.year,
      month: +out.month,
      day: +out.day,
      hour: +out.hour % U.HOURS_PER_DAY, // some engines write midnight as 24
      minute: +out.minute,
      second: +out.second,
      name: out.timeZoneName || '',
    };
  }

  /**
   * Offset of a zone from UTC at an instant.
   * @param {number} unixMs UTC instant in unix ms
   * @param {string} zone valid IANA zone name
   * @returns {number} milliseconds east of UTC (EST is -18000000)
   */
  function offsetMs(unixMs, zone) {
    const p = wallParts(unixMs, zone, false);
    const wall = Date.UTC(p.year, p.month - 1, p.day, p.hour, p.minute, p.second);
    return wall - Math.floor(unixMs / U.MS_PER_SEC) * U.MS_PER_SEC;
  }

  // ---------- wall clock in a zone -> UTC instant ----------
  /**
   * Convert a wall-clock reading (as if it were UTC) in a zone to the UTC instant, using the ambiguity and gap rule
   * documented in the file header.
   * @param {number} wallMs wall-clock fields packed with Date.UTC (not a real instant)
   * @param {string} zone valid IANA zone name
   * @returns {number} UTC instant in unix ms
   */
  function wallToInstant(wallMs, zone) {
    const before = offsetMs(wallMs - TZ.PROBE_MS, zone);
    const after = offsetMs(wallMs + TZ.PROBE_MS, zone);
    if (before === after) return wallMs - before;
    const early = wallMs - before;
    if (offsetMs(early, zone) === before) return early; // also the first of two ambiguous readings
    const late = wallMs - after;
    return offsetMs(late, zone) === after ? late : early; // neither fits: inside a gap, keep the pre-change offset
  }

  const lastResolved = new Map(); // zone -> {wallMinute, instantMinute}: log lines arrive in runs within one minute

  /**
   * wallToInstant with a one-minute memo per zone (a multi-million line log would otherwise call Intl per line).
   * @param {number} wallMs wall-clock fields packed with Date.UTC
   * @param {string} zone valid IANA zone name
   * @returns {number} UTC instant in unix ms
   */
  function resolveWall(wallMs, zone) {
    const wallMinute = Math.floor(wallMs / MS_PER_MIN) * MS_PER_MIN;
    let memo = lastResolved.get(zone);
    if (!memo || memo.wallMinute !== wallMinute) {
      memo = { wallMinute, instantMinute: wallToInstant(wallMinute, zone) };
      lastResolved.set(zone, memo);
    }
    return memo.instantMinute + (wallMs - wallMinute);
  }

  /**
   * "M/D/YYYY HH:MM:SS.mmm[+-H[:MM]]" to a UTC instant. A line's own offset always wins.
   * @param {string} text log timestamp
   * @param {string} [fallbackZone] IANA zone applied when the line has no offset (invalid names count as 'UTC'); when
   *   omitted the host zone is used
   * @returns {number} unix ms, NaN when malformed
   */
  function parseLogTimestamp(text, fallbackZone) {
    const r = readFields(text);
    if (!r) return NaN;
    const [mo, dd, y, hh, mm, ss] = r.fields;
    if (r.offsetSec !== null) return Date.UTC(y, mo - 1, dd, hh, mm, ss, r.ms) - r.offsetSec * U.MS_PER_SEC;
    if (!fallbackZone) return new Date(y, mo - 1, dd, hh, mm, ss, r.ms).getTime();
    const zone = isValidZone(fallbackZone) ? fallbackZone : TZ.FALLBACK_ZONE;
    return resolveWall(Date.UTC(y, mo - 1, dd, hh, mm, ss, r.ms), zone);
  }

  // ---------- UTC instant -> text ----------
  /**
   * ISO 8601 text of an instant with an explicit UTC offset (for display, tooltips and export; never stored).
   * @param {number} unixMs UTC instant in unix ms
   * @returns {string} 'YYYY-MM-DDTHH:MM:SS.mmm+00:00', or '' when the value is not a finite time
   */
  function toUtcIso(unixMs) {
    if (!isFinite(unixMs)) return '';
    return new Date(unixMs).toISOString().replace('Z', '+00:00');
  }

  const pad2 = n => String(n).padStart(2, '0');

  /**
   * Operator-facing time in a zone, with a short offset label.
   * @param {number} unixMs UTC instant in unix ms
   * @param {string} zone IANA zone name; an unknown or empty name is shown in 'UTC'
   * @param {{label?: boolean}} [opts] label: false omits the 'EST' / 'GMT+5:30' / 'UTC' suffix
   * @returns {string} 'YYYY-MM-DD HH:MM:SS GMT-5'; '-' when the value is not a finite time
   */
  function formatInZone(unixMs, zone, opts) {
    if (!isFinite(unixMs)) return '-';
    const p = wallParts(unixMs, isValidZone(zone) ? zone : TZ.FALLBACK_ZONE, true);
    const text = `${p.year}-${pad2(p.month)}-${pad2(p.day)} ${pad2(p.hour)}:${pad2(p.minute)}:${pad2(p.second)}`;
    return opts && opts.label === false ? text : text + ' ' + p.name;
  }

  // ---------- zone choices ----------
  /**
   * Zones to offer in the picker: every zone the runtime knows plus 'UTC'.
   * @returns {string[]} IANA names, 'UTC' first
   */
  function supportedZones() {
    const known = typeof Intl.supportedValuesOf === 'function' ? Intl.supportedValuesOf('timeZone') : [];
    return [TZ.OPTION_UTC_LABEL].concat(known.filter(name => name !== TZ.OPTION_UTC_LABEL));
  }

  /**
   * The zone this browser runs in.
   * @returns {string} IANA name from Intl, or 'UTC' when the runtime does not say
   */
  function hostZone() {
    try {
      const name = new Intl.DateTimeFormat().resolvedOptions().timeZone;
      return isValidZone(name) ? name : TZ.FALLBACK_ZONE;
    } catch (e) {
      return TZ.FALLBACK_ZONE;
    }
  }

  /**
   * The zone times are shown in: the operator's setting when valid, otherwise the host zone.
   * @param {unknown} configured settings.timezone (may be missing)
   * @returns {string} valid IANA zone name
   */
  function effectiveZone(configured) {
    return isValidZone(configured) ? configured : hostZone();
  }

  const api = {
    parseLogTimestamp,
    toUtcIso,
    formatInZone,
    isValidZone,
    supportedZones,
    hostZone,
    effectiveZone,
    offsetMs,
  };
  (g.Radar || (g.Radar = {})).timeZone = api;
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
})(typeof self !== 'undefined' ? self : globalThis);
