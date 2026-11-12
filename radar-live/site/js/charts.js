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

const SAFE_IMAGE_FILE = /^[A-Za-z0-9_-][A-Za-z0-9_.-]*\.(webp|png)$/;

/**
 * {url, w, h} for a chart's zone image, or null. The file name must be a plain name inside the maps folder
 * (no path, no scheme), so a crafted index.json can not point the page at another origin or folder.
 */
export function mapImageFor(index, name, baseUrl) {
  const entry = findMapEntry(index, name);
  if (!entry || typeof entry.file !== 'string' || !SAFE_IMAGE_FILE.test(entry.file)) return null;
  if (!(entry.w > 0) || !(entry.h > 0)) return null;
  return { url: baseUrl + entry.file, w: entry.w, h: entry.h };
}
