import test from 'node:test';
import assert from 'node:assert/strict';
import {
  isPrivateHost, serverWarning, validateForm, startPayload, formatBytes, formatRate,
  formatClock, formatAgo, describeState, statusRows, eventLines, baseName,
} from '../logic.js';

const good = { server: 'http://127.0.0.1:8788', ruleset: '', logPath: 'C:\\Logs\\WoWCombatLog.txt', followNewest: false, includeExisting: false, key: '' };

test('isPrivateHost recognises local and private addresses', () => {
  for (const h of ['localhost', '127.0.0.1', '10.1.2.3', '192.168.0.9', '172.16.0.1', '172.31.255.1', '169.254.1.1', '[::1]', 'nas', 'box.local', 'box.lan', 'srv.internal']) {
    assert.equal(isPrivateHost(h), true, h);
  }
  for (const h of ['example.com', '8.8.8.8', '172.32.0.1', '11.0.0.1', 'radar.example.test']) {
    assert.equal(isPrivateHost(h), false, h);
  }
});

test('serverWarning flags plain http to a public host only', () => {
  assert.match(serverWarning('http://radar.example.test:8788'), /not encrypted/i);
  assert.equal(serverWarning('https://radar.example.test'), '');
  assert.equal(serverWarning('http://192.168.1.5:8788'), '');
  assert.equal(serverWarning('http://localhost:8788'), '');
  assert.equal(serverWarning('not a url'), '');
  assert.equal(serverWarning(''), '');
});

test('validateForm accepts a complete form', () => {
  const r = validateForm(good, { keySource: 'env' });
  assert.equal(r.ok, true);
  assert.deepEqual(r.errors, {});
  assert.deepEqual(r.warnings, []);
});

test('validateForm reports each problem on its field', () => {
  const r = validateForm({ ...good, server: 'radar.example', logPath: '  ', ruleset: 'hardcore' }, { keySource: 'env' });
  assert.equal(r.ok, false);
  assert.match(r.errors.server, /http/);
  assert.match(r.errors.logPath, /log/i);
  assert.match(r.errors.ruleset, /rule-set/i);
});

test('validateForm rejects an empty server and credentials in the address', () => {
  assert.match(validateForm({ ...good, server: '' }, {}).errors.server, /required/i);
  assert.match(validateForm({ ...good, server: 'http://user:pw@host' }, {}).errors.server, /user name or password/i);
  assert.match(validateForm({ ...good, server: 'http://' }, {}).errors.server, /host|http/i);
});

test('validateForm accepts the rule-sets in any case', () => {
  for (const rs of ['', 'normal', 'PVP', 'rp', 'RpPvp']) {
    assert.equal(validateForm({ ...good, ruleset: rs }, { keySource: 'env' }).ok, true, rs);
  }
});

test('validateForm warns about a missing key and an unencrypted public server', () => {
  const r = validateForm({ ...good, server: 'http://radar.example.test:8788' }, { keySource: 'none' });
  assert.equal(r.ok, true);
  assert.equal(r.warnings.length, 2);
  assert.ok(r.warnings.some((w) => /key/i.test(w)));
  assert.ok(r.warnings.some((w) => /not encrypted/i.test(w)));
  assert.equal(validateForm({ ...good, key: 'typed' }, { keySource: 'none' }).warnings.length, 0);
});

test('startPayload trims text and sends the key only when typed', () => {
  const p = startPayload({ ...good, server: ' http://h:1/ ', logPath: ' "C:\\a b\\log.txt" ', ruleset: ' PvP ', key: '  secret  ' });
  assert.deepEqual(p, { server: 'http://h:1', ruleset: 'pvp', logPath: 'C:\\a b\\log.txt', followNewest: false, includeExisting: false, key: 'secret' });
  assert.equal('key' in startPayload(good), false);
});

test('formatBytes', () => {
  assert.equal(formatBytes(0), '0 B');
  assert.equal(formatBytes(1023), '1023 B');
  assert.equal(formatBytes(1536), '1.5 KB');
  assert.equal(formatBytes(5 * 1024 * 1024), '5.0 MB');
  assert.equal(formatBytes(3 * 1024 ** 3), '3.00 GB');
});

test('formatRate', () => {
  assert.equal(formatRate(0), '0 lines/s');
  assert.equal(formatRate(12.34), '12.3 lines/s');
  assert.equal(formatRate(250.6), '251 lines/s');
});

test('formatClock and formatAgo', () => {
  const t = new Date(2030, 0, 2, 3, 4, 5).getTime();
  assert.equal(formatClock(t), '03:04:05');
  assert.equal(formatClock(0), '-');
  assert.equal(formatAgo(t, t + 4000), '4 s ago');
  assert.equal(formatAgo(t, t + 125000), '2 min ago');
  assert.equal(formatAgo(t, t + 3 * 3600 * 1000), '3 h ago');
  assert.equal(formatAgo(0, t), 'never');
  assert.equal(formatAgo(t, t - 5000), 'just now');
});

