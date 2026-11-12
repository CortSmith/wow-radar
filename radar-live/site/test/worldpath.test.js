import test from 'node:test';
import assert from 'node:assert/strict';
import { worldPath } from '../js/worldpath.js';

const F = (t, mapId, over = {}) => ({ t, mapId, x: 0, y: 0, kind: 'exact', ...over });

test('worldPath: empty input -> empty array', () => {
  assert.deepEqual(worldPath([]), []);
  assert.deepEqual(worldPath(null), []);
  assert.deepEqual(worldPath(undefined), []);
});

test('worldPath: single fix -> one visit with enteredAt === leftAt', () => {
  const fix = F(100, 'a');
  const visits = worldPath([fix]);
  assert.equal(visits.length, 1);
  assert.deepEqual(visits[0], { mapId: 'a', enteredAt: 100, leftAt: 100, lastFix: fix });
});

test('worldPath: A -> B -> C collapses into three visits', () => {
  const trail = [F(1, 'a'), F(2, 'a'), F(3, 'b'), F(4, 'c'), F(5, 'c')];
  const visits = worldPath(trail);
  assert.deepEqual(visits.map((v) => v.mapId), ['a', 'b', 'c']);
  assert.deepEqual(visits.map((v) => [v.enteredAt, v.leftAt]), [[1, 2], [3, 3], [4, 5]]);
});

test('worldPath: a run of 3+ consecutive same-mapId fixes collapses to one visit with correct bounds', () => {
  const trail = [F(10, 'a'), F(20, 'a'), F(30, 'a'), F(40, 'a')];
  const visits = worldPath(trail);
  assert.equal(visits.length, 1);
  assert.equal(visits[0].enteredAt, 10);
  assert.equal(visits[0].leftAt, 40);
  assert.equal(visits[0].lastFix.t, 40);
});

test('worldPath: A -> B -> A produces two separate A visits, not merged', () => {
  const trail = [F(1, 'a'), F(2, 'a'), F(3, 'b'), F(4, 'a'), F(5, 'a')];
  const visits = worldPath(trail);
  assert.deepEqual(visits.map((v) => v.mapId), ['a', 'b', 'a']);
  assert.deepEqual(visits.map((v) => [v.enteredAt, v.leftAt]), [[1, 2], [3, 3], [4, 5]]);
});

test('worldPath: unsorted input is sorted by t before collapsing, input array not mutated', () => {
  const trail = [F(3, 'b'), F(1, 'a'), F(2, 'a')];
  const copy = [...trail];
  const visits = worldPath(trail);
  assert.deepEqual(visits.map((v) => v.mapId), ['a', 'b']);
  assert.deepEqual(visits.map((v) => v.enteredAt), [1, 3]);
  assert.deepEqual(trail, copy); // original order/array untouched
});

test('worldPath: identical timestamps tie-break stably without throwing (same mapId still one visit)', () => {
  const trail = [F(5, 'a'), F(5, 'a'), F(5, 'b')];
  assert.doesNotThrow(() => worldPath(trail));
  const visits = worldPath(trail);
  assert.deepEqual(visits.map((v) => v.mapId), ['a', 'b']);
  assert.equal(visits[0].enteredAt, 5);
  assert.equal(visits[0].leftAt, 5);
});

test('worldPath: malformed entries (missing mapId or t, or null) are skipped, not thrown', () => {
  const trail = [
    F(1, 'a'),
    { t: 2 }, // missing mapId
    { mapId: 'b' }, // missing t
    null,
    undefined,
    F(3, 'b'),
  ];
  assert.doesNotThrow(() => worldPath(trail));
  const visits = worldPath(trail);
  assert.deepEqual(visits.map((v) => v.mapId), ['a', 'b']);
  assert.deepEqual(visits.map((v) => v.enteredAt), [1, 3]);
});
