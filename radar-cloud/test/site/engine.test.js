'use strict';
// engine.js (with tracker and analyzer): history deltas, trails, watermarks, bounty credit, rule-set detection, flush.
const test = require('node:test');
const assert = require('node:assert/strict');
const T = Object.assign(
  {},
  ...['tracker', 'analyzer', 'engine', 'engine-protocol'].map(m => require('../../site/' + m + '.js')),
);
const H = require('./helpers.js');

function eng(o) {
  const e = new T.Engine(Object.assign({ clock: () => H.T0 + 3600000 }, o || {}));
  e.begin({ file: 'log.txt', wm: {} });
  return e;
}
const feed = (e, lines) => e.feedText(lines.join('\n') + '\n', true);
const find = (batch, g) => batch.players.find(p => p.guid === g);

test('history deltas: events, srcEvents, damage, hp, maxHp, level, last position', () => {
  const e = eng();
  feed(e, [
    H.dmg(1, H.FOE, H.ALLY, { x: 10, y: 10, hp: 1000, max: 2000, level: 60, amount: 100 }),
    H.dmg(2, H.FOE, H.ALLY, { x: 12, y: 10, hp: 800, max: 2200, level: 61, amount: 300 }),
    H.dmg(3, H.ALLY, H.FOE, { infoOf: 'src', x: 20, y: 20, hp: 700, max: 2000, level: 61, amount: 50 }),
  ]);
  const [b] = e.flush();
  assert.equal(b.rs, 'pvp');
  const foe = find(b, H.FOE.g),
    ally = find(b, H.ALLY.g);
  assert.equal(foe.d.events, 3);
  assert.equal(foe.d.srcEvents, 2);
  assert.equal(foe.d.dmgHits, 2);
  assert.equal(foe.d.dmgTotal, 400);
  assert.equal(foe.d.dmgEvents, 2);
  assert.equal(ally.d.events, 3);
  assert.equal(ally.d.srcEvents, 1);
  assert.equal(ally.d.dmgTotal, 50);
  assert.equal(ally.d.hpSamples, 3);
  assert.equal(ally.d.hpSum, 1000 + 800 + 700);
  assert.equal(ally.d.maxHp, 2200);
  assert.equal(ally.level, 61);
  assert.equal(ally.reaction, 'friendly');
  assert.equal(foe.reaction, 'hostile');
  assert.equal(ally.x, 20);
  assert.equal(ally.posTime, H.T0 + 3000);
  assert.equal(ally.mapId, 1429);
  assert.equal(foe.hpSamples, undefined);
  assert.equal(foe.d.hpSamples, 0);
  assert.equal(ally.firstSeen, H.T0 + 1000);
  assert.equal(ally.lastSeen, H.T0 + 3000);
  assert.equal(b.from, 0);
  assert.equal(b.to, H.T0 + 3000);
});

test('tracks: 0.1 rounding, <2yd/<5s dedupe, other map kept, far or late kept', () => {
  const e = eng();
  const at = (s, x, y, map) => H.dmg(s, H.FOE, H.ALLY, { x, y, map: map || 1429 });
  feed(e, [at(0, 10.04, 20.06), at(1, 10.9, 20.0), at(2, 13.5, 20.0), at(7, 13.6, 20.0), at(8, 13.6, 20.0, 99)]);
  const [b] = e.flush();
  const f = find(b, H.ALLY.g).fixes;
  assert.deepEqual(
    f.map(x => x.slice(1)),
    [
      [1429, 10, 20.1, 'exact'],
      [1429, 13.5, 20, 'exact'],
      [1429, 13.6, 20, 'exact'],
      [99, 13.6, 20, 'exact'],
    ],
  );
  assert.equal(f[0][0], H.T0);
});

test('addFix keeps chronological order for out-of-order input and caps at 5000', () => {
  const tr = [];
  T.addFix(tr, [10000, 1, 0, 0, 'exact']);
  T.addFix(tr, [30000, 1, 100, 0, 'exact']);
  T.addFix(tr, [20000, 1, 50, 0, 'exact']);
  assert.deepEqual(
    tr.map(x => x[0]),
    [10000, 20000, 30000],
  );
  assert.equal(T.addFix(tr, [20500, 1, 50.5, 0, 'exact']), false);
  const big = [];
  for (let i = 0; i < 5100; i++) T.addFix(big, [i * 10000, 1, i * 10, 0, 'exact']);
  assert.equal(big.length, 5000);
  assert.equal(big[0][0], 100 * 10000);
});

