'use strict';
/**
 * operator-flow.js - single responsibility: the operator session lifecycle (enter after login/restore, log out,
 * expiry).
 *
 * Public API (Radar.ui.operatorFlow):
 *  - init(state, bus)
 *  - start()    restore a stored pass and enter, or show the login overlay
 *  - enter()    after a successful login: load settings (GET /api/me), then data and the sweep
 *  - logout()   drop the pass and every piece of operator data from memory, show the login overlay
 * A 401 from any call ends the session through logout() (registered with session.onExpired).
 */
(function (g) {
  const radar = g.Radar,
    { $ } = radar.dom,
    feedback = radar.ui.feedback;
  let st = null,
    bus = null;

  /**
   * Settings follow the operator across browsers; if the server is unreachable the local copy stays in use.
   * @returns {Promise<void>} resolves when done
   */
  async function loadServerSettings() {
    try {
      const me = await radar.session.api('/api/me');
      if (me && me.settings && typeof me.settings === 'object') radar.ui.settings.applyServerSettings(me.settings);
    } catch (e) {
      /* keep local settings */
    }
  }

  /**
   * After login or restore: show the user, load server settings, adopt the ruleset, then start loading data.
   * @returns {Promise<void>} resolves when done
   */
  async function enter() {
    st.signedIn = true;
    radar.ui.auth.hide();
    $('operator-name').textContent = radar.session.username();
    $('logout').hidden = false;
    feedback.connection('sweep');
    await loadServerSettings();
    radar.ui.ruleset.settle();
    bus.emit('engine-config');
    await radar.ui.cloudSync.begin();
  }

  /**
   * Drop the pass and every operator data held in memory, stop timers and live tailing, show the login overlay.
   */
  function logout() {
    radar.ui.tailUi.stopLive(true);
    radar.session.end();
    st.signedIn = false;
    st.snap = null;
    st.selected = null;
    st.track = null;
    st.cloudRows = [];
    st.cloudByGuid = new Map();
    st.bounty = { rows: [], by: {} };
    radar.ui.cloudSync.end();
    radar.ui.view.rebuild();
    radar.ui.auth.show();
  }

  function start() {
    radar.session.restore();
    if (radar.session.isSignedIn()) enter();
    else radar.ui.auth.show();
  }

  /**
   * Bind the module to the shared state (and bus) and attach its DOM listeners.
   * @param {Object} state shared UI state
   * @param {Object} eventBus event bus
   */
  function init(state, eventBus) {
    st = state;
    bus = eventBus;
    radar.session.onExpired(logout);
    $('logout').addEventListener('click', logout);
  }

  (radar.ui || (radar.ui = {})).operatorFlow = { init, start, enter, logout };
})(typeof self !== 'undefined' ? self : globalThis);
