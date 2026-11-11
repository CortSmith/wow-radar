'use strict';
// console-settings.js: compaction for PUT /api/settings, defaults and merge.
const test = require('node:test');
const assert = require('node:assert/strict');
const C = require('../../site/console-settings.js');

test('compactSettings drops identity calibrations, rounds, and stays under 8 KB', () => {
  const cal = { 1: { dx: 0, dy: 0, sx: 1, sy: 1 }, 2: { dx: 1.23456, dy: -2, sx: 1.0049999, sy: 0.9 } };
  const out = C.compactSettings({ mode: 'auto', ruleset: 'pvp', cal, prefs: { follow: true } });
  assert.ok(!out.cal[1]);
  assert.deepEqual(out.cal[2], { dx: 1.23, dy: -2, sx: 1.005, sy: 0.9 });
  assert.equal(out.ruleset, 'pvp');
  for (let i = 100; i < 600; i++) cal[i] = { dx: i / 7, dy: i / 3, sx: 1.01, sy: 0.99 };
  const big = C.compactSettings({ mode: 'auto', ruleset: 'pvp', cal });
  assert.ok(JSON.stringify(big).length <= 8000);
  assert.ok(Object.keys(big.cal).length > 50);
});

test('console settings: defaults, merge (server wins, calibration combined), fresh objects each time', () => {
  const a = C.defaultSettings(),
    b = C.defaultSettings();
  assert.notEqual(a.cal, b.cal);
  assert.deepEqual(a, { mode: 'auto', ruleset: 'pvp', cal: {}, prefs: {}, uploadSec: 60 });
  const merged = C.mergeSettings(
    { mode: 'auto', ruleset: 'pvp', cal: { 1: 'local' }, prefs: { follow: true, range: 300 }, uploadSec: 60 },
    { mode: 'manual', prefs: { range: 500 }, cal: { 2: 'server' } },
    { 3: 'stored' },
  );
  assert.equal(merged.mode, 'manual');
  assert.deepEqual(merged.prefs, { follow: true, range: 500 });
  assert.deepEqual(merged.cal, { 3: 'stored', 2: 'server' });
});

test('timezone is optional, absent from the defaults, kept by compaction and merge (server wins)', () => {
  assert.ok(!('timezone' in C.defaultSettings()));
  const out = C.compactSettings({ mode: 'auto', ruleset: 'pvp', cal: {}, prefs: {}, timezone: 'Asia/Kolkata' });
  assert.equal(out.timezone, 'Asia/Kolkata');
  const merged = C.mergeSettings({ timezone: 'UTC' }, { timezone: 'America/New_York' }, {});
  assert.equal(merged.timezone, 'America/New_York');
  assert.equal(C.mergeSettings({ timezone: 'UTC' }, {}, {}).timezone, 'UTC');
});
