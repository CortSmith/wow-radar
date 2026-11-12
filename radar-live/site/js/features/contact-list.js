// Side list of the contacts in the live view (gone rows dropped), newest first. Click selects a player.
import { visibleContacts, classify } from '../staleness.js';
import { colorOf } from '../view-model.js';
import { fmtAge } from '../format.js';
import { clearNode } from '../dom.js';

const LIST_ROWS_MAX = 300;

export default {
  name: 'contact-list',
  init(ctx) {
    const host = ctx.mount('side-list');
    if (!host) return;
    const { MSG, fmt, state } = ctx;
    const summary = ctx.el('div', { className: 'summary muted' });
    const list = ctx.el('ul', { className: 'list' });
    host.appendChild(summary);
    host.appendChild(list);

    function render() {
      const now = ctx.now();
      const pinned = state.player ? state.player.guid : '';
      const rows = visibleContacts(state.contacts, now, pinned)
        .filter((c) => (c.reaction === 'friendly' ? state.filters.friendly : state.filters.hostile))
        .filter((c) => state.filters.stale || classify(c, now) !== 'stale' || c.guid === pinned)
        .sort((a, b) => b.lastSeen - a.lastSeen)
        .slice(0, LIST_ROWS_MAX);
      const stale = rows.filter((c) => classify(c, now) === 'stale').length;
      summary.textContent = fmt(MSG.summary, { n: rows.length, s: stale });
      clearNode(list);
      for (const c of rows) list.appendChild(row(ctx, c, now, pinned));
    }
    for (const ev of ['sweep', 'reset', 'selection', 'filters', 'tick']) ctx.bus.on(ev, render);
    render();
  },
};

function row(ctx, c, now, pinned) {
  const status = classify(c, now);
  const cls = `${status === 'stale' ? 'stale' : ''} ${c.guid === pinned ? 'sel' : ''}`.trim();
  const meta = `${c.level ? `lvl ${c.level} - ` : ''}${fmtAge(now - c.lastSeen)}`;
  return ctx.el('li', { className: cls, on: { click: () => ctx.setPlayer({ guid: c.guid, name: c.name }) } },
    ctx.el('span', { className: `sw ${colorOf(c, status)}` }),
    ctx.el('span', { className: 'nm', text: c.name || c.guid }),
    ctx.el('span', { className: 'meta', text: meta }));
}
