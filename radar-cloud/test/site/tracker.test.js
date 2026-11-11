'use strict';
// tracker.js: live units (me, fixes, hp, stale/expiry, approximate fixes, estimates, sources, names).
const test = require('node:test');
const assert = require('node:assert/strict');
const P = require('../../site/parser.js');
const T = Object.assign(
  {},
  ...['tracker', 'analyzer', 'engine', 'engine-protocol'].map(m => require('../../site/' + m + '.js')),
);
const H = require('./helpers.js');

function mk(opts) {
  let now = H.T0;
  const tr = new T.Tracker(opts || {}, () => now);
  return {
    tr,
    set: ms => {
      now = ms;
    },
    adv: ms => {
      now += ms;
    },
    feed(src, line) {
      tr.apply(src || 'a', P.parseLine(line));
    },
  };
}

test('me detection, exact fix, hp/level, damage totals', () => {
  const m = mk();
  m.feed('a', H.dmg(1, H.ME, H.FOE, { infoOf: 'src', x: 10, y: 20, hp: 500, max: 1000, level: 61, amount: 250 }));
  const s = m.tr.snapshot();
  assert.equal(s.me.guid, H.ME.g);
  assert.ok(s.me.me);
  assert.equal(s.me.posKind, 'exact');
  assert.equal(s.me.x, 10);
  assert.equal(s.me.mapId, 1429);
  assert.equal(s.me.level, 61);
  assert.equal(s.me.hp, 500);
  assert.equal(s.me.maxHp, 1000);
  assert.equal(s.me.damageTotal, 250);
  assert.equal(s.me.damageHits, 1);
  assert.equal(s.mapId, 1429);
  assert.equal(s.pips.length, 2);
  assert.equal(s.stats.events, 1);
  assert.equal(s.staleSec, 300);
  assert.equal(s.expireSec, 900);
});

test('NPCs are not tracked by default but are with trackNPCs', () => {
  const npc = { g: 'Creature-0-1-2-3-4-5', n: 'Boar', f: 0xa48 };
  let m = mk();
  m.feed('a', H.dmg(1, H.ME, npc, { infoOf: 'src' }));
  assert.equal(m.tr.snapshot().pips.length, 1);
  m = mk({ trackNPCs: true });
  m.feed('a', H.dmg(1, H.ME, npc, { infoOf: 'src' }));
  const s = m.tr.snapshot();
  assert.equal(s.pips.length, 2);
  assert.equal(s.pips.find(p => p.guid === npc.g).kind, 'npc');
});

test('stale after 5 min, expired after 15 min, me never expires', () => {
  const m = mk();
  m.feed('a', H.dmg(0, H.ME, H.FOE, { infoOf: 'src' }));
  m.feed('a', H.dmg(0, H.ALLY, H.FOE, { infoOf: 'dst', x: 1, y: 1 }));
  m.adv(60000);
  assert.deepEqual(
    m.tr.snapshot().pips.map(p => p.state),
    ['active', 'active', 'active'],
  );
  m.adv(260000); // 320 s since last event
  let s = m.tr.snapshot();
  assert.ok(s.pips.every(p => p.state === 'stale'));
  assert.ok(s.pips[0].ageSec > 300);
  m.adv(600000); // 920 s
  s = m.tr.snapshot();
  assert.deepEqual(
    s.pips.map(p => p.guid),
    [H.ME.g],
  );
  assert.ok(s.me);
});

test('approximate sightings near me; exact beats approx unless approx newer by > 30 s', () => {
  const m = mk();
  m.feed('a', H.dmg(0, H.ME, H.FOE, { infoOf: 'src', x: 50, y: 60 })); // me exact at 0s
  m.feed('a', H.dmg(10, H.ALLY, H.FOE, { infoOf: 'dst', x: 0, y: 0 })); // hmm: info is FOE (dst) -> foe exact
  let s = m.tr.snapshot();
  const ally = s.pips.find(p => p.guid === H.ALLY.g),
    foe = s.pips.find(p => p.guid === H.FOE.g);
  assert.equal(ally.posKind, 'approx');
  assert.equal(ally.x, 50);
  assert.equal(ally.weight, 0.3);
  assert.equal(foe.posKind, 'exact');
  assert.equal(foe.weight, 1);
  // ally gets an exact fix at t=20, then only an approx sighting 10 s later: exact still wins
  m.feed('a', H.dmg(20, H.FOE, H.ALLY, { infoOf: 'dst', x: 7, y: 8 }));
  m.feed('a', H.dmg(30, H.ME, H.ALLY, { infoOf: 'src', x: 51, y: 61 }));
  assert.equal(m.tr.snapshot().pips.find(p => p.guid === H.ALLY.g).posKind, 'exact');
  // approx 40 s after the exact fix (me fix is < 120 s old): approx wins
  m.feed('a', H.dmg(61, H.ME, H.ALLY, { infoOf: 'src', x: 52, y: 62 }));
  m.feed('a', H.dmg(62, H.FOE2, H.ALLY, { infoOf: 'dst', x: 9, y: 9 })); // exact again
  m.feed('a', H.dmg(95, H.ME, H.ALLY, { infoOf: 'src', x: 53, y: 63 }));
  const p = m.tr.snapshot().pips.find(q => q.guid === H.ALLY.g);
  assert.equal(p.posKind, 'approx');
  assert.equal(p.x, 53);
});

