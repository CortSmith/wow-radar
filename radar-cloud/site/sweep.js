'use strict';
/**
 * sweep.js - single responsibility: the cloud sweep loop (one "sweep" = one GET /api/poll) and merging of its delta
 * rows.
 *
 * A sweep looks at one Sector: a player trail < a zone (+ friendly/hostile group) < everything. The loop is adaptive:
 * fast while the user acts or data changes, slow when quiet, exponential backoff on errors, paused while the tab is
 * hidden.
 * Timers, clock, random and visibility are injected so the loop is deterministic under test. No DOM.
 *
 * Public API (Radar.sweep):
 *  - pickSector(ui) -> Sector|null     what to sweep, from the UI state (null when nothing visible needs data)
 *  - sectorKey(sector) -> string       changes when the sweep must restart from a full read
 *  - mergeRows(contactsByGuid, rows) -> count    delta rows into a Map(guid -> row); newer lastSeen wins field-wise
 *  - mergeFixes(have, add) -> Fix[]    trail fixes sorted by t, deduped on t/x/y
 *  - normalizeRow(row)                 delta rows may use x/y/mapId/zone instead of lastX/lastY/lastMap/lastZone
 *  - Sweeper(deps): start(), stop(), poke(immediate), onVisibility(), reset()
 *      deps: {api(path), getScope() -> Sector|null, onResponse(resp, sector, initial), onError(err), setTimeout,
 * clearTimeout,
 *             now(), random(), hidden(), getTrackTail() -> latest trail fix time}
 *  - BASE_MS, SLOW_MS, UNCHANGED_MS, OVERLAP_MS (re-exported from config)
 */
