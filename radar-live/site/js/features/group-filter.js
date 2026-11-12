// Blue / Red / Stale checkboxes. Blue and Red change the sector (grp), Stale only the view.
export default {
  name: 'group-filter',
  init(ctx) {
    const host = ctx.mount('header-controls');
    if (!host) return;
    const { MSG, state } = ctx;
    const saved = ctx.prefs.get(ctx.config.PREFS_STORAGE, {});
    if (saved.filters) Object.assign(state.filters, saved.filters);
    const make = (key, label, hint, swatch) => {
      const input = ctx.el('input', {
        type: 'checkbox',
        checked: state.filters[key],
        on: {
          change: (e) => {
            ctx.setFilters({ [key]: e.target.checked });
            ctx.prefs.set(ctx.config.PREFS_STORAGE, { ...ctx.prefs.get(ctx.config.PREFS_STORAGE, {}), filters: { ...state.filters } });
          },
        },
      });
      host.appendChild(ctx.el('label', { title: hint }, input, ctx.el('span', { className: `sw ${swatch}` }), label));
    };
    make('friendly', MSG.blue, MSG.blueHint, 'friendly');
    make('hostile', MSG.red, MSG.redHint, 'hostile');
    make('stale', MSG.stale, MSG.staleHint, 'stale');
  },
};
