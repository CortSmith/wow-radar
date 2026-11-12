// Ruleset segmented control: Default (server default), Normal, PvP, RP, RP-PvP. Sends rs on every sweep.
const IDS = ['default', 'normal', 'pvp', 'rp', 'rppvp'];

export default {
  name: 'ruleset-switch',
  init(ctx) {
    const host = ctx.mount('header-controls');
    if (!host) return;
    const { MSG, state } = ctx;
    const saved = ctx.prefs.get(ctx.config.PREFS_STORAGE, {});
    if (IDS.includes(saved.rs)) state.rs = saved.rs;
    const group = ctx.el('div', { className: 'seg', title: MSG.rulesetHint, attrs: { role: 'group', 'aria-label': MSG.rulesetLabel } });
    const buttons = IDS.map((id) => ctx.el('button', {
      type: 'button',
      text: MSG.rulesets[id],
      on: {
        click: () => {
          ctx.setRuleset(id);
          ctx.prefs.set(ctx.config.PREFS_STORAGE, { ...ctx.prefs.get(ctx.config.PREFS_STORAGE, {}), rs: id });
        },
      },
    }));
    buttons.forEach((b) => group.appendChild(b));
    const mark = () => buttons.forEach((b, i) => b.classList.toggle('on', IDS[i] === state.rs));
    ctx.bus.on('ruleset', mark);
    mark();
    host.appendChild(group);
  },
};