test('baseName handles both path separators', () => {
  assert.equal(baseName('C:\\Logs\\WoWCombatLog-1.txt'), 'WoWCombatLog-1.txt');
  assert.equal(baseName('/home/u/log.txt'), 'log.txt');
  assert.equal(baseName(''), '');
});

const status = (over) => ({ state: 'idle', running: false, error: '', note: '', file: '', linesSent: 0, bytesSent: 0, linesPerSec: 0, lastUploadMs: 0, lastReply: '', retryInSec: 0, queued: 0, dropped: 0, events: [], ...over });

test('describeState covers every state', () => {
  assert.deepEqual(describeState(status()), { label: 'Stopped', tone: 'idle', hint: '' });
  assert.equal(describeState(status({ running: true, state: 'connecting' })).label, 'Connecting...');
  assert.equal(describeState(status({ running: true, state: 'uploading' })).tone, 'ok');
  assert.equal(describeState(status({ running: true, state: 'waiting', file: 'x.txt' })).label, 'Waiting for new data');
  assert.equal(describeState(status({ running: true, state: 'waiting', file: '' })).label, 'Waiting for the log file');
});

test('describeState shows the reason and the countdown for an error', () => {
  const d = describeState(status({ running: true, state: 'error', error: 'The server rejected the ingest key (401).', retryInSec: 7 }));
  assert.equal(d.tone, 'error');
  assert.equal(d.label, 'Error');
  assert.match(d.hint, /rejected the ingest key/);
  assert.match(d.hint, /retrying in 7 s/i);
  assert.doesNotMatch(describeState(status({ running: true, state: 'error', error: 'x', retryInSec: 0 })).hint, /retrying/i);
});

test('describeState shows a stopped upload that ended in an error (refused key)', () => {
  const error = 'The server rejected the ingest key (401). The upload stopped; fix the key and click Start again.';
  const d = describeState(status({ running: false, state: 'error', error }));
  assert.deepEqual(d, { label: 'Error', tone: 'error', hint: error });
});

test('startPayload strips pasted spaces and line breaks around the key', () => {
  assert.equal(startPayload({ ...good, key: '\r\n  key-123 \n' }).key, 'key-123');
});

test('describeState keeps a note visible', () => {
  assert.match(describeState(status({ running: true, state: 'waiting', note: 'Cannot read the log folder' })).hint, /Cannot read the log folder/);
});

test('statusRows lists the live numbers', () => {
  const now = new Date(2030, 0, 2, 3, 4, 10).getTime();
  const rows = statusRows(status({
    running: true, state: 'uploading', file: 'C:\\Logs\\WoWCombatLog-1.txt', linesSent: 1234, bytesSent: 2048, linesPerSec: 3.2,
    lastUploadMs: new Date(2030, 0, 2, 3, 4, 5).getTime(), lastReply: '5 lines, 4 events', queued: 9, dropped: 1,
  }), now);
  const map = Object.fromEntries(rows);
  assert.equal(map['Log file'], 'WoWCombatLog-1.txt');
  assert.equal(map['Lines sent'], '1,234');
  assert.equal(map['Data sent'], '2.0 KB');
  assert.equal(map['Speed'], '3.2 lines/s');
  assert.equal(map['Last upload'], '03:04:05 (5 s ago)');
  assert.equal(map['Server reply'], '5 lines, 4 events');
  assert.equal(map['Waiting to send'], '9 lines (1 dropped)');
});

test('statusRows shows dashes before anything happened', () => {
  const map = Object.fromEntries(statusRows(status(), 0));
  assert.equal(map['Log file'], '-');
  assert.equal(map['Last upload'], 'never');
  assert.equal(map['Server reply'], '-');
  assert.equal(map['Waiting to send'], '0 lines');
});

test('eventLines formats the newest last', () => {
  const t = new Date(2030, 0, 2, 3, 4, 5).getTime();
  assert.deepEqual(eventLines(status({ events: [{ timeMs: t, text: 'Started' }, { timeMs: t + 1000, text: 'Uploading' }] })), ['03:04:05  Started', '03:04:06  Uploading']);
  assert.deepEqual(eventLines(status()), []);
});

test('startPayload sends remember only together with a typed key', () => {
  assert.equal(startPayload({ ...good, key: ' k ', remember: true }).remember, true);
  assert.equal('remember' in startPayload({ ...good, key: ' k ', remember: false }), false);
  assert.equal('remember' in startPayload({ ...good, key: '', remember: true }), false);
});
