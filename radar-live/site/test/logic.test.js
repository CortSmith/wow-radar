import test from 'node:test';
import assert from 'node:assert/strict';
import { classify, visibleContacts } from '../js/staleness.js';
import { mergeContacts, mergeTrail } from '../js/merge.js';
import { groupParam, sectorOf, rosterSectorOf, sectorKey, sweepParams } from '../js/sector.js';
import { fmtAge, fmtTime, fmtNum, fmtAvg, safeZone } from '../js/format.js';
import { worldToPct, fitFrame, clampView, zoomAt, panFrom, freeProjection, hitTest, hitTestScene, imageToPct, continentProjector } from '../js/projection.js';
import { findMapEntry } from '../js/charts.js';
import { chooseViewMap, toBlips } from '../js/view-model.js';
import { CONFIG, resolveConfig } from '../js/config.js';

const NOW = 1_700_000_000_000;
const C = (guid, over = {}) => ({
  guid, name: guid, reaction: 'friendly', mapId: 1, x: 10, y: 20, posTime: NOW, lastSeen: NOW, ...over,
});

test('classify: live, stale after 5 min, gone after 15 min', () => {
  assert.equal(classify(C('a'), NOW), 'live');
  assert.equal(classify(C('a', { lastSeen: NOW - 299_999 }), NOW), 'live');
  assert.equal(classify(C('a', { lastSeen: NOW - 300_000 }), NOW), 'stale');
  assert.equal(classify(C('a', { lastSeen: NOW - 899_999 }), NOW), 'stale');
  assert.equal(classify(C('a', { lastSeen: NOW - 900_000 }), NOW), 'gone');
});

test('visibleContacts drops gone rows but keeps a pinned guid', () => {
  const map = new Map([['a', C('a')], ['b', C('b', { lastSeen: NOW - 1e7 })], ['c', C('c', { lastSeen: NOW - 1e7 })]]);
  assert.deepEqual(visibleContacts(map, NOW).map((c) => c.guid), ['a']);
  assert.deepEqual(visibleContacts(map, NOW, 'c').map((c) => c.guid).sort(), ['a', 'c']);
});

test('mergeContacts replaces by guid, newest lastSeen wins, cap evicts oldest', () => {
  const map = new Map();
  mergeContacts(map, [C('a', { lastSeen: 5 }), C('b', { lastSeen: 6 })]);
  mergeContacts(map, [C('a', { lastSeen: 9, x: 99 }), C('b', { lastSeen: 1 })]);
  assert.equal(map.get('a').x, 99);
  assert.equal(map.get('b').lastSeen, 6);
  mergeContacts(map, [C('c', { lastSeen: 10 }), C('d', { lastSeen: 11 })], 2);
  assert.deepEqual([...map.keys()].sort(), ['c', 'd']);
  assert.equal(mergeContacts(map, null), map);
});

test('mergeTrail dedupes by t, sorts, caps', () => {
  const f = (t) => ({ t, mapId: 1, x: 0, y: 0, kind: 'exact' });
  const out = mergeTrail([f(1), f(3)], [f(2), f(3), f(4)], 10);
  assert.deepEqual(out.map((x) => x.t), [1, 2, 3, 4]);
  assert.deepEqual(mergeTrail(out, [f(5)], 3).map((x) => x.t), [3, 4, 5]);
  assert.deepEqual(mergeTrail(undefined, undefined), []);
});

test('groupParam maps Blue/Red checkboxes', () => {
  assert.equal(groupParam({ friendly: true, hostile: true }), 'any');
  assert.equal(groupParam({ friendly: true, hostile: false }), 'friendly');
  // Red means hostile, neutral and unknown; the server's 'hostile' group leaves out neutral/unknown, so Red-only asks for all
  // and the page filters locally (otherwise unticking Blue also hid the red neutral/unknown dots).
  assert.equal(groupParam({ friendly: false, hostile: true }), 'any');
  assert.equal(groupParam({ friendly: false, hostile: false }), 'any');
});

