// Stage 14 W3b: zoneCounts (per-mapId friendly/hostile counts of currently-live contacts) and
// chipRadius (sqrt-area chip sizing), both pure.
import test from 'node:test';
import assert from 'node:assert/strict';
import { zoneCounts, chipRadius, chipTooltipModel } from '../js/worldcounts.js';

const NOW = 1_760_000_000_000;
const C = (guid, over = {}) => ({
  guid, name: guid, reaction: 'friendly', mapId: 1, x: 10, y: 20, posTime: NOW, lastSeen: NOW, ...over,
});

test('zoneCounts: no contacts -> empty map', () => {
  assert.deepEqual(zoneCounts(new Map(), NOW), new Map());
});

test('zoneCounts: splits friendly vs hostile/neutral/unknown, grouped per mapId', () => {
  const contacts = new Map([
    ['a', C('a', { mapId: 1, reaction: 'friendly' })],
    ['b', C('b', { mapId: 1, reaction: 'hostile' })],
    ['c', C('c', { mapId: 1, reaction: 'neutral' })],
    ['d', C('d', { mapId: 2, reaction: 'unknown' })],
    ['e', C('e', { mapId: 2, reaction: 'friendly' })],
  ]);
  const out = zoneCounts(contacts, NOW);
  assert.deepEqual(out.get(1), { friendly: 1, hostile: 2 }); // neutral counts into the hostile bucket
  assert.deepEqual(out.get(2), { friendly: 1, hostile: 1 }); // unknown counts into the hostile bucket
});

test('zoneCounts: stale and gone contacts are excluded (only classify() === live counts)', () => {
  const contacts = new Map([
    ['live', C('live', { mapId: 1, lastSeen: NOW })],
    ['stale', C('stale', { mapId: 1, lastSeen: NOW - 300_000 })], // >= STALE_AFTER_MS
    ['gone', C('gone', { mapId: 1, lastSeen: NOW - 900_000 })], // >= GONE_AFTER_MS
  ]);
  const out = zoneCounts(contacts, NOW);
  assert.deepEqual(out.get(1), { friendly: 1, hostile: 0 });
});

test('zoneCounts: contacts with no usable position are excluded', () => {
  const contacts = new Map([['a', C('a', { posTime: 0 })], ['b', C('b', { mapId: null })]]);
  assert.deepEqual(zoneCounts(contacts, NOW), new Map());
});

test('chipRadius: 0 count -> 0 (no chip)', () => {
  assert.equal(chipRadius(0), 0);
  assert.equal(chipRadius(-1), 0);
});

test('chipRadius: 1 count clears the minimum floor and is > 0', () => {
  assert.ok(chipRadius(1) > 0);
});

test('chipRadius: scales by sqrt(count), not linearly - assert the ratio above the floor', () => {
  const r1 = chipRadius(1);
  const r4 = chipRadius(4);
  const r9 = chipRadius(9);
  const floor = chipRadius(0.0001); // ~0, i.e. the part of r1 that is pure floor is NOT part of this ratio
  // chipRadius(n) = FLOOR + SCALE*sqrt(n), so (r(n) - r(epsilon-ish-floor-only)) should scale as sqrt(n).
  // Isolate the two unknowns (FLOOR, SCALE) from two samples and check the third predicts correctly.
  const scale = r4 - r1; // SCALE*(sqrt(4)-sqrt(1)) = SCALE*1
  assert.ok(Math.abs((r9 - r1) - scale * (Math.sqrt(9) - Math.sqrt(1))) < 1e-9);
  assert.ok(floor >= 0);
});

test('chipTooltipModel: no hit -> empty rows', () => {
  assert.deepEqual(chipTooltipModel(null, { MSG: {}, fmt: (s) => s }), []);
});

test('chipTooltipModel: zone name + exact friendly/hostile counts', () => {
  const MSG = { worldChipFriendly: '{n} friendly', worldChipHostile: '{n} hostile' };
  const fmt = (t, vars) => t.replace('{n}', String(vars.n));
  const rows = chipTooltipModel({ name: 'Ashenvale', friendly: 3, hostile: 7 }, { MSG, fmt });
  assert.deepEqual(rows, ['Ashenvale', '3 friendly', '7 hostile']);
});
