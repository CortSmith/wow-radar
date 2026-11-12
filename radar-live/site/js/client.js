// Thin fetch wrapper for the read API.

/** An API or network failure. status 0 = could not reach the server. */
export class ApiError extends Error {
  constructor(status, code, message) {
    super(message || code);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
  }
}

/** base + path + query string; empty/undefined values are skipped. */
export function buildUrl(base, path, params = {}) {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== null && v !== '') q.set(k, String(v));
  }
  const qs = q.toString();
  return `${base}${path}${qs ? `?${qs}` : ''}`;
}

/** createClient({fetch, base, getKey, onAuth}) -> {get(path, params, signal)}. */
export function createClient({ fetch, base, getKey, onAuth }) {
  async function get(path, params, signal) {
    const headers = { Accept: 'application/json' };
    const key = getKey();
    if (key) headers['X-View-Key'] = key;
    let res;
    try {
      res = await fetch(buildUrl(base, path, params), { headers, signal, cache: 'no-store' });
    } catch (err) {
      if (err && err.name === 'AbortError') throw err;
      throw new ApiError(0, 'network', err && err.message);
    }
    const body = await res.json().catch(() => null);
    if (res.ok) return body;
    if (res.status === 401 && onAuth) onAuth();
    throw new ApiError(res.status, (body && body.error) || `http_${res.status}`, body && body.message);
  }
  return { get };
}