test('sectorOf: player beats zone beats all', () => {
  const filters = { friendly: true, hostile: true };
  assert.deepEqual(sectorOf({ player: { guid: 'Player-1' }, zone: { mapId: 3 }, filters }), { scope: 'player', guid: 'Player-1' });
  assert.deepEqual(sectorOf({ zone: { mapId: 0 }, filters }), { scope: 'zone', mapId: 0, grp: 'any' });
  assert.deepEqual(sectorOf({ filters }), { scope: 'all' });
});

test('rosterSectorOf is always scope=all: zone, world view and filters are applied by the page itself', () => {
  const filters = { friendly: true, hostile: false };
  assert.deepEqual(rosterSectorOf({ zone: { mapId: 37 }, filters, worldView: false }), { scope: 'all' });
  assert.deepEqual(rosterSectorOf({ zone: { mapId: 37 }, filters, worldView: true }), { scope: 'all' });
  assert.deepEqual(rosterSectorOf({ zone: null, filters }), { scope: 'all' });
});

test('sweepParams builds the contract query', () => {
  assert.deepEqual(sweepParams({ scope: 'all' }, 'default', 0, false), { scope: 'all', since: 0 });
  assert.deepEqual(sweepParams({ scope: 'zone', mapId: 0, grp: 'hostile' }, 'pvp', 55, false),
    { rs: 'pvp', scope: 'zone', mapId: 0, grp: 'hostile', since: 55 });
  assert.deepEqual(sweepParams({ scope: 'player', guid: 'Player-9' }, 'default', 1, true),
    { scope: 'player', guid: 'Player-9', since: 1, trail: 1 });
});

test('sectorKey ignores since but sees rs/scope changes', () => {
  const a = sectorKey({ scope: 'all' }, 'pvp');
  assert.equal(a, sectorKey({ scope: 'all' }, 'pvp'));
  assert.notEqual(a, sectorKey({ scope: 'all' }, 'rp'));
  assert.notEqual(a, sectorKey({ scope: 'zone', mapId: 1, grp: 'any' }, 'pvp'));
});

test('format helpers', () => {
  assert.equal(fmtAge(0), '0s');
  assert.equal(fmtAge(59_000), '59s');
  assert.equal(fmtAge(61_000), '1m');
  assert.equal(fmtAge(3_700_000), '1h 1m');
  assert.equal(fmtAge(-5), '0s');
  assert.equal(fmtNum(12.345, 1), '12.3');
  assert.equal(fmtNum(NaN), '-');
  assert.equal(fmtAvg(10, 4), '2.5');
  assert.equal(fmtAvg(10, 0), '-');
  assert.equal(fmtTime(Date.UTC(2024, 0, 2, 3, 4, 5), 'UTC'), '2024-01-02 03:04:05');
  assert.equal(fmtTime(Date.UTC(2024, 0, 2, 3, 4, 5), 'Asia/Tokyo'), '2024-01-02 12:04:05');
  assert.equal(fmtTime(0, 'UTC'), '-');
  assert.equal(safeZone('Not/AZone'), 'UTC');
  assert.equal(safeZone('Europe/Paris'), 'Europe/Paris');
});

const chart = { x0: 0, x1: 200, y0: 0, y1: 100 };
test('worldToPct: y counted from the left edge at y1, x from the top at x1', () => {
  assert.deepEqual(worldToPct(chart, 200, 100), [0, 0]);
  assert.deepEqual(worldToPct(chart, 0, 0), [100, 100]);
  assert.deepEqual(worldToPct(chart, 100, 50), [50, 50]);
  assert.deepEqual(worldToPct({ x0: 1, x1: 1, y0: 0, y1: 1 }, 5, 5), [50, 50]);
});

