// History table built from the loaded rows (every contact the sweeps returned, stale ones included).
import { fmtAvg, fmtNum } from '../format.js';
import { clearNode } from '../dom.js';

export default {
  name: 'history-panel',
  init(ctx) {
    const toggleHost = ctx.mount('header-controls');
    const below = ctx.mount('below');
    if (!toggleHost || !below) return;
    const { MSG, fmt, state, config } = ctx;
    const count = ctx.el('span', { className: 'muted' });
    const body = ctx.el('tbody');
    const head = ctx.el('tr', {}, ...MSG.historyCols.map((c) => ctx.el('th', { text: c })));
    const section = ctx.el('section', { className: 'history', hidden: true },
      ctx.el('div', { className: 'hhead' }, ctx.el('strong', { text: MSG.historyTitle }), count),
      ctx.el('div', { className: 'hscroll' }, ctx.el('table', {}, ctx.el('thead', {}, head), body)));
    below.appendChild(section);
    const box = ctx.el('input', { type: 'checkbox', on: { change: (e) => { section.hidden = !e.target.checked; render(); } } });
    toggleHost.appendChild(ctx.el('label', { title: MSG.historyTitle }, box, MSG.history));

    function render() {
      if (section.hidden) return;
      const rows = [...state.contacts.values()].sort((a, b) => b.lastSeen - a.lastSeen).slice(0, config.HISTORY_ROWS_MAX);
      count.textContent = fmt(MSG.historyCount, { n: rows.length });
      clearNode(body);
      for (const c of rows) {
        const cells = [c.name, fmtAvg(c.dmgTotal, c.dmgHits), fmtAvg(c.dmgTotal, c.dmgEvents), fmtAvg(c.hpSum, c.hpSamples),
          fmtNum(c.maxHp), String(c.hpSamples ?? 0), ctx.fmtTime(c.lastSeen), c.zone || '-'];
        body.appendChild(ctx.el('tr', { on: { click: () => ctx.setPlayer({ guid: c.guid, name: c.name }) } },
          ...cells.map((t) => ctx.el('td', { text: t }))));
      }
    }
    for (const ev of ['sweep', 'reset', 'view']) ctx.bus.on(ev, render);
  },
};
