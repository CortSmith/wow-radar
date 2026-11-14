import test from 'node:test';
import assert from 'node:assert/strict';
import { createApp } from '../app.js';
import { createFakeDocument } from './fake-dom.mjs';

const nolimits = { lineRate: null, lineBurst: null, byteRate: null, byteBurst: null, maxStreams: null };
const mk = (over) => ({
  id: 'k1', masked: 'abcd...wxyz', email: 'alice@x.co', player: 'Thrall', tags: ['raid'], notes: '',
  createdAt: '2030-01-01T00:00:00Z', expiresAt: null, disabled: false, disabledReason: '', status: 'active',
  canReveal: true, limits: nolimits, usage: { lastSeen: '2030-01-02T03:04:00Z', linesToday: 1234, bytesToday: 5 }, ...over,
});
const defaults = { lineRate: 20000, lineBurst: 40000, byteRate: 262144, byteBurst: 524288, maxStreams: 4 };
const RAW = 'RAW-SECRET-KEY-123';

function setup(over = {}) {
  const doc = createFakeDocument('tok-123');
  const calls = [];
  const state = { keys: over.keys || [mk(), mk({ id: 'k2', email: 'bob@x.co', canReveal: false, tags: [], createdAt: '2030-02-01T00:00:00Z' }), mk({ id: 'k3', email: 'old@x.co', status: 'legacy', canReveal: false })] };
  const routes = {
    'GET /api/keys': () => ({ keys: state.keys, defaults }),
    'GET /api/keys/k1/reveal': () => ({ key: RAW }),
    'POST /api/keys': (b) => ({ __status: 201, __body: { key: RAW, entry: mk({ id: 'k9', email: b.email }) } }),
    'POST /api/keys/k1/disable': (b) => mk({ disabled: true, disabledReason: b.reason, status: 'blocked' }),
    'POST /api/keys/k1/enable': () => mk(),
    'POST /api/keys/k1/regenerate': () => ({ key: 'NEW-RAW', entry: mk({ masked: 'new...key' }) }),
    'PUT /api/keys/k1': (b) => mk({ email: b.email, player: b.player }),
    'DELETE /api/keys/k1': () => ({ __status: 204, __body: {} }),
    ...over.routes,
  };
  const fetch = async (path, init) => {
    const key = `${init.method} ${path}`;
    const body = init.body ? JSON.parse(init.body) : undefined;
    calls.push({ key, body, headers: init.headers });
    const h = routes[key];
    if (!h) return { ok: false, status: 404, json: async () => ({ message: 'no route' }) };
    const out = h(body);
    const status = out && out.__status ? out.__status : 200;
    return { ok: status < 400, status, json: async () => (out && out.__body !== undefined ? out.__body : out) };
  };
  const timers = {
    pending: [],
    setTimeout(fn, ms) { const t = { fn, ms, cleared: false }; this.pending.push(t); return t; },
    clearTimeout(t) { if (t) t.cleared = true; },
    async run() { for (const t of this.pending.filter((x) => !x.cleared)) { t.cleared = true; await t.fn(); } },
  };
  const copied = [];
  const answers = { prompt: over.prompt === undefined ? 'sharing key' : over.prompt, confirm: over.confirm ?? true };
  const app = createApp({
    document: doc, fetch, timers,
    confirm: () => answers.confirm,
    prompt: () => answers.prompt,
    copy: async (t) => { copied.push(t); },
  });
  const el = (id) => doc.getElementById(id);
  const rows = () => el('rows').children;
  return { app, doc, calls, el, rows, timers, copied, answers };
}

test('list renders newest-first rows with token header, masked keys and limits', async () => {
  const { app, el, rows, calls } = setup();
  await app.init();
  assert.ok(calls.every((c) => c.headers['X-Radar-Token'] === 'tok-123'));
  assert.equal(rows().length, 3);
  assert.deepEqual(rows().map((r) => r.attributes['data-id']), ['k2', 'k1', 'k3']);
  const k1 = rows()[1];
  assert.match(k1.textContent, /abcd\.\.\.wxyz/);
  assert.match(k1.textContent, /alice@x\.co/);
  assert.match(k1.textContent, /1,234/);
  assert.match(k1.textContent, /default/);
  assert.match(k1.textContent, /active/);
  assert.equal(el('countText').textContent, '3 of 3 keys');
  assert.ok(!k1.textContent.includes(RAW));
});

test('legacy rows are read-only; keys without a stored copy have a disabled eye with a tooltip', async () => {
  const { app, rows } = setup();
  await app.init();
  const legacy = rows()[2];
  assert.equal(legacy.action('toggle')[0].disabled, true);
  assert.equal(legacy.action('edit').length + legacy.action('delete').length + legacy.action('regenerate').length, 0);
  const noCopy = rows()[0].action('reveal')[0];
  assert.equal(noCopy.disabled, true);
  assert.equal(noCopy.title, 'not stored, use Regenerate');
  assert.equal(rows()[1].action('reveal')[0].disabled, false);
});

