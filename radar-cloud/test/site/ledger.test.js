'use strict';
// ledger.js: fingerprints, watermarks, request chunking, resumable batches, the outbox, and resume from a watermark.
const test = require('node:test');
const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const C = require('../../site/ledger.js');
const T = Object.assign(
  {},
  ...['tracker', 'analyzer', 'engine', 'engine-protocol'].map(m => require('../../site/' + m + '.js')),
);
const H = require('./helpers.js');

const res = (status, body) => ({
  status,
  ok: status >= 200 && status < 300,
  json: async () => {
    if (body === undefined) throw new Error('no json');
    return body;
  },
});
const sha256 = async u8 => crypto.createHash('sha256').update(u8).digest('hex');
const mkPlayers = (n, fixes) =>
  Array.from({ length: n }, (_, i) => ({
    guid: 'Player-' + i,
    name: 'P' + i,
    fixes: Array.from({ length: fixes || 0 }, () => [0, 1, 0, 0, 'exact']),
  }));

test('fingerprint depends on content and file name', async () => {
  const a = new Uint8Array([1, 2, 3]);
  const f1 = await C.fingerprint(a, 'WoWCombatLog_1.txt', sha256),
    f2 = await C.fingerprint(a, 'WoWCombatLog_2.txt', sha256),
    f3 = await C.fingerprint(new Uint8Array([1, 2, 4]), 'WoWCombatLog_1.txt', sha256);
  assert.equal(f1.length, 32);
  assert.notEqual(f1, f2);
  assert.notEqual(f1, f3);
  assert.equal(f1, await C.fingerprint(a, 'WoWCombatLog_1.txt', sha256));
});

test('loadLedger asks every rule-set and defaults to 0', async () => {
  const seen = [];
  const api = async path => {
    seen.push(path);
    return path.includes('rs=rp&') ? { lastTs: 1234 } : {};
  };
  const wm = await C.loadLedger(api, 'abc');
  assert.deepEqual(wm, { normal: 0, pvp: 0, rp: 1234, rppvp: 0 });
  assert.equal(seen.length, 4);
  assert.ok(seen.every(p => p.startsWith('/api/ledger?') && p.includes('fp=abc')));
});

test('chunkPlayers: <= 500 players and <= fix cap per request, never splits a player', () => {
  const ch = C.chunkPlayers(mkPlayers(1200));
  assert.deepEqual(
    ch.map(c => c.length),
    [500, 500, 200],
  );
  const byFix = C.chunkPlayers(mkPlayers(10, 30), 500, 100);
  assert.deepEqual(
    byFix.map(c => c.length),
    [3, 3, 3, 1],
  );
  assert.deepEqual(C.chunkPlayers([]), []);
  assert.equal(C.chunkPlayers(mkPlayers(1, 500), 500, 100).length, 1);
});

test('uploadBatch: sequential requests, final only on the last, maps once, same batchId, totals', async () => {
  const bodies = [];
  const api = async (p, o) => {
    const b = JSON.parse(o.body);
    bodies.push(b);
    return { received: b.players.length, changed: b.players.length - 1 };
  };
  const batch = { rs: 'rp', from: 5, to: 99, players: mkPlayers(1100), maps: [{ id: 1 }] };
  const prog = [];
  await C.uploadBatch(api, 'f'.repeat(32), batch, (n, t) => prog.push([n, t]));
  assert.equal(bodies.length, 3);
  assert.deepEqual(
    bodies.map(b => b.players.length),
    [500, 500, 100],
  );
  assert.deepEqual(
    bodies.map(b => b.final),
    [false, false, true],
  );
  assert.deepEqual(
    bodies.map(b => b.maps.length),
    [1, 0, 0],
  );
  assert.equal(new Set(bodies.map(b => b.batchId)).size, 1);
  assert.ok(bodies.every(b => b.rs === 'rp' && b.fp === 'f'.repeat(32) && b.from === 5 && b.to === 99));
  assert.equal(batch.received, 1100);
  assert.equal(batch.changed, 1097);
  assert.deepEqual(prog, [
    [1, 3],
    [2, 3],
    [3, 3],
  ]);
});

test('uploadBatch retry resumes at the failed request with the SAME batchId', async () => {
  const ids = [],
    sizes = [];
  let fail = true;
  const api = async (p, o) => {
    const b = JSON.parse(o.body);
    if (b.players.length === 500 && ids.length === 1 && fail) {
      fail = false;
      throw new Error('net');
    }
    ids.push(b.batchId);
    sizes.push(b.players.length);
    return { received: b.players.length, changed: 0 };
  };
  const batch = { rs: 'pvp', from: 0, to: 1, players: mkPlayers(1100), maps: [] };
  await assert.rejects(C.uploadBatch(api, 'fp12345678', batch), /net/);
  assert.equal(batch.next, 1);
  const id = batch.batchId;
  await C.uploadBatch(api, 'fp12345678', batch);
  assert.deepEqual(sizes, [500, 500, 100]);
  assert.ok(ids.every(x => x === id));
  assert.equal(batch.received, 1100);
});

test('Outbox keeps failed batches and resumes; completed ones are dropped', async () => {
  const ob = new C.Outbox();
  ob.add('fp', [
    { rs: 'pvp', from: 0, to: 1, players: mkPlayers(2), maps: [] },
    { rs: 'rp', from: 0, to: 2, players: mkPlayers(3), maps: [] },
  ]);
  let calls = 0;
  const api = async (p, o) => {
    calls++;
    if (calls === 2) throw new Error('down');
    const b = JSON.parse(o.body);
    return { received: b.players.length, changed: b.players.length };
  };
  await assert.rejects(ob.drain(api), /down/);
  assert.equal(ob.length, 1);
  assert.equal(ob.items[0].batch.rs, 'rp');
  const r = await ob.drain(api);
  assert.equal(r.received, 3);
  assert.equal(ob.length, 0);
});

test('resume from watermark: ledger -> engine skips old events -> only new data is uploaded', async () => {
  const T = Object.assign(
      {},
      ...['tracker', 'analyzer', 'engine', 'engine-protocol'].map(m => require('../../site/' + m + '.js')),
    ),
    H = require('./helpers.js');
  const lines = [H.dmg(1, H.FOE, H.ALLY), H.dmg(2, H.FOE, H.ALLY), H.dmg(3, H.FOE, H.ALLY)];
  const run = async ledgerTs => {
    const sent = [];
    const api = async (p, o) => {
      if (p.startsWith('/api/ledger')) return { lastTs: p.includes('rs=pvp') ? ledgerTs : 0 };
      const b = JSON.parse(o.body);
      sent.push(b);
      return { received: b.players.length, changed: b.players.length };
    };
    const wm = await C.loadLedger(api, 'fp');
    const e = new T.Engine({ clock: () => H.T0 });
    e.begin({ file: 'f', wm });
    e.feedText(lines.join('\n') + '\n', true);
    const ob = new C.Outbox();
    ob.add('fp', e.flush());
    await ob.drain(api);
    return sent;
  };
  const first = await run(0);
  assert.equal(first.length, 1);
  assert.equal(first[0].from, 0);
  assert.equal(first[0].to, H.T0 + 3000);
  assert.equal(first[0].players.find(p => p.guid === H.FOE.g).d.events, 3);
  const second = await run(H.T0 + 3000); // everything already ingested
  assert.equal(second.length, 0);
  const partial = await run(H.T0 + 1000);
  assert.equal(partial[0].players.find(p => p.guid === H.FOE.g).d.events, 2);
  assert.equal(partial[0].from, H.T0 + 1000);
});
