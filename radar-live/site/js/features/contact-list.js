// Side list of the contacts in the live view (gone rows dropped).
//
// Stage 11 U4a: when nothing is tracked, rendering/behaviour is byte-for-byte the same as before this
// task - no header line, no dimming, no per-row x, newest-first sort - clicking a row simply tracks it
// (ctx.addPlayer; with nothing tracked that is equivalent to the old single-select "click selects").
//
// Once state.tracked is non-empty: tracked rows render first (newest-tracked - i.e. most recently added
// or focused, state.tracked's Map order, reversed - first; this matches "today"'s newest-first feel more
// closely than insertion order would), each with a small track-colour marker (js/trackcolors.js) and its
// own x button (ctx.removePlayer) - the ONLY way to remove someone from here now. Non-tracked rows render
// after, dimmed (.dim) but still clickable (clicking one adds it to tracked). Clicking a row's body (not
// the x) either focuses an already-tracked row or adds a not-yet-tracked one - never deselects anymore
// (the x replaces that). A header line and a "Show only tracked" checkbox (persisted like group-filter's
// checkboxes, via ctx.prefs under ctx.config.PREFS_STORAGE) round it out.
import { visibleContacts, classify } from '../staleness.js';
import { colorOf, inViewZone } from '../view-model.js';
import { fmtAge } from '../format.js';
import { clearNode } from '../dom.js';
import { trackColorClass } from '../trackcolors.js';

const LIST_ROWS_MAX = 300;
const ONLY_TRACKED_KEY = 'onlyTracked';
const AGE_REFRESH_TICKS = 15;

export default {
  name: 'contact-list',
  init(ctx) {
    const host = ctx.mount('side-list');
    if (!host) return;
    const { MSG, fmt, state } = ctx;
    const trackSummary = ctx.el('div', { className: 'summary muted', hidden: true });
    const summary = ctx.el('div', { className: 'summary muted' });
    const list = ctx.el('ul', { className: 'list' });
    host.appendChild(trackSummary);
    host.appendChild(summary);
    host.appendChild(list);

    let onlyTracked = !!ctx.prefs.get(ctx.config.PREFS_STORAGE, {})[ONLY_TRACKED_KEY];
    const controlsHost = ctx.mount('header-controls');
    if (controlsHost) {
      const checkbox = ctx.el('input', {
        type: 'checkbox',
        checked: onlyTracked,
        on: {
          change: (e) => {
            onlyTracked = e.target.checked;
            ctx.prefs.set(ctx.config.PREFS_STORAGE, { ...ctx.prefs.get(ctx.config.PREFS_STORAGE, {}), [ONLY_TRACKED_KEY]: onlyTracked });
            render();
          },
        },
      });
      controlsHost.appendChild(ctx.el('label', { title: MSG.onlyTrackedHint }, checkbox, MSG.onlyTracked));
    }

    function render() {
      const now = ctx.now();
      const pinned = state.player ? state.player.guid : '';
      const trackedSize = state.tracked.size;
      const dimEnabled = trackedSize > 0;
      let rows = visibleContacts(state.contacts, now, pinned)
        .filter((c) => c.guid === pinned || state.tracked.has(c.guid) || inViewZone(state, c))
        .filter((c) => (c.reaction === 'friendly' ? state.filters.friendly : state.filters.hostile))
        .filter((c) => state.filters.stale || classify(c, now) !== 'stale' || c.guid === pinned);
      const total = rows.length;
      if (dimEnabled && onlyTracked) rows = rows.filter((c) => state.tracked.has(c.guid));
      if (dimEnabled) {
        const order = [...state.tracked.keys()]; // Map insertion order; last = most recently added/focused
        rows.sort((a, b) => {
          const at = state.tracked.has(a.guid);
          const bt = state.tracked.has(b.guid);
          if (at !== bt) return at ? -1 : 1; // tracked rows first
          if (at && bt) return order.indexOf(b.guid) - order.indexOf(a.guid); // newest-tracked first
          return b.lastSeen - a.lastSeen;
        });
      } else {
        rows.sort((a, b) => b.lastSeen - a.lastSeen);
      }
      rows = rows.slice(0, LIST_ROWS_MAX);
      const stale = rows.filter((c) => classify(c, now) === 'stale').length;
      summary.textContent = fmt(MSG.summary, { n: rows.length, s: stale });
      trackSummary.hidden = !dimEnabled;
      if (dimEnabled) trackSummary.textContent = fmt(MSG.trackSummary, { tracked: trackedSize, total });
      clearNode(list);
      for (const c of rows) list.appendChild(row(ctx, c, now, pinned, dimEnabled && state.tracked.has(c.guid), dimEnabled));
    }
    for (const ev of ['sweep', 'reset', 'selection', 'filters', 'worldView']) ctx.bus.on(ev, render);
    // Ages only change slowly: rebuilding up to 300 rows every second made the page laggy.
    let ticks = 0;
    ctx.bus.on('tick', () => { ticks += 1; if (ticks % AGE_REFRESH_TICKS === 0) render(); });
    render();
  },
};

/** Already-tracked row -> focus it; not-yet-tracked row -> add it. The per-row x is the only way to
 * remove a tracked player from this list (replaces the old click-on-selected-row-to-deselect). */
function toggle(ctx, c) {
  if (ctx.state.tracked.has(c.guid)) ctx.focusPlayer(c.guid);
  else ctx.addPlayer({ guid: c.guid, name: c.name });
}

function row(ctx, c, now, pinned, isTracked, dimEnabled) {
  const { MSG, fmt } = ctx;
  const status = classify(c, now);
  const dim = dimEnabled && !isTracked;
  const cls = [status === 'stale' ? 'stale' : '', c.guid === pinned ? 'sel' : '', dim ? 'dim' : '']
    .filter(Boolean).join(' ');
  const meta = `${c.level ? `lvl ${c.level} - ` : ''}${fmtAge(now - c.lastSeen)}`;
  const children = [];
  if (isTracked) children.push(ctx.el('span', { className: `sw ${trackColorClass(c.guid)}` }));
  children.push(ctx.el('span', { className: `sw ${colorOf(c, status)}` }));
  children.push(ctx.el('span', { className: 'nm', text: c.name || c.guid }));
  children.push(ctx.el('span', { className: 'meta', text: meta }));
  if (isTracked) {
    children.push(ctx.el('button', {
      type: 'button', className: 'x', text: 'x',
      attrs: { 'aria-label': fmt(MSG.searchRemove, { name: c.name || c.guid }) },
      on: { click: (e) => { e.stopPropagation(); ctx.removePlayer(c.guid); } },
    }));
  }
  const attrs = dim ? { title: MSG.notTracked, 'aria-label': MSG.notTracked } : {};
  return ctx.el('li', { className: cls, attrs, on: { click: () => toggle(ctx, c) } }, ...children);
}
