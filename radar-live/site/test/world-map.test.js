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
import { worldPathPoints, computeTrackedPaths, paintWorldPath, zonePoint, layoutChips, paintPie, filterPatchFor, gotoZone } from '../js/features/world-map.js';

const T0 = 1_760_000_000_000;
const settle = async (n = 8) => { for (let i = 0; i < n; i++) await new Promise((r) => setImmediate(r)); };

const WORLD_INDEX = {
  continents: [
    { id: 'kalimdor', name: 'Kalimdor', file: 'Kalimdor.png', w: 1002, h: 668, zones: [{ mapId: 1440, name: 'Ashenvale', x: 418, y: 221, w: 157, h: 105 }] },
    { id: 'easternkingdoms', name: 'Eastern Kingdoms', file: 'EasternKingdoms.png', w: 1002, h: 668, zones: [{ mapId: 1431, name: 'Duskwood', x: 432, y: 489, w: 77, h: 51 }] },
  ],
};

// Stage 14 W3b: one continent sized 100x100 with a single zone whose position point is its centre (50,50).
// With the default 800x600 fake canvas and the default unzoomed view, that lands on an exact, easy-to-reason-
// about screen pixel (400, 300) - see fitFrame/continentProjector in projection.js - so pie hit-testing in
// the tests below doesn't need to replicate that math.
const SQUARE_WORLD_INDEX = {
  continents: [{ id: 'test', name: 'TestLand', file: 'Test.png', w: 100, h: 100, zones: [{ mapId: 999, name: 'TestZone', px: 50, py: 50 }] }],
};
// Same continent, but the zone is described the legacy way (x/y/w/h rect, no px/py): its centre is the point.
const LEGACY_RECT_WORLD_INDEX = {
  continents: [{ id: 'test', name: 'TestLand', file: 'Test.png', w: 100, h: 100, zones: [{ mapId: 999, name: 'TestZone', x: 0, y: 0, w: 100, h: 100 }] }],
};

const liveContact = (now, guid, reaction) => ({ guid, name: guid, reaction, mapId: 999, x: 0, y: 0, posTime: now, lastSeen: now });

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

test('W3b: hovering a pie shows the zone name + exact friendly/neutral/hostile counts', async () => {
  const h = await boot({ withWorld: true, index: SQUARE_WORLD_INDEX });
  const now = h.timers.now();
  for (const [g, r] of [['f1', 'friendly'], ['f2', 'friendly'], ['f3', 'friendly'], ['h1', 'hostile'], ['n1', 'neutral']]) h.app.state.contacts.set(g, liveContact(now, g, r));
  h.app.bus.emit('sweep', {});
  const canvas = h.world.find((n) => n.tagName === 'CANVAS');
  const tip = h.world.find((n) => n.className === 'tip');

  canvas.dispatch('mousemove', { clientX: 400, clientY: 300 }); // the pie's exact centre
  assert.equal(tip.hidden, false);
  assert.equal(tip.textContent, 'TestZone\n3 friendly\n1 neutral\n1 hostile');

  canvas.dispatch('mousemove', { clientX: 700, clientY: 590 }); // nowhere near the pie
  assert.equal(tip.hidden, true);
  h.app.stop();
});

test('W3b: a zone with no live contacts draws no pie (nothing to hover there)', async () => {
  const h = await boot({ withWorld: true, index: SQUARE_WORLD_INDEX });
  const canvas = h.world.find((n) => n.tagName === 'CANVAS');
  const tip = h.world.find((n) => n.className === 'tip');
  canvas.dispatch('mousemove', { clientX: 400, clientY: 300 });
  assert.equal(tip.hidden, true);
  h.app.stop();
});

test('pies: a legacy rect-only zone (no px/py) is still placed at its rectangle centre', async () => {
  const h = await boot({ withWorld: true, index: LEGACY_RECT_WORLD_INDEX });
  const now = h.timers.now();
  h.app.state.contacts.set('f1', liveContact(now, 'f1', 'friendly'));
  h.app.bus.emit('sweep', {});
  const canvas = h.world.find((n) => n.tagName === 'CANVAS');
  const tip = h.world.find((n) => n.className === 'tip');
  canvas.dispatch('mousemove', { clientX: 400, clientY: 300 });
  assert.equal(tip.hidden, false);
  h.app.stop();
});

