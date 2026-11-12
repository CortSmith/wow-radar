// Zone images: maps/index.json format, the picker with and without maps, same-origin image URLs.
import test from 'node:test';
import assert from 'node:assert/strict';
import { createApp } from '../js/core.js';
import { findMapEntry, mapImageFor } from '../js/charts.js';
import { loaders } from '../js/features.js';
import { demoApi } from '../dev-demo.mjs';
import { createFakeDocument } from './fake-dom.js';
import { createFakeTimers } from './fake-timers.js';

const T0 = 1_760_000_000_000;
const settle = async (n = 8) => { for (let i = 0; i < n; i++) await new Promise((r) => setImmediate(r)); };

// The shape written by radar-cloud\site\maps\index.json.
const INDEX = {
  zones: {
    dunmorogh: { name: 'Dun Morogh', file: 'DunMorogh.webp', w: 1002, h: 668, revealed: true },
    elwynnforest: { name: 'Elwynn Forest', file: 'Elwynn.webp', w: 1002, h: 668, revealed: true },
    evil: { name: 'Evil', file: '../../etc/passwd', w: 1, h: 1, revealed: true },
    remote: { name: 'Remote', file: 'http://evil.example/x.webp', w: 1, h: 1, revealed: true },
  },
};

test('findMapEntry matches chart names ignoring case and punctuation', () => {
  assert.equal(findMapEntry(INDEX, 'dun  morogh').file, 'DunMorogh.webp');
  assert.equal(findMapEntry(INDEX, 'Nowhere'), null);
  assert.equal(findMapEntry(null, 'Dun Morogh'), null);
  assert.equal(findMapEntry({}, 'Dun Morogh'), null);
});

test('mapImageFor builds same-origin urls and refuses unsafe file names', () => {
  assert.deepEqual(mapImageFor(INDEX, 'Dun Morogh', 'maps/'), { url: 'maps/DunMorogh.webp', w: 1002, h: 668 });
  assert.equal(mapImageFor(INDEX, 'Evil', 'maps/'), null);
  assert.equal(mapImageFor(INDEX, 'Remote', 'maps/'), null);
  assert.equal(mapImageFor({ zones: { a: { name: 'A', file: 'a.webp', w: 0, h: 5 } } }, 'A', 'maps/'), null);
  assert.equal(mapImageFor(null, 'Dun Morogh', 'maps/'), null);
});

async function boot({ withIndex }) {
  const features = await Promise.all(loaders.map(async (l) => (await l()).default));
  const timers = createFakeTimers(T0);
  const doc = createFakeDocument();
  const urls = [];
  const env = {
    document: doc, window: null, timers, now: timers.now, features, storage: null, logs: [],
    log: (m, e) => env.logs.push([m, e && e.message]),
    fetch: async (url) => {
      urls.push(url);
      const u = new URL(url, 'http://x');
      if (u.pathname === '/maps/index.json' || url === 'maps/index.json') {
        return withIndex ? { ok: true, status: 200, json: async () => INDEX } : { ok: false, status: 404, json: async () => null };
      }
      if (!url.startsWith('/api/')) return { ok: false, status: 404, json: async () => null };
      const out = demoApi(url, timers.now());
      return { ok: out.status < 400, status: out.status, json: async () => out.body };
    },
  };
  const app = createApp(env);
  app.start();
  await timers.advance(0); await settle();
  const note = doc.querySelector('[data-mount="header-controls"]').find((n) => n.className.includes('mapNote'));
  return { app, env, timers, urls, note };
}

test('with maps present the picked zone gets its image (same-origin relative url)', async () => {
  const h = await boot({ withIndex: true });
  assert.ok(h.urls.includes('maps/index.json'));
  h.app.ctx.setZone({ mapId: 27, name: 'Dun Morogh' });
  await h.timers.advance(0); await settle();
  assert.deepEqual(h.app.state.mapImage, { url: 'maps/DunMorogh.webp', w: 1002, h: 668 });
  assert.equal(h.note.hidden, true);
  assert.deepEqual(h.env.logs, []);
  h.app.stop();
});

test('without index.json the picker degrades: no image, a muted note, no errors', async () => {
  const h = await boot({ withIndex: false });
  h.app.ctx.setZone({ mapId: 27, name: 'Dun Morogh' });
  await h.timers.advance(0); await settle();
  assert.equal(h.app.state.mapImage, null);
  assert.equal(h.note.hidden, false);
  assert.match(h.note.textContent, /no map image/i);
  assert.ok(h.note.className.includes('muted'));
  assert.equal(h.app.state.charts.length > 0, true);
  assert.deepEqual(h.env.logs, []);
  h.app.stop();
});

test('a zone missing from index.json shows the note but keeps the grid', async () => {
  const h = await boot({ withIndex: true });
  h.app.state.charts.push({ mapId: 999, name: 'Unmapped Place', x0: 0, x1: 1, y0: 0, y1: 1 });
  h.app.ctx.setZone({ mapId: 999, name: 'Unmapped Place' });
  await h.timers.advance(0); await settle();
  assert.equal(h.app.state.mapImage, null);
  assert.equal(h.note.hidden, false);
  h.app.stop();
});
