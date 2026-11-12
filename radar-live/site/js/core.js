// The page host. It knows nothing about radars or players beyond the sweep contract: it owns the state
// object, an event bus, the feature registry, the mount points, the sweep loop and the API client.
// Features (js/features/*.js) plug into it; removing one never breaks the others.
import { resolveConfig } from './config.js';
import { MSG, fmt } from './messages.js';
import { makeEl } from './dom.js';
import { createClient } from './client.js';
import { createScheduler } from './scheduler.js';
import { mergeContacts, mergeTrail } from './merge.js';
import { sectorOf, sectorKey, sweepParams } from './sector.js';
import { fmtTime, safeZone } from './format.js';

const SWEEP_PATH = '/api/v1/sweep';

/** Tiny event bus. on() returns an unsubscribe function; a throwing listener never stops the others. */
export function createBus(onError = () => {}) {
  const listeners = new Map();
  return {
    on(event, fn) {
      if (!listeners.has(event)) listeners.set(event, new Set());
      listeners.get(event).add(fn);
      return () => listeners.get(event).delete(fn);
    },
    emit(event, payload) {
      for (const fn of [...(listeners.get(event) || [])]) {
        try {
          fn(payload);
        } catch (err) {
          onError(err, event);
        }
      }
    },
  };
}

/** localStorage wrapper that never throws (private windows, blocked storage). */
export function safeStorage(storage) {
  return {
    get(key, fallback = null) {
      try {
        const raw = storage && storage.getItem(key);
        return raw === null || raw === undefined ? fallback : JSON.parse(raw);
      } catch {
        return fallback;
      }
    },
    set(key, value) {
      try {
        if (storage) storage.setItem(key, JSON.stringify(value));
      } catch {
        /* storage unavailable: the page works without it */
      }
    },
    remove(key) {
      try {
        if (storage) storage.removeItem(key);
      } catch {
        /* storage unavailable: nothing was stored */
      }
    },
  };
}

function initialState(prefs, config) {
  return {
    rs: 'default',
    player: null,
    zone: null,
    filters: { friendly: true, hostile: true, stale: true },
    contacts: new Map(),
    trail: [],
    charts: [],
    lastNow: 0,
    sweeps: 0,
    conn: 'idle',
    error: null,
    lastOkAt: 0,
    tz: safeZone(Intl.DateTimeFormat().resolvedOptions().timeZone),
    wantTrail: false,
    viewKey: prefs.get(config.VIEW_KEY_STORAGE, '') || '',
    mapImage: null,
  };
}

/**
 * Builds the page. env: {document, window, fetch, storage, timers, now, features, config, log}.
 * features: array of {name, init(ctx), onSweep(result, ctx), destroy()} (any may be missing).
 */
