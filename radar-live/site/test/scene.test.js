import test from 'node:test';
import assert from 'node:assert/strict';
import { buildScene } from '../js/scene.js';
import { paintScene, readColors } from '../js/draw.js';
import { hitTestScene } from '../js/projection.js';
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

test('stage 11 U4b: every pip is tracked:true and scene.hasTracked is false when nothing is tracked', () => {
  const s = buildScene({ state: mkState({ tracked: new Map() }), now: NOW, size: { w: 900, h: 600 }, view, opts: { fixed: true, rangeYd: 300 }, image: null });
  assert.equal(s.hasTracked, false);
  assert.ok(s.pips.length > 0);
  assert.ok(s.pips.every((p) => p.tracked === true));
});

test('stage 11 U4b: only tracked guids get tracked:true, and scene.hasTracked is true, when a set is active', () => {
  const state = mkState();
  const guid = [...state.contacts.keys()][0];
  state.tracked = new Map([[guid, { guid, name: 'x' }]]);
  const s = buildScene({ state, now: NOW, size: { w: 900, h: 600 }, view, opts: { fixed: true, rangeYd: 300 }, image: null });
  assert.equal(s.hasTracked, true);
  const tracked = s.pips.filter((p) => p.tracked);
  assert.deepEqual(tracked.map((p) => p.guid), [guid]);
  assert.ok(s.pips.some((p) => p.tracked === false), 'other pips are not tracked');
});

test('stage 11 U4b: hit-testing still finds a faded (non-tracked) pip at the same radius', () => {
  const state = mkState();
  const [faded, tracked] = [...state.contacts.keys()];
  state.tracked = new Map([[tracked, { guid: tracked, name: 'x' }]]);
  const s = buildScene({ state, now: NOW, size: { w: 900, h: 600 }, view, opts: { fixed: true, rangeYd: 300 }, image: null });
  const fadedPip = s.pips.find((p) => p.guid === faded);
  assert.equal(fadedPip.tracked, false);
  const hit = hitTestScene(s.pips, s.trail, fadedPip.sx, fadedPip.sy, 1);
  assert.equal(hit, fadedPip, 'hit-testing finds the faded pip at its exact coordinates');
});

test('stage 11 U4b: paintPips fades non-tracked pips and rings tracked ones when a set is active', () => {
  const state = mkState();
  const [faded, tracked] = [...state.contacts.keys()];
  state.tracked = new Map([[tracked, { guid: tracked, name: 'x' }]]);
  const calls = [];
  const rec = new Proxy({ strokeStyle: null }, {
    get: (t, k) => (k in t ? t[k] : (...a) => calls.push({ fn: k, strokeStyle: t.strokeStyle, alpha: t.globalAlpha })),
    set: (t, k, v) => { t[k] = v; return true; },
  });
  const size = { w: 900, h: 600 };
  const scene = buildScene({ state, now: NOW, size, view, opts: { fixed: true, rangeYd: 300 }, image: null });
  paintScene(rec, scene, size, readColors(null, null), null);
  const fillCalls = calls.filter((c) => c.fn === 'fill');
  assert.ok(fillCalls.some((c) => c.alpha === 0.35), 'at least one fill happens at reduced alpha for a non-tracked pip');
  assert.ok(fillCalls.some((c) => c.alpha === 1), 'the tracked pip is filled at full alpha');
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
