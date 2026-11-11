'use strict';
/**
 * format.js - single responsibility: small pure text helpers (ages, numbers, percents, names, query strings).
 *
 * Public API (Radar.format): fmtAge(sec), num(v), fmtPct([x,y]), mb(bytes), normName(s), esc(s), qs(obj).
 * esc() is kept for tests and legacy callers; the UI never builds HTML from log data (it uses textContent).
 */
(function (g) {
  const U = (typeof require !== 'undefined' && typeof module !== 'undefined' ? require('./config.js') : g.Radar.config)
    .units;
  const SEC_PER_MIN = U.SEC_PER_MIN,
    MIN_PER_HOUR = U.MIN_PER_HOUR,
    BYTES_PER_MB = U.BYTES_PER_MB;
  const HOURS_BEFORE_DAYS = 48; // ages up to two days are shown in hours

  /** Compact age: "42s", "7m", "3h12m", "5d". @param {number} sec */
  function fmtAge(sec) {
    sec = Math.max(0, Math.round(sec));
    if (sec < SEC_PER_MIN) return sec + 's';
    const m = Math.floor(sec / SEC_PER_MIN);
    if (m < MIN_PER_HOUR) return m + 'm';
    const h = Math.floor(m / MIN_PER_HOUR);
    return h < HOURS_BEFORE_DAYS ? h + 'h' + (m % MIN_PER_HOUR) + 'm' : Math.floor(h / U.HOURS_PER_DAY) + 'd';
  }
  const ESCAPES = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' };
  const esc = s => String(s).replace(/[&<>"]/g, c => ESCAPES[c]);
  const normName = s =>
    String(s)
      .toLowerCase()
      .replace(/[^a-z0-9]/g, '');
  /** One decimal, "-" for zero/empty. */
  const num = v => (v ? (Math.round(v * U.ROUND_1DP) / U.ROUND_1DP).toString() : '-');
  const fmtPct = p => p[0].toFixed(1) + ', ' + p[1].toFixed(1);
  const mb = bytes => (bytes / BYTES_PER_MB).toFixed(1);
  /** Query string from an object; undefined, null and "" values are skipped. */
  const qs = o =>
    Object.keys(o)
      .filter(k => o[k] !== undefined && o[k] !== null && o[k] !== '')
      .map(k => encodeURIComponent(k) + '=' + encodeURIComponent(o[k]))
      .join('&');

  const api = { fmtAge, esc, normName, num, fmtPct, mb, qs };
  (g.Radar || (g.Radar = {})).format = api;
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
})(typeof self !== 'undefined' ? self : globalThis);
