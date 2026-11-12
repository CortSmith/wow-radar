// Bug: a selected contact could not be unselected. Every path must deselect and revert the sweep scope.
import test from 'node:test';
import assert from 'node:assert/strict';
import { createApp } from '../js/core.js';
import { loaders } from '../js/features.js';
import { buildScene } from '../js/scene.js';
import { demoApi } from '../dev-demo.mjs';
import { createFakeDocument } from './fake-dom.js';
import { createFakeTimers } from './fake-timers.js';
import { visibleContacts } from '../js/staleness.js';

const T0 = 1_760_000_000_000;
const DEBOUNCE_MS = 300;
const settle = async (n = 6) => { for (let i = 0; i < n; i++) await new Promise((r) => setImmediate(r)); };
const SIZE = { w: 800, h: 600 };

async function boot() {
  const features = await Promise.all(loaders.map(async (l) => (await l()).default));
  const timers = createFakeTimers(T0);
  const doc = createFakeDocument();
  const requests = [];
  const env = {
    document: doc, window: null, timers, now: timers.now, features, storage: null, logs: [],
    log: (m, e) => env.logs.push([m, e && e.message]),
    fetch: async (url) => {
      requests.push(url);
      const u = new URL(url, 'http://x');
      if (!u.pathname.startsWith('/api/')) return { ok: false, status: 404, json: async () => null };
      const out = demoApi(url, timers.now());
      return { ok: out.status < 400, status: out.status, json: async () => out.body };
    },
  };
  const app = createApp(env);
  app.start();
  await timers.advance(0); await settle();
  const radar = doc.querySelector('[data-mount="radar"]');
  const side = doc.querySelector('[data-mount="side-list"]');
  const top = doc.querySelector('[data-mount="side-top"]');
  return {
    app, env, timers, doc, requests, top,
    canvas: radar.find((n) => n.tagName === 'CANVAS'),
    rows: () => side.findAll((n) => n.tagName === 'LI'),
    input: top.find((n) => n.tagName === 'INPUT'),
    clearBtn: top.find((n) => n.tagName === 'BUTTON'),
    sweeps: () => requests.filter((u) => u.startsWith('/api/v1/sweep')).map((u) => new URL(u, 'http://x').searchParams),
    pips: () => buildScene({ state: app.state, now: timers.now(), size: SIZE, view: { z: 1, cx: 50, cy: 50 }, opts: { fixed: true, rangeYd: 300 }, image: null }).pips,
    async select(c) { app.ctx.setPlayer({ guid: c.guid, name: c.name }); await timers.advance(DEBOUNCE_MS); await settle(); },
  };
}

function click(h, pip) {
  const at = pip ? { clientX: pip.sx, clientY: pip.sy } : { clientX: 2, clientY: 2 };
  h.canvas.dispatch('mousedown', at);
  h.canvas.dispatch('mouseup', at);
}

/** After a deselect: no player, an immediate (debounced) sweep with the given scope and since=0, nothing highlighted. */
async function expectReverted(h, scope = 'all') {
  assert.equal(h.app.state.player, null);
  await h.timers.advance(DEBOUNCE_MS - 1); await settle();
  const before = h.sweeps().length;
  await h.timers.advance(1); await settle();
  const s = h.sweeps();
  assert.equal(s.length, before + 1, 'one sweep after the debounce');
  const last = s[s.length - 1];
  assert.equal(last.get('scope'), scope);
  assert.equal(last.get('since'), '0');
  assert.equal(last.has('guid'), false);
  assert.equal(last.has('trail'), false);
  assert.equal(h.app.state.trail.length, 0);
  assert.equal(h.top.find((n) => n.className.includes('trackInfo')).hidden, true);
  assert.equal(h.rows().filter((n) => n.className.includes('sel')).length, 0);
  assert.equal(h.pips().filter((p) => p.selected).length, 0);
  assert.deepEqual(h.env.logs, []);
}

test('setPlayer(null) emits selection and clears the player', async () => {
  const h = await boot();
  const seen = [];
  h.app.bus.on('selection', () => seen.push(h.app.state.player));
  const c = [...h.app.state.contacts.values()][0];
  h.app.ctx.setPlayer({ guid: c.guid, name: c.name });
  h.app.ctx.setPlayer(null);
  assert.equal(seen.length, 2);
  assert.equal(seen[1], null);
  h.app.stop();
});

test('clicking the selected list row again deselects', async () => {
  const h = await boot();
  await h.select([...h.app.state.contacts.values()][0]);
  const row = h.rows().find((n) => n.className.includes('sel'));
  assert.ok(row, 'selected row highlighted');
  row.dispatch('click');
  await expectReverted(h);
  h.app.stop();
});

