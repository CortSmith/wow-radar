import { CONFIG } from './config.js';
import { classify } from './staleness.js';

/** Which map the radar shows: the picked zone, the selected player's map, else the busiest live map. */
export function chooseViewMap(state, now, cfg = CONFIG) {
  if (state.zone && Number.isFinite(state.zone.mapId)) return state.zone.mapId;
  if (state.player) {
    const c = state.contacts.get(state.player.guid);
    if (c && c.posTime > 0) return c.mapId;
  }
  const counts = new Map();
  for (const c of state.contacts.values()) {
    if (c.posTime > 0 && classify(c, now, cfg) !== 'gone') counts.set(c.mapId, (counts.get(c.mapId) || 0) + 1);
  }
  let best = null;
  for (const [id, n] of counts) {
    if (best === null || n > counts.get(best)) best = id;
  }
  return best;
}

/** True when a contact belongs to the zone view: no zone picked/world view shows everyone, else the picked zone. */
export function inViewZone(state, contact) {
  if (state.worldView || !state.zone || !Number.isFinite(state.zone.mapId)) return true;
  return contact.mapId === state.zone.mapId;
}

/** Colour class of a contact: friendly blue, hostile/neutral/unknown red, stale grey. */
export function colorOf(contact, status) {
  if (status === 'stale') return 'stale';
  return contact.reaction === 'friendly' ? 'friendly' : 'hostile';
}

/** Drawable contacts on mapId after the Blue/Red/Stale filters. */
export function toBlips(state, now, mapId, cfg = CONFIG) {
  const out = [];
  const pinned = state.player ? state.player.guid : '';
  for (const c of state.contacts.values()) {
    const status = classify(c, now, cfg);
    if (c.posTime <= 0 || c.mapId !== mapId) continue;
    if (status === 'gone' && c.guid !== pinned) continue;
    if (status === 'stale' && !state.filters.stale && c.guid !== pinned) continue;
    if (c.reaction === 'friendly' ? !state.filters.friendly : !state.filters.hostile) continue;
    out.push({ guid: c.guid, name: c.name, x: c.x, y: c.y, status, color: colorOf(c, status), contact: c });
  }
  return out;
}
