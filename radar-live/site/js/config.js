// Page configuration. Edit here (or host a different copy) to point the page at another API.
export const CONFIG = Object.freeze({
  API_BASE: '', // '' = same origin; e.g. 'http://192.168.1.20:8787' for a separate static host
  SWEEP_INTERVAL_MS: 60_000, // the poll period
  SINCE_OVERLAP_MS: 5_000, // since = lastNow - overlap
  DEBOUNCE_MS: 300, // user input -> immediate sweep delay
  RETRY_BASE_MS: 5_000, // first retry delay after an error, doubling
  MAX_BACKOFF_MS: 300_000, // retry delay cap (5 min)
  STALE_AFTER_MS: 300_000, // dark grey after 5 min
  GONE_AFTER_MS: 900_000, // removed from the live view after 15 min
  COUNTDOWN_TICK_MS: 1_000,
  SEARCH_DEBOUNCE_MS: 250,
  SEARCH_LIMIT: 50,
  BOUNTY_LIMIT: 50,
  HISTORY_ROWS_MAX: 200,
  MAX_CONTACTS: 5_000, // bound on rows kept in the browser
  TRAIL_CAP: 5_000,
  MAPS_BASE_URL: 'maps/', // folder holding zone images and index.json (optional)
  MAPS_INDEX_FILE: 'index.json',
  VIEW_KEY_STORAGE: 'radarLive.viewKey',
  PREFS_STORAGE: 'radarLive.prefs',
});

/**
 * A maps folder must stay on this origin: plain path characters only, no scheme, no "//" (protocol-relative),
 * no backslashes, no ".." segments. Anything else is ignored so a crafted link can not point the page at a foreign host.
 */
export function isSafeMapsFolder(v) {
  return typeof v === 'string' && v.length > 0 && v.length <= 100 && /^[A-Za-z0-9_./-]+$/.test(v) &&
    !v.startsWith('//') && !v.includes('..') && !v.includes('//');
}

/** Returns CONFIG with the URL overrides that are safe to accept (only the map folder). */
export function resolveConfig(search = '') {
  const params = new URLSearchParams(search);
  const maps = params.get('maps');
  return Object.freeze({ ...CONFIG, ...(isSafeMapsFolder(maps) ? { MAPS_BASE_URL: maps } : {}) });
}
