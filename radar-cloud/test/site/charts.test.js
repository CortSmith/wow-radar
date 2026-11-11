'use strict';
// charts.js: world-to-frame percent math and joining charts with the maps index.
const test = require('node:test');
const assert = require('node:assert/strict');
const C = require('../../site/charts.js');

test('worldToPct: X north/up, Y west/left, with calibration', () => {
  const m = { x0: 0, x1: 100, y0: 0, y1: 200 };
  assert.deepEqual(C.worldToPct(m, 100, 200), [0, 0]); // north-west corner
  assert.deepEqual(C.worldToPct(m, 0, 0), [100, 100]);
  assert.deepEqual(C.worldToPct(m, 50, 100), [50, 50]);
  assert.deepEqual(C.worldToPct(m, 50, 100, { dx: 2, dy: -1, sx: 1, sy: 1 }), [52, 49]);
});

test('buildView joins MAP_CHANGE rectangles with the maps index (available vs noImage, sorted)', () => {
  const zones = { elwynnforest: { file: 'Elwynn.webp', w: 1002, h: 668, revealed: true } };
  const v = C.buildView(
    {
      2: { name: 'Zeta', x0: 0, x1: 1, y0: 0, y1: 1 },
      1: { name: 'Elwynn Forest', x0: 0, x1: 1, y0: 0, y1: 1 },
      3: { name: 'Bad', x0: 1, x1: 1, y0: 0, y1: 1 },
    },
    zones,
  );
  assert.deepEqual(
    v.available.map(m => [m.mapId, m.url]),
    [[1, 'maps/Elwynn.webp']],
  );
  assert.deepEqual(
    v.noImage.map(m => m.mapId),
    [2],
  );
  assert.equal(
    C.currentZone({ zone: 'x', mapId: 1, maps: { 1: { name: 'Elwynn Forest' } } }, zones).url,
    'maps/Elwynn.webp',
  );
  assert.equal(C.currentZone({ zone: 'Nowhere', mapId: 0, maps: {} }, zones), null);
});

test('charts: worldToPct maps corners, buildView splits charts with and without an image', () => {
  const chart = { x0: 0, x1: 100, y0: 0, y1: 200 };
  assert.deepEqual(C.worldToPct(chart, 100, 200), [0, 0]);
  assert.deepEqual(C.worldToPct(chart, 0, 0), [100, 100]);
  assert.deepEqual(C.worldToPct(chart, 50, 100, { dx: 5, dy: 0, sx: 2, sy: 1 }), [105, 50]);
  const view = C.buildView(
    {
      1: { name: 'Elwynn Forest', x0: 0, x1: 1, y0: 0, y1: 1 },
      2: { name: 'Nowhere', x0: 0, x1: 1, y0: 0, y1: 1 },
      3: { name: 'Flat', x0: 1, x1: 1, y0: 0, y1: 1 },
    },
    { elwynnforest: { file: 'e.webp', w: 10, h: 20, revealed: true } },
  );
  assert.deepEqual(
    view.available.map(m => m.url),
    ['maps/e.webp'],
  );
  assert.deepEqual(
    view.noImage.map(m => m.name),
    ['Nowhere'],
  );
});
