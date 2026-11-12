// Sector selection -> the sweep query (PLAN.md section 4).

/**
 * Blue/Red checkboxes -> grp parameter. Only Blue-only maps to a server group. Red means hostile, neutral and unknown,
 * which no single server group covers ('hostile' leaves out neutral/unknown), so Red-only asks for everything and the
 * page hides the blue ones itself.
 */
export function groupParam(filters) {
  if (filters.friendly && !filters.hostile) return 'friendly';
  return 'any';
}

/** The sector implied by the page state: player beats zone beats all. */
export function sectorOf(state) {
  if (state.player && state.player.guid) return { scope: 'player', guid: state.player.guid };
  if (state.zone && Number.isFinite(state.zone.mapId)) {
    return { scope: 'zone', mapId: state.zone.mapId, grp: groupParam(state.filters) };
  }
  return { scope: 'all' };
}

/**
 * Stage 11 U2: the sector for the always-on ROSTER sweep. Unlike sectorOf, this never takes the
 * player branch - state.tracked (and therefore state.player) is irrelevant to the roster stream, so
 * adding/removing a tracked player can never look like a sector change to this function.
 */
export function rosterSectorOf(state) {
  // Stage 14 W6: the world map needs every zone's contacts (it draws per-zone friendly/hostile chips
  // for the whole map), so while state.worldView is true the roster sweep is never narrowed to the
  // selected zone, even if one is still selected underneath. Zone view (worldView false, the default)
  // keeps the original U2 behaviour unchanged: a selected zone still scopes the roster sweep to it.
  if (!state.worldView && state.zone && Number.isFinite(state.zone.mapId)) {
    return { scope: 'zone', mapId: state.zone.mapId, grp: groupParam(state.filters) };
  }
  return { scope: 'all' };
}

/** Query parameters for GET /api/v1/sweep. rs 'default' (or '') is omitted. */
export function sweepParams(sector, rs, since, wantTrail) {
  const p = {};
  if (rs && rs !== 'default') p.rs = rs;
  p.scope = sector.scope;
  if (sector.scope === 'player') p.guid = sector.guid;
  if (sector.scope === 'zone') {
    p.mapId = sector.mapId;
    p.grp = sector.grp;
  }
  p.since = since;
  if (sector.scope === 'player' && wantTrail) p.trail = 1;
  return p;
}

/** Identity of a sector: when it changes the loaded rows are discarded and since restarts at 0. */
export function sectorKey(sector, rs) {
  return JSON.stringify([rs || 'default', sector.scope, sector.guid ?? '', sector.mapId ?? '', sector.grp ?? '']);
}

/**
 * Stage 11 U2: identity of the tracked set, independent of sectorKey/rosterSectorOf. core.js uses this
 * only to notice that a specific guid dropped out of state.tracked (to prune its trackedData entry) -
 * never to decide whether to discard the roster.
 */
export function trackKey(trackedGuids) {
  return JSON.stringify([...trackedGuids].sort());
}
