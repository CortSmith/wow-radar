// Stage 11 U5: tooltipModel (pure, no DOM) and resolveHoverPoint/hoverKeyFor (also pure). See
// docs/plans/stage-11-radar-ux.md section 3.5.
import test from 'node:test';
import assert from 'node:assert/strict';
import { tooltipModel, resolveHoverPoint, hoverKeyFor, isTrailPoint } from '../js/tooltip.js';
import { MSG, fmt } from '../js/messages.js';
import { fmtAge, fmtTime } from '../js/format.js';

const NOW = 1_700_000_000_000;

function makeCtx(over = {}) {
  return {
    MSG,
    fmt,
    now: () => NOW,
    fmtAge,
    fmtTime: (ms) => fmtTime(ms, 'UTC'),
    state: { player: null, trackedData: new Map(), contacts: new Map(), ...over },
  };
}

const pip = (over = {}) => ({
  guid: 'Player-1', name: 'Brenna', sx: 1, sy: 2, color: 'friendly', status: 'live', selected: false,
  contact: { zone: 'Elwynn Forest', reaction: 'friendly', level: 42, lastSeen: NOW - 3000 },
  ...over,
});

const trailPoint = (over = {}) => ({ t: NOW - 120_000, mapId: 27, x: 10, y: 20, kind: 'exact', sx: 5, sy: 6, ...over });

test('isTrailPoint distinguishes trail points (kind, no contact) from pips (contact)', () => {
  assert.equal(isTrailPoint(trailPoint()), true);
  assert.equal(isTrailPoint(pip()), false);
  assert.equal(isTrailPoint(null), false);
});

test('tooltipModel: a live pip shows the same content as the old tipText - head, reaction/state/level, seen-age', () => {
  const rows = tooltipModel(pip(), makeCtx());
  assert.equal(rows.length, 3);
  assert.equal(rows[0], 'Brenna - Elwynn Forest');
  assert.equal(rows[1], fmt(MSG.tipLine, { reaction: 'friendly', state: 'live', level: 42 }));
  assert.equal(rows[2], fmt(MSG.tipSeen, { age: fmtAge(3000) }));
});

test('tooltipModel: a live pip with no zone/level falls back the same way tipText did', () => {
  const rows = tooltipModel(pip({ contact: { reaction: 'hostile', lastSeen: NOW - 1000 } }), makeCtx());
  assert.equal(rows[0], 'Brenna');
  assert.equal(rows[1], fmt(MSG.tipLine, { reaction: 'hostile', state: 'live', level: '?' }));
});

test('tooltipModel: a history (trail) point shows the FIX\'s own time/kind, name+zone from trackedData, and NO reaction/state row', () => {
  const guid = 'Player-9';
  const ctx = makeCtx({
    player: { guid, name: 'Old Name' },
    trackedData: new Map([[guid, { contact: { name: 'Fresh Name', zone: 'Westfall' }, trail: [] }]]),
  });
  const fix = trailPoint({ t: NOW - 125_000, kind: 'exact' });
  const rows = tooltipModel(fix, ctx);
  assert.equal(rows.length, 3);
  assert.equal(rows[0], 'Fresh Name - Westfall', 'name/zone come from trackedData, not the fix itself');
  assert.equal(rows[1], fmt(MSG.tipHistory, { time: fmtTime(fix.t, 'UTC'), age: fmtAge(125_000) }));
  assert.equal(rows[2], MSG.tipKindExact);
  assert.ok(!rows.some((r) => /friendly|hostile|neutral/.test(r)), 'no fabricated reaction/state for a history point');
});

test('tooltipModel: an approx fix is labelled approx, and falls back to the roster contact when trackedData has none', () => {
  const guid = 'Player-9';
  const ctx = makeCtx({
    player: { guid, name: 'x' },
    contacts: new Map([[guid, { name: 'Roster Name', zone: 'Duskwood' }]]),
  });
  const rows = tooltipModel(trailPoint({ kind: 'approx' }), ctx);
  assert.equal(rows[0], 'Roster Name - Duskwood');
  assert.equal(rows[2], MSG.tipKindApprox);
});

test('tooltipModel: a history point for a guid with no contact info anywhere falls back gracefully (name = guid), no crash', () => {
  const guid = 'Player-ghost';
  const ctx = makeCtx({ player: { guid, name: 'x' } });
  const rows = tooltipModel(trailPoint(), ctx);
  assert.equal(rows[0], guid);
});

test('tooltipModel: a history point with no focused player at all falls back to MSG.tipUnknown, no crash', () => {
  const rows = tooltipModel(trailPoint(), makeCtx({ player: null }));
  assert.equal(rows[0], MSG.tipUnknown);
});

test('tooltipModel: null target returns an empty array', () => {
  assert.deepEqual(tooltipModel(null, makeCtx()), []);
});

test('hoverKeyFor: a stable key per target kind, null for no target', () => {
  assert.deepEqual(hoverKeyFor(pip()), { type: 'pip', guid: 'Player-1' });
  const fix = trailPoint({ t: 5, mapId: 27 });
  assert.deepEqual(hoverKeyFor(fix), { type: 'trail', t: 5, mapId: 27 });
  assert.equal(hoverKeyFor(null), null);
});

test('resolveHoverPoint: re-locates a pip or trail point inside a freshly rebuilt scene by key, not by object identity', () => {
  const scene = {
    pips: [{ guid: 'a', sx: 11, sy: 22 }],
    trail: [{ t: 1, mapId: 27, sx: 1, sy: 1 }, { t: 2, mapId: 27, sx: 2, sy: 2 }, { t: 3, mapId: 27, sx: 3, sy: 3 }],
  };
  const pipPoint = resolveHoverPoint(scene, { type: 'pip', guid: 'a' });
  assert.deepEqual(pipPoint, { sx: 11, sy: 22, isTrail: false, prev: null, next: null });
  const mid = resolveHoverPoint(scene, { type: 'trail', t: 2, mapId: 27 });
  assert.equal(mid.isTrail, true);
  assert.deepEqual([mid.sx, mid.sy], [2, 2]);
  assert.deepEqual(mid.prev, scene.trail[0]);
  assert.deepEqual(mid.next, scene.trail[2]);
  const first = resolveHoverPoint(scene, { type: 'trail', t: 1, mapId: 27 });
  assert.equal(first.prev, null, 'no previous neighbour for the first point');
});

test('resolveHoverPoint: null key, missing scene, or a point no longer present all return null', () => {
  const scene = { pips: [], trail: [] };
  assert.equal(resolveHoverPoint(scene, null), null);
  assert.equal(resolveHoverPoint(null, { type: 'pip', guid: 'a' }), null);
  assert.equal(resolveHoverPoint(scene, { type: 'pip', guid: 'gone' }), null);
  assert.equal(resolveHoverPoint(scene, { type: 'trail', t: 1, mapId: 27 }), null);
});
