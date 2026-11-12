// Connection dot, "next update in Ns" countdown, Update now button, footer.
export default {
  name: 'status',
  init(ctx) {
    const { MSG, fmt, state } = ctx;
    const dot = ctx.el('span', { className: 'dot', title: MSG.connIdle });
    const text = ctx.el('span', { className: 'muted' });
    const button = ctx.el('button', { type: 'button', className: 'btn', text: MSG.updateNow, on: { click: () => ctx.refreshNow() } });
    const header = ctx.mount('header-status');
    if (header) {
      header.appendChild(dot);
      header.appendChild(text);
      header.appendChild(button);
    }
    const footer = ctx.mount('footer');
    const foot = ctx.el('span', { text: MSG.footer });
    const updated = ctx.el('span', { className: 'right' });
    if (footer) {
      footer.appendChild(foot);
      footer.appendChild(updated);
    }

    function render() {
      const label = { ok: MSG.connOk, polling: MSG.connPolling, error: MSG.connError, idle: MSG.connIdle }[state.conn];
      dot.className = `dot ${state.conn}`;
      dot.title = label;
      const secs = Math.ceil(ctx.nextInMs() / 1000);
      if (state.conn === 'error') text.textContent = fmt(MSG.retryIn, { s: secs });
      else if (state.conn === 'polling') text.textContent = MSG.updating;
      else text.textContent = fmt(MSG.nextUpdate, { s: secs });
      updated.textContent = state.lastOkAt ? fmt(MSG.lastUpdate, { t: ctx.fmtTime(state.lastOkAt) }) : '';
    }
    for (const ev of ['conn', 'tick', 'sweep', 'view']) ctx.bus.on(ev, render);
    render();
  },
};
