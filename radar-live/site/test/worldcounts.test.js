// Stage 14 W3b: zoneCounts (per-mapId friendly/hostile counts of currently-live contacts) and
// chipRadius (sqrt-area chip sizing), both pure.
import test from 'node:test';
import assert from 'node:assert/strict';
import { zoneCounts, chipRadius, chipTooltipModel, zoneTotal, pieSlices, sliceAt } from '../js/worldcounts.js';

const NOW = 1_760_000_000_000;
const C = (guid, over = {}) => ({
  guid, name: guid, reaction: 'friendly', mapId: 1, x: 10, y: 20, posTime: NOW, lastSeen: NOW, ...over,
});

test('zoneCounts: no contacts -> empty map', () => {
  assert.deepEqual(zoneCounts(new Map(), NOW), new Map());
});

test('zoneCounts: splits friendly / neutral / hostile(+unknown), grouped per mapId', () => {
  const contacts = new Map([
    ['a', C('a', { mapId: 1, reaction: 'friendly' })],
    ['b', C('b', { mapId: 1, reaction: 'hostile' })],
    ['c', C('c', { mapId: 1, reaction: 'neutral' })],
    ['d', C('d', { mapId: 2, reaction: 'unknown' })],
    ['e', C('e', { mapId: 2, reaction: 'friendly' })],
  ]);
  const out = zoneCounts(contacts, NOW);
  assert.deepEqual(out.get(1), { friendly: 1, neutral: 1, hostile: 1 });
  assert.deepEqual(out.get(2), { friendly: 1, neutral: 0, hostile: 1 }); // unknown counts into the hostile bucket
});

test('zoneCounts: stale and gone contacts are excluded (only classify() === live counts)', () => {
  const contacts = new Map([
    ['live', C('live', { mapId: 1, lastSeen: NOW })],
    ['stale', C('stale', { mapId: 1, lastSeen: NOW - 300_000 })], // >= STALE_AFTER_MS
    ['gone', C('gone', { mapId: 1, lastSeen: NOW - 900_000 })], // >= GONE_AFTER_MS
  ]);
  const out = zoneCounts(contacts, NOW);
  assert.deepEqual(out.get(1), { friendly: 1, neutral: 0, hostile: 0 });
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

test('chipTooltipModel: zone name + exact friendly/neutral/hostile counts', () => {
  const MSG = { worldChipFriendly: '{n} friendly', worldChipNeutral: '{n} neutral', worldChipHostile: '{n} hostile' };
  const fmt = (t, vars) => t.replace('{n}', String(vars.n));
  const rows = chipTooltipModel({ name: 'Ashenvale', friendly: 3, neutral: 2, hostile: 7 }, { MSG, fmt });
  assert.deepEqual(rows, ['Ashenvale', '3 friendly', '2 neutral', '7 hostile']);
});

test('zoneTotal: sums the three buckets; missing/negative buckets count as 0', () => {
  assert.equal(zoneTotal({ friendly: 1, neutral: 2, hostile: 3 }), 6);
  assert.equal(zoneTotal({ friendly: 4 }), 4);
  assert.equal(zoneTotal({ friendly: -1, hostile: 2 }), 2);
  assert.equal(zoneTotal(null), 0);
});

const TAU = Math.PI * 2;
const TOP = -Math.PI / 2;

test('pieSlices: zero total -> no slices', () => {
  assert.deepEqual(pieSlices({ friendly: 0, neutral: 0, hostile: 0 }), []);
});

test('pieSlices: proportional angles, clockwise from 12 o-clock, friendly/neutral/hostile order', () => {
  const s = pieSlices({ friendly: 2, neutral: 1, hostile: 1 });
  assert.deepEqual(s.map((x) => x.reaction), ['friendly', 'neutral', 'hostile']);
  assert.ok(Math.abs(s[0].start - TOP) < 1e-9);
  assert.ok(Math.abs((s[0].end - s[0].start) - TAU / 2) < 1e-9);
  assert.ok(Math.abs((s[1].end - s[1].start) - TAU / 4) < 1e-9);
  assert.ok(Math.abs(s[1].start - s[0].end) < 1e-9, 'slices are contiguous');
  assert.ok(Math.abs(s[2].end - (TOP + TAU)) < 1e-9, 'last slice closes the circle');
});

test('pieSlices: empty buckets skipped; a single bucket is one full disc', () => {
  const s = pieSlices({ friendly: 0, neutral: 5, hostile: 0 });
  assert.equal(s.length, 1);
  assert.equal(s[0].reaction, 'neutral');
  assert.ok(Math.abs(s[0].start - TOP) < 1e-9);
  assert.ok(Math.abs(s[0].end - s[0].start - TAU) < 1e-9);
  assert.deepEqual(pieSlices({ friendly: 1, neutral: 0, hostile: 1 }).map((x) => x.reaction), ['friendly', 'hostile']);
});

test('sliceAt: angle picks the slice (clockwise from 12 o-clock)', () => {
  const chip = { x: 100, y: 100, friendly: 1, neutral: 1, hostile: 2 }; // friendly 0-90deg, neutral 90-180, hostile 180-360
  assert.equal(sliceAt(chip, 110, 90), 'friendly'); // up-right
  assert.equal(sliceAt(chip, 110, 110), 'neutral'); // down-right
  assert.equal(sliceAt(chip, 90, 110), 'hostile'); // down-left
  assert.equal(sliceAt(chip, 90, 90), 'hostile'); // up-left
});

test('sliceAt: single bucket -> that reaction anywhere; empty chip -> null', () => {
  assert.equal(sliceAt({ x: 0, y: 0, friendly: 3, neutral: 0, hostile: 0 }, -5, 7), 'friendly');
  assert.equal(sliceAt({ x: 0, y: 0, friendly: 0, neutral: 0, hostile: 0 }, 1, 1), null);
});
