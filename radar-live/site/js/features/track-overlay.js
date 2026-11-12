// Dotted track for the selected player: asks sweeps to include trail=1 and loads the full trail once
// (GET /api/v1/trail). The radar draws state.trail; this feature only fills it and shows a summary.
import { mergeTrail } from '../merge.js';

export default {
  name: 'track-overlay',
  init(ctx) {
    const { MSG, fmt, state } = ctx;
    state.wantTrail = true;
    const host = ctx.mount('side-top');
    const info = ctx.el('div', { className: 'trackInfo muted', hidden: true });
    if (host) host.appendChild(info);

    const render = () => {
      info.hidden = !state.player;
      if (!state.player) return;
      const approx = state.trail.filter((f) => f.kind === 'approx').length;
      info.textContent = state.trail.length
        ? `${fmt(MSG.tracking, { name: state.player.name })}\n${fmt(MSG.trackInfo, { n: state.trail.length, a: approx })}`
        : `${fmt(MSG.tracking, { name: state.player.name })}\n${MSG.trackNone}`;
    };

    async function loadTrail() {
      if (!state.player) return;
      const guid = state.player.guid;
      try {
        const rs = state.rs === 'default' ? undefined : state.rs;
        const fixes = await ctx.api('/api/v1/trail', { rs, guid });
        if (!state.player || state.player.guid !== guid) return;
        state.trail = mergeTrail(state.trail, fixes, ctx.config.TRAIL_CAP);
        ctx.redraw();
        render();
      } catch {
        /* the sweep's own trail still arrives; the full history is a bonus */
      }
    }
    ctx.bus.on('selection', () => { render(); loadTrail(); });
    ctx.bus.on('sweep', render);
    ctx.bus.on('reset', render);
    render();
  },
  destroy() {},
};
