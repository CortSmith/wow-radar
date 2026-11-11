'use strict';
/**
 * console-settings.js - single responsibility: shrink the operator's console settings so they fit PUT /api/settings.
 *
 * Public API (Radar.consoleSettings):
 *  - defaultSettings() -> fresh Settings {mode, ruleset, cal, prefs, uploadSec}; the optional timezone (IANA name) is
 *    absent until the operator picks one, and survives compaction untouched
 *  - compactSettings(settings) -> copy with default calibrations dropped, numbers rounded, and the oldest calibrations
 *    removed until the JSON fits config.ping.SETTINGS_MAX_BYTES
 *  - mergeSettings(local, remote, localCal) -> Settings   server values win; calibration of both sides is combined
 * Calibration is also kept in localStorage by settings-ui.js as an offline fallback. No DOM.
 */
(function (g) {
  const dep = (name, file) =>
    typeof require !== 'undefined' && typeof module !== 'undefined' ? require(file) : g.Radar[name];
  const CFG = dep('config', './config.js');
  const SETTINGS_MAX = CFG.ping.SETTINGS_MAX_BYTES;
  const OFFSET_DECIMALS = 2,
    SCALE_DECIMALS = 4;

  const isIdentity = c => c.dx === 0 && c.dy === 0 && c.sx === 1 && c.sy === 1;

  /**
   * Copy of the settings fit for PUT /api/settings: identity calibrations dropped, numbers rounded, oldest
   * calibrations evicted until under the size cap.
   * @param {Object} s the s
   * @returns {*} the computed value
   */
  function compactSettings(s) {
    const out = Object.assign({}, s, { cal: {} });
    for (const k of Object.keys(s.cal || {})) {
      const c = s.cal[k];
      if (!c || isIdentity(c)) continue;
      out.cal[k] = {
        dx: +c.dx.toFixed(OFFSET_DECIMALS),
        dy: +c.dy.toFixed(OFFSET_DECIMALS),
        sx: +c.sx.toFixed(SCALE_DECIMALS),
        sy: +c.sy.toFixed(SCALE_DECIMALS),
      };
    }
    const keys = Object.keys(out.cal);
    while (JSON.stringify(out).length > SETTINGS_MAX && keys.length) delete out.cal[keys.shift()];
    return out;
  }

  const defaultSettings = () => ({
    mode: 'auto',
    ruleset: CFG.rulesets.DEFAULT_ID,
    cal: {},
    prefs: {},
    uploadSec: CFG.pingInterval.DEFAULT_SEC,
  });

  /**
   * localCal (saved in this browser) is the base so an offline calibration survives; the server's entries override it.
   * @param {Object} local local settings
   * @param {Object} remote settings from the server
   * @param {Object} localCal calibration saved in this browser
   * @returns {*} the computed value
   */
  function mergeSettings(local, remote, localCal) {
    const out = Object.assign(defaultSettings(), local, remote);
    out.cal = Object.assign({}, localCal || {}, remote.cal || {});
    out.prefs = Object.assign({}, local.prefs, remote.prefs || {});
    return out;
  }

  const api = { defaultSettings, compactSettings, mergeSettings };
  (g.Radar || (g.Radar = {})).consoleSettings = api;
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
})(typeof self !== 'undefined' ? self : globalThis);
