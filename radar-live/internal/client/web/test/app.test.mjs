import test from 'node:test';
import assert from 'node:assert/strict';
import { createApp } from '../app.js';
import { createFakeDocument } from './fake-dom.mjs';

const T0 = new Date(2030, 0, 2, 3, 4, 10).getTime();
const emptyStatus = { state: 'idle', running: false, error: '', note: '', file: '', linesSent: 0, bytesSent: 0, linesPerSec: 0, lastUploadMs: 0, lastReply: '', retryInSec: 0, queued: 0, dropped: 0, events: [] };
const baseSettings = { server: 'http://127.0.0.1:8788', ruleset: '', logPath: '', followNewest: false, includeExisting: false, keySource: 'none', canBrowse: true, canQuit: false, version: 'v-test', warning: '' };

function setup(over = {}) {
  const doc = createFakeDocument('tok-123');
  const calls = [];
  const routes = {
    'GET /api/settings': () => ({ ...baseSettings, ...over.settings }),
    'GET /api/status': () => ({ ...emptyStatus, ...over.status }),
    'POST /api/start': () => ({ ...emptyStatus, running: true, state: 'waiting' }),
    'POST /api/stop': () => emptyStatus,
    'POST /api/pick-file': () => ({ path: 'C:\\Logs\\WoWCombatLog.txt' }),
    'POST /api/quit': () => ({}),
    ...over.routes,
  };
  const fetch = async (path, init) => {
    const key = `${init.method} ${path}`;
    const body = init.body ? JSON.parse(init.body) : undefined;
    calls.push({ key, body, headers: init.headers });
    const handler = routes[key];
    if (!handler) return { ok: false, status: 404, json: async () => ({ message: 'no route' }) };
    const out = handler(body);
    const status = out && out.__status ? out.__status : 200;
    return { ok: status < 400, status, json: async () => (out && out.__body !== undefined ? out.__body : out) };
  };
  const timers = { callback: null, setInterval(fn) { this.callback = fn; return 1; } };
  const app = createApp({ document: doc, fetch, timers, now: () => T0 });
  const el = (id) => doc.getElementById(id);
  return { app, doc, calls, el, timers };
}

function fillForm(el, over = {}) {
  el('server').value = over.server ?? 'http://127.0.0.1:8788';
  el('logPath').value = over.logPath ?? 'C:\\Logs\\WoWCombatLog.txt';
  el('key').value = over.key ?? '';
  el('ruleset').value = over.ruleset ?? '';
}

test('init loads settings into the form and polls the status', async () => {
  const { app, el, calls, timers } = setup({ settings: { server: 'http://10.0.0.5:8788', logPath: 'C:\\Logs\\x.txt', ruleset: 'rp', followNewest: true, includeExisting: true } });
  await app.init();
  assert.equal(el('server').value, 'http://10.0.0.5:8788');
  assert.equal(el('logPath').value, 'C:\\Logs\\x.txt');
  assert.equal(el('ruleset').value, 'rp');
  assert.equal(el('followNewest').checked, true);
  assert.equal(el('includeExisting').checked, true);
  assert.equal(el('stateText').textContent, 'Stopped');
  assert.ok(calls.every((c) => c.headers['X-Radar-Token'] === 'tok-123'));
  assert.equal(typeof timers.callback, 'function');
  const before = calls.length;
  await timers.callback();
  assert.equal(calls.length, before + 1);
  assert.equal(calls.at(-1).key, 'GET /api/status');
});

test('the key field explains where the key comes from and never shows it', async () => {
  const env = setup({ settings: { keySource: 'env' } });
  await env.app.init();
  assert.match(env.el('keyHint').textContent, /RADAR_CLIENT_INGEST_KEY/);
  const none = setup({ settings: { keySource: 'none' } });
  await none.app.init();
  assert.match(none.el('keyHint').textContent, /memory/i);
});

test('browse and quit buttons follow the capabilities', async () => {
  const a = setup({ settings: { canBrowse: false, canQuit: true } });
  await a.app.init();
  assert.equal(a.el('browse').hidden, true);
  assert.equal(a.el('quitBtn').hidden, false);
  const b = setup();
  await b.app.init();
  assert.equal(b.el('browse').hidden, false);
  assert.equal(b.el('quitBtn').hidden, true);
});

test('the log label changes with the follow-newest option', async () => {
  const { app, el } = setup();
  await app.init();
  el('followNewest').checked = true;
  await el('followNewest').fire('change');
  assert.match(el('logLabel').textContent, /folder/i);
  el('followNewest').checked = false;
  await el('followNewest').fire('change');
  assert.equal(el('logLabel').textContent, 'Log file');
});

test('Start validates first and does not call the server when the form is wrong', async () => {
  const { app, el, calls } = setup();
  await app.init();
  fillForm(el, { server: 'nope', logPath: '' });
  await el('startBtn').fire('click');
  assert.ok(!calls.some((c) => c.key === 'POST /api/start'));
  assert.match(el('serverMsg').textContent, /http/);
  assert.match(el('logMsg').textContent, /log/i);
});

