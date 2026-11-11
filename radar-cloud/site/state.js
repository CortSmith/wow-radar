'use strict';
/**
 * state.js - single responsibility: the one shared, mutable UI state object and the event bus between UI modules.
 *
 * Why a bus: modules such as the canvas, the contact list and the search all need to trigger each other's redraw.
 * Emitting
 * a named event instead of importing each other keeps the dependency graph acyclic (app.js subscribes everything).
 * Public API (Radar.state):
 *  - createState() -> State (see the field comments below; shape documented as State in types.js)
 *  - createBus() -> {on(event, fn), emit(event, ...args)}
 * Events: "draw", "list", "header", "trail-info", "chart-input", "bounty", "history", "rebuild", "sector", "refresh",
 *         "zone-changed" (the display time zone changed: repaint every timestamp), "ruleset-changed" (emitted by the module that owns the change; handled in app.js). No DOM.
 */
(function (g) {
  const dep = (name, file) =>
    typeof require !== 'undefined' && typeof module !== 'undefined' ? require(file) : g.Radar[name];
  const CFG = dep('config', './config.js');
  const { Outbox } = dep('ledger', './ledger.js');
  const { defaultSettings } = dep('consoleSettings', './console-settings.js');

  const FRAME_CENTRE_PCT = CFG.units.PCT_FULL / 2;

  /** Fresh state. Only controllers write to it; view-model.js and canvas.js read it. */
  function createState() {
    return {
      signedIn: false,
      snap: null, // current Snapshot: live engine or cloud rows, plus snap.view (charts)
      zoneIdx: {}, // normalised chart name -> {file,w,h,revealed} from maps/index.json
      cloudRows: [],
      cloudMaps: [],
      cloudByGuid: new Map(),
      selected: null, // guid highlighted in the list and on the canvas
      track: null, // searched contact + its trail {guid,name,reaction,total,maps,fixes}
      bounty: { rows: [], by: {} },
      viewMapId: null, // chart chosen by the operator; null = follow the game / busiest chart
      lastViewId: null,
      cam: { x: 0, y: 0 }, // free-mode camera centre (world yards)
      fz: { z: 1, cx: FRAME_CENTRE_PCT, cy: FRAME_CENTRE_PCT }, // fixed-chart zoom and centre (frame percent)
      detectedRuleset: null, // rule-set the engine detected from the character's realm
      lastRuleset: null,
      flags: { follow: true, stale: true, bg: true, fit: true, showBlue: true, showRed: true, showHistory: false },
      rangeYd: CFG.geometry.RANGE_DEFAULT_YD,
      settings: defaultSettings(), // persisted console settings; settings.timezone is the operator's chosen IANA zone
      zone: CFG.timeZone.FALLBACK_ZONE, // zone every time is displayed in (settings-ui.js resolves it from settings)
      uploadSpan: null, // {firstT, lastT} UTC unix ms of the last uploaded log, or null
      live: { active: false, fileName: '' },
      outbox: new Outbox(),
    };
  }

  /**
   * Minimal synchronous event bus; handlers run in subscription order and unknown events are ignored.
   * @returns {*} the computed value
   */
  function createBus() {
    const handlers = new Map();
    return {
      on(event, fn) {
        if (!handlers.has(event)) handlers.set(event, []);
        handlers.get(event).push(fn);
      },
      emit(event, ...args) {
        for (const fn of handlers.get(event) || []) fn(...args);
      },
    };
  }

  const api = { createState, createBus, FRAME_CENTRE_PCT };
  (g.Radar || (g.Radar = {})).state = api;
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
})(typeof self !== 'undefined' ? self : globalThis);
