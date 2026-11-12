// Zone picker: lists the maps the server has seen (GET /api/v1/charts). Picking one switches the sweep to
// scope=zone. Optionally loads zone images from maps/index.json (silently skipped when unavailable).
import { findMapEntry } from '../charts.js';
import { chooseViewMap } from '../view-model.js';

const REFRESH_EVERY_SWEEPS = 10;

export default {
  name: 'chart-picker',
  init(ctx) {
    const host = ctx.mount('header-controls');
    if (!host) return;
    const { MSG, state } = ctx;
    const select = ctx.el('select', { title: MSG.zoneLabel, attrs: { 'aria-label': MSG.zoneLabel } });
    select.addEventListener('change', () => {
      const chart = state.charts.find((c) => String(c.mapId) === select.value);
      ctx.setZone(chart ? { mapId: chart.mapId, name: chart.name } : null);
    });
    host.appendChild(ctx.el('label', {}, `${MSG.zoneLabel} `, select));

    let mapIndex = null;
    const fill = () => {
      while (select.firstChild) select.removeChild(select.firstChild);
      select.appendChild(ctx.el('option', { value: '', text: MSG.allZones }));
      for (const c of [...state.charts].sort((a, b) => a.name.localeCompare(b.name))) {
        select.appendChild(ctx.el('option', { value: String(c.mapId), text: c.name }));
      }
      select.value = state.zone ? String(state.zone.mapId) : '';
    };
    const setImage = () => {
      const mapId = chooseViewMap(state, ctx.now());
      const chart = state.charts.find((c) => c.mapId === mapId);
      const entry = chart ? findMapEntry(mapIndex, chart.name) : null;
      state.mapImage = entry ? { url: ctx.config.MAPS_BASE_URL + entry.file, w: entry.w, h: entry.h } : null;
      ctx.redraw();
    };
    const loadCharts = async () => {
      try {
        const rs = state.rs === 'default' ? undefined : state.rs;
        state.charts = await ctx.api('/api/v1/charts', { rs });
        fill();
        ctx.bus.emit('charts');
        setImage();
      } catch {
        /* charts are optional: the radar falls back to the free range view */
      }
    };
    const loadIndex = async () => {
      try {
        const res = await ctx.fetchJson(ctx.config.MAPS_BASE_URL + ctx.config.MAPS_INDEX_FILE);
        mapIndex = res;
        setImage();
      } catch {
        mapIndex = null;
      }
    };
    ctx.bus.on('selection', () => { select.value = state.zone ? String(state.zone.mapId) : ''; setImage(); });
    ctx.bus.on('ruleset', loadCharts);
    ctx.bus.on('sweep', () => {
      if (state.sweeps % REFRESH_EVERY_SWEEPS === 0) loadCharts();
      else setImage();
    });
    fill();
    loadCharts();
    loadIndex();
  },
};