test('approx sightings land in the track as kind approx but never set the last position', () => {
  const e = eng();
  feed(e, [
    H.dmg(0, H.ME, H.FOE, { infoOf: 'src', x: 50, y: 60 }),
    `${H.ts(5)}  SPELL_AURA_APPLIED,${H.ALLY.g},"Ally-PvP-US",0x514,0x0,${H.FOE2.g},"Foe2-PvP-US",` +
      `0x548,0x0,1,"Buff",0x1,BUFF`,
  ]);
  const [b] = e.flush();
  const ally = find(b, H.ALLY.g);
  assert.equal(ally.fixes.length, 1);
  assert.equal(ally.fixes[0][4], 'approx');
  assert.equal(ally.fixes[0][2], 50);
  assert.equal(ally.posTime, undefined);
  assert.equal(ally.x, undefined);
});

test('past names recorded when a GUID changes name; display uses suffix', () => {
  const e = eng();
  const a = H.player('00000ABC', 'Old-PvP-US', H.HOSTILE),
    b = H.player('00000ABC', 'New-PvP-US', H.HOSTILE);
  feed(e, [H.dmg(1, a, H.ALLY), H.dmg(2, b, H.ALLY)]);
  const p = find(e.flush()[0], a.g);
  assert.ok(p.pastNames.includes('Old-PvP-US'));
  assert.equal(p.name, 'New PvP US');
});

test('watermark gating, flush advances watermark, second flush is empty', () => {
  const e = new T.Engine({ clock: () => H.T0 });
  e.begin({ file: 'x', wm: { pvp: H.T0 + 2000 } });
  feed(e, [H.dmg(1, H.FOE, H.ALLY), H.dmg(2, H.FOE, H.ALLY), H.dmg(3, H.FOE, H.ALLY)]);
  const out = e.flush();
  assert.equal(out.length, 1);
  assert.equal(out[0].from, H.T0 + 2000);
  assert.equal(out[0].to, H.T0 + 3000);
  assert.equal(find(out[0], H.FOE.g).d.events, 1);
  assert.deepEqual(e.flush(), []);
  feed(e, [H.dmg(3, H.FOE, H.ALLY), H.dmg(4, H.FOE, H.ALLY)]); // 3 s already flushed
  const o2 = e.flush();
  assert.equal(find(o2[0], H.FOE.g).d.events, 1);
  assert.equal(o2[0].from, H.T0 + 3000);
});

test('feedText handles lines split across chunks and a final unterminated line', () => {
  const e = eng();
  const l1 = H.dmg(1, H.FOE, H.ALLY),
    l2 = H.dmg(2, H.FOE, H.ALLY);
  const all = l1 + '\n' + l2;
  e.feedText(all.slice(0, 50), false);
  e.feedText(all.slice(50, l1.length + 10), false);
  e.feedText(all.slice(l1.length + 10), true);
  assert.equal(find(e.flush()[0], H.FOE.g).d.events, 2);
});

test('lines without Player- guids are skipped, garbage counted as bad', () => {
  const e = eng();
  feed(e, ['junk', `${H.ts(1)}  SPELL_DAMAGE,Creature-0-1,"Boar",0xa48,0x0,Creature-0-2,"Wolf",0xa48,0x0,1,"x",0x1`]);
  assert.equal(e.bad, 1);
  assert.deepEqual(e.flush(), []);
});

// ---- bounty ----
const kbOf = (b, g) => (find(b, g) || { d: { kb: 0, assists: 0 } }).d;

test('bounty: killing blow = last hit within 5 s, other hitters within 15 s assist', () => {
  const e = eng();
  feed(e, [
    H.dmg(0, H.FOE2, H.ALLY), // 10 s before death -> assist
    H.dmg(8, H.FOE, H.ALLY), // 2 s before -> killing blow
    H.died(10, H.ALLY),
  ]);
  const [b] = e.flush();
  assert.equal(kbOf(b, H.FOE.g).kb, 1);
  assert.equal(kbOf(b, H.FOE.g).assists, 0);
  assert.equal(kbOf(b, H.FOE2.g).kb, 0);
  assert.equal(kbOf(b, H.FOE2.g).assists, 1);
});

