// Pure helpers of the radar-client page: validation, formatting and the text of the status panel.
// Nothing here touches the DOM, so it is tested with `node --test`.

const RULESETS = ['', 'normal', 'pvp', 'rp', 'rppvp'];
const PRIVATE_SUFFIXES = ['.local', '.lan', '.internal', '.home.arpa'];
const SECOND_MS = 1000;
const MINUTE_S = 60;
const HOUR_S = 3600;
const KB = 1024;

/** True for loopback, private-range, link-local and single-label (LAN) host names. */
export function isPrivateHost(host) {
  const h = String(host).toLowerCase().replace(/^\[|\]$/g, '');
  if (h === 'localhost' || h === '::1' || !h.includes('.') && !h.includes(':')) return true;
  if (PRIVATE_SUFFIXES.some((s) => h.endsWith(s))) return true;
  const m = /^(\d+)\.(\d+)\.\d+\.\d+$/.exec(h);
  if (!m) return false;
  const [a, b] = [Number(m[1]), Number(m[2])];
  return a === 127 || a === 10 || (a === 192 && b === 168) || (a === 172 && b >= 16 && b <= 31) || (a === 169 && b === 254);
}

/** A warning when the key would travel unencrypted over the internet; '' otherwise. */
export function serverWarning(server) {
  let url;
  try {
    url = new URL(String(server).trim());
  } catch {
    return '';
  }
  if (url.protocol !== 'http:' || isPrivateHost(url.hostname)) return '';
  return 'This address uses plain http to a non-private host: the ingest key and the log are not encrypted on the way. Use https if the server offers it.';
}

function serverError(server) {
  const text = String(server).trim();
  if (!text) return 'The server address is required.';
  let url;
  try {
    url = new URL(text);
  } catch {
    return 'The server address must start with http:// or https:// and name a host.';
  }
  if (url.protocol !== 'http:' && url.protocol !== 'https:') return 'The server address must start with http:// or https://.';
  if (url.username || url.password) return 'The server address must not contain a user name or password.';
  return '';
}

/**
 * Checks the form. errors maps a field name to a message (blocks Start); warnings is a list of
 * remarks that do not block. ctx.keySource is 'none' | 'env' | 'typed'.
 */
export function validateForm(values, ctx = {}) {
  const errors = {};
  const warnings = [];
  const server = serverError(values.server);
  if (server) errors.server = server;
  if (!String(values.logPath).trim()) errors.logPath = 'Choose the combat log file (or its folder) first.';
  if (!RULESETS.includes(String(values.ruleset).trim().toLowerCase())) errors.ruleset = 'Unknown rule-set.';
  if (ctx.keySource === 'none' && !String(values.key).trim()) {
    warnings.push('No ingest key is set. Type one above, or start the program with RADAR_CLIENT_INGEST_KEY. Without it only an open server accepts the upload.');
  }
  const insecure = server ? '' : serverWarning(values.server);
  if (insecure) warnings.push(insecure);
  return { ok: Object.keys(errors).length === 0, errors, warnings };
}

/** The body of POST /api/start. The key is included only when something was typed. */
export function startPayload(values) {
  const payload = {
    server: String(values.server).trim().replace(/\/+$/, ''),
    ruleset: String(values.ruleset).trim().toLowerCase(),
    logPath: String(values.logPath).trim().replace(/^"|"$/g, '').trim(),
    followNewest: Boolean(values.followNewest),
    includeExisting: Boolean(values.includeExisting),
  };
  const key = String(values.key || '').trim();
  if (key) payload.key = key;
  return payload;
}

/** 1536 -> "1.5 KB". */
export function formatBytes(n) {
  if (n < KB) return `${n} B`;
  if (n < KB * KB) return `${(n / KB).toFixed(1)} KB`;
  if (n < KB ** 3) return `${(n / KB ** 2).toFixed(1)} MB`;
  return `${(n / KB ** 3).toFixed(2)} GB`;
}

/** 12.34 -> "12.3 lines/s". */
export function formatRate(perSec) {
  if (perSec >= 100) return `${Math.round(perSec)} lines/s`;
  if (perSec === 0) return '0 lines/s';
  return `${perSec.toFixed(1)} lines/s`;
}

function formatCount(n) {
  return String(n).replace(/\B(?=(\d{3})+(?!\d))/g, ',');
}

const two = (n) => String(n).padStart(2, '0');

/** Local time of day, "03:04:05"; "-" for 0. */
export function formatClock(ms) {
  if (!ms) return '-';
  const d = new Date(ms);
  return `${two(d.getHours())}:${two(d.getMinutes())}:${two(d.getSeconds())}`;
}

/** "4 s ago", "2 min ago", "3 h ago". */
export function formatAgo(ms, nowMs) {
  if (!ms) return 'never';
  const s = Math.round((nowMs - ms) / SECOND_MS);
  if (s < 1) return 'just now';
  if (s < MINUTE_S) return `${s} s ago`;
  if (s < HOUR_S) return `${Math.floor(s / MINUTE_S)} min ago`;
  return `${Math.floor(s / HOUR_S)} h ago`;
}

/** The last component of a Windows or POSIX path. */
export function baseName(path) {
  return String(path).split(/[\\/]/).pop();
}

/** Label, colour tone and explanatory hint for the state badge. */
export function describeState(status) {
  const hint = [status.error, retryText(status), status.note].filter(Boolean).join(' ');
  // A refused key ends the run (running is false) but must stay visible as an error.
  if (status.state === 'error' && !status.running) return { label: 'Error', tone: 'error', hint };
  if (!status.running) return { label: 'Stopped', tone: 'idle', hint };
  switch (status.state) {
    case 'connecting': return { label: 'Connecting...', tone: 'busy', hint };
    case 'uploading': return { label: 'Uploading', tone: 'ok', hint };
    case 'error': return { label: 'Error', tone: 'error', hint };
    default: return { label: status.file ? 'Waiting for new data' : 'Waiting for the log file', tone: 'ok', hint };
  }
}

function retryText(status) {
  return status.state === 'error' && status.retryInSec > 0 ? `Retrying in ${status.retryInSec} s.` : '';
}

/** [label, value] pairs for the status panel. */
export function statusRows(status, nowMs) {
  const last = status.lastUploadMs ? `${formatClock(status.lastUploadMs)} (${formatAgo(status.lastUploadMs, nowMs)})` : 'never';
  const dropped = status.dropped > 0 ? ` (${formatCount(status.dropped)} dropped)` : '';
  return [
    ['Log file', status.file ? baseName(status.file) : '-'],
    ['Lines sent', formatCount(status.linesSent)],
    ['Data sent', formatBytes(status.bytesSent)],
    ['Speed', formatRate(status.linesPerSec)],
    ['Last upload', last],
    ['Server reply', status.lastReply || '-'],
    ['Waiting to send', `${formatCount(status.queued)} lines${dropped}`],
  ];
}

/** "03:04:05  Started" lines, oldest first. */
export function eventLines(status) {
  return status.events.map((e) => `${formatClock(e.timeMs)}  ${e.text}`);
}
