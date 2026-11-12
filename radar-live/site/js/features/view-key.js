// When the server answers 401, shows a small key prompt; the key is kept in memory only, and in localStorage
// (try/catch wrapped) only when the user ticks "remember on this device".
export default {
  name: 'view-key',
  init(ctx) {
    const host = ctx.mount('notice');
    if (!host) return;
    const { MSG } = ctx;
    const input = ctx.el('input', { type: 'password', attrs: { placeholder: MSG.keyPlaceholder, autocomplete: 'off', 'aria-label': MSG.keyPlaceholder } });
    const msg = ctx.el('span', { text: MSG.keyPrompt });
    const remember = ctx.el('input', { type: 'checkbox', checked: false, attrs: { 'aria-label': MSG.keyRemember } });
    const rememberLabel = ctx.el('label', { className: 'muted', title: MSG.keyRememberHint }, remember, ' ' + MSG.keyRemember);
    const save = ctx.el('button', {
      type: 'button', className: 'btn', text: MSG.keySave,
      on: { click: () => { ctx.setViewKey(input.value.trim(), remember.checked); input.value = ''; } },
    });
    const bar = ctx.el('div', { className: 'keybar', hidden: true }, msg, input, rememberLabel, save);
    host.appendChild(bar);
    let asked = false;
    ctx.bus.on('auth', () => {
      msg.textContent = asked ? MSG.keyBad : MSG.keyPrompt;
      asked = Boolean(ctx.state.viewKey);
      bar.hidden = false;
    });
    ctx.bus.on('sweep', () => { bar.hidden = true; });
  },
};
