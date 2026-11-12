/** Collapses a (possibly unsorted) trail into ordered cross-zone visits: [{mapId, enteredAt, leftAt, lastFix}, ...].
 *  Consecutive fixes sharing the same mapId merge into one visit; non-adjacent runs of the same mapId stay separate.
 *  Fixes missing mapId or t are skipped (can't place them chronologically or attribute them to a zone). */
export function worldPath(trail) {
  const sorted = [...(trail || [])]
    .filter((f) => f && f.mapId != null && typeof f.t === 'number' && !Number.isNaN(f.t))
    .sort((a, b) => a.t - b.t);

  const visits = [];
  for (const fix of sorted) {
    const last = visits[visits.length - 1];
    if (last && last.mapId === fix.mapId) {
      last.leftAt = fix.t;
      last.lastFix = fix;
    } else {
      visits.push({ mapId: fix.mapId, enteredAt: fix.t, leftAt: fix.t, lastFix: fix });
    }
  }
  return visits;
}