test('imageToPct: plain pixel space, no axis swap (unlike worldToPct)', () => {
  assert.deepEqual(imageToPct(200, 100, 0, 0), [0, 0]);
  assert.deepEqual(imageToPct(200, 100, 200, 100), [100, 100]);
  assert.deepEqual(imageToPct(200, 100, 100, 50), [50, 50]);
  assert.deepEqual(imageToPct(0, 0, 5, 5), [50, 50]);
});

test('continentProjector: maps a continent image onto a frame, respecting pan/zoom like fixedProjector', () => {
  const frame = { x: 0, y: 0, w: 100, h: 100 };
  const view = { z: 1, cx: 50, cy: 50 };
  const project = continentProjector(200, 100, frame, view);
  assert.deepEqual(project(0, 0), [0, 0]);
  assert.deepEqual(project(200, 100), [100, 100]);
  assert.deepEqual(project(100, 50), [50, 50]);
  // zoomed in 2x centred: the centre point stays put, the corners move outward off-frame
  const zoomedProject = continentProjector(200, 100, frame, { z: 2, cx: 50, cy: 50 });
  assert.deepEqual(zoomedProject(100, 50), [50, 50]);
  assert.ok(zoomedProject(0, 0)[0] < 0);
});

test('fitFrame keeps aspect inside padding', () => {
  const f = fitFrame(1000, 500, 2, 10);
  assert.ok(Math.abs(f.w / f.h - 2) < 1e-9);
  assert.ok(f.x >= 10 && f.y >= 10 && f.x + f.w <= 990 && f.y + f.h <= 490);
  assert.equal(fitFrame(100, 1000, 2, 0).w, 100);
});

test('clampView/zoomAt/panFrom stay inside 0..100', () => {
  assert.deepEqual(clampView({ z: 0.5, cx: -10, cy: 200 }), { z: 1, cx: 50, cy: 50 });
  assert.deepEqual(clampView({ z: 2, cx: 0, cy: 100 }), { z: 2, cx: 25, cy: 75 });
  const f = { x: 0, y: 0, w: 100, h: 100 };
  const zoomed = zoomAt({ z: 1, cx: 50, cy: 50 }, f, 50, 50, 2);
  assert.equal(zoomed.z, 2);
  assert.ok(Math.abs(zoomed.cx - 50) < 1e-9);
  assert.equal(zoomAt({ z: 1, cx: 50, cy: 50 }, f, 0, 0, 1000).z, 40);
  assert.ok(panFrom({ z: 2, cx: 50, cy: 50 }, f, 10, 0).cx < 50);
});

test('freeProjection: north up, east right, centred', () => {
  const p = freeProjection({ w: 200, h: 200, center: { x: 100, y: 100 }, rangeYd: 50 });
  assert.deepEqual(p(100, 100), [100, 100]);
  const north = p(125, 100);
  assert.ok(north[1] < 100 && north[0] === 100);
  assert.ok(p(100, 75)[0] > 100);
});

test('hitTest finds nearest within radius', () => {
  const pts = [{ sx: 10, sy: 10, id: 'a' }, { sx: 14, sy: 10, id: 'b' }];
  assert.equal(hitTest(pts, 13, 10, 8).id, 'b');
  assert.equal(hitTest(pts, 100, 100, 8), null);
  assert.equal(hitTest([], 0, 0, 8), null);
});

// Stage 11 U5: hitTestScene tests pips and the trail together, same radius, pips winning ties.
test('hitTestScene: trail points are now hittable, same radius as pips', () => {
  const pips = [];
  const trail = [{ sx: 10, sy: 10, t: 1, kind: 'exact' }];
  assert.equal(hitTestScene(pips, trail, 11, 10, 8).t, 1, 'a trail point within radius is hit');
  assert.equal(hitTestScene(pips, trail, 100, 100, 8), null, 'out of radius: no hit');
  assert.equal(hitTestScene([], [], 0, 0, 8), null, 'empty pips and trail: no hit');
});

