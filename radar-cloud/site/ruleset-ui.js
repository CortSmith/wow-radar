'use strict';
/**
 * ruleset-ui.js - single responsibility: the server rule-set segmented control (Auto, Normal, PvP, RP, RP-PvP).
 *
 * Each rule-set keeps its own contacts, trails and bounty ledger, so changing the active one reloads data.
 * Public API (Radar.ui.ruleset):
 *  - init(state, bus)
 *  - paint()                      reflect settings and the detected rule-set in the buttons (aria-pressed) and the
 * bounty tag
 *  - note(engineRuleset)          the engine reported its detected rule-set ({detected}); repaint and announce a change
 *  - settle()                     adopt the current rule-set as the baseline without announcing a change (after login)
 * Emits "ruleset-changed" and "engine-config" (the live engine must learn the manual choice).
 */
(function (g) {
  const radar = g.Radar,
    { $ } = radar.dom,
    VM = radar.viewModel,
    CFG = radar.config;
  const AUTO = CFG.rulesets.AUTO_ID;
  const CHOICES = [[AUTO, 'Auto']].concat(CFG.rulesets.LIST);
  let st = null,
    bus = null;

  function isChosen(id) {
    return id === AUTO ? st.settings.mode !== 'manual' : st.settings.mode === 'manual' && st.settings.ruleset === id;
  }

  /**
   * Reflect the choice in the buttons (class on, aria-pressed); the Auto button also names the ruleset it currently
   * resolves to.
   */
  function paint() {
    const active = VM.currentRuleset(st);
    for (const [id, label] of CHOICES) {
      const button = $('rs-' + id),
        on = isChosen(id);
      button.className = on ? 'on' : '';
      button.setAttribute('aria-pressed', on ? 'true' : 'false');
      button.textContent = id === AUTO && on ? 'Auto: ' + VM.rulesetName(active) : label;
    }
    $('bounty-ruleset').textContent = VM.rulesetName(active);
  }

  function note(engineRuleset) {
    if (!engineRuleset) return;
    if (engineRuleset.detected && engineRuleset.detected !== st.detectedRuleset)
      st.detectedRuleset = engineRuleset.detected;
    const previous = st.lastRuleset;
    st.lastRuleset = VM.currentRuleset(st);
    paint();
    if (previous !== null && previous !== st.lastRuleset) bus.emit('ruleset-changed');
  }

  function settle() {
    st.lastRuleset = VM.currentRuleset(st);
    paint();
  }

  /**
   * Operator picked a ruleset: store it, tell the live engine, persist, and announce the change once.
   * @param {string} id element id or map id
   */
  function select(id) {
    if (id === AUTO) st.settings.mode = 'auto';
    else {
      st.settings.mode = 'manual';
      st.settings.ruleset = id;
    }
    bus.emit('engine-config');
    radar.ui.settings.saveSettings();
    settle();
    bus.emit('ruleset-changed');
  }

  /**
   * Bind the module to the shared state (and bus) and attach its DOM listeners.
   * @param {Object} state shared UI state
   * @param {Object} eventBus event bus
   */
  function init(state, eventBus) {
    st = state;
    bus = eventBus;
    for (const [id] of CHOICES) $('rs-' + id).addEventListener('click', () => select(id));
    paint();
  }

  (radar.ui || (radar.ui = {})).ruleset = { init, paint, note, settle };
})(typeof self !== 'undefined' ? self : globalThis);
