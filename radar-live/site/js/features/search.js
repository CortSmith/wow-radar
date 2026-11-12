// Player search (250 ms debounce) -> GET /api/v1/contacts?q=. Stage 11 U3: picking a result ADDS a
// chip (ctx.addPlayer) and clears the typed text immediately, instead of filling the box with the name
// (that single-select behaviour is gone - state.tracked can hold several players at once, see
// docs/plans/stage-11-radar-ux.md section 3.3). One chip is rendered per entry of state.tracked, in
// insertion order, each with a stable track colour (js/trackcolors.js, shared with later tasks U4/U5).
import { fmtAge } from '../format.js';
import { clearNode } from '../dom.js';
import { trackColorClass } from '../trackcolors.js';

const ENTER_KEY = 'Enter';
const BACKSPACE_KEY = 'Backspace';

export default {
  name: 'search',
  init(ctx) {
    const host = ctx.mount('side-top');
    if (!host) return;
    const { MSG, fmt, state, config } = ctx;
    const input = ctx.el('input', {
      type: 'text', className: 'q', maxLength: 100,
      attrs: { placeholder: MSG.searchPlaceholder, autocomplete: 'off', spellcheck: 'false', 'aria-label': MSG.searchPlaceholder },
    });
    // The x next to the input clears typed text AND deselects everyone (same as Escape) - it always did,
    // kept as the one-click "start over" action; chips below have their own per-player x and Clear all.
    const clear = ctx.el('button', { type: 'button', className: 'btn', text: 'x', title: MSG.searchClear, hidden: true });
    const results = ctx.el('ul', { className: 'results', hidden: true });
    const chips = ctx.el('div', { className: 'chips', hidden: true });
    const clearAll = ctx.el('button', { type: 'button', className: 'clearAll', text: MSG.searchClearAll, hidden: true });
    host.appendChild(ctx.el('div', { className: 'search' }, ctx.el('div', { className: 'qrow' }, input, clear), results, chips, clearAll));

    let timer = null;
    let inflight = null;
    let shown = []; // the contacts currently listed in `results` (for Enter-picks-the-one-result)
    const hide = () => { results.hidden = true; shown = []; clearNode(results); };
    const message = (text) => { clearNode(results); results.appendChild(ctx.el('li', { className: 'none', text })); results.hidden = false; shown = []; };
    const pick = (c) => { ctx.addPlayer({ guid: c.guid, name: c.name }); input.value = ''; hide(); clear.hidden = true; };
    const wipeText = () => { input.value = ''; hide(); clear.hidden = true; };
    const reset = () => { wipeText(); ctx.deselect(); };

    function renderChips() {
      clearNode(chips);
      const tracked = [...state.tracked.values()];
      for (const p of tracked) chips.appendChild(chipEl(ctx, p));
      chips.hidden = tracked.length === 0;
      clearAll.hidden = tracked.length < 2;
    }

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
        shown = res.contacts;
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
    input.addEventListener('keydown', (e) => {
      if (e.key === ENTER_KEY) {
        if (shown.length === 1) { e.preventDefault(); pick(shown[0]); }
        return;
      }
      if (e.key === BACKSPACE_KEY && !input.value) {
        const last = [...state.tracked.keys()].pop();
        if (last !== undefined) { e.preventDefault(); ctx.removePlayer(last); }
      }
    });
    ctx.bus.on('escape', wipeText);
    clear.addEventListener('click', reset);
    clearAll.addEventListener('click', () => ctx.clearPlayers());
    ctx.bus.on('selection', renderChips);
    renderChips();
  },
};

function resultRow(ctx, c, pick) {
  const meta = `${c.zone || ''} ${c.lastSeen ? `- ${fmtAge(ctx.now() - c.lastSeen)} ago` : ''}`.trim();
  return ctx.el('li', { on: { click: () => pick(c) } },
    ctx.el('span', { className: `sw ${c.reaction === 'friendly' ? 'friendly' : 'hostile'}` }),
    ctx.el('span', { className: 'nm', text: c.name }),
    ctx.el('span', { className: 'meta', text: meta }));
}

/** One chip: a track-coloured swatch + name (clicking either focuses the player) and a separate x (removes). */
function chipEl(ctx, p) {
  const { MSG, fmt } = ctx;
  const name = p.name || p.guid;
  const body = ctx.el('span', { className: 'chipBody', on: { click: () => ctx.focusPlayer(p.guid) } },
    ctx.el('span', { className: `sw ${trackColorClass(p.guid)}` }),
    ctx.el('span', { className: 'nm', text: name }));
  const x = ctx.el('button', {
    type: 'button', className: 'x', text: 'x',
    attrs: { 'aria-label': fmt(MSG.searchRemove, { name }) },
    on: { click: () => ctx.removePlayer(p.guid) },
  });
  return ctx.el('span', { className: 'chip' }, body, x);
}