test('hitTestScene: a pip wins over an overlapping (even closer) trail point within the radius', () => {
  const pips = [{ sx: 10, sy: 10, guid: 'p', color: 'friendly' }];
  const trail = [{ sx: 10, sy: 10, t: 1, kind: 'exact' }]; // sits exactly on top of the pip
  const hit = hitTestScene(pips, trail, 10, 10, 8);
  assert.equal(hit.guid, 'p', 'the pip is returned, not the historical fix underneath it');
});

test('hitTestScene: falls back to the trail when no pip is within radius', () => {
  const pips = [{ sx: 90, sy: 90, guid: 'p' }];
  const trail = [{ sx: 10, sy: 10, t: 1, kind: 'approx' }];
  assert.equal(hitTestScene(pips, trail, 10, 10, 8).kind, 'approx');
});

test('findMapEntry matches chart names loosely', () => {
  const index = { zones: { dunmorogh: { name: 'Dun Morogh', file: 'DunMorogh.png', w: 10, h: 5 } } };
  assert.equal(findMapEntry(index, 'dun morogh').file, 'DunMorogh.png');
  assert.equal(findMapEntry(index, 'Elwynn'), null);
  assert.equal(findMapEntry(null, 'x'), null);
});

test('chooseViewMap: zone, then selected player, then busiest map', () => {
  const contacts = new Map([['a', C('a', { mapId: 5 })], ['b', C('b', { mapId: 7 })], ['c', C('c', { mapId: 7 })]]);
  assert.equal(chooseViewMap({ zone: { mapId: 2 }, contacts }, NOW), 2);
  assert.equal(chooseViewMap({ player: { guid: 'a' }, contacts }, NOW), 5);
  assert.equal(chooseViewMap({ contacts }, NOW), 7);
  assert.equal(chooseViewMap({ contacts: new Map() }, NOW), null);
});

test('toBlips applies filters, map, position and staleness', () => {
  const contacts = new Map([
    ['f', C('f')], ['h', C('h', { reaction: 'hostile' })], ['n', C('n', { reaction: 'neutral' })],
    ['s', C('s', { lastSeen: NOW - 400_000 })], ['o', C('o', { mapId: 9 })], ['p', C('p', { posTime: 0 })],
    ['g', C('g', { lastSeen: NOW - 2e6 })],
  ]);
  const base = { contacts, filters: { friendly: true, hostile: true, stale: true } };
  const all = toBlips(base, NOW, 1);
  assert.deepEqual(all.map((b) => b.guid).sort(), ['f', 'h', 'n', 's']);
  assert.equal(all.find((b) => b.guid === 'n').color, 'hostile');
  assert.equal(all.find((b) => b.guid === 's').color, 'stale');
  const red = toBlips({ ...base, filters: { friendly: false, hostile: true, stale: false } }, NOW, 1);
  assert.deepEqual(red.map((b) => b.guid).sort(), ['h', 'n']);
});

test('config: defaults and url override only for maps', () => {
  assert.equal(CONFIG.SWEEP_INTERVAL_MS, 60_000);
  assert.equal(CONFIG.SINCE_OVERLAP_MS, 5_000);
  const c = resolveConfig('?maps=/m/&api=http://evil&interval=1');
  assert.equal(c.MAPS_BASE_URL, '/m/');
  assert.equal(c.API_BASE, CONFIG.API_BASE);
  assert.equal(c.SWEEP_INTERVAL_MS, 60_000);
});

test('inViewZone: world view and "All Zones" show everyone, a picked zone only its own contacts', async () => {
  const { inViewZone } = await import('../js/view-model.js');
  const c = { mapId: 37 };
  assert.equal(inViewZone({ worldView: true, zone: { mapId: 27 } }, c), true);
  assert.equal(inViewZone({ worldView: false, zone: null }, c), true);
  assert.equal(inViewZone({ worldView: false, zone: { mapId: 27 } }, c), false);
  assert.equal(inViewZone({ worldView: false, zone: { mapId: 37 } }, c), true);
});
