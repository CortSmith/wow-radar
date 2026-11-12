// Stage 14 W3a: the world-map feature registers without throwing, fetches maps/world/index.json
// through the same same-site-JSON mechanism chart-picker.js uses for maps/index.json, mounts one
// canvas per continent, and degrades cleanly (empty-state note, no errors) when the asset is absent.
import test from 'node:test';
import assert from 'node:assert/strict';
import { createApp } from '../js/core.js';
import { loaders } from '../js/features.js';
import { demoApi } from '../dev-demo.mjs';
import { createFakeDocument } from './fake-dom.js';
import { createFakeTimers } from './fake-timers.js';
import { worldPathPoints, computeTrackedPaths, paintWorldPath, hitZoneRect, filterPatchFor, gotoZone } from '../js/features/world-map.js';

const T0 = 1_760_000_000_000;
const settle = async (n = 8) => { for (let i = 0; i < n; i++) await new Promise((r) => setImmediate(r)); };

const WORLD_INDEX = {
  continents: [
    { id: 'kalimdor', name: 'Kalimdor', file: 'Kalimdor.png', w: 1002, h: 668, zones: [{ mapId: 1440, name: 'Ashenvale', x: 418, y: 221, w: 157, h: 105 }] },
    { id: 'easternkingdoms', name: 'Eastern Kingdoms', file: 'EasternKingdoms.png', w: 1002, h: 668, zones: [{ mapId: 1431, name: 'Duskwood', x: 432, y: 489, w: 77, h: 51 }] },
  ],
};

// Stage 14 W3b: one continent sized 100x100 with a single zone covering it entirely (0,0,100,100).
// With the default 800x600 fake canvas and the default unzoomed view, this makes the zone rect's
// screen centre land on an exact, easy-to-reason-about pixel (400, 300) - see fitFrame/continentProjector
// in projection.js - so chip hit-testing in the hover test below doesn't need to replicate that math.
const SQUARE_WORLD_INDEX = {
  continents: [{ id: 'test', name: 'TestLand', file: 'Test.png', w: 100, h: 100, zones: [{ mapId: 999, name: 'TestZone', x: 0, y: 0, w: 100, h: 100 }] }],
};

async function boot({ withWorld, index }) {
  const features = await Promise.all(loaders.map(async (l) => (await l()).default));
  const timers = createFakeTimers(T0);
  const doc = createFakeDocument();
  const urls = [];
  const env = {
    document: doc, window: null, timers, now: timers.now, features, storage: null, logs: [],
    log: (m, e) => env.logs.push([m, e && e.message]),
    fetch: async (url) => {
      urls.push(url);
      if (url === 'maps/world/index.json') {
        return withWorld ? { ok: true, status: 200, json: async () => (index || WORLD_INDEX) } : { ok: false, status: 404, json: async () => null };
      }
      if (!url.startsWith('/api/')) return { ok: false, status: 404, json: async () => null };
      const out = demoApi(url, timers.now());
      return { ok: out.status < 400, status: out.status, json: async () => out.body };
    },
  };
  const app = createApp(env);
  app.start();
  await timers.advance(0); await settle();
  const world = doc.querySelector('[data-mount="world"]');
  return { app, env, timers, urls, world };
}

test('world-map is a registered feature, listed in features.js', async () => {
  const files = loaders.length;
  assert.ok(files > 0);
  const { app } = await boot({ withWorld: true });
  assert.ok(app.featureNames().includes('world-map'));
  app.stop();
});

test('fetches maps/world/index.json via the same mechanism as maps/index.json, no errors', async () => {
  const h = await boot({ withWorld: true });
  assert.ok(h.urls.includes('maps/world/index.json'));
  assert.deepEqual(h.env.logs, []);
  h.app.stop();
});

test('with the world index present, mounts one canvas per continent with its own label', async () => {
  const h = await boot({ withWorld: true });
  const canvases = h.world.findAll((n) => n.tagName === 'CANVAS');
  assert.equal(canvases.length, 2);
  const labels = h.world.findAll((n) => n.className === 'worldContinentName').map((n) => n.textContent);
  assert.deepEqual(labels, ['Kalimdor', 'Eastern Kingdoms']);
  const empty = h.world.find((n) => n.className.includes('empty'));
  assert.equal(empty.hidden, true);
  h.app.stop();
});

