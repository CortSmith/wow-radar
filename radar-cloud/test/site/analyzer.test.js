'use strict';
// analyzer.js: killing blow, assist and duplicate-death rules, and the bounded memory of victims and owners.
const test = require('node:test');
const assert = require('node:assert/strict');
const { Analyzer } = require('../../site/analyzer.js');
const P = require('../../site/parser.js');
const H = require('./helpers.js');

function run(lines) {
  const deaths = [];
  const analyzer = new Analyzer(d => deaths.push(d));
  analyzer.setFile('log');
  for (const line of lines) analyzer.apply(P.parseLine(line));
  return deaths;
}

test('a hostile hit then death within 5 s is a killing blow for the hostile player', () => {
  const deaths = run([H.dmg(1, H.FOE, H.ALLY), H.died(3, H.ALLY)]);
  assert.equal(deaths.length, 1);
  assert.equal(deaths[0].killer.guid, H.FOE.g);
  assert.equal(deaths[0].victimGuid, H.ALLY.g);
});

test('a second hitter within 15 s is an assist, not the killer', () => {
  const deaths = run([H.dmg(1, H.FOE2, H.ALLY), H.dmg(9, H.FOE, H.ALLY), H.died(10, H.ALLY)]);
  assert.equal(deaths[0].killer.guid, H.FOE.g);
  assert.deepEqual(
    deaths[0].assists.map(a => a.guid),
    [H.FOE2.g],
  );
});

test('two death events within 3 s are one death; switching file forgets the windows', () => {
  const analyzer = new Analyzer(() => {});
  assert.equal(run([H.dmg(1, H.FOE, H.ALLY), H.died(2, H.ALLY), H.died(3, H.ALLY)]).length, 1);
  analyzer.setFile('a');
  analyzer.apply(P.parseLine(H.dmg(1, H.FOE, H.ALLY)));
  assert.equal(analyzer.wins.size, 1);
  analyzer.setFile('b');
  assert.equal(analyzer.wins.size, 0);
});