test('pies: zonePoint prefers px/py, falls back to the rect centre, and is null with neither', () => {
  assert.deepEqual(zonePoint({ px: 12, py: 34, x: 0, y: 0, w: 10, h: 10 }), { x: 12, y: 34 });
  assert.deepEqual(zonePoint({ x: 10, y: 20, w: 40, h: 20 }), { x: 30, y: 30 });
  assert.equal(zonePoint({ x: 10, y: 20, w: 0, h: 20 }), null);
  assert.equal(zonePoint({ mapId: 1 }), null);
  assert.equal(zonePoint(null), null);
});

test('pies: layoutChips makes one pie per zone with contacts, at its projected point, sized by the total', () => {
  const zones = [{ mapId: 1, name: 'A', px: 10, py: 20 }, { mapId: 2, name: 'B', px: 30, py: 40 }, { mapId: 3, name: 'C', px: 5, py: 5 }, { mapId: 4, name: 'D' }];
  const counts = new Map([
    [1, { friendly: 2, neutral: 1, hostile: 1 }],
    [2, { friendly: 0, neutral: 0, hostile: 0 }], // present but empty -> no pie
    [4, { friendly: 1, neutral: 0, hostile: 0 }], // no usable point -> skipped, no crash
  ]);
  const chips = layoutChips(zones, (x, y) => [x * 2, y * 2], counts);
  assert.equal(chips.length, 1);
  const c = chips[0];
  assert.deepEqual([c.mapId, c.name, c.x, c.y, c.friendly, c.neutral, c.hostile], [1, 'A', 20, 40, 2, 1, 1]);
  assert.ok(c.r > 0);
  const bigger = layoutChips(zones, (x, y) => [x, y], new Map([[1, { friendly: 20, neutral: 0, hostile: 0 }]]))[0];
  assert.ok(bigger.r > c.r, 'more contacts, bigger pie');
});

test('pies: paintPie draws one wedge per non-empty bucket in the right colours, with separators', () => {
  const { g, calls } = fakeCtx();
  const colors = { friendly: '#f', neutral: '#n', hostile: '#h' };
  paintPie(g, { x: 10, y: 10, r: 8, friendly: 2, neutral: 0, hostile: 2 }, colors);
  const fills = calls.filter((c) => c.fn === 'fill');
  assert.deepEqual(fills.map((c) => c.fillStyle), ['#f', '#h']); // neutral is empty: no wedge
  assert.equal(calls.filter((c) => c.fn === 'arc').length, 2);
  assert.equal(calls.filter((c) => c.fn === 'stroke').length, 2); // a separator per wedge
});

test('pies: a single non-empty bucket paints one whole disc (no wedge, no separator), nothing for an empty chip', () => {
  const one = fakeCtx();
  paintPie(one.g, { x: 0, y: 0, r: 5, friendly: 0, neutral: 3, hostile: 0 }, { friendly: '#f', neutral: '#n', hostile: '#h' });
  assert.deepEqual(one.calls.filter((c) => c.fn === 'fill').map((c) => c.fillStyle), ['#n']);
  assert.ok(!one.calls.some((c) => c.fn === 'moveTo' || c.fn === 'stroke'));
  const none = fakeCtx();
  paintPie(none.g, { x: 0, y: 0, r: 5, friendly: 0, neutral: 0, hostile: 0 }, {});
  assert.deepEqual(none.calls, []);
});