test('W3b: hovering a chip shows the zone name + exact friendly/hostile counts', async () => {
  const h = await boot({ withWorld: true, index: SQUARE_WORLD_INDEX });
  const now = h.timers.now();
  const c = (guid, reaction) => ({ guid, name: guid, reaction, mapId: 999, x: 0, y: 0, posTime: now, lastSeen: now });
  h.app.state.contacts.set('f1', c('f1', 'friendly'));
  h.app.state.contacts.set('f2', c('f2', 'friendly'));
  h.app.state.contacts.set('f3', c('f3', 'friendly'));
  h.app.state.contacts.set('h1', c('h1', 'hostile'));
  h.app.bus.emit('sweep', {});
  const canvas = h.world.find((n) => n.tagName === 'CANVAS');
  const tip = h.world.find((n) => n.className === 'tip');

  canvas.dispatch('mousemove', { clientX: 394, clientY: 300 }); // the friendly chip's exact centre
  assert.equal(tip.hidden, false);
  assert.equal(tip.textContent, 'TestZone\n3 friendly\n1 hostile');

  canvas.dispatch('mousemove', { clientX: 700, clientY: 590 }); // nowhere near either chip
  assert.equal(tip.hidden, true);
  h.app.stop();
});

test('W3b: a reaction bucket at 0 draws no chip for it (nothing to hover there)', async () => {
  const h = await boot({ withWorld: true, index: SQUARE_WORLD_INDEX });
  const now = h.timers.now();
  h.app.state.contacts.set('f1', { guid: 'f1', name: 'f1', reaction: 'friendly', mapId: 999, x: 0, y: 0, posTime: now, lastSeen: now });
  h.app.bus.emit('sweep', {});
  const canvas = h.world.find((n) => n.tagName === 'CANVAS');
  const tip = h.world.find((n) => n.className === 'tip');
  canvas.dispatch('mousemove', { clientX: 406, clientY: 300 }); // where the hostile chip would be if drawn
  assert.equal(tip.hidden, true);
  h.app.stop();
});

// W4: cross-zone world paths for tracked players.

test('W4: worldPathPoints returns ordered centre points, skipping a visit whose zone is not on this continent', () => {
  const zones = [{ mapId: 1440, x: 0, y: 0, w: 10, h: 10 }, { mapId: 1431, x: 20, y: 20, w: 10, h: 10 }];
  const project = (x, y) => [x, y]; // identity projector - easy centres
  const visits = [
    { mapId: 1440, enteredAt: 100 },
    { mapId: 9999, enteredAt: 150 }, // not one of this continent's zones - skipped, no crash
    { mapId: 1431, enteredAt: 200 },
  ];
  assert.deepEqual(worldPathPoints(visits, zones, project), [
    { x: 5, y: 5, enteredAt: 100 },
    { x: 25, y: 25, enteredAt: 200 },
  ]);
});

test('W4: worldPathPoints is empty for no visits or no zones, never throws', () => {
  const project = (x, y) => [x, y];
  assert.deepEqual(worldPathPoints([], [], project), []);
  assert.deepEqual(worldPathPoints(null, [{ mapId: 1, x: 0, y: 0, w: 1, h: 1 }], project), []);
  assert.deepEqual(worldPathPoints([{ mapId: 1, enteredAt: 1 }], [], project), []);
});

test("W4: computeTrackedPaths builds one path per tracked guid, touching both visited zones' rects, and never throws on a missing/empty trail", () => {
  const zones = [{ mapId: 1440, x: 0, y: 0, w: 10, h: 10 }, { mapId: 1431, x: 20, y: 20, w: 10, h: 10 }];
  const project = (x, y) => [x, y];
  const tracked = new Map([['p1', { guid: 'p1' }], ['p2', { guid: 'p2' }]]);
  const trackedData = new Map([
    ['p1', { contact: null, trail: [{ mapId: 1440, t: 100 }, { mapId: 1431, t: 200 }] }],
    // p2 has no trackedData entry at all yet (sweep hasn't landed one) - must not throw
  ]);
  const paths = computeTrackedPaths(tracked, trackedData, zones, project);
  assert.equal(paths.length, 2);
  const p1 = paths.find((p) => p.guid === 'p1');
  assert.deepEqual(p1.points, [{ x: 5, y: 5, enteredAt: 100 }, { x: 25, y: 25, enteredAt: 200 }]);
  const p2 = paths.find((p) => p.guid === 'p2');
  assert.deepEqual(p2.points, []);
});

