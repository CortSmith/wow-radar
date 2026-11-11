'use strict';
/**
 * analyzer.js - single responsibility: bounty analysis (killing blows and assists) from deaths of friendly players
 * (port of go-radar bounty/analyzer.go).
 *
 * Public API (Radar.analyzer): Analyzer(onDeath): setFile(name), reset(), apply(event). onDeath receives
 * {t, victimGuid, victimName, zone, killer, assists[], hasPos, x, y, mapId}. Memory is capped (config.engine.MAX_*):
 * old victims, known units and pet owners are evicted. No DOM.
 */
(function (g) {
  const dep = (name, file) =>
    typeof require !== 'undefined' && typeof module !== 'undefined' ? require(file) : g.Radar[name];
  const P = dep('parser', './parser.js');
  const E = dep('config', './config.js').engine;

  const ASSIST_WINDOW = E.ASSIST_WINDOW_MS;
  const KILLING_BLOW_WINDOW = E.KILLING_BLOW_WINDOW_MS;
  const DUPLICATE_DEATH_WINDOW = E.DUPLICATE_DEATH_MS;
  const MAX_HITS = E.MAX_HITS;
  const MAX_VICTIMS = E.MAX_VICTIMS;
  const MAX_KNOWN = E.MAX_KNOWN;
  const MAX_OWNERS = E.MAX_OWNERS;
  const MAX_LAST = E.MAX_LAST;

  /** @param {{guid: string, flags: number}|null} u @returns {boolean} a friendly player unit */
  const isFriendlyPlayer = u => !!u && P.kindOf(u.guid) === P.KIND_PLAYER && P.reactionOf(u.flags) === 'friendly';

  /**
   * Make room in a bounded map: at the cap, drop the oldest half (insertion order).
   * @param {Map} map
   * @param {number} cap
   */
  function dropOldestHalf(map, cap) {
    if (map.size < cap) return;
    let dropped = 0;
    for (const k of map.keys()) {
      map.delete(k);
      if (++dropped >= cap / 2) break;
    }
  }

  class Analyzer {
    /**
     * Set up empty windows and caps; onDeath receives each resolved death (killer, assists, position in yards).
     * @param {Function} onDeath the on death
     */
    constructor(onDeath) {
      this.onDeath = onDeath;
      this.namer = new P.Namer('');
      this.file = null;
      this.reset();
    }
    /**
     * Forget accumulated state so the next cycle starts from scratch.
     */
    reset() {
      this.wins = new Map();
      this.known = new Map();
      this.owners = new Map();
      this.last = new Map();
      this.zone = '';
      this.maps = new Map();
    }
    /**
     * Switch to a new log file (resets windows when the name changes).
     * @param {string} name name
     */
    setFile(name) {
      if (name === this.file) return;
      this.file = name;
      this.reset();
    }
    /**
     * Apply one parsed log event.
     * @param {Object} ev parsed log event
     */
    apply(ev) {
      switch (ev.type) {
        case 'ZONE_CHANGE':
          if (ev.zone) this.zone = ev.zone;
          return;
        case 'MAP_CHANGE':
          if (ev.map) {
            if (this.maps.size > E.MAX_MAPS) this.maps = new Map();
            this.maps.set(ev.map.id, ev.map.name);
          }
          return;
        case 'COMBAT_LOG_VERSION':
          return;
      }
      if (!ev.src) return;
      this.learn(ev);
      if (ev.type === 'UNIT_DIED') {
        if (ev.unconscious) return;
        this.death(ev, null);
      } else if (ev.type === 'PARTY_KILL' || ev.type === 'SPELL_INSTAKILL') this.death(ev, ev.src);
      else if (ev.isDamage) this.hit(ev);
    }
    /** Remember who is who: player reactions and pet owners (from info blocks and summons). @param {Object} ev */
    learn(ev) {
      for (const u of [ev.src, ev.dst]) this.rememberUnit(u);
      if (ev.info && ev.info.owner && P.kindOf(ev.info.guid) !== P.KIND_PLAYER)
        this.setOwner(ev.info.guid, ev.info.owner);
      if (ev.type === 'SPELL_SUMMON' && P.kindOf(ev.src.guid) === P.KIND_PLAYER && ev.dst.guid) {
        this.setOwner(ev.dst.guid, ev.src.guid);
      }
    }
    /** Record a player unit's name and reaction; units without flags carry no reaction information. @param {Object} u
     * */
    rememberUnit(u) {
      if (P.kindOf(u.guid) !== P.KIND_PLAYER) return;
      if (u.name) this.namer.observe(u.name);
      const reaction = P.reactionOf(u.flags);
      if (reaction === 'unknown' && u.flags === 0) return;
      dropOldestHalf(this.known, MAX_KNOWN);
      this.known.set(u.guid, { raw: u.name, hostile: reaction === 'hostile', friendly: reaction === 'friendly' });
    }
    /** @param {string} pet @param {string} owner */
    setOwner(pet, owner) {
      dropOldestHalf(this.owners, MAX_OWNERS);
      this.owners.set(pet, owner);
    }
    /** Make room for a new victim window by dropping the least recently touched one. */
    evictStalestVictim() {
      let oldest = null;
      let oldestTouched = 0;
      for (const [guid, w] of this.wins) {
        if (oldest === null || w.touched < oldestTouched) {
          oldest = guid;
          oldestTouched = w.touched;
        }
      }
      this.wins.delete(oldest);
    }
    /** The damage window of a friendly victim, created on demand. @param {string} guid @param {number} t */
    victimWindow(guid, t) {
      let w = this.wins.get(guid);
      if (w) return w;
      if (this.wins.size >= MAX_VICTIMS) this.evictStalestVictim();
      w = { hits: [], touched: t };
      this.wins.set(guid, w);
      return w;
    }
    /** One damage hit against a friendly player: remember who dealt it (for assists and the killing blow). @param
     * {Object} ev */
    hit(ev) {
      if (!isFriendlyPlayer(ev.dst)) return;
      const [credit, friend] = this.creditOf(ev.src);
      const h = { t: ev.t, credit, friend, hasPos: false, posOf: '', x: 0, y: 0, mapId: 0 };
      if (ev.info && ev.info.hasPos) {
        Object.assign(h, { hasPos: true, posOf: ev.info.guid, x: ev.info.x, y: ev.info.y, mapId: ev.info.mapId });
      }
      const w = this.victimWindow(ev.dst.guid, ev.t);
      w.touched = ev.t;
      w.hits = w.hits.filter(o => ev.t - o.t <= ASSIST_WINDOW);
      w.hits.push(h);
      if (w.hits.length > MAX_HITS) w.hits.splice(0, w.hits.length - MAX_HITS);
    }
    /**
     * Who gets bounty credit for damage from src: [hostile player guid or "", fromFriendlyPlayer].
     * @param {Object} src the src
     * @returns {*} the computed value
     */
    creditOf(src) {
      switch (P.kindOf(src.guid)) {
        case P.KIND_PLAYER: {
          const r = P.reactionOf(src.flags);
          return r === 'hostile' ? [src.guid, false] : ['', r === 'friendly'];
        }
        case P.KIND_PET:
        case P.KIND_NPC: {
          const owner = this.owners.get(src.guid);
          if (!owner) return ['', false];
          const k = this.known.get(owner);
          if (k) return k.hostile ? [owner, false] : ['', k.friendly];
        }
      }
      return ['', false];
    }
    /**
     * Describe a credited player (guid, raw and display name).
     * @param {string} guid unit guid
     * @returns {*} the computed value
     */
    participant(guid) {
      const raw = (this.known.get(guid) || {}).raw || '';
      return { guid, raw, display: raw ? this.namer.display(raw) : '' };
    }
    /**
     * Record a death time and report whether it repeats a death of the same unit within DUPLICATE_DEATH_MS
     * (UNIT_DIED and PARTY_KILL describe the same death).
     * @param {string} guid victim guid
     * @param {number} t event time (unix ms)
     * @returns {boolean} true when this is a duplicate that must be ignored
     */
    isDuplicateDeath(guid, t) {
      const previous = this.last.get(guid);
      if (previous !== undefined && Math.abs(t - previous) <= DUPLICATE_DEATH_WINDOW) return true;
      if (this.last.size >= MAX_LAST) this.last = new Map();
      this.last.set(guid, t);
      return false;
    }
    /**
     * Take (and forget) the hits on a victim that fall inside the assist window before the death.
     * @param {string} guid victim guid
     * @param {number} t death time (unix ms)
     * @returns {Object[]} hits within ASSIST_WINDOW_MS before t, oldest first
     */
    takeHits(guid, t) {
      const w = this.wins.get(guid);
      if (!w) return [];
      this.wins.delete(guid);
      return w.hits.filter(h => t - h.t >= 0 && t - h.t <= ASSIST_WINDOW);
    }
    /**
     * Killing blow: the explicit killer of a PARTY_KILL, else the newest hit if it is within KILLING_BLOW_WINDOW_MS.
     * @param {Object} ev death event
     * @param {Object|null} pkSrc source of a PARTY_KILL / instakill, null for UNIT_DIED
     * @param {Object[]} hits hits inside the assist window
     * @returns {[string, boolean]} [killer guid or "", true when a friendly player (no bounty) got the kill]
     */
    findKiller(ev, pkSrc, hits) {
      if (pkSrc) return this.creditOf(pkSrc);
      if (!hits.length) return ['', false];
      let last = hits[hits.length - 1];
      for (const h of hits) if (h.t >= last.t) last = h;
      return ev.t - last.t <= KILLING_BLOW_WINDOW ? [last.credit, last.friend] : ['', false];
    }
    /**
     * Fresh death record for the victim; position and credits are filled in afterwards.
     * @param {Object} ev death event
     * @param {{guid: string, name: string}} victim destination unit
     * @returns {Object} death with empty assists and no position
     */
    newDeath(ev, victim) {
      return {
        t: ev.t,
        victimGuid: victim.guid,
        victimName: this.namer.display(victim.name),
        zone: this.zone,
        killer: null,
        assists: [],
        hasPos: false,
        x: 0,
        y: 0,
        mapId: 0,
      };
    }
    /**
     * Name the killer and the distinct assisting players of a death (each credited once).
     * @param {Object} d death record, mutated
     * @param {string} killer killer guid or ""
     * @param {Object[]} hits hits inside the assist window
     */
    addCredits(d, killer, hits) {
      if (killer) d.killer = this.participant(killer);
      const seen = new Set([killer, '']);
      for (const h of hits) {
        if (seen.has(h.credit)) continue;
        seen.add(h.credit);
        d.assists.push(this.participant(h.credit));
      }
    }
    /**
     * Best known position of the death: the killer's own hit position beats others, then the newest hit.
     * @param {Object} d death record, mutated (hasPos, x, y in world yards, mapId, zone)
     * @param {string} killer killer guid or ""
     * @param {Object[]} hits hits inside the assist window
     */
    locate(d, killer, hits) {
      let best = null;
      for (const h of hits) {
        if (!h.hasPos) continue;
        const own = killer !== '' && h.posOf === killer;
        const bestOwn = best !== null && killer !== '' && best.posOf === killer;
        if (best === null || (own && !bestOwn) || (own === bestOwn && h.t >= best.t)) best = h;
      }
      if (!best) return;
      Object.assign(d, { hasPos: true, x: best.x, y: best.y, mapId: best.mapId });
      const zone = this.maps.get(best.mapId);
      if (zone) d.zone = zone;
    }
    /**
     * A friendly player died: work out who gets bounty credit and report it through onDeath.
     * @param {Object} ev UNIT_DIED, PARTY_KILL or SPELL_INSTAKILL event
     * @param {Object|null} pkSrc killer unit for the latter two, null for UNIT_DIED
     */
    death(ev, pkSrc) {
      const victim = ev.dst;
      if (!isFriendlyPlayer(victim) || this.isDuplicateDeath(victim.guid, ev.t)) return;
      const hits = this.takeHits(victim.guid, ev.t);
      const [killer, friendlyKill] = this.findKiller(ev, pkSrc, hits);
      const d = this.newDeath(ev, victim);
      if (!friendlyKill) this.addCredits(d, killer, hits);
      this.locate(d, killer, hits);
      this.onDeath(d);
    }
  }

  const api = { Analyzer };
  (g.Radar || (g.Radar = {})).analyzer = api;
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
})(typeof self !== 'undefined' ? self : globalThis);
