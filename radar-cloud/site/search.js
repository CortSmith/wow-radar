'use strict';
/**
 * search.js - single responsibility: search stored contacts by name and follow one contact's trail.
 *
 * Flow: typing -> debounced GET /api/players?q= -> result list -> pickContact() -> GET /api/track loads the trail,
 * which the
 * canvas draws and the sweep then keeps current. Escape or the clear button drops the trail.
 * Public API (Radar.ui.search):
 *  - init(state, bus)
 *  - pickContact({guid, name, reaction})   start following a contact (also used by the bounty list)
 *  - clear()                               stop following, empty the box
 *  - renderTrailInfo()                     the text under the search box
 *  - onRulesetChanged()                    re-run the query and reload the trail for the new rule-set
 *  - onZoneChanged()                       repaint the times of the results and the trail text in the new zone
 * Search and trail failures are non-critical: the list stays empty / the trail is dropped, nothing else is affected.
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
    searchTimer = null,
    searchSeq = 0,
    shownRows = [];

  /**
   * Show the trail summary text under the search box.
   */
  function renderTrailInfo() {
    const text = VM.describeTrail(st),
      box = $('trail-info');
    box.textContent = text === null ? '' : text;
    box.hidden = text === null;
  }

  /**
   * Result rows come from the server: build them with textContent only.
   * @param {number} r row or response
   * @returns {*} the computed value
   */
  function buildResultRow(r) {
    const swatch = el('span', { className: 'swatch' });
    swatch.style.background = radar.blips.COLORS[r.reaction] || radar.blips.COLORS.unknown;
    const seen = VM.describeTimestamp(st, r.lastSeen);
    const meta = el('span', {
      className: 'meta',
      text: (r.fixes || 0) + ' fixes, ' + seen.text,
      attrs: { title: seen.title },
    });
    const li = el('li', {}, [swatch, el('span', { className: 'name', text: r.name }), meta]);
    li.addEventListener('click', () => pickContact(r));
    return li;
  }
  /**
   * Show search results (or the empty row).
   * @param {Array} rows rows
   */
  function renderResults(rows) {
    shownRows = rows;
    const items = rows.length
      ? rows.map(buildResultRow)
      : [el('li', { className: 'empty-row', text: M.noMatchingContacts })];
    replaceChildren($('search-results'), items);
    $('search-results').hidden = false;
  }

  async function runSearch(query) {
    const seq = ++searchSeq;
    try {
      const d = await radar.session.api('/api/players?' + F.qs({ rs: VM.currentRuleset(st), q: query }));
      if (seq === searchSeq) renderResults((d.players || []).slice(0, CFG.limits.SEARCH_RESULTS));
    } catch (e) {
      /* non-critical: keep the previous results */
    }
  }

  /**
   * After the trail arrives: show the chart the contact was last seen on if the current one has none of its fixes.
   * @param {Object} d response data
   * @param {boolean} switchMap the switch map
   */
  function adoptTrail(d, switchMap) {
    const fixes = (d.fixes || []).slice().sort((a, b) => a.t - b.t);
    const sum = radar.contacts.trackSummary(fixes);
    Object.assign(st.track, { fixes, total: d.total !== undefined ? d.total : sum.total, maps: d.maps || sum.maps });
    if (fixes.length && (switchMap || st.viewMapId === null) && !VM.trailFixesOnView(st).length) {
      const lastMap = fixes[fixes.length - 1].mapId;
      if (st.snap && st.snap.maps && st.snap.maps[String(lastMap)]) {
        st.viewMapId = lastMap;
        radar.ui.view.syncView();
      }
    }
    renderTrailInfo();
    for (const event of ['header', 'chart-input', 'draw']) bus.emit(event);
  }
  async function loadTrail(guid, switchMap) {
    try {
      const d = await radar.session.api('/api/track?' + F.qs({ rs: VM.currentRuleset(st), guid }));
      if (st.track && st.track.guid === guid) adoptTrail(d, switchMap);
    } catch (e) {
      if (st.track && st.track.guid === guid && switchMap) clear();
    }
  }

  /**
   * Follow a contact: select it, load its trail and let the sweep watch that player.
   * @param {Object} row contact row
   */
  function pickContact(row) {
    st.track = { guid: row.guid, name: row.name, reaction: row.reaction || 'unknown', total: 0, maps: [], fixes: [] };
    st.selected = row.guid;
    $('search-results').hidden = true;
    $('search-clear').hidden = false;
    if (st.snap) {
      bus.emit('list');
      bus.emit('draw');
    }
    loadTrail(row.guid, true);
    bus.emit('sector');
  }
  function cancelPending() {
    if (searchTimer) {
      clearTimeout(searchTimer);
      searchTimer = null;
    }
    searchSeq++;
  }
  /**
   * Clear the pending timer or the current selection.
   */
  function clear() {
    st.track = null;
    cancelPending();
    $('search-input').value = '';
    $('search-clear').hidden = true;
    $('search-results').hidden = true;
    renderTrailInfo();
    if (st.snap) bus.emit('list');
    bus.emit('draw');
    bus.emit('sector');
  }

  /**
   * Debounced search while typing; an empty box cancels outstanding requests.
   */
  function onInput() {
    const input = $('search-input'),
      query = input.value.trim();
    $('search-clear').hidden = input.value === '' && !st.track;
    if (searchTimer) clearTimeout(searchTimer);
    if (!query) {
      searchSeq++;
      $('search-results').hidden = true;
      return;
    }
    searchTimer = setTimeout(() => runSearch(query), CFG.timing.SEARCH_DEBOUNCE_MS);
  }

  /**
   * The display zone changed: rebuild the visible result rows and the trail text.
   */
  function onZoneChanged() {
    if (!$('search-results').hidden) renderResults(shownRows);
    renderTrailInfo();
  }

  function onRulesetChanged() {
    const query = $('search-input').value.trim();
    if (query) runSearch(query);
    else {
      searchSeq++;
      $('search-results').hidden = true;
    }
    if (st.track) loadTrail(st.track.guid, true);
  }

  /**
   * Bind the module to the shared state (and bus) and attach its DOM listeners.
   * @param {Object} state shared UI state
   * @param {Object} eventBus event bus
   */
  function init(state, eventBus) {
    st = state;
    bus = eventBus;
    $('search-input').addEventListener('input', onInput);
    $('search-clear').addEventListener('click', clear);
    g.addEventListener('keydown', e => {
      if (e.key === 'Escape' && (st.track || $('search-input').value !== '')) clear();
    });
  }

  (radar.ui || (radar.ui = {})).search = {
    init,
    pickContact,
    clear,
    renderTrailInfo,
    onRulesetChanged,
    onZoneChanged,
  };
})(typeof self !== 'undefined' ? self : globalThis);
