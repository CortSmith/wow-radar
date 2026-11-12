// Stage 11 U4a: full roster list with greying + "Show only tracked". See deselect.test.js for the
// legacy single-select click behaviour this must not regress when nothing is tracked, and search.test.js
// for the chip-side behaviour of the same state.tracked set.
import test from 'node:test';
import assert from 'node:assert/strict';
import { createApp } from '../js/core.js';
import { loaders } from '../js/features.js';
import { demoApi } from '../dev-demo.mjs';
import { createFakeDocument } from './fake-dom.js';
import { createFakeTimers } from './fake-timers.js';
import { trackColorClass } from '../js/trackcolors.js';

const T0 = 1_760_000_000_000;
const settle = async (n = 6) => { for (let i = 0; i < n; i++) await new Promise((r) => setImmediate(r)); };

async function boot() {
  const features = await Promise.all(loaders.map(async (l) => (await l()).default));
  const timers = createFakeTimers(T0);
  const doc = createFakeDocument();
  const store = new Map();
  const env = {
    document: doc, window: null, timers, now: timers.now, features, logs: [],
    storage: { getItem: (k) => (store.has(k) ? store.get(k) : null), setItem: (k, v) => store.set(k, v), removeItem: (k) => store.delete(k) },
    log: (m, e) => env.logs.push([m, e && e.message]),
    fetch: async (url) => {
      const u = new URL(url, 'http://x');
      if (!u.pathname.startsWith('/api/')) return { ok: false, status: 404, json: async () => null };
      const out = demoApi(url, timers.now());
      return { ok: out.status < 400, status: out.status, json: async () => out.body };
    },
  };
  const app = createApp(env);
  app.start();
  await timers.advance(0); await settle();
  const side = doc.querySelector('[data-mount="side-list"]');
  const controls = doc.querySelector('[data-mount="header-controls"]');
  return {
    app, env, timers, doc, side, controls,
    rows: () => side.findAll((n) => n.tagName === 'LI'),
    summaries: () => side.findAll((n) => n.className === 'summary muted'),
  };
}

function contacts(h) {
  return [...h.app.state.contacts.values()];
}

test('zero tracked: rendering is unchanged (no header line, no dim rows, no x buttons)', async () => {
  const h = await boot();
  const rows = h.rows();
  assert.ok(rows.length > 0, 'roster rendered');
  for (const r of rows) {
    assert.ok(!r.className.includes('dim'), 'no row is dimmed with nothing tracked');
    assert.equal(r.children.some((n) => n.tagName === 'BUTTON'), false, 'no x button on any row');
  }
  const sums = h.summaries();
  const visible = sums.filter((n) => !n.hidden);
  assert.equal(visible.length, 1, 'only the plain summary line is visible (header line hidden)');
  h.app.stop();
});

test('2 tracked: tracked rows normal, others .dim, header line shows correct counts', async () => {
  const h = await boot();
  const [a, b] = contacts(h);
  h.app.ctx.addPlayer({ guid: a.guid, name: a.name });
  h.app.ctx.addPlayer({ guid: b.guid, name: b.name });
  const rows = h.rows();
  const dimRows = rows.filter((n) => n.className.includes('dim'));
  const trackedRows = rows.filter((n) => !n.className.includes('dim'));
  assert.ok(dimRows.length > 0, 'non-tracked rows are dimmed');
  assert.equal(trackedRows.length, 2, 'exactly the 2 tracked rows are not dimmed');
  for (const r of dimRows) {
    assert.equal(r.getAttribute('title'), 'not in your search');
    assert.equal(r.getAttribute('aria-label'), 'not in your search');
  }
  for (const r of trackedRows) {
    assert.ok(r.children.some((n) => n.tagName === 'BUTTON'), 'tracked row has an x button');
    assert.ok(r.children.some((n) => n.className && n.className.startsWith('sw track-')), 'tracked row has a track-colour marker');
  }
  const header = h.side.find((n) => n.className === 'summary muted' && n.textContent.startsWith('Tracking'));
  assert.ok(header, 'header line rendered');
  assert.ok(header.textContent.includes('Tracking 2 of'), header.textContent);
  h.app.stop();
});

