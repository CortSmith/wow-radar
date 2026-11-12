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
 * The sector for the always-on ROSTER sweep. It is always scope=all: zone, world view, filters and tracked
 * players are applied by the page itself (view-model.js, contact-list.js, world-map.js), so switching any of
 * them re-renders instantly from the rows already loaded and never discards them or waits for the network.
 */
export function rosterSectorOf() {
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
