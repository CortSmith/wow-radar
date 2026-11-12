const SEC_MS = 1000;
const MIN_S = 60;
const HOUR_S = 3600;

/** Age in ms -> '45s', '3m', '1h 5m'. */
export function fmtAge(ms) {
  const s = Math.max(0, Math.floor(ms / SEC_MS));
  if (s < MIN_S) return `${s}s`;
  if (s < HOUR_S) return `${Math.floor(s / MIN_S)}m`;
  return `${Math.floor(s / HOUR_S)}h ${Math.floor((s % HOUR_S) / MIN_S)}m`;
}

/** Number with fixed decimals, '-' when not finite. */
export function fmtNum(v, digits = 0) {
  return Number.isFinite(v) ? v.toFixed(digits) : '-';
}

/** total / count with one decimal, '-' when count is zero. */
export function fmtAvg(total, count) {
  return count > 0 ? fmtNum(total / count, 1) : '-';
}

/** A valid IANA zone or 'UTC'. */
export function safeZone(zone) {
  try {
    new Intl.DateTimeFormat('en-GB', { timeZone: zone });
    return zone;
  } catch {
    return 'UTC';
  }
}

/** Epoch ms -> 'YYYY-MM-DD HH:MM:SS' in the zone ('-' for 0). */
export function fmtTime(ms, zone) {
  if (!ms) return '-';
  const parts = new Intl.DateTimeFormat('en-CA', {
    timeZone: safeZone(zone),
    hourCycle: 'h23',
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  }).formatToParts(new Date(ms));
  const v = Object.fromEntries(parts.map((p) => [p.type, p.value]));
  return `${v.year}-${v.month}-${v.day} ${v.hour}:${v.minute}:${v.second}`;
}