function fakeCtx() {
  const calls = [];
  const state = { strokeStyle: null, fillStyle: null, lineWidth: null };
  const g = new Proxy(state, {
    get: (t, k) => (k in t ? t[k] : (...a) => { calls.push({ fn: k, strokeStyle: t.strokeStyle, fillStyle: t.fillStyle, lineWidth: t.lineWidth }); }),
    set: (t, k, v) => { t[k] = v; return true; },
  });
  return { g, calls };
}

test('W4: paintWorldPath strokes a growing-width line, drops a dot per point, and fills an arrowhead at the end, all in the given colour', () => {
  const { g, calls } = fakeCtx();
  paintWorldPath(g, [{ x: 0, y: 0, enteredAt: 1 }, { x: 10, y: 0, enteredAt: 2 }, { x: 20, y: 0, enteredAt: 3 }], '#abc123');
  const strokes = calls.filter((c) => c.fn === 'stroke');
  assert.equal(strokes.length, 2); // one per segment
  assert.ok(strokes.every((c) => c.strokeStyle === '#abc123'));
  assert.ok(strokes[1].lineWidth > strokes[0].lineWidth); // growing thickness toward the recent end
  const dots = calls.filter((c) => c.fn === 'arc');
  assert.equal(dots.length, 3); // one per visited point
  const fills = calls.filter((c) => c.fn === 'fill');
  assert.ok(fills.length >= 4); // 3 dots + the arrowhead, all in the player's colour
  assert.ok(fills.every((c) => c.fillStyle === '#abc123'));
});

test('W4: paintWorldPath draws only a dot (no line/arrowhead) for a single-point path, and nothing for an empty one', () => {
  const single = fakeCtx();
  paintWorldPath(single.g, [{ x: 1, y: 1, enteredAt: 1 }], '#fff');
  assert.ok(single.calls.some((c) => c.fn === 'arc'));
  assert.ok(!single.calls.some((c) => c.fn === 'stroke' || c.fn === 'moveTo'));

  const empty = fakeCtx();
  paintWorldPath(empty.g, [], '#fff');
  assert.deepEqual(empty.calls, []);
});

test('W4: two tracked players draw with visibly distinct track colours', () => {
  const { g, calls } = fakeCtx();
  paintWorldPath(g, [{ x: 0, y: 0 }, { x: 5, y: 5 }], '#111111');
  paintWorldPath(g, [{ x: 0, y: 0 }, { x: 5, y: 5 }], '#222222');
  const usedColors = new Set(calls.filter((c) => c.fn === 'fill').map((c) => c.fillStyle));
  assert.deepEqual([...usedColors].sort(), ['#111111', '#222222']);
});

test('W4: a tracked player with no trail (or no trackedData yet) draws without throwing, via the full app', async () => {
  const h = await boot({ withWorld: true, index: SQUARE_WORLD_INDEX });
  h.app.ctx.addPlayer({ guid: 'ghost', name: 'ghost' }); // tracked but no sweep has landed trackedData yet
  assert.doesNotThrow(() => h.app.bus.emit('selection'));
  assert.deepEqual(h.env.logs, []);
  h.app.stop();
});

// W5: click-to-navigate.

test('W5: filterPatchFor maps a chip reaction to the matching filter patch, and omits it for a plain zone click', () => {
  assert.deepEqual(filterPatchFor('friendly'), { friendly: true, hostile: false });
  assert.deepEqual(filterPatchFor('hostile'), { friendly: false, hostile: true });
  assert.equal(filterPatchFor(undefined), null);
  assert.equal(filterPatchFor(null), null);
});

test('W5: gotoZone always sets the zone, but calls setFilters only when a reaction is given', () => {
  const calls = { setZone: [], setFilters: [], setWorldView: [] };
  const ctx = {
    setZone: (z) => calls.setZone.push(z),
    setFilters: (p) => calls.setFilters.push(p),
    setWorldView: (v) => calls.setWorldView.push(v),
  };

  gotoZone(ctx, 1440, 'Ashenvale', 'friendly');
  assert.deepEqual(calls.setZone, [{ mapId: 1440, name: 'Ashenvale' }]);
  assert.deepEqual(calls.setFilters, [{ friendly: true, hostile: false }]);

  gotoZone(ctx, 1431, 'Duskwood', 'hostile');
  assert.deepEqual(calls.setZone[1], { mapId: 1431, name: 'Duskwood' });
  assert.deepEqual(calls.setFilters[1], { friendly: false, hostile: true });

  gotoZone(ctx, 1440, 'Ashenvale'); // plain zone click, no reaction - filters must be left untouched
  assert.deepEqual(calls.setZone[2], { mapId: 1440, name: 'Ashenvale' });
  assert.equal(calls.setFilters.length, 2); // unchanged - setFilters was NOT called a third time

  // W6: every gotoZone call - chip or plain zone click - also exits the world view so the user actually
  // sees the zone they clicked into.
  assert.deepEqual(calls.setWorldView, [false, false, false]);
});