test('bounty: last hit older than 5 s gives no killing blow (only assists); hits older than 15 s ignored', () => {
  const e = eng();
  feed(e, [H.dmg(0, H.FOE2, H.ALLY), H.dmg(20, H.FOE, H.ALLY), H.died(26, H.ALLY)]);
  const [b] = e.flush();
  assert.equal(kbOf(b, H.FOE.g).kb, 0);
  assert.equal(kbOf(b, H.FOE.g).assists, 1);
  assert.equal(kbOf(b, H.FOE2.g).assists, 0);
});

test('bounty: PARTY_KILL names the killer; UNIT_DIED+PARTY_KILL within 3 s are one death', () => {
  const e = eng();
  feed(e, [H.dmg(1, H.FOE2, H.ALLY), H.partyKill(2, H.FOE, H.ALLY), H.died(3, H.ALLY), H.died(4.5, H.ALLY)]);
  const [b] = e.flush();
  assert.equal(kbOf(b, H.FOE.g).kb, 1);
  assert.equal(kbOf(b, H.FOE2.g).assists, 1);
  assert.equal(kbOf(b, H.FOE.g).assists, 0);
});

test('bounty: a second death more than 3 s later counts again', () => {
  const e = eng();
  feed(e, [H.dmg(1, H.FOE, H.ALLY), H.died(2, H.ALLY), H.dmg(10, H.FOE, H.ALLY), H.died(11, H.ALLY)]);
  assert.equal(kbOf(e.flush()[0], H.FOE.g).kb, 2);
});

test('bounty: feign death, friendly fire, NPC and environment kills give no credit', () => {
  const e = eng();
  const npc = { g: 'Creature-0-9', n: 'Boar', f: 0xa48 };
  feed(e, [
    H.dmg(1, H.FOE, H.ALLY),
    H.died(2, H.ALLY, '1'), // feign
    H.dmg(20, H.ME, H.ALLY),
    H.died(21, H.ALLY), // friendly player hit
    H.dmg(40, npc, H.ALLY),
    H.died(41, H.ALLY), // npc
    H.dmg(60, H.FOE, npc),
    H.died(61, npc), // victim not a friendly player
  ]);
  const [b] = e.flush();
  assert.equal(kbOf(b, H.FOE.g).kb, 0);
  assert.equal(kbOf(b, H.FOE.g).assists, 0);
});

test('bounty: pets credit their hostile owner (via info owner field and SPELL_SUMMON)', () => {
  const e = eng();
  const pet = { g: 'Pet-0-1-2-3-4-5', n: 'Wolf', f: 0xa48 };
  feed(e, [
    `${H.ts(0)}  SPELL_SUMMON,${H.FOE.g},"Foe-PvP-US",0x548,0x0,${pet.g},"Wolf",0xa48,0x0,1,"Call",0x1`,
    H.dmg(5, pet, H.ALLY),
    H.died(6, H.ALLY),
  ]);
  assert.equal(kbOf(e.flush()[0], H.FOE.g).kb, 1);
  const e2 = eng();
  feed(e2, [H.dmg(1, H.FOE, pet, { infoOf: 'dst', owner: H.FOE.g }), H.dmg(5, pet, H.ALLY), H.died(6, H.ALLY)]);
  assert.equal(kbOf(e2.flush()[0], H.FOE.g).kb, 1);
});

test('bounty value: killing blows x10, assists worth 0 (ranking in contacts.js)', () => {
  const C = require('../../site/contacts.js');
  const r = C.rankBounty([
    { guid: 'a', name: 'A', kb: 1, assists: 9, score: 10 },
    { guid: 'b', name: 'B', kb: 3, assists: 0, score: 30 },
    { guid: 'c', name: 'C', kb: 0, assists: 5, score: 0 },
    { guid: 'd', name: 'D', kb: 1, assists: 10, score: 10 },
  ]);
  assert.deepEqual(
    r.rows.map(x => x.guid),
    ['b', 'd', 'a'],
  );
  assert.equal(r.by.b.rank, 1);
  assert.equal(r.by.a.rank, 3);
  assert.ok(!r.by.c);
  assert.equal(C.bountyValue(3, 7), 30);
});