test('Start sends the form, clears the key field and locks the inputs', async () => {
  const { app, el, calls } = setup();
  await app.init();
  fillForm(el, { key: 'typed-key-123', ruleset: 'pvp' });
  el('followNewest').checked = true;
  await el('startBtn').fire('click');
  const start = calls.find((c) => c.key === 'POST /api/start');
  assert.deepEqual(start.body, { server: 'http://127.0.0.1:8788', ruleset: 'pvp', logPath: 'C:\\Logs\\WoWCombatLog.txt', followNewest: true, includeExisting: false, key: 'typed-key-123' });
  assert.equal(el('key').value, '', 'the typed key must not stay in the page');
  assert.equal(el('startBtn').disabled, true);
  assert.equal(el('stopBtn').disabled, false);
  assert.equal(el('server').disabled, true);
  assert.equal(el('stateText').textContent, 'Waiting for the log file');
});

test('a refused key shows the error and leaves Start usable', async () => {
  const error = 'The server rejected the ingest key (401). The upload stopped; fix the key and click Start again.';
  const { app, el } = setup({ status: { running: false, state: 'error', error } });
  await app.init();
  assert.equal(el('stateText').textContent, 'Error');
  assert.match(el('stateHint').textContent, /click Start again/);
  assert.equal(el('startBtn').disabled, false);
  assert.equal(el('key').disabled, false);
});

test('Start shows the server message when it refuses', async () => {
  const { app, el } = setup({ routes: { 'POST /api/start': () => ({ __status: 409, __body: { error: 'already_running', message: 'an upload is already running' } }) } });
  await app.init();
  fillForm(el);
  await el('startBtn').fire('click');
  assert.match(el('formMsg').textContent, /already running/);
  assert.equal(el('startBtn').disabled, false);
});

test('Start shows warnings without blocking', async () => {
  const { app, el, calls } = setup();
  await app.init();
  fillForm(el, { server: 'http://radar.example.test:8788' });
  await el('startBtn').fire('click');
  assert.ok(calls.some((c) => c.key === 'POST /api/start'));
  assert.match(el('formWarnings').textContent, /not encrypted/);
  assert.match(el('formWarnings').textContent, /No ingest key/);
});

test('Stop calls the server and unlocks the form', async () => {
  const { app, el, calls } = setup({ status: { running: true, state: 'uploading' } });
  await app.init();
  assert.equal(el('stopBtn').disabled, false);
  await el('stopBtn').fire('click');
  assert.ok(calls.some((c) => c.key === 'POST /api/stop'));
  assert.equal(el('startBtn').disabled, false);
  assert.equal(el('server').disabled, false);
});

test('Browse fills the log path', async () => {
  const { app, el, calls } = setup();
  await app.init();
  el('logPath').value = 'C:\\old';
  await el('browse').fire('click');
  assert.equal(el('logPath').value, 'C:\\Logs\\WoWCombatLog.txt');
  assert.equal(calls.find((c) => c.key === 'POST /api/pick-file').body.current, 'C:\\old');
});

test('Browse keeps the old path when cancelled and hides itself when unsupported', async () => {
  const cancelled = setup({ routes: { 'POST /api/pick-file': () => ({ cancelled: true }) } });
  await cancelled.app.init();
  cancelled.el('logPath').value = 'keep';
  await cancelled.el('browse').fire('click');
  assert.equal(cancelled.el('logPath').value, 'keep');
  const none = setup({ routes: { 'POST /api/pick-file': () => ({ __status: 501, __body: { message: 'type the path' } }) } });
  await none.app.init();
  await none.el('browse').fire('click');
  assert.equal(none.el('browse').hidden, true);
  assert.match(none.el('logMsg').textContent, /type the path/);
});

test('status rendering uses text only and lists rows and events', async () => {
  const hostile = '<img src=x onerror=alert(1)>';
  const { app, el } = setup({ status: { running: true, state: 'uploading', file: `C:\\Logs\\${hostile}.txt`, linesSent: 1500, events: [{ timeMs: T0, text: hostile }], lastReply: hostile } });
  await app.init();
  assert.equal(el('stateText').textContent, 'Uploading');
  assert.match(el('stateBadge').className, /\bok\b/);
  const rows = el('statusRows').children;
  assert.equal(rows.length, 14, 'seven label/value pairs');
  assert.equal(rows[0].tagName, 'DT');
  assert.equal(rows[2].textContent, 'Lines sent');
  assert.equal(rows[3].textContent, '1,500');
  assert.ok(el('eventLog').textContent.includes(hostile), 'shown literally');
  assert.equal(el('eventLog').children.length, 0, 'plain text node, no markup');
  assert.equal(el('eventLog').scrollTop, el('eventLog').scrollHeight);
});

test('a failed poll says contact is lost', async () => {
  const env = setup({ routes: { 'GET /api/status': () => ({ __status: 500, __body: { message: 'x' } }) } });
  await env.app.init();
  assert.match(env.el('stateText').textContent, /lost contact/i);
});

test('Quit tells the server and says the program ended', async () => {
  const { app, el, calls } = setup({ settings: { canQuit: true } });
  await app.init();
  await el('quitBtn').fire('click');
  assert.ok(calls.some((c) => c.key === 'POST /api/quit'));
  assert.match(el('stateText').textContent, /closed/i);
});

test('a settings warning from loading is shown', async () => {
  const { app, el } = setup({ settings: { warning: 'settings file is not valid JSON' } });
  await app.init();
  assert.equal(el('loadWarning').hidden, false);
  assert.match(el('loadWarning').textContent, /not valid JSON/);
});
