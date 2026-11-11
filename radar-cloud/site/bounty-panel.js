'use strict';
/**
 * bounty-panel.js - single responsibility: the bounty leaderboard (killing blows of hostile players against friendly
 * ones)
 * in the side panel.
 *
 * Public API (Radar.ui.bountyPanel):
 *  - init(state, bus)
 *  - load() -> Promise    GET /api/bounty for the current rule-set and render it (stale answers are dropped via a
 * sequence number)
 *  - render()             repaint from state.bounty
 *  - reset()              empty the board (rule-set change)
 * Clicking a row follows that contact (search.pickContact) and fetches its detail line. Fetch failures are
 * non-critical:
 * the board keeps what it had.
 */
(function (g) {
  const radar = g.Radar,
    { $, el, replaceChildren } = radar.dom,
    VM = radar.viewModel,
    F = radar.format,
    M = radar.messages,
    CFG = radar.config;
  let st = null,
    bus = null,
    loadSeq = 0;

  /**
   * One leaderboard row; texts are set with textContent because names come from combat logs.
   * @param {number} r row or response
   * @returns {*} the computed value
   */
  function buildRow(r) {
    const d = VM.describeBounty(r);
    const li = el(
      'li',
      { className: st.track && st.track.guid === r.guid ? 'selected' : '', attrs: { title: d.title } },
      [
        el('span', { className: 'rank', text: d.rank }),
        el('span', { className: 'name', text: d.name }),
        el('span', { className: 'bounty-value', text: d.score }),
        el('span', { className: 'meta', text: d.meta }),
      ],
    );
    li.addEventListener('click', () => pick(r));
    return li;
  }

  /**
   * Repaint this panel from the shared state.
   */
  function render() {
    const rows = st.bounty.rows;
    $('bounty-count').textContent = rows.length ? '(' + rows.length + ' ranked)' : '';
    const items = rows.length
      ? rows.slice(0, CFG.limits.BOUNTY_ROWS).map(buildRow)
      : [el('li', { className: 'empty-row', text: M.noBounties })];
    replaceChildren($('bounty-list'), items);
  }

  async function showDetail(r) {
    try {
      const d = await radar.session.api('/api/bounty/player?' + F.qs({ rs: VM.currentRuleset(st), guid: r.guid }));
      $('bounty-detail').textContent = VM.describeBountyDetail(d, r);
      $('bounty-detail').hidden = false;
    } catch (e) {
      /* non-critical: the row itself already shows the numbers */
    }
  }
  function pick(r) {
    $('search-input').value = r.name;
    $('search-clear').hidden = false;
    radar.ui.search.pickContact({ guid: r.guid, name: r.name, reaction: 'hostile' });
    render();
    showDetail(r);
  }

  /**
   * Fetch the board for the current ruleset; an answer for an older request is dropped so a slow reply cannot
   * overwrite a newer board.
   * @returns {Promise<void>} resolves when done
   */
  async function load() {
    if (!radar.session.isSignedIn()) return;
    const seq = ++loadSeq;
    try {
      const d = await radar.session.api('/api/bounty?' + F.qs({ rs: VM.currentRuleset(st) }));
      if (seq !== loadSeq) return;
      st.bounty = radar.contacts.rankBounty(d.entries);
      render();
      if (st.snap) {
        bus.emit('list');
        bus.emit('draw');
      }
    } catch (e) {
      /* non-critical: keep the previous board */
    }
  }

  /**
   * Forget accumulated state so the next cycle starts from scratch.
   */
  function reset() {
    st.bounty = { rows: [], by: {} };
    render();
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

  (radar.ui || (radar.ui = {})).bountyPanel = { init, load, render, reset };
})(typeof self !== 'undefined' ? self : globalThis);
