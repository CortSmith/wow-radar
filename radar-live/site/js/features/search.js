// Player search (250 ms debounce) -> GET /api/v1/contacts?q=. Picking a result selects the player (scope=player).
import { fmtAge } from '../format.js';
import { clearNode } from '../dom.js';


export default {
  name: 'search',
  init(ctx) {
    const host = ctx.mount('side-top');
    if (!host) return;
    const { MSG, state, config } = ctx;
    const input = ctx.el('input', {
      type: 'text', className: 'q', maxLength: 100,
      attrs: { placeholder: MSG.searchPlaceholder, autocomplete: 'off', spellcheck: 'false', 'aria-label': MSG.searchPlaceholder },
    });
    const clear = ctx.el('button', { type: 'button', className: 'btn', text: 'x', title: MSG.searchClear, hidden: true });
    const results = ctx.el('ul', { className: 'results', hidden: true });
    host.appendChild(ctx.el('div', { className: 'search' }, ctx.el('div', { className: 'qrow' }, input, clear), results));

    let timer = null;
    let inflight = null;
    const hide = () => { results.hidden = true; clearNode(results); };
    const message = (text) => { clearNode(results); results.appendChild(ctx.el('li', { className: 'none', text })); results.hidden = false; };
    const pick = (c) => { input.value = c.name; hide(); clear.hidden = false; ctx.setPlayer({ guid: c.guid, name: c.name }); };
    const wipe = () => { input.value = ''; hide(); clear.hidden = true; };
    const reset = () => { wipe(); ctx.deselect(); };

    async function run() {
      const q = input.value.trim();
      if (inflight) inflight.abort();
      if (!q) { hide(); return; }
      inflight = new AbortController();
      try {
        const rs = state.rs === 'default' ? undefined : state.rs;
        const res = await ctx.api('/api/v1/contacts', { q, rs, limit: config.SEARCH_LIMIT }, inflight.signal);
        if (input.value.trim() !== q) return;
        if (!res.contacts.length) { message(MSG.searchNone); return; }
        clearNode(results);
        for (const c of res.contacts) results.appendChild(resultRow(ctx, c, pick));
        results.hidden = false;
      } catch (err) {
        if (!err || err.name !== 'AbortError') message(MSG.searchFailed);
      }
    }
    input.addEventListener('input', () => {
      clear.hidden = !input.value;
      if (timer !== null) ctx.timers.clearTimeout(timer);
      timer = ctx.timers.setTimeout(run, config.SEARCH_DEBOUNCE_MS);
    });
    ctx.bus.on('escape', wipe);
    clear.addEventListener('click', reset);
    ctx.bus.on('selection', () => { if (!state.player && input.value && results.hidden) { input.value = ''; clear.hidden = true; } });
  },
};

function resultRow(ctx, c, pick) {
  const meta = `${c.zone || ''} ${c.lastSeen ? `- ${fmtAge(ctx.now() - c.lastSeen)} ago` : ''}`.trim();
  return ctx.el('li', { on: { click: () => pick(c) } },
    ctx.el('span', { className: `sw ${c.reaction === 'friendly' ? 'friendly' : 'hostile'}` }),
    ctx.el('span', { className: 'nm', text: c.name }),
    ctx.el('span', { className: 'meta', text: meta }));
}