test('no approx fixes when my fix is older than 120 s or approx is disabled', () => {
  let m = mk();
  m.feed('a', H.dmg(0, H.ME, H.FOE, { infoOf: 'src', x: 50, y: 60 }));
  m.feed('a', H.dmg(130, H.ALLY, H.FOE2, { infoOf: 'src', x: 1, y: 1 }).replace('0x514', '0x514'));
  // ALLY is the info unit there (src) so it has an exact fix; use a line without a position for the real check
  m = mk();
  m.feed('a', H.dmg(0, H.ME, H.FOE, { infoOf: 'src', x: 50, y: 60 }));
  const noPos =
    `${H.ts(130)}  SPELL_AURA_APPLIED,${H.ALLY.g},"Ally-PvP-US",0x514,0x0,${H.FOE2.g},` +
    `"Foe2-PvP-US",0x548,0x0,1,"Buff",0x1,BUFF`;
  m.feed('a', noPos);
  assert.equal(m.tr.snapshot().pips.find(p => p.guid === H.ALLY.g).posKind, 'none');
  m = mk({ disableApprox: true });
  m.feed('a', H.dmg(0, H.ME, H.FOE, { infoOf: 'src', x: 50, y: 60 }));
  m.feed('a', noPos.replace(H.ts(130), H.ts(10)));
  assert.equal(m.tr.snapshot().pips.find(p => p.guid === H.ALLY.g).posKind, 'none');
});

test('estimate: weighted mean, minimum spread 8, extrapolation when moving', () => {
  assert.equal(T.estimate([], 0, 15, 30000), null);
  const fx = (t, x, y, kind) => ({ t, x, y, kind: kind || 'exact', mapId: 1 });
  const still = T.estimate([fx(1000, 10, 10), fx(2000, 10, 10)], 3000, 15, 30000);
  assert.equal(still.x, 10);
  assert.equal(still.spread, 8);
  assert.equal(still.n, 2);
  assert.equal(still.exact, 2);
  const moving = T.estimate([fx(0, 0, 0), fx(2000, 20, 0), fx(4000, 40, 0)], 6000, 15, 30000); // 10 yd/s east
  assert.ok(moving.x > 30, 'extrapolated ahead: ' + moving.x);
  const apx = T.estimate([fx(0, 0, 0, 'approx'), fx(1000, 10, 0, 'exact')], 1000, 15, 30000);
  assert.equal(apx.exact, 1);
  assert.ok(apx.x > 5);
  // gap > 30 s ends the encounter; other map ends it too
  assert.equal(T.estimate([fx(0, 0, 0), fx(100000, 5, 5)], 100000, 15, 30000).n, 1);
  assert.equal(T.estimate([fx(0, 0, 0), { t: 1000, x: 1, y: 1, kind: 'exact', mapId: 2 }], 1000, 15, 30000).n, 1);
});

test('multi-source: separate me per log, same GUID is one pip', () => {
  const m = mk();
  const other = H.player('000000EE', 'Alt-PvP-US', H.MINE);
  m.feed('log1', H.dmg(0, H.ME, H.FOE, { infoOf: 'src', x: 1, y: 1 }));
  m.feed('log2', H.dmg(1, other, H.FOE, { infoOf: 'src', x: 500, y: 500 }));
  const s = m.tr.snapshot();
  assert.deepEqual(s.sources, ['log1', 'log2']);
  assert.equal(s.me.guid, other.g); // primary = most recent source
  assert.equal(s.pips.filter(p => p.guid === H.FOE.g).length, 1);
  assert.equal(s.pips.filter(p => p.me).length, 2);
});

test('maps and zone tracking, advanced flag', () => {
  const m = mk();
  m.feed('a', `${H.ts(0)}  COMBAT_LOG_VERSION,20,ADVANCED_LOG_ENABLED,0`);
  m.feed('a', H.zoneChange(0, 'Westfall'));
  m.feed('a', H.mapChange(0, 1436, 'Westfall', 100, -100, 50, -50));
  const s = m.tr.snapshot();
  assert.equal(s.zone, 'Westfall');
  assert.equal(s.mapId, 1436);
  assert.equal(s.advanced, false);
  assert.deepEqual(s.maps['1436'], { name: 'Westfall', x0: -100, x1: 100, y0: -50, y1: 50 });
});

test('display names use the detected realm suffix', () => {
  const m = mk();
  for (let i = 0; i < 6; i++)
    m.feed(
      'a',
      H.dmg(i, H.player('0000100' + i, 'Pl' + i + '-PvP-US', H.HOSTILE), H.ALLY, { infoOf: 'src', x: i, y: i }),
    );
  const s = m.tr.snapshot();
  assert.equal(s.realmSuffix, '-PvP-US');
  assert.ok(s.pips.some(p => p.name === 'Pl3'));
});