test('search and tag filter narrow the table', async () => {
  const { app, el, rows } = setup();
  await app.init();
  el('search').value = 'bob';
  await el('search').fire('input');
  assert.equal(rows().length, 1);
  el('search').value = '';
  el('tagFilter').value = 'raid';
  await el('tagFilter').fire('change');
  assert.deepEqual(rows().map((r) => r.attributes['data-id']), ['k1', 'k3']);
});

test('unchecking Enabled prompts for a reason, calls disable and re-renders as blocked', async () => {
  const { app, rows, calls } = setup();
  await app.init();
  const box = rows()[1].action('toggle')[0];
  assert.equal(box.checked, true);
  box.checked = false;
  await box.fire('change');
  const c = calls.find((x) => x.key === 'POST /api/keys/k1/disable');
  assert.deepEqual(c.body, { reason: 'sharing key' });
  const row = rows().find((r) => r.attributes['data-id'] === 'k1');
  assert.match(row.textContent, /blocked/);
  assert.equal(row.action('toggle')[0].checked, false);
});

test('cancelling the reason prompt makes no call; re-checking calls enable', async () => {
  const { app, rows, calls, answers } = setup();
  await app.init();
  answers.prompt = null;
  let box = rows()[1].action('toggle')[0];
  box.checked = false;
  await box.fire('change');
  assert.ok(!calls.some((x) => x.key.endsWith('/disable')));
  assert.equal(rows()[1].action('toggle')[0].checked, true);
  box = rows()[1].action('toggle')[0];
  box.checked = true;
  await box.fire('change');
  assert.ok(calls.some((x) => x.key === 'POST /api/keys/k1/enable'));
});

test('eye reveals the key, then the injected 10 s timer re-masks and clears it from the DOM', async () => {
  const { app, rows, timers, copied } = setup();
  await app.init();
  await rows()[1].action('reveal')[0].fire('click');
  assert.match(rows()[1].textContent, new RegExp(RAW));
  assert.equal(timers.pending.at(-1).ms, 10000);
  await rows()[1].action('copy-revealed')[0].fire('click');
  assert.deepEqual(copied, [RAW]);
  await timers.run();
  assert.ok(!rows()[1].textContent.includes(RAW));
  assert.match(rows()[1].textContent, /abcd\.\.\.wxyz/);
  assert.equal(rows()[1].action('copy-revealed').length, 0);
});

test('clicking the eye again hides early and cancels the timer', async () => {
  const { app, rows, timers } = setup();
  await app.init();
  await rows()[1].action('reveal')[0].fire('click');
  await rows()[1].action('reveal')[0].fire('click');
  assert.ok(!rows()[1].textContent.includes(RAW));
  assert.ok(timers.pending.every((t) => t.cleared));
});

test('reveal 404 shows the message and no key', async () => {
  const { app, rows, el } = setup({ routes: { 'GET /api/keys/k1/reveal': () => ({ __status: 404, __body: { error: 'no_copy', message: 'No stored copy' } }) } });
  await app.init();
  await rows()[1].action('reveal')[0].fire('click');
  assert.match(el('pageMsg').textContent, /Use Regenerate/);
  assert.ok(!rows()[1].textContent.includes(RAW));
});

test('create flow: validates, posts payload with keepCopy, shows the one-time key, then clears it', async () => {
  const { app, el, rows, calls, copied } = setup();
  await app.init();
  await el('newBtn').fire('click');
  assert.equal(el('editDlg').hidden, false);
  assert.equal(el('fLineRate').placeholder, 'default 20,000');
  assert.equal(el('fKeepCopy').checked, true);
  el('fEmail').value = 'new@x.co';
  el('fPlayer').value = 'Jaina';
  el('fTags').value = 'a, b';
  el('fLineRate').value = '500';
  el('fExpires').value = '2031-01-01';
  el('fKeepCopy').checked = false;
  await el('dlgSave').fire('click');
  const c = calls.find((x) => x.key === 'POST /api/keys');
  assert.deepEqual(c.body, {
    email: 'new@x.co', player: 'Jaina', tags: ['a', 'b'], notes: '', expiresAt: '2031-01-01T23:59:59Z',
    limits: { lineRate: 500, lineBurst: null, byteRate: null, byteBurst: null, maxStreams: null }, keepCopy: false,
  });
  assert.equal(el('editDlg').hidden, true);
  assert.equal(el('keyDlg').hidden, false);
  assert.equal(el('newKeyText').textContent, RAW);
  assert.ok(rows().some((r) => r.attributes['data-id'] === 'k9'));
  await el('copyNewKey').fire('click');
  assert.deepEqual(copied, [RAW]);
  assert.equal(el('copyMsg').textContent, 'Copied.');
  await el('closeKey').fire('click');
  assert.equal(el('newKeyText').textContent, '');
  assert.equal(el('keyDlg').hidden, true);
});

