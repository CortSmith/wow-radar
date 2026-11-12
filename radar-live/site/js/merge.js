import { CONFIG } from './config.js';

/** Merges delta rows into the Map by guid (newer lastSeen wins) and evicts the oldest beyond cap. */
export function mergeContacts(map, incoming, cap = CONFIG.MAX_CONTACTS) {
  for (const c of incoming || []) {
    const old = map.get(c.guid);
    if (!old || c.lastSeen >= old.lastSeen) map.set(c.guid, c);
  }
  if (map.size > cap) {
    const oldest = [...map.values()].sort((a, b) => a.lastSeen - b.lastSeen).slice(0, map.size - cap);
    for (const c of oldest) map.delete(c.guid);
  }
  return map;
}

/** Appends fixes to a trail: dedupe by t, ascending, keep the newest cap. */
export function mergeTrail(trail, fixes, cap = CONFIG.TRAIL_CAP) {
  const byTime = new Map();
  for (const f of [...(trail || []), ...(fixes || [])]) byTime.set(f.t, f);
  const sorted = [...byTime.values()].sort((a, b) => a.t - b.t);
  return sorted.length > cap ? sorted.slice(sorted.length - cap) : sorted;
}
