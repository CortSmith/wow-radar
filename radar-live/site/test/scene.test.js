import test from 'node:test';
import assert from 'node:assert/strict';
import { buildScene } from '../js/scene.js';
import { paintScene, readColors } from '../js/draw.js';
import { demoApi } from '../dev-demo.mjs';

const NOW = 1_760_000_000_000;
const chart = { mapId: 27, name: 'Dun Morogh', x0: -2500, x1: -500, y0: -1000, y1: 2000 };
const mkState = (over = {}) => {
  const contacts = new Map(demoApi('/api/v1/sweep?scope=zone&mapId=27&since=0', NOW).body.contacts.map((c) => [c.guid, c]));
  return { contacts, filters: { friendly: true, hostile: true, stale: true }, charts: [chart], trail: [], player: null, zone: { mapId: 27, name: 'x' }, ...over };
};
const view = { z: 1, cx: 50, cy: 50 };

test('fixed scene projects every pip inside the frame', () => {
  const s = buildScene({ state: mkState(), now: NOW, size: { w: 900, h: 600 }, view, opts: { fixed: true, rangeYd: 300 }, image: null });
  assert.equal(s.fixed, true);
  assert.ok(s.pips.length > 3);
  for (const p of s.pips) {
    assert.ok(p.sx >= s.frame.x - 1 && p.sx <= s.frame.x + s.frame.w + 1, 'x inside frame');
    assert.ok(p.sy >= s.frame.y - 1 && p.sy <= s.frame.y + s.frame.h + 1, 'y inside frame');
  }
});

test('without a chart (or Fixed off) the free range view is used and the selected player is centred', () => {
  const state = mkState({ charts: [] });
  const guid = [...state.contacts.keys()][0];
  state.player = { guid, name: 'x' };
  const s = buildScene({ state, now: NOW, size: { w: 800, h: 600 }, view, opts: { fixed: true, rangeYd: 300 }, image: null });
  assert.equal(s.fixed, false);
  const sel = s.pips.find((p) => p.selected);
  assert.deepEqual([sel.sx, sel.sy], [400, 300]);
});

test('trail points are projected only for the viewed map', () => {
  const state = mkState();
  state.trail = [{ t: 1, mapId: 27, x: -1500, y: 500, kind: 'exact' }, { t: 2, mapId: 99, x: 0, y: 0, kind: 'exact' }];
  const s = buildScene({ state, now: NOW, size: { w: 900, h: 600 }, view, opts: { fixed: true, rangeYd: 300 }, image: null });
  assert.equal(s.trail.length, 1);
});

test('no map known -> empty scene', () => {
  const s = buildScene({ state: mkState({ contacts: new Map(), zone: null }), now: NOW, size: { w: 10, h: 10 }, view, opts: { fixed: true, rangeYd: 300 }, image: null });
  assert.deepEqual(s.pips, []);
});

test('paintScene draws pips on a recording context', () => {
  const calls = [];
  const g = new Proxy({}, { get: (_, k) => (...a) => calls.push(k), set: () => true });
  const state = mkState();
  state.trail = [{ t: 1, mapId: 27, x: -1500, y: 500, kind: 'approx' }, { t: 2, mapId: 27, x: -1400, y: 600, kind: 'exact' }];
  const size = { w: 900, h: 600 };
  const scene = buildScene({ state, now: NOW, size, view, opts: { fixed: true, rangeYd: 300 }, image: null });
  paintScene(g, scene, size, readColors(null, null), null);
  assert.ok(calls.filter((c) => c === 'arc').length >= scene.pips.length);
  assert.ok(calls.includes('setLineDash'));
  const free = buildScene({ state, now: NOW, size, view, opts: { fixed: false, rangeYd: 300 }, image: null });
  paintScene(g, free, size, readColors(null, null), null);
});

test('readColors reads CSS variables', () => {
  const win = { getComputedStyle: () => ({ getPropertyValue: (v) => (v === '--friendly' ? ' #123456 ' : '') }) };
  assert.equal(readColors(win, {}).friendly, '#123456');
  assert.equal(readColors(win, {}).hostile, '#ef4444');
});

test('demo api honours since, grp and unknown paths', () => {
  const all = demoApi('/api/v1/sweep?scope=all&since=0', NOW).body;
  assert.ok(all.contacts.length > 0 && all.changed);
  const later = demoApi(`/api/v1/sweep?scope=all&since=${NOW + 1}`, NOW).body;
  assert.equal(later.contacts.length, 0);
  assert.equal(later.changed, false);
  const red = demoApi('/api/v1/sweep?scope=all&since=0&grp=hostile', NOW).body.contacts;
  assert.ok(red.length > 0 && red.every((c) => c.reaction === 'hostile'));
  assert.equal(demoApi('/api/v1/nope', NOW).status, 404);
  assert.ok(demoApi('/api/v1/bounty?limit=3', NOW).body.length <= 3);
  assert.equal(demoApi('/api/v1/charts', NOW).body.length, 2);
  assert.equal(demoApi('/api/v1/status', NOW).status, 200);
  assert.equal(demoApi('/api/v1/contacts?q=zzz', NOW).body.total, 0);
});

test('zooming crops the map image to the visible part, so the map zooms with the dots', () => {
  const drawn = [];
  const g = new Proxy({}, { get: (_, k) => (...a) => { if (k === 'drawImage') drawn.push(a); }, set: () => true });
  const img = { naturalWidth: 1000, naturalHeight: 600 };
  const size = { w: 900, h: 600 };
  const paint = (v) => {
    drawn.length = 0;
    const scene = buildScene({ state: mkState(), now: NOW, size, view: v, opts: { fixed: true, rangeYd: 300 }, image: { w: 1000, h: 600 } });
    paintScene(g, scene, size, readColors(null, null), img);
    return { scene, call: drawn[0] };
  };
  const whole = paint({ z: 1, cx: 50, cy: 50 });
  assert.deepEqual(whole.call.slice(1, 5), [0, 0, 1000, 600], 'unzoomed: the whole image');
  const zoomed = paint({ z: 2, cx: 25, cy: 25 });
  assert.deepEqual(zoomed.call.slice(1, 5), [0, 0, 500, 300], 'z=2 at the top-left: the top-left quarter');
  const panned = paint({ z: 4, cx: 75, cy: 50 });
  assert.deepEqual(panned.call.slice(1, 5), [625, 225, 250, 150], 'z=4 centred right: a quarter-size crop');
  const f = zoomed.scene.frame;
  assert.deepEqual(zoomed.call.slice(5), [f.x, f.y, f.w, f.h], 'the crop always fills the frame');
});

test('grid lines move with the zoom instead of staying fixed to the frame', () => {
  const lines = [];
  const g = new Proxy({}, { get: (_, k) => (...a) => { if (k === 'moveTo') lines.push(a); }, set: () => true });
  const size = { w: 900, h: 600 };
  const scene = buildScene({ state: mkState(), now: NOW, size, view: { z: 2, cx: 25, cy: 25 }, opts: { fixed: true, rangeYd: 300 }, image: null });
  paintScene(g, scene, size, readColors(null, null), null);
  const xs = lines.map((l) => l[0]).filter((x) => x > scene.frame.x && x < scene.frame.x + scene.frame.w);
  assert.ok(xs.length > 0 && xs.length < 10, 'only grid lines inside the visible part are drawn');
  const spacing = scene.frame.w / 5; // 10 percent of the map is 1/5 of the frame at z=2
  assert.ok(Math.abs(xs[1] - xs[0] - spacing) < 0.01, 'spacing doubled with zoom 2');
});
