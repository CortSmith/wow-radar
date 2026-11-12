import test from 'node:test';
import assert from 'node:assert/strict';
import { readdirSync } from 'node:fs';
import { createApp } from '../js/core.js';
import { loaders } from '../js/features.js';
import { demoApi } from '../dev-demo.mjs';
import { createFakeDocument } from './fake-dom.js';
import { createFakeTimers } from './fake-timers.js';

const T0 = 1_760_000_000_000;
const settle = async (n = 6) => { for (let i = 0; i < n; i++) await new Promise((r) => setImmediate(r)); };

async function allFeatures() {
  return Promise.all(loaders.map(async (l) => (await l()).default));
}

function makeEnv(features, over = {}) {
  const timers = createFakeTimers(T0);
  const doc = createFakeDocument();
  const requests = [];
  const store = new Map();
  const env = {
    document: doc,
    window: null,
    timers,
    now: timers.now,
    features,
    storage: { getItem: (k) => (store.has(k) ? store.get(k) : null), setItem: (k, v) => store.set(k, v), removeItem: (k) => store.delete(k) },
    log: (m, e) => { env.logs.push([m, e && e.message]); },
    logs: [],
    fetch: async (url, init = {}) => {
      requests.push({ url, headers: init.headers || {}, signal: init.signal });
      if (over.fetch) {
        const r = over.fetch(url, init);
        if (r) return r;
      }
      const u = new URL(url, 'http://x');
      if (!u.pathname.startsWith('/api/')) return { ok: false, status: 404, json: async () => null };
      const out = demoApi(url, timers.now());
      return { ok: out.status < 400, status: out.status, json: async () => out.body };
    },
  };
  return { env, timers, doc, requests };
}

const sweeps = (requests) => requests.filter((r) => r.url.startsWith('/api/v1/sweep')).map((r) => new URL(r.url, 'http://x').searchParams);

test('boots: first sweep since=0, then every 60 s with since = lastNow - 5000', async () => {
  const { env, timers, doc, requests } = makeEnv(await allFeatures());
  const app = createApp(env);
  app.start();
  await timers.advance(0); await settle();
  let s = sweeps(requests);
  assert.equal(s.length, 1);
  assert.equal(s[0].get('since'), '0');
  assert.equal(s[0].get('scope'), 'all');
  assert.equal(s[0].has('rs'), false);
  assert.ok(app.state.contacts.size > 0);
  assert.equal(app.state.conn, 'ok');
  assert.ok(doc.querySelector('[data-mount="side-list"]').textContent.includes('contacts ('));
  const lastNow = app.state.lastNow;
  await timers.advance(59_999); await settle();
  assert.equal(sweeps(requests).length, 1);
  await timers.advance(1); await settle();
  s = sweeps(requests);
  assert.equal(s.length, 2);
  assert.equal(s[1].get('since'), String(lastNow - 5000));
  app.stop();
});

test('every feature mounts something and the feature list is complete', async () => {
  const features = await allFeatures();
  assert.equal(features.length, loaders.length);
  assert.deepEqual(new Set(features.map((f) => f.name)).size, features.length);
  const { env, timers } = makeEnv(features);
  const app = createApp(env);
  app.start();
  await timers.advance(0); await settle();
  assert.deepEqual(app.featureNames().sort(), features.map((f) => f.name).sort());
  assert.deepEqual(env.logs, []);
  app.stop();
});

test('removing any one feature leaves the page working', async () => {
  const features = await allFeatures();
  for (const gone of features) {
    const rest = features.filter((f) => f !== gone);
    const { env, timers, requests } = makeEnv(rest);
    const app = createApp(env);
    app.start();
    await timers.advance(0); await settle();
    assert.equal(sweeps(requests).length, 1, `without ${gone.name}`);
    app.ctx.setZone({ mapId: 27, name: 'Dun Morogh' });
    app.ctx.setPlayer({ guid: [...app.state.contacts.keys()][0] || 'Player-1-1', name: 'x' });
    await timers.advance(400); await settle();
    app.ctx.setPlayer(null);
    await timers.advance(60_400); await settle();
    assert.equal(app.state.conn, 'ok', `without ${gone.name}`);
    assert.deepEqual(env.logs, [], `without ${gone.name}`);
    app.stop();
  }
});

test('page still works with every feature removed', async () => {
  const { env, timers, requests } = makeEnv([]);
  const app = createApp(env);
  app.start();
  await timers.advance(0); await settle();
  assert.equal(sweeps(requests).length, 1);
  assert.equal(app.state.conn, 'ok');
  app.stop();
});