test('clicking the selected blip again deselects', async () => {
  const h = await boot();
  const pip = h.pips()[0];
  assert.ok(pip, 'a blip exists');
  click(h, pip);
  assert.equal(h.app.state.player.guid, pip.guid);
  await h.timers.advance(DEBOUNCE_MS); await settle();
  click(h, h.pips().find((p) => p.guid === pip.guid));
  await expectReverted(h);
  h.app.stop();
});

test('clicking empty canvas space deselects', async () => {
  const h = await boot();
  await h.select([...h.app.state.contacts.values()][0]);
  click(h, null);
  await expectReverted(h);
  h.app.stop();
});

test('empty canvas click with nothing selected triggers no sweep and keeps the search text', async () => {
  const h = await boot();
  h.input.value = 'abc';
  h.input.dispatch('input');
  const n = h.sweeps().length;
  click(h, null);
  await h.timers.advance(DEBOUNCE_MS); await settle();
  assert.equal(h.sweeps().length, n);
  assert.equal(h.input.value, 'abc');
  h.app.stop();
});

test('a drag that ends on empty space does not deselect', async () => {
  const h = await boot();
  const c = [...h.app.state.contacts.values()][0];
  await h.select(c);
  h.canvas.dispatch('mousedown', { clientX: 2, clientY: 2 });
  h.canvas.dispatch('mouseup', { clientX: 60, clientY: 60 });
  assert.equal(h.app.state.player.guid, c.guid);
  h.app.stop();
});

test('Escape deselects, clears the search box and hides its clear button', async () => {
  const h = await boot();
  const c = [...h.app.state.contacts.values()][0];
  h.input.value = c.name;
  h.input.dispatch('input');
  await h.select(c);
  h.doc.dispatch('keydown', { key: 'Escape' });
  assert.equal(h.input.value, '');
  assert.equal(h.clearBtn.hidden, true);
  await expectReverted(h);
  h.app.stop();
});

test('Escape with nothing selected still clears typed search text; other keys do nothing', async () => {
  const h = await boot();
  h.input.value = 'abc';
  h.input.dispatch('input');
  assert.equal(h.clearBtn.hidden, false);
  h.doc.dispatch('keydown', { key: 'a' });
  assert.equal(h.input.value, 'abc');
  h.doc.dispatch('keydown', { key: 'Escape' });
  assert.equal(h.input.value, '');
  assert.equal(h.clearBtn.hidden, true);
  h.app.stop();
});

test('the search clear button deselects', async () => {
  const h = await boot();
  const c = [...h.app.state.contacts.values()][0];
  h.input.value = c.name;
  h.input.dispatch('input');
  await h.select(c);
  h.clearBtn.dispatch('click');
  assert.equal(h.input.value, '');
  await expectReverted(h);
  h.app.stop();
});

test('deselecting returns to the zone scope when a zone is picked', async () => {
  const h = await boot();
  h.app.ctx.setZone({ mapId: 27, name: 'Dun Morogh' });
  await h.timers.advance(DEBOUNCE_MS); await settle();
  await h.select([...h.app.state.contacts.values()][0]);
  assert.equal(h.sweeps().pop().get('scope'), 'player');
  h.doc.dispatch('keydown', { key: 'Escape' });
  await expectReverted(h, 'zone');
  h.app.stop();
});

test('no stale player stays pinned after deselecting', async () => {
  const h = await boot();
  const c = [...h.app.state.contacts.values()][0];
  await h.select(c);
  h.app.state.contacts.get(c.guid).lastSeen = T0 - 3_600_000;
  assert.equal(visibleContacts(h.app.state.contacts, h.timers.now(), h.app.state.player.guid).length, 1);
  h.doc.dispatch('keydown', { key: 'Escape' });
  assert.equal(h.app.state.player, null);
  assert.equal(h.app.state.contacts.has(c.guid), false);
  h.app.stop();
});

test('a visible "Show everyone" bar appears while a contact is selected and deselects it', async () => {
  const h = await boot();
  const side = h.doc.querySelector('[data-mount="side-list"]');
  const bar = () => side.find((n) => n.className.includes('selection-bar'));
  const button = () => bar().find((n) => n.tagName === 'BUTTON');
  assert.equal(bar().hidden, true, 'hidden while nothing is selected');
  const c = [...h.app.state.contacts.values()][0];
  await h.select(c);
  assert.equal(bar().hidden, false, 'shown while a contact is selected');
  assert.ok(bar().textContent.includes(c.name), 'names the selected contact');
  assert.equal(button().textContent, 'Show everyone');
  button().dispatch('click');
  await expectReverted(h);
  assert.equal(bar().hidden, true, 'hidden again after deselecting');
  h.app.stop();
});
