// Bounty ranking (GET /api/v1/bounty), refreshed with every sweep while the panel is open.
import { clearNode } from '../dom.js';

export default {
  name: 'bounty-panel',
  init(ctx) {
    const host = ctx.mount('side-panels');
    if (!host) return;
    const { MSG, fmt, state, config } = ctx;
    const count = ctx.el('span', { className: 'muted' });
    const list = ctx.el('ol', { className: 'bountyList' });
    const panel = ctx.el('details', { className: 'bounty', open: true, title: MSG.bountyHint },
      ctx.el('summary', {}, ctx.el('strong', { text: MSG.bounty }), ' ', count), list);
    host.appendChild(panel);

    let loading = false;
    async function load() {
      if (loading || panel.open === false) return;
      loading = true;
      try {
        const rs = state.rs === 'default' ? undefined : state.rs;
        const rows = await ctx.api('/api/v1/bounty', { rs, limit: config.BOUNTY_LIMIT });
        render(rows);
      } catch {
        /* keep the previous list; the next sweep retries */
      } finally {
        loading = false;
      }
    }
    function render(rows) {
      count.textContent = String(rows.length);
      clearNode(list);
      if (!rows.length) list.appendChild(ctx.el('li', { className: 'none', text: MSG.bountyEmpty }));
      for (const r of rows) {
        list.appendChild(ctx.el('li', { on: { click: () => ctx.setPlayer({ guid: r.guid, name: r.name }) } },
          ctx.el('span', { className: 'rk', text: String(r.rank) }),
          ctx.el('span', { className: 'nm', text: r.name }),
          ctx.el('span', { className: 'bv', text: String(r.score) }),
          ctx.el('span', { className: 'meta', text: fmt(MSG.bountyRow, { kb: r.kb, a: r.assists }) })));
      }
    }
    ctx.bus.on('sweep', load);
    ctx.bus.on('ruleset', load);
    panel.addEventListener('toggle', load);
    load();
  },
};
