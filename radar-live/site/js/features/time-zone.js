// Display time zone chooser. Times from the server are UTC epoch ms; this only changes how they are shown.
import { safeZone } from '../format.js';

const FALLBACK_ZONES = ['UTC', 'America/New_York', 'America/Chicago', 'America/Los_Angeles', 'Europe/London',
  'Europe/Berlin', 'Europe/Moscow', 'Asia/Tokyo', 'Australia/Sydney'];

function zoneList(browserZone) {
  let zones = FALLBACK_ZONES;
  if (typeof Intl.supportedValuesOf === 'function') {
    try {
      zones = ['UTC', ...Intl.supportedValuesOf('timeZone')];
    } catch {
      zones = FALLBACK_ZONES;
    }
  }
  return zones.includes(browserZone) ? zones : [browserZone, ...zones];
}

export default {
  name: 'time-zone',
  init(ctx) {
    const host = ctx.mount('header-controls');
    if (!host) return;
    const { MSG, state } = ctx;
    const saved = ctx.prefs.get(ctx.config.PREFS_STORAGE, {});
    if (saved.tz) state.tz = safeZone(saved.tz);
    const select = ctx.el('select', {
      title: MSG.tzHint, attrs: { 'aria-label': MSG.timeZone },
      on: {
        change: (e) => {
          state.tz = safeZone(e.target.value);
          ctx.prefs.set(ctx.config.PREFS_STORAGE, { ...ctx.prefs.get(ctx.config.PREFS_STORAGE, {}), tz: state.tz });
          ctx.redraw();
        },
      },
    });
    for (const z of zoneList(state.tz)) select.appendChild(ctx.el('option', { value: z, text: z }));
    select.value = state.tz;
    host.appendChild(ctx.el('label', { title: MSG.tzHint }, `${MSG.timeZone} `, select));
  },
};
