'use strict';
// contacts.js: cloud snapshot from stored rows, history table cells, trail summaries.
const test = require('node:test');
const assert = require('node:assert/strict');
const C = require('../../site/contacts.js');

test('cloudSnapshot: pips from history rows, 24 h stale rule, auto map = busiest with image', () => {
  const now = 1e12,
    day = 86400000;
  const rows = [
    { guid: 'a', name: 'A', reaction: 'friendly', lastX: 1, lastY: 2, lastMap: 10, lastSeen: now - 1000, level: 60 },
    { guid: 'b', name: 'B', reaction: 'hostile', lastX: 3, lastY: 4, lastMap: 10, lastSeen: now - 2 * day },
    { guid: 'c', name: 'C', reaction: 'hostile', lastSeen: now },
  ];
  const s = C.cloudSnapshot(rows, [{ id: 10, name: 'Z', x0: 0, x1: 1, y0: 0, y1: 1 }], now);
  assert.equal(s.mapId, 10);
  assert.equal(s.me, null);
  assert.ok(s.cloud);
  assert.deepEqual(
    s.pips.map(p => [p.guid, p.state, p.posKind]),
    [
      ['a', 'active', 'exact'],
      ['b', 'stale', 'exact'],
      ['c', 'active', 'none'],
    ],
  );
  assert.equal(s.maps['10'].name, 'Z');
});

test('historyRow computes the original columns from additive counters', () => {
  const r = C.historyRow({
    name: 'N',
    dmgTotal: 1000,
    dmgHits: 4,
    srcEvents: 10,
    hpSum: 600,
    hpSamples: 3,
    maxHp: 2000,
    events: 25,
    lastSeen: 5,
    lastZone: 'Z',
  });
  assert.deepEqual(r, {
    name: 'N',
    avgDmgHit: 250,
    avgDmgEvent: 100,
    avgHp: 200,
    maxHp: 2000,
    samples: 25,
    lastSeen: 5,
    zone: 'Z',
  });
  const z = C.historyRow({ name: 'Q' });
  assert.equal(z.avgDmgHit, 0);
  assert.equal(z.avgHp, 0);
});

test('trackSummary counts fixes per map ascending', () => {
  const s = C.trackSummary([{ mapId: 5, zone: 'B' }, { mapId: 2, zone: 'A' }, { mapId: 5 }]);
  assert.equal(s.total, 3);
  assert.deepEqual(s.maps, [
    { mapId: 2, zone: 'A', count: 1 },
    { mapId: 5, zone: 'B', count: 2 },
  ]);
});