export function createApp(env) {
  const config = env.config || resolveConfig('');
  const log = env.log || (() => {});
  const prefs = safeStorage(env.storage);
  const bus = createBus((err, event) => log(`listener for ${event} failed`, err));
  const state = initialState(prefs, config);
  const doc = env.document;
  const client = createClient({
    fetch: env.fetch,
    base: config.API_BASE,
    getKey: () => state.viewKey,
    onAuth: () => bus.emit('auth', { required: true }),
  });
  let loadedKey = '';
  const active = [];
  let ticker = null;

  /** Drops loaded rows when the sector (rule-set, scope, guid, map, group) changed. */
  function applySectorChange() {
    const key = sectorKey(sectorOf(state), state.rs);
    if (key === loadedKey) return false;
    loadedKey = key;
    state.contacts.clear();
    state.trail = [];
    state.lastNow = 0;
    bus.emit('reset');
    return true;
  }

  async function runSweep(signal) {
    applySectorChange();
    const key = loadedKey;
    const since = state.lastNow ? Math.max(0, state.lastNow - config.SINCE_OVERLAP_MS) : 0;
    state.conn = 'polling';
    bus.emit('conn');
    let res;
    try {
      res = await client.get(SWEEP_PATH, sweepParams(sectorOf(state), state.rs, since, state.wantTrail), signal);
    } catch (err) {
      if (!signal.aborted) {
        state.conn = 'error';
        state.error = err;
        bus.emit('conn');
      }
      throw err;
    }
    if (signal.aborted || key !== loadedKey) return;
    absorb(res);
  }

  function absorb(res) {
    mergeContacts(state.contacts, res.contacts, config.MAX_CONTACTS);
    if (res.trail && res.trail.length) state.trail = mergeTrail(state.trail, res.trail, config.TRAIL_CAP);
    state.lastNow = Number.isFinite(res.now) ? res.now : env.now();
    state.sweeps += 1;
    state.conn = 'ok';
    state.error = null;
    state.lastOkAt = env.now();
    bus.emit('conn');
    bus.emit('sweep', res);
    for (const f of active) {
      if (typeof f.onSweep !== 'function') continue;
      try {
        f.onSweep(res, ctx);
      } catch (err) {
        log(`feature ${f.name} onSweep failed`, err);
      }
    }
  }

  const scheduler = createScheduler({
    run: runSweep,
    intervalMs: config.SWEEP_INTERVAL_MS,
    debounceMs: config.DEBOUNCE_MS,
    retryBaseMs: config.RETRY_BASE_MS,
    maxBackoffMs: config.MAX_BACKOFF_MS,
    timers: env.timers,
    now: env.now,
    onError: (err) => log('sweep failed', err),
  });

  const changed = (event) => {
    applySectorChange();
    bus.emit(event);
    scheduler.requestNow();
  };

  const ctx = {
    state,
    bus,
    config,
    MSG,
    fmt,
    doc,
    win: env.window || null,
    timers: env.timers,
    now: env.now,
    prefs,
    el: (tag, props, ...kids) => makeEl(doc, tag, props, ...kids),
    mount: (name) => doc.querySelector(`[data-mount="${name}"]`),
    api: (path, params, signal) => client.get(path, params, signal),
    /** Plain GET of a same-site JSON file (no view key), for optional assets such as maps/index.json. */
    async fetchJson(url) {
      const res = await env.fetch(url);
      if (!res.ok) throw new Error(`http ${res.status}`);
      return res.json();
    },
    fmtTime: (ms) => fmtTime(ms, state.tz),
    nextInMs: () => (scheduler.nextAt() ? Math.max(0, scheduler.nextAt() - env.now()) : 0),
    failures: () => scheduler.failures(),
    setPlayer(player) {
      state.player = player && player.guid ? { guid: player.guid, name: player.name || '' } : null;
      changed('selection');
    },
    setZone(zone) {
      state.zone = zone && Number.isFinite(zone.mapId) ? { mapId: zone.mapId, name: zone.name || '' } : null;
      changed('selection');
    },
    setRuleset(rs) {
      state.rs = rs || 'default';
      changed('ruleset');
    },
    setFilters(patch) {
      Object.assign(state.filters, patch);
      changed('filters');
    },
    /** The key lives in memory; it is written to localStorage only when the user ticked "remember". */
    setViewKey(key, remember = false) {
      state.viewKey = key;
      if (remember && key) prefs.set(config.VIEW_KEY_STORAGE, key);
      else prefs.remove(config.VIEW_KEY_STORAGE);
      scheduler.refreshNow();
    },
    requestSweep: () => scheduler.requestNow(),
    refreshNow: () => scheduler.refreshNow(),
    redraw: () => bus.emit('view'),
  };

  function registerFeature(feature) {
    if (!feature || !feature.name || active.some((f) => f.name === feature.name)) return false;
    try {
      if (typeof feature.init === 'function') feature.init(ctx);
    } catch (err) {
      log(`feature ${feature.name} failed to start`, err);
      return false;
    }
    active.push(feature);
    return true;
  }

  function start() {
    for (const f of env.features || []) registerFeature(f);
    const visibility = () => scheduler.setHidden(Boolean(doc.hidden));
    doc.addEventListener('visibilitychange', visibility);
    ticker = env.timers.setInterval(() => bus.emit('tick'), config.COUNTDOWN_TICK_MS);
    scheduler.setHidden(Boolean(doc.hidden));
    scheduler.start();
  }

  function stop() {
    scheduler.stop();
    if (ticker !== null) env.timers.clearInterval(ticker);
    for (const f of active) {
      try {
        if (typeof f.destroy === 'function') f.destroy();
      } catch (err) {
        log(`feature ${f.name} destroy failed`, err);
      }
    }
  }

  return { state, bus, ctx, start, stop, registerFeature, featureNames: () => active.map((f) => f.name) };
}
