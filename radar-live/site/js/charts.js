const norm = (s) => String(s || '').toLowerCase().replace(/[^a-z0-9]/g, '');

/** Entry of maps/index.json whose name matches a chart name (case/punctuation insensitive), or null. */
export function findMapEntry(index, name) {
  const want = norm(name);
  if (!index || !index.zones || !want) return null;
  for (const entry of Object.values(index.zones)) {
    if (norm(entry.name) === want) return entry;
  }
  return null;
}