test('a feature that throws in init is skipped, others keep running', async () => {
  const bad = { name: 'bad', init() { throw new Error('nope'); } };
  const { env, timers, requests } = makeEnv([bad, ...(await allFeatures())]);
  const app = createApp(env);
  app.start();
  await timers.advance(0); await settle();
  assert.equal(app.featureNames().includes('bad'), false);
  assert.equal(sweeps(requests).length, 1);
  assert.equal(env.logs.length, 1);
});

test('zone and group selection never discard the roster: the page filters what it already has', async () => {
  const { env, timers, requests } = makeEnv(await allFeatures());
  const app = createApp(env);
  app.start();
  await timers.advance(0); await settle();
  const before = app.state.contacts.size;
  assert.ok(before > 0);
  app.ctx.setZone({ mapId: 27, name: 'Dun Morogh' });
  app.ctx.setFilters({ hostile: false });
  assert.equal(app.state.contacts.size, before, 'rows are kept immediately, nothing waits for the network');
  await timers.advance(300); await settle();
  for (const s of sweeps(requests)) assert.equal(s.get('scope'), 'all');
  assert.equal(app.state.contacts.size >= before, true);
  app.stop();
});

test('selecting a player sweeps scope=player with trail=1 and fetches the trail', async () => {
  const { env, timers, requests } = makeEnv(await allFeatures());
  const app = createApp(env);
  app.start();
  await timers.advance(0); await settle();
  const guid = [...app.state.contacts.keys()][0];
  app.ctx.setPlayer({ guid, name: 'Pick' });
  await timers.advance(300); await settle();
  const s = sweeps(requests).at(-1);
  assert.equal(s.get('scope'), 'player');
  assert.equal(s.get('guid'), guid);
  assert.equal(s.get('trail'), '1');
  assert.ok(requests.some((r) => r.url.startsWith('/api/v1/trail')));
  assert.ok(app.state.trail.length > 0);
  app.ctx.setRuleset('rp');
  await timers.advance(300); await settle();
  assert.equal(sweeps(requests).at(-1).get('rs'), 'rp');
  app.stop();
});

test('search box debounces 250 ms and queries /contacts', async () => {
  const { env, timers, doc, requests } = makeEnv(await allFeatures());
  const app = createApp(env);
  app.start();
  await timers.advance(0); await settle();
  const input = doc.querySelector('[data-mount="side-top"]').find((n) => n.tagName === 'INPUT');
  input.value = 'bre';
  input.dispatch('input');
  await timers.advance(249); await settle();
  assert.equal(requests.some((r) => r.url.startsWith('/api/v1/contacts')), false);
  await timers.advance(1); await settle();
  const req = requests.find((r) => r.url.startsWith('/api/v1/contacts'));
  assert.ok(req && req.url.includes('q=bre'));
  const results = doc.querySelector('[data-mount="side-top"]').find((n) => n.className === 'results');
  assert.equal(results.hidden, false);
  assert.ok(results.textContent.includes('Brenna'));
  app.stop();
});

test('errors: connection dot turns error, backoff, recovery', async () => {
  let fail = true;
  const { env, timers, requests } = makeEnv(await allFeatures(), {
    fetch: (url) => (fail && url.startsWith('/api/v1/sweep') ? { ok: false, status: 503, json: async () => ({ error: 'down' }) } : null),
  });
  const app = createApp(env);
  app.start();
  await timers.advance(0); await settle();
  assert.equal(app.state.conn, 'error');
  await timers.advance(5000); await settle();
  assert.equal(sweeps(requests).length, 2);
  fail = false;
  await timers.advance(10_000); await settle();
  assert.equal(app.state.conn, 'ok');
  app.stop();
});

test('401 shows the key prompt; saving sends X-View-Key and keeps it in memory only by default', async () => {
  const { env, timers, doc, requests } = makeEnv(await allFeatures(), {
    fetch: (url, init) => (url.startsWith('/api/') && !(init.headers || {})['X-View-Key']
      ? { ok: false, status: 401, json: async () => ({ error: 'unauthorized' }) } : null),
  });
  const app = createApp(env);
  app.start();
  await timers.advance(0); await settle();
  const bar = doc.querySelector('[data-mount="notice"]').find((n) => n.className === 'keybar');
  assert.equal(bar.hidden, false);
  const input = bar.find((n) => n.tagName === 'INPUT');
  input.value = 'secret';
  bar.find((n) => n.tagName === 'BUTTON').dispatch('click');
  await timers.advance(0); await settle();
  assert.equal(requests.filter((r) => r.headers['X-View-Key']).at(-1).headers['X-View-Key'], 'secret');
  assert.equal(app.state.conn, 'ok');
  assert.equal(bar.hidden, true);
  assert.equal(env.storage.getItem('radarLive.viewKey'), null, 'key must not be persisted unless the user opts in');
  app.stop();
});

