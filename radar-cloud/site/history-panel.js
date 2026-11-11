'use strict';
/**
 * history-panel.js - single responsibility: the optional stored-history table below the radar (toggle: checkbox or
 * key H).
 *
 * Public API (Radar.ui.historyPanel):
 *  - init(state, bus)
 *  - render()     table of the cloud rows, newest first, at most config.limits.HISTORY_ROWS
 *  - set(on)      show or hide the panel; showing it renders and asks for fresh rows ("load-cloud")
 * Cell texts are written with textContent: names come from combat logs.
 */
(function (g) {
  const radar = g.Radar,
    { $, el, replaceChildren } = radar.dom,
    F = radar.format,
    CFG = radar.config;
  let st = null,
    bus = null;

  /**
   * One table row of the history columns (see contacts.historyRow); all cells are text. The Last seen cell shows the
   * operator's zone and carries the ISO UTC instant (+00:00) as its title.
   * @param {Object} contact the contact
   * @returns {*} the computed value
   */
  function buildRow(contact) {
    const h = radar.contacts.historyRow(contact);
    const lastSeen = radar.viewModel.describeTimestamp(st, h.lastSeen);
    const plain = [h.name, F.num(h.avgDmgHit), F.num(h.avgDmgEvent), F.num(h.avgHp), F.num(h.maxHp), h.samples];
    const cells = plain.map(text => el('td', { text: String(text) }));
    cells.push(el('td', { text: lastSeen.text, attrs: { title: lastSeen.title } }));
    cells.push(el('td', { text: String(h.zone) }));
    return el('tr', {}, cells);
  }

  /**
   * Repaint this panel from the shared state.
   */
  function render() {
    const rows = st.cloudRows.slice().sort((a, b) => b.lastSeen - a.lastSeen);
    $('history-count').textContent = rows.length + ' targets';
    replaceChildren($('history-table').tBodies[0], rows.slice(0, CFG.limits.HISTORY_ROWS).map(buildRow));
  }

  /**
   * Show or hide the panel; showing it renders now and asks for fresh rows. Resizes the canvas afterwards because the
   * radar height changes.
   * @param {boolean} on show or hide
   */
  function set(on) {
    st.flags.showHistory = on;
    $('show-history').checked = on;
    $('history-panel').hidden = !on;
    if (on) {
      render();
      bus.emit('load-cloud');
    }
    setTimeout(() => radar.ui.canvas.resize(), 0); // the panel changes the radar's height
  }

  function isTypingTarget(e) {
    return e.target.tagName === 'INPUT';
  }

  /**
   * Bind the module to the shared state (and bus) and attach its DOM listeners.
   * @param {Object} state shared UI state
   * @param {Object} eventBus event bus
   */
  function init(state, eventBus) {
    st = state;
    bus = eventBus;
    $('show-history').checked = false;
    $('show-history').addEventListener('change', () => set($('show-history').checked));
    g.addEventListener('keydown', e => {
      if ((e.key === 'h' || e.key === 'H') && !e.ctrlKey && !e.altKey && !e.metaKey && !isTypingTarget(e))
        set(!st.flags.showHistory);
    });
  }

  (radar.ui || (radar.ui = {})).historyPanel = { init, render, set };
})(typeof self !== 'undefined' ? self : globalThis);
