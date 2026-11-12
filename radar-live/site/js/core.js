// The page host. It knows nothing about radars or players beyond the sweep contract: it owns the state
// object, an event bus, the feature registry, the mount points, the sweep loop and the API client.
// Features (js/features/*.js) plug into it; removing one never breaks the others.
import { resolveConfig } from './config.js';
import { MSG, fmt } from './messages.js';
import { makeEl } from './dom.js';
import { createClient } from './client.js';
import { createScheduler } from './scheduler.js';
import { mergeContacts, mergeTrail } from './merge.js';
import { rosterSectorOf, sectorKey, sweepParams } from './sector.js';
import { fmtTime, safeZone } from './format.js';

const SWEEP_PATH = '/api/v1/sweep';
const ESCAPE_KEY = 'Escape';

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

/**
 * state.tracked is an ordered Map<guid,{guid,name}> (stage 11 U1): the set of players the user has
 * added to track, insertion-ordered so the LAST entry is always the most recently added/focused one.
 * state.player stays a read-only derived getter = that most-recent entry (or null when empty), so every
 * feature written for single-select (history-panel, bounty-panel, view-model, track-overlay, sector.js,
 * scene.js, draw.js, radar-canvas.js, contact-list.js, search.js) keeps working unchanged.
 */
function initialState(prefs, config) {
  const tracked = new Map();
  const state = {
    rs: 'default',
    zone: null,
    filters: { friendly: true, hostile: true, stale: true },
    contacts: new Map(),
    tracked,
    trackedData: new Map(), // stage 11 U2: Map<guid,{contact, trail}>, one entry per tracked player
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
    // Stage 14 W6/W7: pure view-mode flag (world map vs the single-zone radar). Never resets
    // player/tracked/zone/filters; toggled by features/world-map.js's header-controls button, by
    // gotoZone (clicking a zone in the world view switches back to zone view automatically), and by
    // setZone itself (W7: "All Zones" - no zone picked - now defaults to the world map; picking any
    // real zone, from the world map OR the chart-picker dropdown, switches back to zone view; clearing
    // the zone back to "All Zones" restores the world map default).
    worldView: true,
  };
  Object.defineProperty(state, 'player', {
    enumerable: true,
    get() {
      let focused = null;
      for (const p of tracked.values()) focused = p; // Map iteration order = insertion order; last = most recent
      return focused;
    },
  });
  return state;
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
  let keydown = null;

  /**
   * Drops loaded roster rows when the ROSTER sector (rule-set, zone, group) changed. Stage 11 U2:
   * uses rosterSectorOf, which never looks at state.tracked/state.player, so adding, removing or
   * focusing a tracked player never trips this and never wipes the roster everyone else sees.
   */
  function applyRosterSectorChange() {
    const key = sectorKey(rosterSectorOf(state), state.rs);
    if (key === loadedKey) return false;
    loadedKey = key;
    state.contacts.clear();
    state.trail = [];
    state.lastNow = 0;
    bus.emit('reset');
    return true;
  }

  /**
   * Stage 11 U2: one GET per tracked guid (scope=player&guid=&trail=1), sequential (TRACK_MAX is
   * small, no concurrency pool needed). Each result's contact is merged into the shared roster
   * (state.contacts) so that row stays fresh even if the player left the roster's zone/all window;
   * its own trail is kept distinctly in state.trackedData so a later task can draw several trails.
   * A failed request for one guid is logged and skipped - it never drops the roster or the other guids.
   */
  async function runTrackedSweeps(signal) {
    for (const guid of [...state.tracked.keys()]) {
      if (signal.aborted) return;
      let res;
      try {
        res = await client.get(SWEEP_PATH, sweepParams({ scope: 'player', guid }, state.rs, 0, true), signal);
      } catch (err) {
        if (err && err.name === 'AbortError') return; // the whole cycle was aborted, not just this guid
        log(`tracked sweep failed for ${guid}`, err);
        continue;
      }
      if (signal.aborted || !state.tracked.has(guid)) continue; // removed mid-cycle: drop the result
      const contact = (res.contacts && res.contacts[0]) || null;
      const prev = state.trackedData.get(guid) || { contact: null, trail: [] };
      const trail = res.trail && res.trail.length ? mergeTrail(prev.trail, res.trail, config.TRAIL_CAP) : prev.trail;
      state.trackedData.set(guid, { contact: contact || prev.contact, trail });
      if (contact) mergeContacts(state.contacts, [contact], config.MAX_CONTACTS);
    }
  }

  async function runSweep(signal) {
    applyRosterSectorChange();
    const key = loadedKey;
    const since = state.lastNow ? Math.max(0, state.lastNow - config.SINCE_OVERLAP_MS) : 0;
    state.conn = 'polling';
    bus.emit('conn');
    let res;
    try {
      res = await client.get(SWEEP_PATH, sweepParams(rosterSectorOf(state), state.rs, since, false), signal);
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
    if (state.tracked.size) await runTrackedSweeps(signal);
    if (!signal.aborted) {
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

  /** Tiny shared helper: sets the raw flag only, no bus emission - setWorldView and setZone each pick their own event. */
  function applyWorldView(v) {
    state.worldView = Boolean(v);
  }

  /** Adds/re-focuses one player in state.tracked (moves it to the end = most recent), evicting the oldest past TRACK_MAX. */
  function addTracked(p) {
    const player = { guid: p.guid, name: p.name || '' };
    const existed = state.tracked.has(player.guid);
    state.tracked.delete(player.guid);
    if (!existed && state.tracked.size >= config.TRACK_MAX) {
      const oldest = state.tracked.keys().next().value;
      state.tracked.delete(oldest);
      state.trackedData.delete(oldest); // stage 11 U2: no lingering trail/contact for an evicted guid
    }
    state.tracked.set(player.guid, player);
  }

  // The single-select legacy trail (state.trail, filled by track-overlay.js's own /api/v1/trail fetch)
  // is scoped to whichever player is currently focused. Stage 11 U2 stopped resetting it as a side
  // effect of a sector-key change (the roster sector no longer depends on the focused player at all),
  // so it is reset explicitly here whenever the focused guid itself changes - including to null on a
  // full deselect - restoring the "no leftover trail after deselecting / switching players" contract.
  let focusedGuid = null;
  const changed = (event) => {
    applyRosterSectorChange();
    if (event === 'selection') {
      const nowFocused = state.player ? state.player.guid : null;
      if (nowFocused !== focusedGuid) {
        focusedGuid = nowFocused;
        state.trail = [];
      }
    }
    bus.emit(event);
    // Filters and the world/zone view are applied by the page to rows it already holds: no network needed.
    if (event !== 'filters' && event !== 'worldView') scheduler.requestNow();
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
    /**
     * Adds (or re-focuses, if already tracked) one player. Design decision (plan left this ambiguous):
     * past TRACK_MAX the OLDEST tracked player is dropped to make room, rather than refusing the add -
     * less surprising for a type-ahead search/click flow, where the most recent action should always win.
     * Invalid input (no guid) is silently ignored, no event.
     */
    addPlayer(p) {
      if (!p || !p.guid) return;
      addTracked(p);
      changed('selection');
    },
    /** Removes one tracked guid. If it was focused, focus falls back to the next most-recent, or null. No-op if untracked. */
    removePlayer(guid) {
      if (!state.tracked.has(guid)) return;
      state.tracked.delete(guid);
      state.trackedData.delete(guid); // stage 11 U2: prune immediately, don't wait for next sweep
      changed('selection');
    },
    /** Empties the tracked set unconditionally (always emits selection, even if already empty). */
    clearPlayers() {
      state.tracked.clear();
      state.trackedData.clear();
      changed('selection');
    },
    /** Makes an already-tracked guid the focused one (moves it to "most recent") without adding/removing anyone. */
    focusPlayer(guid) {
      if (!state.tracked.has(guid)) return;
      const p = state.tracked.get(guid);
      state.tracked.delete(guid);
      state.tracked.set(guid, p);
      changed('selection');
    },
    /**
     * Backward-compatible single-select (pre-U1 behaviour, kept for search.js/contact-list.js/radar-canvas.js
     * until later tasks move them to the multi-select API): null/falsy clears everyone; a player clears
     * everyone else and tracks just that one. Always emits exactly one 'selection', even clearing nothing.
     */
    setPlayer(player) {
      state.tracked.clear();
      state.trackedData.clear();
      if (player && player.guid) addTracked(player);
      changed('selection');
    },
    /** Drops every tracked player (sector falls back to the zone/all scope); a no-op when nothing is tracked. */
    deselect() {
      if (state.tracked.size === 0) return;
      state.tracked.clear();
      state.trackedData.clear();
      changed('selection');
    },
    /**
     * Stage 14 W7: picking a real zone also exits world view; clearing back to "All Zones" (null)
     * restores it, since the world map is now the default for "no zone picked". Rides along in the
     * same state update as the zone itself, one changed('selection') emission per call - same
     * single-emission discipline as before, just with worldView set alongside state.zone rather than
     * a separate changed('worldView') call (that stays setWorldView's job for the manual toggle).
     */
    setZone(zone) {
      const real = Boolean(zone && Number.isFinite(zone.mapId));
      state.zone = real ? { mapId: zone.mapId, name: zone.name || '' } : null;
      applyWorldView(!real);
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
    /**
     * Stage 14 W6: switches between the world map and the single-zone radar. A pure view-mode flag -
     * never touches player/tracked/zone/filters. Routed through changed('worldView') (not a bare
     * bus.emit) because rosterSectorOf now looks at state.worldView too (world view always wants the
     * 'all' scope roster sweep, regardless of a selected zone), so toggling it must also re-check
     * applyRosterSectorChange and kick the scheduler, same as every other sector-affecting setter here.
     */
    setWorldView(v) {
      applyWorldView(v);
      changed('worldView');
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
    keydown = (e) => {
      if (!e || e.key !== ESCAPE_KEY) return;
      bus.emit('escape');
      ctx.deselect();
    };
    doc.addEventListener('keydown', keydown);
  }

  function stop() {
    scheduler.stop();
    if (keydown && doc.removeEventListener) doc.removeEventListener('keydown', keydown);
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