test("a tracked row's x removes only that player, not the others", async () => {
  const h = await boot();
  const [a, b, c] = contacts(h);
  for (const p of [a, b, c]) h.app.ctx.addPlayer({ guid: p.guid, name: p.name });
  assert.equal(h.app.state.tracked.size, 3);
  const bRow = h.rows().find((n) => n.textContent.includes(b.name) && n.children.some((x) => x.tagName === 'BUTTON'));
  const x = bRow.children.find((n) => n.tagName === 'BUTTON');
  x.dispatch('click');
  assert.equal(h.app.state.tracked.has(b.guid), false);
  assert.equal(h.app.state.tracked.has(a.guid), true);
  assert.equal(h.app.state.tracked.has(c.guid), true);
  h.app.stop();
});

test('clicking a non-tracked (dim) row adds it to the tracked set', async () => {
  const h = await boot();
  const [a, b] = contacts(h);
  h.app.ctx.addPlayer({ guid: a.guid, name: a.name });
  const bRow = h.rows().find((n) => n.textContent.includes(b.name));
  assert.ok(bRow.className.includes('dim'));
  bRow.dispatch('click');
  assert.equal(h.app.state.tracked.has(b.guid), true);
  assert.equal(h.app.state.tracked.size, 2);
  h.app.stop();
});

test('clicking an already-tracked row focuses it without adding a duplicate', async () => {
  const h = await boot();
  const [a, b] = contacts(h);
  h.app.ctx.addPlayer({ guid: a.guid, name: a.name });
  h.app.ctx.addPlayer({ guid: b.guid, name: b.name });
  assert.equal(h.app.state.player.guid, b.guid);
  const aRow = h.rows().find((n) => n.textContent.includes(a.name) && n.children.some((x) => x.tagName === 'BUTTON'));
  aRow.dispatch('click');
  assert.equal(h.app.state.tracked.size, 2, 'focusing never adds or removes anyone');
  assert.equal(h.app.state.player.guid, a.guid, 'a is now focused');
  h.app.stop();
});

test('"Show only tracked" hides non-tracked rows when checked and persists via prefs', async () => {
  const h = await boot();
  const [a, b] = contacts(h);
  h.app.ctx.addPlayer({ guid: a.guid, name: a.name });
  const label = h.controls.find((n) => n.tagName === 'LABEL' && n.textContent.includes('Show only tracked'));
  assert.ok(label, 'the "Show only tracked" control is mounted');
  const box = label.children.find((n) => n.tagName === 'INPUT');
  assert.ok(box, 'the "Show only tracked" checkbox is mounted');
  assert.equal(box.checked, false, 'defaults OFF');
  box.checked = true;
  box.dispatch('change');
  const rows = h.rows();
  assert.equal(rows.length, 1, 'only the tracked row remains');
  assert.equal(rows[0].textContent.includes(a.name), true);
  const saved = JSON.parse(h.env.storage.getItem('radarLive.prefs'));
  assert.equal(saved.onlyTracked, true, 'persisted via ctx.prefs under the shared prefs key');
  h.app.stop();
});

test('track colours used in the list match js/trackcolors.js and stay stable across re-renders', async () => {
  const h = await boot();
  const [a] = contacts(h);
  h.app.ctx.addPlayer({ guid: a.guid, name: a.name });
  const row = () => h.rows().find((n) => n.textContent.includes(a.name));
  const marker = () => row().children.find((n) => n.className && n.className.startsWith('sw track-'));
  const before = marker().className;
  h.app.ctx.focusPlayer(a.guid); // forces a re-render via the 'selection' bus event
  assert.equal(marker().className, before);
  assert.equal(before, `sw ${trackColorClass(a.guid)}`);
  h.app.stop();
});
