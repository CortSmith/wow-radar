'use strict';
/**
 * contact-panel.js - single responsibility: the header readouts (zone, summary, footer, source tag, empty hint) and the
 * side list of contacts.
 *
 * Public API (Radar.ui.contactPanel):
 *  - init(state, bus)
 *  - renderHeader()   zone, summary, footer, source tag and the "no players" hint
 *  - renderList()     the visible contacts (at most config.limits.LIST_ROWS); clicking one selects it and centres the
 * view
 * Texts come from view-model.js; names are written with textContent only.
 */
(function (g) {
  const radar = g.Radar,
    { $, el, replaceChildren } = radar.dom,
    VM = radar.viewModel,
    blips = radar.blips;
  let st = null,
    bus = null;

  /**
   * Repaint the header readouts from the view model.
   */
  function renderHeader() {
    const h = VM.describeHeader(st, { canPickDirectory: radar.adapters.canPickDirectory() });
    $('zone').textContent = h.zone;
    $('contact-summary').textContent = h.summary;
    $('status-footer').textContent = h.footer;
    $('status-footer').title = h.footerTitle || '';
    $('empty-hint').hidden = h.emptyHidden;
    if (h.emptyText !== null) $('empty-hint').textContent = h.emptyText;
    if (h.sourceTag !== null) $('source-tag').textContent = h.sourceTag;
  }

  /**
   * Centre the view on a contact; in a zoomed fixed chart also move the zoom window to it.
   * @param {Object} blip blip or contact
   */
  function centreOn(blip) {
    if (blip.posKind === 'none') return;
    st.cam = { x: blip.x, y: blip.y };
    radar.ui.settings.setFlag('follow', false);
    const chart = st.snap.maps[String(blip.mapId)];
    if (chart && st.fz.z > 1) {
      const pct = VM.percentOf(st, chart, blip.x, blip.y);
      st.fz.cx = pct[0];
      st.fz.cy = pct[1];
      blips.clampView(st.fz);
    }
  }
  function select(blip) {
    st.selected = blip.guid;
    centreOn(blip);
    renderList();
    bus.emit('draw');
  }

  /**
   * One list row. Name, level and age are written as text, never markup; the swatch color comes from the blip state.
   * @param {Object} blip blip or contact
   * @returns {*} the computed value
   */
  function buildRow(blip) {
    const d = VM.describeContact(st, blip);
    const swatch = el('span', { className: 'swatch' });
    swatch.style.background = d.color;
    const metaParts = [];
    if (d.bountyScore !== null)
      metaParts.push(el('span', { className: 'bounty-tag', text: 'bounty ' + d.bountyScore }));
    metaParts.push(el('span', { text: (d.bountyScore !== null ? ' ' : '') + d.meta }));
    const meta = el('span', { className: 'meta' }, metaParts);
    const li = el('li', { className: (d.isStale ? 'stale ' : '') + (blip.guid === st.selected ? 'selected' : '') }, [
      swatch,
      el('span', { className: 'name', text: d.label }),
      meta,
    ]);
    li.addEventListener('click', () => select(blip));
    return li;
  }

  /**
   * Repaint the list from the shared state.
   */
  function renderList() {
    const list = $('contact-list');
    if (!st.snap) {
      replaceChildren(list, []);
      return;
    }
    const shown = st.snap.pips.filter(blip => VM.isVisible(st, blip)).slice(0, radar.config.limits.LIST_ROWS);
    replaceChildren(list, shown.map(buildRow));
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

  (radar.ui || (radar.ui = {})).contactPanel = { init, renderHeader, renderList };
})(typeof self !== 'undefined' ? self : globalThis);