test('W5: hitZoneRect finds the containing rect (inclusive edges) or null outside every rect', () => {
  const rects = [
    { mapId: 1, name: 'A', x0: 0, y0: 0, x1: 10, y1: 10 },
    { mapId: 2, name: 'B', x0: 20, y0: 20, x1: 10, y1: 30 }, // corners reversed on x - must still work
  ];
  assert.equal(hitZoneRect(rects, 5, 5).mapId, 1); // inside A
  assert.equal(hitZoneRect(rects, 0, 0).mapId, 1); // top-left edge, inclusive
  assert.equal(hitZoneRect(rects, 10, 10).mapId, 1); // bottom-right edge, inclusive
  assert.equal(hitZoneRect(rects, 11, 5), null); // just outside A, not in B either
  assert.equal(hitZoneRect(rects, 15, 25).mapId, 2); // inside B despite x0 > x1
  assert.equal(hitZoneRect([], 0, 0), null);
});

test('W5: clicking a friendly chip navigates to its zone with the friendly-only filter', async () => {
  const h = await boot({ withWorld: true, index: SQUARE_WORLD_INDEX });
  const now = h.timers.now();
  h.app.state.contacts.set('f1', { guid: 'f1', name: 'f1', reaction: 'friendly', mapId: 999, x: 0, y: 0, posTime: now, lastSeen: now });
  h.app.bus.emit('sweep', {});
  const canvas = h.world.find((n) => n.tagName === 'CANVAS');
  canvas.dispatch('mousedown', { clientX: 394, clientY: 300 });
  canvas.dispatch('mouseup', { clientX: 394, clientY: 300 }); // the friendly chip's exact centre
  assert.deepEqual(h.app.state.zone, { mapId: 999, name: 'TestZone' });
  assert.deepEqual(h.app.state.filters, { friendly: true, hostile: false, stale: true });
  h.app.stop();
});

test('W5: clicking a hostile chip navigates to its zone with the hostile-only filter', async () => {
  const h = await boot({ withWorld: true, index: SQUARE_WORLD_INDEX });
  const now = h.timers.now();
  h.app.state.contacts.set('h1', { guid: 'h1', name: 'h1', reaction: 'hostile', mapId: 999, x: 0, y: 0, posTime: now, lastSeen: now });
  h.app.bus.emit('sweep', {});
  const canvas = h.world.find((n) => n.tagName === 'CANVAS');
  canvas.dispatch('mousedown', { clientX: 406, clientY: 300 });
  canvas.dispatch('mouseup', { clientX: 406, clientY: 300 }); // the hostile chip's exact centre
  assert.deepEqual(h.app.state.zone, { mapId: 999, name: 'TestZone' });
  assert.deepEqual(h.app.state.filters, { friendly: false, hostile: true, stale: true });
  h.app.stop();
});

test('W5: clicking a zone\'s empty area (no chip) navigates there, leaving filters untouched', async () => {
  const h = await boot({ withWorld: true, index: SQUARE_WORLD_INDEX });
  const canvas = h.world.find((n) => n.tagName === 'CANVAS');
  canvas.dispatch('mousedown', { clientX: 200, clientY: 150 });
  canvas.dispatch('mouseup', { clientX: 200, clientY: 150 }); // inside the zone, nowhere near either chip
  assert.deepEqual(h.app.state.zone, { mapId: 999, name: 'TestZone' });
  assert.deepEqual(h.app.state.filters, { friendly: true, hostile: true, stale: true }); // default, untouched
  h.app.stop();
});

test('W5: clicking outside any zone does nothing', async () => {
  const h = await boot({ withWorld: true, index: SQUARE_WORLD_INDEX });
  const canvas = h.world.find((n) => n.tagName === 'CANVAS');
  canvas.dispatch('mousedown', { clientX: 50, clientY: 50 });
  canvas.dispatch('mouseup', { clientX: 50, clientY: 50 }); // outside the padded square frame
  assert.equal(h.app.state.zone, null);
  h.app.stop();
});

