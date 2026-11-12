// Stage 11 U5: pure tooltip-row builder for a hovered radar target (a live pip or a historical trail
// point) plus the pure helper that re-locates a hovered point inside a freshly rebuilt scene. No DOM
// here - radar-canvas.js renders the rows as plain text and draw.js paints the hover ring/lines from
// the resolved point this module hands back.

/**
 * True when `target` is a trail/history point rather than a live pip. Pips (scene.js) always carry
 * `contact`; trail points (scene.js) always carry `kind` and never carry `contact`. That is the one
 * reliable distinguishing field between the two shapes scene.js produces.
 */
export function isTrailPoint(target) {
  return Boolean(target) && target.kind !== undefined && target.contact === undefined;
}

function liveRows(target, ctx) {
  const { MSG, fmt, now, fmtAge } = ctx;
  const c = target.contact || {};
  const head = `${target.name}${c.zone ? ` - ${c.zone}` : ''}`;
  const line = fmt(MSG.tipLine, { reaction: c.reaction, state: target.status, level: c.level || '?' });
  const seen = fmt(MSG.tipSeen, { age: fmtAge(now() - c.lastSeen) });
  return [head, line, seen];
}

/**
 * The trail currently drawn (scene.js's `trail`) is always the focused tracked player's own history,
 * so a hit trail point's owner is `state.player`. Trail fixes carry only {t,mapId,x,y,kind} - no
 * name/zone/level/state - so those come from state.trackedData (falling back to the roster contact)
 * and no reaction/state row is shown: historical state/reaction is not stored (stage-11-radar-ux.md 3.5).
 */
function historyRows(target, ctx) {
  const { MSG, fmt, now, fmtAge, fmtTime, state } = ctx;
  const guid = state.player ? state.player.guid : null;
  const tracked = guid ? state.trackedData.get(guid) : null;
  const contact = (tracked && tracked.contact) || (guid ? state.contacts.get(guid) : null) || null;
  const name = (contact && contact.name) || guid || MSG.tipUnknown;
  const zone = contact && contact.zone ? contact.zone : '';
  const head = `${name}${zone ? ` - ${zone}` : ''}`;
  const time = fmt(MSG.tipHistory, { time: fmtTime(target.t), age: fmtAge(now() - target.t) });
  const kind = target.kind === 'approx' ? MSG.tipKindApprox : MSG.tipKindExact;
  return [head, time, kind];
}

/**
 * Builds the tooltip's row strings for a hovered target (pure, no DOM, easy to unit test).
 * target: a pip ({guid,name,contact,status,...}, from scene.js `pips`) or a trail point
 *   ({t,mapId,x,y,kind,...}, from scene.js `trail`).
 * ctx: {MSG, fmt, now(): ms, fmtAge(ms): string, fmtTime(ms): string, state: {player, trackedData, contacts}}.
 * Returns a plain array of row strings (never HTML).
 */
export function tooltipModel(target, ctx) {
  if (!target) return [];
  return isTrailPoint(target) ? historyRows(target, ctx) : liveRows(target, ctx);
}

/**
 * Re-locates a hovered point inside a freshly built scene from a stable key, since buildScene() makes
 * new pip/trail objects every call (so holding on to the originally-hit object would go stale the next
 * time the canvas redraws for an unrelated reason, e.g. a sweep). key: {type:'pip', guid} or
 * {type:'trail', t, mapId}, or null/undefined for no hover. Returns {sx, sy, isTrail, prev, next} or
 * null when the key's point is no longer in the scene (e.g. it scrolled off after a sweep).
 */
export function resolveHoverPoint(scene, key) {
  if (!key || !scene) return null;
  if (key.type === 'pip') {
    const p = scene.pips.find((x) => x.guid === key.guid);
    return p ? { sx: p.sx, sy: p.sy, isTrail: false, prev: null, next: null } : null;
  }
  const idx = scene.trail.findIndex((x) => x.t === key.t && x.mapId === key.mapId);
  if (idx < 0) return null;
  const p = scene.trail[idx];
  return { sx: p.sx, sy: p.sy, isTrail: true, prev: scene.trail[idx - 1] || null, next: scene.trail[idx + 1] || null };
}

/** The stable hover key for a hit-tested target (see resolveHoverPoint), or null for no hover. */
export function hoverKeyFor(target) {
  if (!target) return null;
  return isTrailPoint(target) ? { type: 'trail', t: target.t, mapId: target.mapId } : { type: 'pip', guid: target.guid };
}