function keyEnv(extra) {
  return makeEnv(extra.features, {
    fetch: (url, init) => (url.startsWith('/api/') && !(init.headers || {})['X-View-Key']
      ? { ok: false, status: 401, json: async () => ({ error: 'unauthorized' }) } : null),
  });
}

test('remember-on-this-device checkbox persists the key and a new page load reuses it', async () => {
  const features = await allFeatures();
  const first = keyEnv({ features });
  const app = createApp(first.env);
  app.start();
  await first.timers.advance(0); await settle();
  const bar = first.doc.querySelector('[data-mount="notice"]').find((n) => n.className === 'keybar');
  const remember = bar.find((n) => n.tagName === 'INPUT' && n.type === 'checkbox');
  assert.ok(remember, 'remember checkbox exists');
  assert.equal(remember.checked, false, 'unchecked by default');
  bar.find((n) => n.tagName === 'INPUT' && n.type === 'password').value = 'secret';
  remember.checked = true;
  bar.find((n) => n.tagName === 'BUTTON').dispatch('click');
  await first.timers.advance(0); await settle();
  assert.equal(first.env.storage.getItem('radarLive.viewKey'), '"secret"');
  app.stop();
  const second = keyEnv({ features });
  second.env.storage.setItem('radarLive.viewKey', first.env.storage.getItem('radarLive.viewKey'));
  const again = createApp(second.env);
  assert.equal(again.state.viewKey, 'secret');
  again.stop();
});

test('saving without the checkbox erases a previously remembered key', async () => {
  const features = await allFeatures();
  const { env, timers, doc } = keyEnv({ features });
  env.storage.setItem('radarLive.viewKey', '"old"');
  const app = createApp(env);
  app.start();
  await timers.advance(0); await settle();
  const bar = doc.querySelector('[data-mount="notice"]').find((n) => n.className === 'keybar');
  bar.find((n) => n.tagName === 'INPUT' && n.type === 'password').value = 'new';
  bar.find((n) => n.tagName === 'BUTTON').dispatch('click');
  await timers.advance(0); await settle();
  assert.equal(app.state.viewKey, 'new');
  assert.equal(env.storage.getItem('radarLive.viewKey'), null);
  app.stop();
});

test('hidden tab pauses sweeps and returning sweeps immediately', async () => {
  const { env, timers, doc, requests } = makeEnv(await allFeatures());
  const app = createApp(env);
  app.start();
  await timers.advance(0); await settle();
  doc.hidden = true; doc.dispatch('visibilitychange');
  await timers.advance(300_000); await settle();
  assert.equal(sweeps(requests).length, 1);
  doc.hidden = false; doc.dispatch('visibilitychange');
  await timers.advance(0); await settle();
  assert.equal(sweeps(requests).length, 2);
  app.stop();
});

test('status countdown text and the Update now button', async () => {
  const { env, timers, doc, requests } = makeEnv(await allFeatures());
  const app = createApp(env);
  app.start();
  await timers.advance(0); await settle();
  await timers.advance(10_000); await settle();
  const status = doc.querySelector('[data-mount="header-status"]');
  assert.match(status.textContent, /next update in 50s/);
  status.find((n) => n.tagName === 'BUTTON').dispatch('click');
  await timers.advance(0); await settle();
  assert.equal(sweeps(requests).length, 2);
  app.stop();
});

test('stale and gone contacts: list keeps stale, drops gone', async () => {
  const { env, timers, doc } = makeEnv(await allFeatures());
  const app = createApp(env);
  app.start();
  await timers.advance(0); await settle();
  const list = doc.querySelector('[data-mount="side-list"]');
  const names = list.findAll((n) => n.className === 'nm').map((n) => n.textContent);
  assert.ok(names.length > 0);
  const age = (c) => timers.now() - c.lastSeen;
  const all = [...app.state.contacts.values()];
  const stale = all.find((c) => age(c) >= 300_000 && age(c) < 900_000);
  assert.ok(stale, 'demo data has a stale contact');
  assert.equal(names.includes(stale.name), true);
  await timers.advance(11 * 60_000); await settle();
  const later = list.findAll((n) => n.className === 'nm').map((n) => n.textContent);
  assert.equal(later.includes(stale.name), false, 'dropped after 15 min without a refresh');
  app.stop();
});

test('every js file under js/features is listed in features.js', () => {
  const files = readdirSync(new URL('../js/features/', import.meta.url)).filter((f) => f.endsWith('.js'));
  assert.equal(files.length, loaders.length);
});