test('validation blocks a bad email and zero/negative limits without calling the server', async () => {
  const { app, el, calls } = setup();
  await app.init();
  await el('newBtn').fire('click');
  el('fEmail').value = 'not-an-email';
  el('fLineRate').value = '0';
  el('fMaxStreams').value = '-3';
  await el('dlgSave').fire('click');
  assert.match(el('eEmail').textContent, /valid/);
  assert.match(el('eLineRate').textContent, /greater than 0/);
  assert.match(el('eMaxStreams').textContent, /greater than 0/);
  assert.equal(el('editDlg').hidden, false);
  assert.ok(!calls.some((x) => x.key === 'POST /api/keys'));
  el('fEmail').value = '';
  await el('dlgSave').fire('click');
  assert.match(el('eEmail').textContent, /required/);
});

test('a server error is shown in the dialog and the dialog stays open', async () => {
  const { app, el } = setup({ routes: { 'POST /api/keys': () => ({ __status: 409, __body: { error: 'dup', message: 'Email already has a key' } }) } });
  await app.init();
  await el('newBtn').fire('click');
  el('fEmail').value = 'a@b.co';
  await el('dlgSave').fire('click');
  assert.equal(el('dlgErr').textContent, 'Email already has a key');
  assert.equal(el('editDlg').hidden, false);
  assert.equal(el('keyDlg').hidden, true);
});

test('a list error is shown on the page', async () => {
  const { app, el } = setup({ routes: { 'GET /api/keys': () => ({ __status: 500, __body: { error: 'x', message: 'vault locked' } }) } });
  await app.init();
  assert.equal(el('pageMsg').textContent, 'vault locked');
});

test('edit pre-fills the dialog, hides keep-copy and PUTs the payload', async () => {
  const { app, el, rows, calls } = setup();
  await app.init();
  await rows()[1].action('edit')[0].fire('click');
  assert.equal(el('dlgTitle').textContent, 'Edit key');
  assert.equal(el('fEmail').value, 'alice@x.co');
  assert.equal(el('keepCopyRow').hidden, true);
  el('fPlayer').value = 'Changed';
  await el('dlgSave').fire('click');
  const c = calls.find((x) => x.key === 'PUT /api/keys/k1');
  assert.equal(c.body.player, 'Changed');
  assert.equal('keepCopy' in c.body, false);
  assert.equal(el('editDlg').hidden, true);
  assert.equal(el('keyDlg').hidden, true);
  assert.match(rows().find((r) => r.attributes['data-id'] === 'k1').textContent, /alice@x\.co/);
});

test('regenerate shows the new one-time key', async () => {
  const { app, el, rows, calls } = setup();
  await app.init();
  await rows()[1].action('regenerate')[0].fire('click');
  assert.deepEqual(calls.find((x) => x.key === 'POST /api/keys/k1/regenerate').body, { keepCopy: true });
  assert.equal(el('newKeyText').textContent, 'NEW-RAW');
  assert.equal(el('keyDlg').hidden, false);
});

test('delete asks for confirmation, then removes the row', async () => {
  const { app, rows, calls, answers } = setup();
  await app.init();
  answers.confirm = false;
  await rows()[1].action('delete')[0].fire('click');
  assert.ok(!calls.some((x) => x.key.startsWith('DELETE')));
  assert.equal(rows().length, 3);
  answers.confirm = true;
  await rows()[1].action('delete')[0].fire('click');
  assert.ok(calls.some((x) => x.key === 'DELETE /api/keys/k1'));
  assert.equal(rows().length, 2);
});

test('refresh reloads and drops any revealed key', async () => {
  const { app, el, rows, calls } = setup();
  await app.init();
  await rows()[1].action('reveal')[0].fire('click');
  await el('refreshBtn').fire('click');
  assert.equal(calls.filter((c) => c.key === 'GET /api/keys').length, 2);
  assert.ok(!rows()[1].textContent.includes(RAW));
});

test('server text is rendered as text, never as markup', async () => {
  const evil = '<img src=x onerror=alert(1)>';
  const { app, rows } = setup({ keys: [mk({ email: evil, player: evil, tags: [evil] })] });
  await app.init();
  assert.ok(rows()[0].textContent.includes(evil));
  assert.equal(rows()[0].findAll((e) => e.tagName === 'IMG').length, 0);
});
