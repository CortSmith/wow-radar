// Pure helpers for radar-keys' web UI: validation, payloads, formatting, filtering. No DOM, no network.

export const LIMIT_FIELDS = ['lineRate', 'lineBurst', 'byteRate', 'byteBurst', 'maxStreams'];
export const LIMIT_LABELS = {
  lineRate: 'lines/s', lineBurst: 'burst', byteRate: 'bytes/s', byteBurst: 'byte burst', maxStreams: 'streams',
};
export const MAX_LIMIT = 1000000000;
export const REVEAL_MS = 10000;

const EMAIL_RE = /^[^\s@,;<>()]+@[^\s@,;<>()]+\.[^\s@,;<>()]+$/;

export function validateEmail(email) {
  const e = String(email ?? '').trim();
  if (!e) return 'Email is required.';
  if (e.length > 254 || !EMAIL_RE.test(e)) return 'Enter a valid email address.';
  return '';
}

/** "a, b,a ;c" -> ['a','b','c']: split on commas/semicolons/newlines, trim, drop empties and case-insensitive duplicates. */
export function parseTags(text) {
  const out = [];
  const seen = new Set();
  for (const part of String(text ?? '').split(/[,;\n]/)) {
    const t = part.trim();
    if (!t || seen.has(t.toLowerCase())) continue;
    seen.add(t.toLowerCase());
    out.push(t);
  }
  return out;
}

/** One limit field: blank -> {value:null}; positive integer within the sanity maximum -> {value:n}; else {error}. */
export function parseLimit(text) {
  const s = String(text ?? '').trim();
  if (s === '') return { value: null };
  if (!/^-?\d+(\.\d+)?$/.test(s)) return { error: 'Enter a whole number or leave blank.' };
  const n = Number(s);
  if (n <= 0) return { error: 'Must be greater than 0 (leave blank for the default).' };
  if (!Number.isInteger(n)) return { error: 'Enter a whole number.' };
  if (n > MAX_LIMIT) return { error: 'Too large.' };
  return { value: n };
}

/** Date input value (YYYY-MM-DD) -> ISO end of that day (UTC), or null when blank. */
export function expiresToIso(dateText) {
  const s = String(dateText ?? '').trim();
  if (!s) return null;
  return `${s}T23:59:59Z`;
}

export function isoToDateInput(iso) {
  return iso ? String(iso).slice(0, 10) : '';
}

export function validDate(dateText) {
  const s = String(dateText ?? '').trim();
  if (!s) return true;
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(s);
  if (!m) return false;
  const d = new Date(Date.UTC(+m[1], +m[2] - 1, +m[3]));
  return d.getUTCFullYear() === +m[1] && d.getUTCMonth() === +m[2] - 1 && d.getUTCDate() === +m[3];
}

/**
 * form: { email, player, tags (text), notes, expires (date text), limits: {lineRate: text, ...}, keepCopy }
 * Returns { ok, errors: {email, expires, <limit>: msg}, payload }.
 */
export function validateEntry(form) {
  const errors = {};
  const emailErr = validateEmail(form.email);
  if (emailErr) errors.email = emailErr;
  if (!validDate(form.expires)) errors.expires = 'Use a valid date.';
  const limits = {};
  for (const f of LIMIT_FIELDS) {
    const r = parseLimit((form.limits || {})[f]);
    if (r.error) errors[f] = r.error;
    limits[f] = r.error ? null : r.value;
  }
  const ok = Object.keys(errors).length === 0;
  const payload = ok ? {
    email: String(form.email).trim(),
    player: String(form.player ?? '').trim(),
    tags: parseTags(form.tags),
    notes: String(form.notes ?? '').trim(),
    expiresAt: expiresToIso(form.expires),
    limits,
  } : null;
  if (ok && form.keepCopy !== undefined) payload.keepCopy = !!form.keepCopy;
  return { ok, errors, payload };
}

/** Form values for the edit dialog from a Key. */
export function formFromKey(key) {
  const limits = {};
  for (const f of LIMIT_FIELDS) limits[f] = key.limits && key.limits[f] != null ? String(key.limits[f]) : '';
  return {
    email: key.email || '', player: key.player || '', tags: (key.tags || []).join(', '), notes: key.notes || '',
    expires: isoToDateInput(key.expiresAt), limits,
  };
}

/** Raw key -> masked text, used only when the server gave no mask. */
export function maskKey(raw) {
  const s = String(raw ?? '');
  if (s.length <= 8) return '•'.repeat(Math.max(s.length, 8));
  return `${s.slice(0, 4)}••••••${s.slice(-4)}`;
}

export function statusLabel(key) {
  const s = key && key.status;
  return ['active', 'blocked', 'expired', 'legacy'].includes(s) ? s : 'active';
}

export function isEnabled(key) {
  return !key.disabled && statusLabel(key) !== 'blocked';
}

export function isReadOnly(key) {
  return statusLabel(key) === 'legacy';
}

export function formatCount(n) {
  const v = Number(n);
  if (!Number.isFinite(v)) return '0';
  return Math.round(v).toLocaleString('en-US');
}

/** ISO -> "YYYY-MM-DD HH:MM" (UTC); fallback text when missing or invalid. */
export function formatTime(iso, empty = 'never') {
  if (!iso) return empty;
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return empty;
  return d.toISOString().slice(0, 16).replace('T', ' ');
}

export function formatDay(iso) {
  if (!iso) return '-';
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? '-' : d.toISOString().slice(0, 10);
}

/** Effective limits: "default" when nothing is customised, else the custom values only. */
export function formatLimits(limits) {
  const parts = [];
  for (const f of LIMIT_FIELDS) {
    const v = limits ? limits[f] : null;
    if (v != null) parts.push(`${formatCount(v)} ${LIMIT_LABELS[f]}`);
  }
  return parts.length ? parts.join(', ') : 'default';
}

/** Placeholder text for a limit input: the server default, or a generic hint. */
export function limitPlaceholder(defaults, field) {
  const v = defaults ? defaults[field] : null;
  return v != null ? `default ${formatCount(v)}` : 'default';
}

export function allTags(keys) {
  const seen = new Map();
  for (const k of keys) for (const t of k.tags || []) if (!seen.has(t.toLowerCase())) seen.set(t.toLowerCase(), t);
  return [...seen.values()].sort((a, b) => a.localeCompare(b));
}

/** Case-insensitive search over email, player, tags, notes, id and mask; optional exact tag filter. */
export function filterKeys(keys, search, tag) {
  const q = String(search ?? '').trim().toLowerCase();
  const t = String(tag ?? '').trim().toLowerCase();
  return keys.filter((k) => {
    if (t && !(k.tags || []).some((x) => x.toLowerCase() === t)) return false;
    if (!q) return true;
    const hay = [k.email, k.player, k.notes, k.id, k.masked, ...(k.tags || [])].join('\n').toLowerCase();
    return hay.includes(q);
  });
}

/** Newest first by createdAt (stable for equal/missing dates). */
export function sortKeys(keys) {
  return keys.map((k, i) => [k, i]).sort((a, b) => {
    const ta = Date.parse(a[0].createdAt) || 0;
    const tb = Date.parse(b[0].createdAt) || 0;
    return tb - ta || a[1] - b[1];
  }).map((x) => x[0]);
}

export function errorMessage(data, fallback = 'Request failed.') {
  return data && typeof data.message === 'string' && data.message ? data.message : fallback;
}
