'use strict';
/**
 * view-controller.js - single responsibility: take a new Snapshot (live engine or cloud rows) into the state and
 * repaint.
 *
 * Public API (Radar.ui.view):
 *  - init(state, bus)
 *  - loadChartIndex() -> Promise   fetch maps/index.json (chart images); repaints once it arrives
 *  - showSnapshot(snapshot)        decorate with the chart view, adopt the rule-set the engine reports, rebuild
 *  - syncView()                    reset zoom when the viewed chart changed
 *  - rebuild()                     repaint every panel (emits "list", "header", "trail-info", "chart-input",
 * "history" when open, "draw")
 */
(function (g) {
  const radar = g.Radar,
    VM = radar.viewModel;
  const FRAME_CENTRE = radar.state.FRAME_CENTRE_PCT;
  let st = null,
    bus = null;

  /**
   * Adds snap.view: the charts known from MAP_CHANGE joined with the image index, plus the zone the game reports
   * (live only).
   * @param {Object} snap Snapshot
   * @returns {*} the computed value
   */
  function decorate(snap) {
    snap.view = radar.charts.buildView(snap.maps, st.zoneIdx);
    snap.view.zone = snap.cloud ? null : radar.charts.currentZone(snap, st.zoneIdx);
    return snap;
  }

  function syncView() {
    const id = VM.viewId(st);
    if (id === st.lastViewId) return;
    st.lastViewId = id;
    st.fz.z = 1;
    st.fz.cx = FRAME_CENTRE;
    st.fz.cy = FRAME_CENTRE;
  }

  function rebuild() {
    syncView();
    for (const event of ['list', 'header', 'trail-info', 'chart-input']) bus.emit(event);
    if (st.flags.showHistory) bus.emit('history');
    bus.emit('draw');
  }

  function showSnapshot(snap) {
    st.snap = decorate(snap);
    if (snap.ruleset) radar.ui.ruleset.note(snap.ruleset);
    const chosen = st.viewMapId;
    if (chosen !== null && !VM.chartById(st, chosen) && !(st.snap.maps && st.snap.maps[String(chosen)]))
      st.viewMapId = null;
    rebuild();
  }

  async function loadChartIndex() {
    const index = await radar.adapters.loadStaticJson('maps/index.json');
    if (!index) return;
    st.zoneIdx = index.zones || {};
    if (st.snap) rebuild();
  }

  /**
   * Bind the module to the shared state (and bus) and attach its DOM listeners.
   * @param {Object} state shared UI state
   * @param {Object} eventBus event bus
   */
  function init(state, eventBus) {
    st = state;
    bus = eventBus;
  }

  (radar.ui || (radar.ui = {})).view = { init, loadChartIndex, showSnapshot, syncView, rebuild };
})(typeof self !== 'undefined' ? self : globalThis);
