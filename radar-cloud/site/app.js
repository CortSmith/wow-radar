'use strict';
/**
 * app.js - single responsibility: wire the modules together and start the page. It contains no logic of its own.
 *
 * Public API: none (the page runs on load). Radar.app exposes {state, bus} for debugging only.
 * Wiring: modules never call each other's redraw directly; they emit events on the bus (names in state.js) and the
 * subscriptions below route them. See ARCHITECTURE.md for the UI -> module -> API map.
 */
(function (g) {
  const radar = g.Radar,
    ui = radar.ui;
  const st = radar.state.createState(),
    bus = radar.state.createBus();

  /**
   * Every timestamp on screen is derived from state.zone, so a zone change repaints them all (no reload: rebuild also
   * repaints the history table when it is open) and tells the live engine which zone to assume for log lines without an
   * offset.
   */
  function onZoneChanged() {
    ui.search.onZoneChanged();
    ui.tailUi.renderUploadSpan();
    ui.view.rebuild();
    bus.emit('engine-config');
  }

  /**
   * Subscribe each bus event to the module that reacts to it; the table is the only place the modules meet.
   */
  function routeEvents() {
    const routes = {
      draw: ui.canvas.draw,
      list: ui.contactPanel.renderList,
      header: ui.contactPanel.renderHeader,
      'trail-info': ui.search.renderTrailInfo,
      'chart-input': ui.chartBox.renderInput,
      history: ui.historyPanel.render,
      sector: ui.cloudSync.sectorMaybeChanged,
      refresh: ui.cloudSync.refreshAll,
      'load-cloud': ui.cloudSync.loadCloud,
      'engine-config': ui.tailUi.sendEngineConfig,
      'engine-ruleset': ui.ruleset.note,
      snapshot: ui.view.showSnapshot,
      'live-started': ui.cloudSync.pauseSweep,
      'zone-changed': onZoneChanged,
    };
    for (const event of Object.keys(routes)) bus.on(event, routes[event]);
    bus.on('ruleset-changed', () => {
      ui.bountyPanel.reset();
      ui.cloudSync.loadCloud();
      ui.bountyPanel.load();
      ui.search.onRulesetChanged();
    });
    bus.on('live-stopped', () => {
      ui.cloudSync.refreshAll();
      if (radar.session.isSignedIn()) ui.cloudSync.startSweep();
    });
  }

  /**
   * The only browser events the cloud sync needs; supplied here so cloud-sync.js stays DOM-free.
   * @returns {*} the computed value
   */
  function browserEnvironment() {
    return {
      isHidden: () => g.document.hidden,
      onVisibilityChange: fn => g.document.addEventListener('visibilitychange', fn),
      onUserInput: fn => {
        for (const type of ['mousedown', 'keydown', 'wheel']) g.addEventListener(type, fn, { passive: true });
      },
    };
  }

  /**
   * Initialise controllers in dependency order (settings and rule-set first, the canvas last, so the first draw sees
   * final state).
   */
  function initModules() {
    ui.settings.init(st, bus);
    ui.ruleset.init(st, bus);
    for (const name of [
      'view',
      'contactPanel',
      'search',
      'bountyPanel',
      'historyPanel',
      'chartBox',
      'canvasInput',
      'tailUi',
      'operatorFlow',
    ])
      ui[name].init(st, bus);
    ui.cloudSync.init(st, bus, browserEnvironment());
    ui.canvas.init(st);
    ui.auth.init({ onSignedIn: ui.operatorFlow.enter });
  }

  routeEvents();
  initModules();
  ui.view.loadChartIndex();
  ui.ads.load();
  ui.operatorFlow.start();

  radar.app = { state: st, bus };
})(typeof self !== 'undefined' ? self : globalThis);
