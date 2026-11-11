'use strict';
/**
 * settings-ui.js - single responsibility: the console controls (checkboxes, range, upload interval), chart calibration
 * keys, and persisting those settings (localStorage immediately, PUT /api/settings debounced).
 *
 * Public API (Radar.ui.settings):
 *  - init(state, bus)            load local settings, bind the controls and calibration keys, paint the controls
 *  - applyServerSettings(remote) merge settings received from GET /api/me, repaint the controls
 *  - setFlag(flag, value)        change a checkbox from code (also persists); flags: follow, stale, bg, fit,
 * showBlue, showRed
 *  - setRange(yards)             change the radar range (also persists)
 *  - saveSettings()              persist now (local) and later (server)
 *  - calibrate(c)                store the calibration of the viewed chart and redraw
 *  - setTimezone(name)           choose the display time zone ('' = browser default); repaints every time, persists
 * Stored pref names (follow, stale, bg, fit, showBlue, showRed, range) and the settings shape are part of stored user
 * data and therefore keep their historic names. settings.timezone is the operator's IANA zone (absent: browser zone);
 * state.zone is the resolved zone every view reads. Emits "draw", "list", "sector", "zone-changed".
 */
(function (g) {
  const radar = g.Radar,
    { $ } = radar.dom,
    CFG = radar.config,
    store = radar.adapters.storage;
  const PREF_PREFIX = 'nr.',
    SETTINGS_KEY = 'settings';
  const FLAG_CONTROLS = {
    follow: 'follow',
    stale: 'stale',
    bg: 'show-chart',
    fit: 'fixed-chart',
    showBlue: 'show-blue',
    showRed: 'show-red',
  };
  const SECONDS = CFG.pingInterval;
  let st = null,
    bus = null,
    saveTimer = null;

  const readPref = (key, fallback) => store.read(PREF_PREFIX + key, fallback);
  const writePref = (key, value) => store.write(PREF_PREFIX + key, value);

  /**
   * Merge the settings saved in this browser into the state; corrupt JSON keeps the defaults.
   */
  function loadLocalSettings() {
    try {
      const saved = JSON.parse(readPref(SETTINGS_KEY, ''));
      if (saved && typeof saved === 'object') Object.assign(st.settings, saved);
    } catch (e) {
      /* no or corrupt saved settings: keep defaults */
    }
    st.settings.cal = st.settings.cal || {};
    st.settings.prefs = st.settings.prefs || {};
  }

  function pushToServer() {
    saveTimer = null;
    const body = JSON.stringify(radar.consoleSettings.compactSettings(st.settings));
    // Best effort: the local copy is already saved, so a failed sync must not interrupt the operator.
    radar.session.api('/api/settings', { method: 'PUT', headers: radar.http.JSON_HEADERS, body }).catch(() => {});
  }
  function saveSettings() {
    writePref(SETTINGS_KEY, JSON.stringify(radar.consoleSettings.compactSettings(st.settings)));
    if (!radar.session.isSignedIn()) return;
    if (saveTimer) clearTimeout(saveTimer);
    saveTimer = setTimeout(pushToServer, CFG.timing.SETTINGS_SAVE_DEBOUNCE_MS);
  }

  function setPref(key, value) {
    st.settings.prefs[key] = value;
    writePref(key, typeof value === 'boolean' ? (value ? '1' : '0') : String(value));
    saveSettings();
  }

  function showRange(yards) {
    $('range').value = yards;
    $('range-value').textContent = $('range').value;
    st.rangeYd = Number($('range').value);
  }
  /**
   * Paint stored preferences into the controls and the state flags (server value, else browser value, else default).
   */
  function applyPrefs() {
    for (const flag of Object.keys(FLAG_CONTROLS)) {
      const stored = st.settings.prefs[flag];
      st.flags[flag] = !!(stored !== undefined ? stored : readPref(flag, '1') === '1');
      $(FLAG_CONTROLS[flag]).checked = st.flags[flag];
    }
    const range = st.settings.prefs.range;
    showRange(range !== undefined ? range : Number(readPref('range', String(CFG.geometry.RANGE_DEFAULT_YD))));
    $('ping-interval').value = st.settings.uploadSec || SECONDS.DEFAULT_SEC;
  }

  // ---------- display time zone ----------
  /**
   * Resolve state.zone from settings.timezone (an unknown or missing name falls back to the browser's zone).
   * @returns {boolean} true when the resolved zone changed
   */
  function resolveZone() {
    const before = st.zone;
    st.zone = radar.timeZone.effectiveZone(st.settings.timezone);
    return st.zone !== before;
  }

  /**
   * Fill the zone picker: a browser-default entry, then 'UTC' and every zone Intl knows, as a <datalist> behind the
   * searchable input.
   */
  function fillZoneChoices() {
    const list = $('timezone-choices');
    const options = radar.timeZone.supportedZones().map(name => radar.dom.el('option', { attrs: { value: name } }));
    radar.dom.replaceChildren(list, options);
    $('timezone-input').placeholder = radar.messages.timezoneBrowserDefault + ' (' + radar.timeZone.hostZone() + ')';
  }

  function paintZone() {
    $('timezone-input').value = st.settings.timezone || '';
    $('timezone-input').title = radar.timeZone.formatInZone(Date.now(), st.zone);
  }

  /**
   * Choose the display zone from code or from the picker. Invalid names are ignored and the field is repainted.
   * @param {string} name IANA zone name; '' returns to the browser default
   */
  function setTimezone(name) {
    const chosen = String(name || '').trim();
    if (chosen && !radar.timeZone.isValidZone(chosen)) {
      paintZone();
      return;
    }
    if (chosen) st.settings.timezone = chosen;
    else delete st.settings.timezone;
    resolveZone();
    paintZone();
    saveSettings();
    bus.emit('zone-changed');
  }

  function setFlag(flag, value) {
    st.flags[flag] = value;
    $(FLAG_CONTROLS[flag]).checked = value;
    setPref(flag, value);
  }
  function setRange(yards) {
    showRange(yards);
    setPref('range', st.rangeYd);
  }

  function onIntervalChange() {
    const input = $('ping-interval');
    st.settings.uploadSec = Math.max(
      SECONDS.MIN_SEC,
      Math.min(SECONDS.MAX_SEC, Number(input.value) || SECONDS.DEFAULT_SEC),
    );
    input.value = st.settings.uploadSec;
    saveSettings();
  }

  /**
   * Wire the checkboxes, range and interval input; filter changes also re-aim the sweep.
   */
  function bindControls() {
    for (const flag of Object.keys(FLAG_CONTROLS)) {
      $(FLAG_CONTROLS[flag]).addEventListener('change', () => {
        setFlag(flag, $(FLAG_CONTROLS[flag]).checked);
        bus.emit('list');
        bus.emit('draw');
      });
    }
    $('range').addEventListener('input', () => {
      setRange(Number($('range').value));
      bus.emit('draw');
    });
    $('ping-interval').addEventListener('change', onIntervalChange);
    $('timezone-input').addEventListener('change', () => setTimezone($('timezone-input').value));
    for (const flag of ['showBlue', 'showRed', 'stale'])
      $(FLAG_CONTROLS[flag]).addEventListener('change', () => bus.emit('sector'));
  }

  function calibrate(cal) {
    st.settings.cal[radar.viewModel.viewId(st)] = cal;
    saveSettings();
    bus.emit('draw');
  }
  /**
   * Shift+arrows nudge the chart, Shift +/- scale it, Shift+0 resets (see blips.calKey); only in the fixed chart view.
   * @param {Object} e the DOM event
   */
  function onCalibrationKey(e) {
    if (
      !e.shiftKey ||
      !st.snap ||
      !radar.viewModel.viewId(st) ||
      !radar.viewModel.isFixedChartActive(st) ||
      e.target.tagName === 'INPUT'
    )
      return;
    const cal = radar.blips.calKey(radar.viewModel.calibrationOf(st), e.key);
    if (!cal) return;
    e.preventDefault();
    calibrate(cal);
  }

  /**
   * Calibration saved in this browser, used as the base when merging server settings (offline edits must survive).
   * @returns {*} the computed value
   */
  function readLocalCalibration() {
    try {
      return JSON.parse(readPref(SETTINGS_KEY, '{}') || '{}').cal;
    } catch (e) {
      return {};
    }
  }

  function applyServerSettings(remote) {
    const localCal = readLocalCalibration();
    st.settings = radar.consoleSettings.mergeSettings(st.settings, remote, localCal);
    if (!radar.parser.parseRuleset(st.settings.ruleset)) st.settings.ruleset = CFG.rulesets.DEFAULT_ID;
    applyPrefs();
    const zoneChanged = resolveZone();
    paintZone();
    if (zoneChanged) bus.emit('zone-changed');
  }

  /**
   * Bind the module to the shared state (and bus) and attach its DOM listeners.
   * @param {Object} state shared UI state
   * @param {Object} eventBus event bus
   */
  function init(state, eventBus) {
    st = state;
    bus = eventBus;
    loadLocalSettings();
    resolveZone();
    fillZoneChoices();
    bindControls();
    applyPrefs();
    paintZone();
    g.addEventListener('keydown', onCalibrationKey);
  }

  (radar.ui || (radar.ui = {})).settings = {
    init,
    applyServerSettings,
    setFlag,
    setRange,
    saveSettings,
    calibrate,
    setTimezone,
  };
})(typeof self !== 'undefined' ? self : globalThis);
