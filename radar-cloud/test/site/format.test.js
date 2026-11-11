'use strict';
// format.js: ages, numbers, percents, names and query strings.
const test = require('node:test');
const assert = require('node:assert/strict');
const C = require('../../site/format.js');

test('qs skips empty values and encodes', () => {
  assert.equal(C.qs({ rs: 'pvp', q: 'a b', guid: '', x: undefined }), 'rs=pvp&q=a%20b');
});

test('fmtAge / esc / num / normName / fmtPct / mb', () => {
  assert.equal(C.fmtAge(5), '5s');
  assert.equal(C.fmtAge(125), '2m');
  assert.equal(C.fmtAge(3700), '1h1m');
  assert.equal(C.fmtAge(3 * 86400), '3d');
  assert.equal(C.fmtAge(-4), '0s');
  assert.equal(C.esc('<a href="x">&</a>'), '&lt;a href=&quot;x&quot;&gt;&amp;&lt;/a&gt;');
  assert.equal(C.num(0), '-');
  assert.equal(C.num(12.345), '12.3');
  assert.equal(C.normName("Un'Goro Crater"), 'ungorocrater');
  assert.equal(C.fmtPct([1.234, 56.78]), '1.2, 56.8');
  assert.equal(C.mb(1048576), '1.0');
});

test('format: ages, numbers, percents and query strings', () => {
  assert.equal(C.fmtAge(5), '5s');
  assert.equal(C.fmtAge(125), '2m');
  assert.equal(C.fmtAge(3 * 3600 + 120), '3h2m');
  assert.equal(C.fmtAge(3 * 86400), '3d');
  assert.equal(C.num(0), '-');
  assert.equal(C.num(12.345), '12.3');
  assert.equal(C.fmtPct([1.25, 99]), '1.3, 99.0');
  assert.equal(C.qs({ a: 1, b: '', c: undefined, d: 'x y' }), 'a=1&d=x%20y');
  assert.equal(C.normName('Elwynn Forest!'), 'elwynnforest');
});