test('pies: the world section has a friendly / neutral / hostile legend', async () => {
  const h = await boot({ withWorld: true, index: SQUARE_WORLD_INDEX });
  const items = h.world.findAll((n) => n.className === 'worldLegendItem');
  assert.equal(items.length, 3);
  assert.deepEqual(items.map((n) => n.textContent), ['Friendly', 'Neutral', 'Hostile']);
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

test('W4: worldPathPoints anchors on a zone\'s px/py point (not a rect centre) when present', () => {
  const zones = [{ mapId: 1440, px: 7, py: 9 }, { mapId: 1431, px: 30, py: 40, x: 0, y: 0, w: 2, h: 2 }];
  const visits = [{ mapId: 1440, enteredAt: 1 }, { mapId: 1431, enteredAt: 2 }];
  assert.deepEqual(worldPathPoints(visits, zones, (x, y) => [x, y]), [{ x: 7, y: 9, enteredAt: 1 }, { x: 30, y: 40, enteredAt: 2 }]);
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
  assert.deepEqual(filterPatchFor('neutral'), { friendly: false, hostile: true }); // Red covers hostile and neutral
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

async function clickAt(h, x, y) {
  const canvas = h.world.find((n) => n.tagName === 'CANVAS');
  canvas.dispatch('mousedown', { clientX: x, clientY: y });
  canvas.dispatch('mouseup', { clientX: x, clientY: y });
}

test('W5: clicking an all-friendly pie navigates to its zone with the friendly-only filter', async () => {
  const h = await boot({ withWorld: true, index: SQUARE_WORLD_INDEX });
  h.app.state.contacts.set('f1', liveContact(h.timers.now(), 'f1', 'friendly'));
  h.app.bus.emit('sweep', {});
  await clickAt(h, 400, 300);
  assert.deepEqual(h.app.state.zone, { mapId: 999, name: 'TestZone' });
  assert.deepEqual(h.app.state.filters, { friendly: true, hostile: false, stale: true });
  h.app.stop();
});

test('W5: clicking an all-hostile pie navigates to its zone with the hostile-only filter', async () => {
  const h = await boot({ withWorld: true, index: SQUARE_WORLD_INDEX });
  h.app.state.contacts.set('h1', liveContact(h.timers.now(), 'h1', 'hostile'));
  h.app.bus.emit('sweep', {});
  await clickAt(h, 400, 300);
  assert.deepEqual(h.app.state.zone, { mapId: 999, name: 'TestZone' });
  assert.deepEqual(h.app.state.filters, { friendly: false, hostile: true, stale: true });
  h.app.stop();
});

test('W5: in a mixed pie the clicked slice picks the filter (friendly right half, hostile left half)', async () => {
  const mk = async () => {
    const h = await boot({ withWorld: true, index: SQUARE_WORLD_INDEX });
    h.app.state.contacts.set('f1', liveContact(h.timers.now(), 'f1', 'friendly'));
    h.app.state.contacts.set('h1', liveContact(h.timers.now(), 'h1', 'hostile'));
    h.app.bus.emit('sweep', {});
    return h;
  };
  const right = await mk();
  await clickAt(right, 404, 300); // clockwise from 12 o'clock: the first half of the turn is friendly
  assert.deepEqual(right.app.state.filters, { friendly: true, hostile: false, stale: true });
  right.app.stop();
  const left = await mk();
  await clickAt(left, 396, 300);
  assert.deepEqual(left.app.state.filters, { friendly: false, hostile: true, stale: true });
  left.app.stop();
});

test('W5: clicking a neutral slice goes to the zone with the Red-only (hostile) filter', async () => {
  const h = await boot({ withWorld: true, index: SQUARE_WORLD_INDEX });
  h.app.state.contacts.set('n1', liveContact(h.timers.now(), 'n1', 'neutral'));
  h.app.bus.emit('sweep', {});
  await clickAt(h, 400, 300);
  assert.deepEqual(h.app.state.zone, { mapId: 999, name: 'TestZone' });
  assert.deepEqual(h.app.state.filters, { friendly: false, hostile: true, stale: true });
  h.app.stop();
});

test('W5: clicking empty map (outside any pie) does nothing, even inside where the old zone box was', async () => {
  const h = await boot({ withWorld: true, index: SQUARE_WORLD_INDEX });
  h.app.state.contacts.set('f1', liveContact(h.timers.now(), 'f1', 'friendly'));
  h.app.bus.emit('sweep', {});
  await clickAt(h, 200, 150);
  assert.equal(h.app.state.zone, null);
  assert.deepEqual(h.app.state.filters, { friendly: true, hostile: true, stale: true }); // default, untouched
  await clickAt(h, 50, 50);
  assert.equal(h.app.state.zone, null);
  h.app.stop();
});

test('W5: a click-and-drag (mouse moved past the threshold) never navigates', async () => {
  const h = await boot({ withWorld: true, index: SQUARE_WORLD_INDEX });
  h.app.state.contacts.set('f1', liveContact(h.timers.now(), 'f1', 'friendly'));
  h.app.bus.emit('sweep', {});
  const canvas = h.world.find((n) => n.tagName === 'CANVAS');
  canvas.dispatch('mousedown', { clientX: 400, clientY: 300 });
  canvas.dispatch('mouseup', { clientX: 440, clientY: 300 }); // 40px of movement - a drag, not a click
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