test('W5: a click-and-drag (mouse moved past the threshold) never navigates', async () => {
  const h = await boot({ withWorld: true, index: SQUARE_WORLD_INDEX });
  const canvas = h.world.find((n) => n.tagName === 'CANVAS');
  canvas.dispatch('mousedown', { clientX: 200, clientY: 150 });
  canvas.dispatch('mouseup', { clientX: 240, clientY: 150 }); // 40px of movement - a drag, not a click
  assert.equal(h.app.state.zone, null);
  h.app.stop();
});

test('W6: ctx.setWorldView toggles state.worldView and emits a worldView bus event', async () => {
  const h = await boot({ withWorld: true });
  const seen = [];
  h.app.ctx.bus.on('worldView', () => seen.push(h.app.state.worldView));
  // W7: the world map is now the default view (no zone picked yet).
  assert.equal(h.app.state.worldView, true);
  h.app.ctx.setWorldView(false);
  assert.equal(h.app.state.worldView, false);
  h.app.ctx.setWorldView(true);
  assert.deepEqual(seen, [false, true]);
  h.app.stop();
});

test('W7: fresh boot with nothing selected defaults to world view, radar hidden', async () => {
  const h = await boot({ withWorld: true });
  const header = h.app.ctx.mount('header-controls');
  const radar = h.app.ctx.mount('radar');
  const world = h.world;
  const btn = header.find((n) => n.tagName === 'BUTTON' && n.textContent === h.app.ctx.MSG.worldViewOff);
  assert.ok(btn, 'a toggle button was mounted in header-controls, already reading "turn off"');
  assert.equal(h.app.state.worldView, true);
  assert.equal(h.app.state.zone, null);
  assert.equal(radar.hidden, true);
  assert.equal(world.hidden, false);
  h.app.stop();
});

test('W6: the header-controls toggle overrides world view in either direction', async () => {
  const h = await boot({ withWorld: true });
  const header = h.app.ctx.mount('header-controls');
  const radar = h.app.ctx.mount('radar');
  const world = h.world;
  const btn = header.find((n) => n.tagName === 'BUTTON' && n.textContent === h.app.ctx.MSG.worldViewOff);
  assert.ok(btn, 'a toggle button was mounted in header-controls');

  // World view is the default (W7): world shown, radar hidden.
  assert.equal(radar.hidden, true);
  assert.equal(world.hidden, false);
  assert.equal(btn.textContent, h.app.ctx.MSG.worldViewOff);

  btn.dispatch('click');
  assert.equal(h.app.state.worldView, false);
  assert.equal(radar.hidden, false);
  assert.equal(world.hidden, true);
  assert.equal(btn.textContent, h.app.ctx.MSG.worldViewOn);

  // Clicking again overrides back to world view, even with no zone selected either way.
  btn.dispatch('click');
  assert.equal(h.app.state.worldView, true);
  assert.equal(radar.hidden, true);
  assert.equal(world.hidden, false);
  h.app.stop();
});

test('W7: picking a real zone exits world view; clearing back to "All Zones" restores it', async () => {
  const h = await boot({ withWorld: true });
  assert.equal(h.app.state.worldView, true);
  h.app.ctx.setZone({ mapId: 1440, name: 'Ashenvale' });
  assert.equal(h.app.state.worldView, false);
  assert.deepEqual(h.app.state.zone, { mapId: 1440, name: 'Ashenvale' });
  h.app.ctx.setZone(null);
  assert.equal(h.app.state.worldView, true);
  assert.equal(h.app.state.zone, null);
  h.app.stop();
});

test('W7: the manual toggle still overrides world view while a zone is selected', async () => {
  const h = await boot({ withWorld: true });
  h.app.ctx.setZone({ mapId: 1440, name: 'Ashenvale' });
  assert.equal(h.app.state.worldView, false);
  h.app.ctx.setWorldView(true);
  assert.equal(h.app.state.worldView, true);
  assert.deepEqual(h.app.state.zone, { mapId: 1440, name: 'Ashenvale' }); // setWorldView never touches zone
  h.app.stop();
});

test('without a world index the section degrades: no canvases, a muted note, no errors', async () => {
  const h = await boot({ withWorld: false });
  const canvases = h.world.findAll((n) => n.tagName === 'CANVAS');
  assert.equal(canvases.length, 0);
  const empty = h.world.find((n) => n.className.includes('empty'));
  assert.equal(empty.hidden, false);
  assert.deepEqual(h.env.logs, []);
  h.app.stop();
});