test('bounty events at or before the watermark are not credited', () => {
  const e = new T.Engine({ clock: () => H.T0 });
  e.begin({ file: 'x', wm: { pvp: H.T0 + 2000 } });
  feed(e, [H.dmg(1, H.FOE, H.ALLY), H.died(2, H.ALLY)]);
  assert.deepEqual(e.flush(), []);
});

// ---- rule-sets ----
test('rule-set auto-detects from my realm token and splits buckets', () => {
  const e = eng();
  const me = H.player('0000000A', 'Hero-RP-PvP-US', H.MINE);
  feed(e, [
    H.dmg(1, H.FOE, H.ALLY, { x: 1, y: 1 }),
    H.dmg(2, me, H.FOE, { infoOf: 'src', x: 5, y: 5 }),
    H.dmg(3, H.FOE, H.ALLY, { x: 2, y: 2 }),
  ]);
  assert.equal(e.activeRs(), 'rppvp');
  const out = e.flush();
  const rs = out.map(b => b.rs).sort();
  assert.deepEqual(rs, ['pvp', 'rppvp']); // first event before detection lands in the default set
  assert.equal(e.rulesetInfo().detected, 'rppvp');
});

test('rule-set manual overrides detection; auto falls back to manual then default', () => {
  const e = eng();
  assert.equal(e.activeRs(), 'pvp');
  e.setConfig({ mode: 'auto', manual: 'rp' });
  assert.equal(e.activeRs(), 'rp');
  feed(e, [H.dmg(1, H.player('0000000A', 'Hero-Normal-EU', H.MINE), H.FOE, { infoOf: 'src' })]);
  assert.equal(e.activeRs(), 'normal');
  e.setConfig({ mode: 'manual', manual: 'rppvp' });
  assert.equal(e.activeRs(), 'rppvp');
  assert.equal(e.rulesetInfo().mode, 'manual');
  feed(e, [H.dmg(2, H.FOE, H.ALLY)]);
  assert.ok(e.flush().some(b => b.rs === 'rppvp'));
});

test('rule-set hint from shared realm suffix while me is unknown', () => {
  const e = eng();
  const lines = [];
  for (let i = 0; i < 7; i++)
    lines.push(
      H.dmg(
        i,
        H.player('000020' + i, 'P' + i + '-RP-US', H.HOSTILE),
        H.player('000030' + i, 'Q' + i + '-RP-US', H.FRIEND),
        { x: i, y: i },
      ),
    );
  feed(e, lines);
  assert.equal(e.activeRs(), 'rp');
});

test('flush includes maps and omits position for players never located', () => {
  const e = eng();
  feed(e, [
    H.mapChange(0, 1429, 'Elwynn Forest', 100, -100, 50, -50),
    `${H.ts(5)}  SPELL_AURA_APPLIED,${H.FOE.g},"Foe-PvP-US",0x548,0x0,${H.ALLY.g},"Ally-PvP-US",` +
      `0x514,0x0,1,"Buff",0x1,BUFF`,
  ]);
  const [b] = e.flush();
  assert.equal(b.maps[0].name, 'Elwynn Forest');
  assert.equal(find(b, H.FOE.g).x, undefined);
  assert.deepEqual(find(b, H.FOE.g).fixes, []);
});

test('synthetic end-to-end log: snapshot shape and flush', () => {
  const e = new T.Engine({ clock: () => H.T0 + 30000 });
  e.begin({ file: 'full.txt', wm: {} });
  const lines = [
    `${H.ts(0)}  COMBAT_LOG_VERSION,20,ADVANCED_LOG_ENABLED,1`,
    H.zoneChange(0, 'Elwynn Forest'),
    H.mapChange(0, 1429, 'Elwynn Forest', 100, -100, 50, -50),
  ];
  for (let i = 0; i < 100; i++)
    lines.push(
      H.dmg(i * 0.2, i % 2 ? H.FOE : H.ME, i % 2 ? H.ME : H.FOE, { infoOf: i % 2 ? 'dst' : 'src', x: i, y: -i }),
    );
  feed(e, lines);
  const s = e.snapshot();
  assert.equal(s.zone, 'Elwynn Forest');
  assert.equal(s.advanced, true);
  assert.ok(s.me);
  assert.equal(s.ruleset.active, 'pvp');
  assert.equal(s.pips.length, 2);
  assert.equal(s.stats.events, 103);
  assert.ok(s.me.est);
  assert.equal(e.pending().players, 2);
});