(function (g) {
  const dep = (name, file) =>
    typeof require !== 'undefined' && typeof module !== 'undefined' ? require(file) : g.Radar[name];
  const { qs } = dep('format', './format.js');
  const S = dep('config', './config.js').sweep;
  const { BASE_MS, SLOW_MS, UNCHANGED_MS, OVERLAP_MS } = S;

  /**
   * Choose what to sweep from the UI state: a followed contact's trail, else the viewed chart (+ group), else
   * everything.
   * @param {{rs: string, track: {guid: string, fixes?: Fix[]}|null, viewMapId: number|null, showBlue: boolean,
   * showRed: boolean}} ui
   * @returns {Sector|null} null when a chart is viewed but both factions are hidden (nothing to fetch)
   */
  function pickSector(ui) {
    if (ui.track)
      return { scope: 'player', rs: ui.rs, guid: ui.track.guid, track: ui.track.fixes !== undefined ? 1 : 0 };
    if (ui.viewMapId !== null && ui.viewMapId !== undefined) {
      if (!ui.showBlue && !ui.showRed) return null;
      const grp = ui.showBlue && ui.showRed ? 'any' : ui.showBlue ? 'friendly' : 'hostile';
      return { scope: 'zone', rs: ui.rs, mapId: ui.viewMapId, grp };
    }
    return { scope: 'all', rs: ui.rs };
  }
  const sectorKey = s => [s.rs, s.scope, s.guid || '', s.mapId || '', s.grp || '', s.track || ''].join('|');

  /**
   * Delta rows may carry x/y/mapId/zone instead of lastX/lastY/lastMap/lastZone; map them to the stored names.
   * @param {number} r row or response
   * @returns {*} the computed value
   */
  function normalizeRow(r) {
    const o = Object.assign({}, r);
    if (o.x !== undefined && o.lastX === undefined) {
      o.lastX = o.x;
      o.lastY = o.y;
      o.lastMap = o.mapId;
      o.lastZone = o.zone;
    }
    return o;
  }

  /**
   * Merge delta rows into a Map(guid -> Contact row). A row older than the held one is ignored; otherwise fields are
   * overlaid.
   * @param {Map<string, Contact>} contactsByGuid
   * @param {Contact[]} rows
   * @returns {number} rows merged
   */
  function mergeRows(contactsByGuid, rows) {
    let merged = 0;
    for (const raw of rows || []) {
      if (!raw || !raw.guid) continue;
      const r = normalizeRow(raw),
        old = contactsByGuid.get(r.guid);
      if (old && old.lastSeen > r.lastSeen) continue;
      contactsByGuid.set(r.guid, old ? Object.assign({}, old, r) : r);
      merged++;
    }
    return merged;
  }

  const fixKey = f => f.t + '|' + f.x + '|' + f.y;
  /**
   * Merge trail fixes, sorted by time and deduplicated on t/x/y.
   * @param {Fix[]} have
   * @param {Fix[]} add
   * @returns {Fix[]} a new array (or `have` itself when there is nothing to add)
   */
  function mergeFixes(have, add) {
    if (!add || !add.length) return have;
    const seen = new Set(have.map(fixKey));
    const out = have.slice();
    for (const f of add) {
      const k = fixKey(f);
      if (!seen.has(k)) {
        seen.add(k);
        out.push(f);
      }
    }
    out.sort((a, b) => a.t - b.t);
    return out;
  }

  class Sweeper {
    /**
     * Wire the injected api, timers, clock, random and visibility; nothing runs until start().
     * @param {Object} deps the deps
     */
    constructor(deps) {
      this.d = deps;
      this.timer = null;
      this.inflight = false;
      this.running = false;
      this.paused = false;
      this.key = null;
      this.lastNow = 0;
      this.v = '';
      this.interval = BASE_MS;
      this.lastChange = deps.now();
      this.errors = 0;
      this.again = false;
      this.requests = [];
    }
    start() {
      if (this.running) return;
      this.running = true;
      this.lastChange = this.d.now();
      this.schedule(0);
    }
    /**
     * Stop the loop and clear its timer.
     */
    stop() {
      this.running = false;
      this.clear();
    }
    /**
     * Clear the pending timer or the current selection.
     */
    clear() {
      if (this.timer !== null) {
        this.d.clearTimeout(this.timer);
        this.timer = null;
      }
    }
    /**
     * Spread a delay by the configured random ratio.
     * @param {number} ms milliseconds
     * @returns {*} the computed value
     */
    jitter(ms) {
      return Math.max(0, Math.round(ms * (1 + (this.d.random() * 2 - 1) * S.JITTER_RATIO)));
    }
    /**
     * Arm the next timer (jittered), unless stopped or the tab is hidden.
     * @param {number} ms milliseconds
     */
    schedule(ms) {
      this.clear();
      if (!this.running) return;
      if (this.d.hidden()) {
        this.paused = true;
        return;
      }
      this.paused = false;
      this.timer = this.d.setTimeout(
        () => {
          this.timer = null;
          this.tick();
        },
        ms === 0 ? 0 : this.jitter(ms),
      );
    }
    /**
     * User input / sector change / visibility: back to the fast cadence. immediate=true sweeps right away.
     * @param {boolean} immediate the immediate
     */
    poke(immediate) {
      if (!this.running) return;
      this.interval = BASE_MS;
      this.lastChange = this.d.now();
      if (this.inflight) {
        this.again = true;
        return;
      }
      this.schedule(immediate ? 0 : BASE_MS);
    }
    /**
     * Pause while the tab is hidden, resume (fast) when visible again.
     */
    onVisibility() {
      if (this.d.hidden()) {
        this.clear();
        this.paused = true;
      } else if (this.running) {
        this.poke(true);
      }
    }
    /**
     * Forget the server watermark so the next sweep is a full read (the page just loaded fresh rows itself).
     */
    reset() {
      this.key = null;
      this.lastNow = 0;
      this.v = '';
    }
    /**
     * `since` is rewound by OVERLAP_MS: rows committed while the previous request was in flight must not fall between
     * two sweeps. `v` is the tripwire version; the server answers {changed:false} cheaply while it is unchanged.
     * @param {Object} sector sweep sector
     * @returns {*} the computed value
     */
    buildPath(sector) {
      const key = sectorKey(sector);
      if (key !== this.key) {
        this.key = key;
        this.lastNow = 0;
        this.v = '';
      }
      const q = {
        rs: sector.rs,
        scope: sector.scope,
        guid: sector.guid,
        mapId: sector.mapId,
        grp: sector.grp,
        since: this.lastNow ? Math.max(0, this.lastNow - OVERLAP_MS) : 0,
        v: this.v,
      };
      if (sector.scope === 'player') {
        q.track = sector.track ? 1 : 0;
        if (sector.track && this.d.getTrackTail) q.tsince = this.d.getTrackTail() || 0;
      }
      return '/api/poll?' + qs(q);
    }
    /**
     * Applies one response; returns the delay before the next sweep.
     * @param {number} r row or response
     * @param {Object} sector sweep sector
     * @param {Object} initial the initial
     * @returns {*} the computed value
     */
    handleResponse(r, sector, initial) {
      if (r && r.now) this.lastNow = r.now;
      if (r && r.v !== undefined) this.v = String(r.v);
      if (r && r.changed) {
        this.interval = BASE_MS;
        this.lastChange = this.d.now();
        this.d.onResponse(r, sector, initial);
      } else if (this.d.now() - this.lastChange >= UNCHANGED_MS) this.interval = SLOW_MS;
      return this.interval;
    }
    /**
     * Exponential backoff: BASE_MS * 2^errors, capped at SLOW_MS.
     * @param {Object} e the DOM event
     * @returns {*} the computed value
     */
    handleError(e) {
      this.errors++;
      if (this.d.onError) this.d.onError(e);
      return Math.min(SLOW_MS, BASE_MS * Math.pow(S.BACKOFF_BASE, this.errors));
    }
    /**
     * Run one cycle: ask the server, merge the answer, schedule the next one.
     * @returns {Promise<void>} resolves when done
     */
    async tick() {
      if (!this.running || this.inflight) return;
      if (this.d.hidden()) {
        this.paused = true;
        return;
      }
      const sector = this.d.getScope();
      if (!sector) {
        this.schedule(this.interval);
        return;
      }
      this.inflight = true;
      let delay;
      try {
        const path = this.buildPath(sector);
        this.requests.push(path);
        const initial = this.lastNow === 0;
        const r = await this.d.api(path);
        this.errors = 0;
        delay = this.handleResponse(r, sector, initial);
      } catch (e) {
        delay = this.handleError(e);
      }
      this.inflight = false;
      if (this.again) {
        this.again = false;
        this.interval = BASE_MS;
        delay = BASE_MS;
      }
      this.schedule(delay);
    }
  }

  const api = {
    BASE_MS,
    SLOW_MS,
    UNCHANGED_MS,
    OVERLAP_MS,
    pickSector,
    sectorKey,
    normalizeRow,
    mergeRows,
    mergeFixes,
    Sweeper,
  };
  (g.Radar || (g.Radar = {})).sweep = api;
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
})(typeof self !== 'undefined' ? self : globalThis);
