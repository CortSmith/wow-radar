'use strict';
/**
 * cloud-sync.js - single responsibility: keep the radar fed from the cloud while no live tail is running.
 *
 * Two sources: a full read (GET /api/players) when the rule-set changes or the operator asks, and the sweep (GET
 * /api/poll)
 * that follows what is viewed (Sector: a contact's trail < a chart < everything) with deltas only.
 * Public API (Radar.ui.cloudSync):
 *  - init(state, bus, env)   env = {isHidden(), onVisibilityChange(fn), onUserInput(fn)}: browser events supplied by
 * app.js
 *  - loadCloud() -> Promise        full read for the current rule-set; ignores the answer if the rule-set changed
 * meanwhile
 *  - refreshAll() -> Promise       full read + bounty board
 *  - begin() -> Promise            after login: refresh, then start the sweep and the housekeeping timers
 *  - end()                         stop the sweep and timers (logout)
 *  - startSweep() / pauseSweep()   used when live tailing stops / starts
 *  - sectorMaybeChanged()          restart the sweep from a full read when the viewed sector changed
 */
(function (g) {
  const radar = g.Radar,
    VM = radar.viewModel,
    F = radar.format,
    M = radar.messages,
    CFG = radar.config,
    feedback = radar.ui.feedback;
  let st = null,
    bus = null,
    env = null,
    sweeper = null,
    lastSectorKey = '',
    timers = [];

  function applyRows(d) {
    st.cloudRows = d.players || [];
    st.cloudMaps = d.maps || [];
    st.cloudByGuid = new Map();
    radar.sweep.mergeRows(st.cloudByGuid, st.cloudRows);
    st.cloudRows = Array.from(st.cloudByGuid.values());
  }
  const showCloud = () =>
    radar.ui.view.showSnapshot(radar.contacts.cloudSnapshot(st.cloudRows, st.cloudMaps, Date.now()));

  /**
   * Full read of the ruleset (GET /api/players). The answer is ignored if the ruleset changed meanwhile; failures go
   * to the footer.
   * @returns {Promise<void>} resolves when done
   */
  async function loadCloud() {
    if (!radar.session.isSignedIn()) return;
    const rs = VM.currentRuleset(st);
    try {
      const d = await radar.session.api('/api/players?' + F.qs({ rs }));
      if (rs !== VM.currentRuleset(st)) return;
      applyRows(d);
      if (sweeper) sweeper.reset();
      if (!st.live.active) {
        feedback.connection('ok');
        showCloud();
      } else if (st.flags.showHistory) bus.emit('history');
    } catch (e) {
      feedback.connection('off');
      feedback.footer(M.errorText(e));
    }
  }
  const refreshAll = () => Promise.all([loadCloud(), radar.ui.bountyPanel.load()]);

  // ---------- sweep ----------
  function onSweep(r, sector) {
    if (st.live.active) return;
    if (sector.scope === 'player') mergePlayerSweep(r, sector);
    else radar.sweep.mergeRows(st.cloudByGuid, r.players);
    st.cloudRows = Array.from(st.cloudByGuid.values());
    showCloud(); // rebuild() also refreshes the history table when it is open
  }
  /**
   * A player sweep carries the contact row and the trail fixes newer than the ones already held.
   * @param {number} r row or response
   * @param {Object} sector sweep sector
   */
  function mergePlayerSweep(r, sector) {
    if (r.player) radar.sweep.mergeRows(st.cloudByGuid, [r.player]);
    const track = st.track;
    if (!track || track.guid !== sector.guid || !r.fixes || !r.fixes.length) return;
    track.fixes = radar.sweep.mergeFixes(track.fixes, r.fixes);
    track.total = track.fixes.length;
    track.maps = radar.contacts.trackSummary(track.fixes).maps;
    radar.ui.search.renderTrailInfo();
  }

  /**
   * What the sweep should look at now; null while live tailing (the engine feeds the view) or signed out.
   */
  const currentSector = () =>
    st.live.active || !radar.session.isSignedIn() ? null : radar.sweep.pickSector(VM.sweepInput(st));

  /**
   * Replace the sweeper with a fresh one wired to the session api, the sector and the browser environment.
   */
  function startSweep() {
    if (sweeper) sweeper.stop();
    sweeper = new radar.sweep.Sweeper({
      api: radar.session.api,
      getScope: currentSector,
      onResponse: onSweep,
      onError: e => feedback.footer(M.sweepFailed(e)),
      setTimeout: (f, ms) => setTimeout(f, ms),
      clearTimeout: h => clearTimeout(h),
      now: () => Date.now(),
      random: Math.random,
      hidden: () => env.isHidden(),
      getTrackTail: () => (st.track && st.track.fixes.length ? st.track.fixes[st.track.fixes.length - 1].t : 0),
    });
    sweeper.start();
  }
  const pauseSweep = () => {
    if (sweeper) sweeper.stop();
  };

  function sectorMaybeChanged() {
    if (!sweeper) return;
    const sector = currentSector(),
      key = sector ? radar.sweep.sectorKey(sector) : '';
    if (key === lastSectorKey) return;
    lastSectorKey = key;
    sweeper.poke(true);
  }

  // ---------- session lifecycle ----------
  function end() {
    for (const t of timers) clearInterval(t);
    timers = [];
    if (sweeper) {
      sweeper.stop();
      sweeper = null;
    }
  }
  /**
   * Start a new log file: reset windows and set the ledger watermarks.
   * @returns {Promise<void>} resolves when done
   */
  async function begin() {
    await refreshAll();
    end();
    startSweep();
    timers.push(
      setInterval(() => {
        radar.ui.bountyPanel.load();
      }, CFG.timing.BOUNTY_REFRESH_MS),
    );
    timers.push(
      setInterval(() => {
        if (st.snap) bus.emit('draw');
      }, CFG.timing.REDRAW_MS),
    ); // ages and stale state advance without new data
  }

  /**
   * Bind the module to the shared state (and bus) and attach its DOM listeners.
   * @param {Object} state shared UI state
   * @param {Object} eventBus event bus
   * @param {Object} environment browser environment callbacks
   */
  function init(state, eventBus, environment) {
    st = state;
    bus = eventBus;
    env = environment;
    env.onVisibilityChange(() => {
      if (sweeper) sweeper.onVisibility();
    });
    // Any input means the operator is looking: sweep fast again, and re-aim the sweep if the viewed sector changed.
    env.onUserInput(() => {
      if (sweeper) sweeper.poke(false);
      sectorMaybeChanged();
    });
  }

  (radar.ui || (radar.ui = {})).cloudSync = {
    init,
    loadCloud,
    refreshAll,
    begin,
    end,
    startSweep,
    pauseSweep,
    sectorMaybeChanged,
  };
})(typeof self !== 'undefined' ? self : globalThis);
