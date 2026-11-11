'use strict';
// ping.js and tail.js: whole-file upload, ledger resume, retries, and live tailing of the newest log.
const test = require('node:test');
const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const T = Object.assign(
  {},
  ...['tracker', 'analyzer', 'engine', 'engine-protocol'].map(m => require('../../site/' + m + '.js')),
);
const C = Object.assign(
  {},
  ...['format', 'charts', 'contacts', 'http', 'ledger', 'console-settings'].map(m =>
    require('../../site/' + m + '.js'),
  ),
);
const I = require('../../site/ping.js');
const EC = require('../../site/engine-client.js');
const TL = require('../../site/tail.js');
const H = require('./helpers.js');

const sha256 = async u8 => crypto.createHash('sha256').update(u8).digest('hex');
const mkFile = (name, text) => {
  const b = new Blob([text]);
  return {
    name,
    get size() {
      return b.size;
    },
    slice: (a, z) => b.slice(a, z),
  };
};
const newClient = () => new EC.LocalClient(T.createHandler(new TextDecoder()));

// Minimal in-memory cloud implementing the API v2 contract parts used by the client.
function fakeCloud() {
  const ledger = {},
    players = {},
    lb = {},
    reqs = [];
  let failAt = -1,
    n = 0;
  const api = async (path, opt) => {
    if (path.startsWith('/api/ledger')) {
      const u = new URL('http://x' + path);
      return { lastTs: ledger[u.searchParams.get('rs') + '|' + u.searchParams.get('fp')] || 0 };
    }
    if (path === '/api/upload') {
      const b = JSON.parse(opt.body);
      n++;
      if (n === failAt) throw new Error('network down');
      reqs.push(b);
      assert.ok(b.players.length <= 500 && b.batchId && b.rs && b.fp);
      let changed = 0;
      for (const p of b.players) {
        const k = b.rs + '|' + p.guid;
        if (lb[k] === b.batchId) continue; // retried batch: skip per player
        lb[k] = b.batchId;
        const e = players[k] || (players[k] = { events: 0, kb: 0, assists: 0, dmgTotal: 0 });
        e.events += p.d.events;
        e.kb += p.d.kb;
        e.assists += p.d.assists;
        e.dmgTotal += p.d.dmgTotal;
        changed++;
      }
      if (b.final) ledger[b.rs + '|' + b.fp] = Math.max(ledger[b.rs + '|' + b.fp] || 0, b.to);
      return { received: b.players.length, changed };
    }
    throw new Error('unexpected ' + path);
  };
  return {
    api,
    ledger,
    players,
    reqs,
    failOn: i => {
      failAt = i;
      n = 0;
    },
  };
}

function bigLog(nPlayers) {
  const lines = ['x'.repeat(100 * 700), H.mapChange(0, 1429, 'Elwynn Forest', 100, -100, 50, -50)];
  const foes = Array.from({ length: nPlayers }, (_, i) =>
    H.player(String(i).padStart(8, '0') + 'F', 'Foe' + i + '-PvP-US', H.HOSTILE),
  );
  foes.forEach((f, i) => lines.push(H.dmg(1 + i * 0.01, f, H.ALLY, { amount: 100, x: i % 90, y: i % 40 })));
  lines.push(H.dmg(2000, H.FOE, H.ALLY), H.died(2001, H.ALLY));
  return lines.join('\n') + '\n';
}

test('ingestFile: parses, uploads in <=500 player requests, final flag last, server counts exact', async () => {
  const cloud = fakeCloud(),
    outbox = new C.Outbox();
  const f = mkFile('WoWCombatLog_1.txt', bigLog(1200));
  const r = await I.ingestFile(f, { client: newClient(), api: cloud.api, sha256, outbox }, {});
  assert.equal(cloud.reqs.filter(q => q.rs === 'pvp').length, 3); // 1202 players -> 500,500,202
  assert.ok(cloud.reqs.every(q => q.players.length <= 500));
  assert.deepEqual(
    cloud.reqs.map(q => q.final),
    [false, false, true],
  );
  assert.equal(new Set(cloud.reqs.map(q => q.batchId)).size, 1);
  assert.equal(cloud.players['pvp|Player-1-0000000C'].kb, 1);
  assert.equal(cloud.players['pvp|Player-1-0000000B'].events, 1202);
  assert.equal(r.received, 1202);
  assert.ok(cloud.reqs.every(q => q.players.length <= 500));
  assert.equal(cloud.ledger['pvp|' + r.fp], H.T0 + 2001000);
});

test('re-ingesting the same file sends nothing (ledger watermark); appended data sends only the delta', async () => {
  const cloud = fakeCloud();
  const text = bigLog(10);
  const run = t =>
    I.ingestFile(
      mkFile('WoWCombatLog_1.txt', t),
      { client: newClient(), api: cloud.api, sha256, outbox: new C.Outbox() },
      {},
    );
  await run(text);
  const n1 = cloud.reqs.length,
    ev = cloud.players['pvp|Player-1-0000000B'].events;
  const r2 = await run(text);
  assert.equal(cloud.reqs.length, n1);
  assert.equal(r2.received, 0);
  await run(text + H.dmg(3000, H.FOE, H.ALLY) + '\n');
  assert.equal(cloud.players['pvp|Player-1-0000000B'].events, ev + 1);
});

test('a failed request is retried with the same batchId and nothing is double counted', async () => {
  const cloud = fakeCloud(),
    outbox = new C.Outbox();
  const deps = { client: newClient(), api: cloud.api, sha256, outbox };
  const f = mkFile('WoWCombatLog_2.txt', bigLog(700));
  cloud.failOn(2); // second request fails
  await assert.rejects(I.ingestFile(f, deps, {}), /network down/);
  assert.equal(outbox.length, 1);
  const firstId = cloud.reqs[0].batchId;
  await outbox.drain(cloud.api);
  assert.ok(cloud.reqs.every(q => q.batchId === firstId));
  assert.equal(cloud.players['pvp|Player-1-0000000B'].events, 702);
  assert.equal(cloud.reqs[cloud.reqs.length - 1].final, true);
});

test('rule-set buckets are uploaded separately with their own rs and watermarks', async () => {
  const cloud = fakeCloud();
  const me = H.player('0000000A', 'Hero-Normal-EU', H.MINE);
  const text =
    [H.dmg(1, H.FOE, H.ALLY), H.dmg(2, me, H.FOE, { infoOf: 'src' }), H.dmg(3, H.FOE, H.ALLY)].join('\n') + '\n';
  await I.ingestFile(
    mkFile('a.txt', text),
    { client: newClient(), api: cloud.api, sha256, outbox: new C.Outbox() },
    {},
  );
  assert.deepEqual(cloud.reqs.map(q => q.rs).sort(), ['normal', 'pvp']);
  assert.equal(Object.keys(cloud.ledger).length, 2);
});

// ---- live tailing ----
function fakeDir() {
  const files = new Map();
  let mtime = 1;
  const api = {
    set(name, text) {
      files.set(name, { text, lastModified: mtime++ });
    },
    append(name, text) {
      const f = files.get(name);
      f.text += text;
      f.lastModified = mtime++;
    },
    list: async () =>
      Array.from(files, ([name, f]) => ({
        name,
        lastModified: f.lastModified,
        getFile: async () => mkFile(name, f.text),
      })),
  };
  return api;
}
// junk lines with no Player-: skipped but count toward the 64 KB head
const pad = n => ('x'.repeat(100) + '\n').repeat(n);

test('live: tails appended bytes, uploads on the interval, final upload on stop', async () => {
  const cloud = fakeCloud(),
    dir = fakeDir(),
    client = newClient();
  let now = 1000000;
  const status = [];
  const live = new TL.LiveSession({
    client,
    api: cloud.api,
    sha256,
    outbox: new C.Outbox(),
    listLogs: dir.list,
    status: t => status.push(t),
    now: () => now,
    intervalSec: () => 300,
  });
  const head = pad(700); // > 64 KB so the fingerprint is stable
  dir.set('WoWCombatLog_a.txt', head + H.dmg(1, H.FOE, H.ALLY) + '\n');
  await live.sweep();
  assert.equal(cloud.reqs.length, 0); // not due yet
  assert.equal((await client.call({ type: 'pending' })).players, 2);
  const snap = (await client.call({ type: 'snap' })).snap;
  assert.equal(snap.pips.length, 2);
  // partial line at the live edge is held back until completed
  const line = H.dmg(2, H.FOE, H.ALLY);
  dir.append('WoWCombatLog_a.txt', line.slice(0, 40));
  await live.sweep();
  assert.equal((await client.call({ type: 'snap' })).snap.stats.events, 1);
  dir.append('WoWCombatLog_a.txt', line.slice(40) + '\n');
  now += 6 * 60000;
  await live.sweep();
  assert.equal(cloud.reqs.length, 1);
  assert.equal(cloud.players['pvp|Player-1-0000000C'].events, 2);
  // more data, then stop -> final upload carries only the delta
  dir.append('WoWCombatLog_a.txt', H.dmg(5, H.FOE, H.ALLY) + '\n');
  await live.sweep();
  await live.stop();
  assert.equal(cloud.players['pvp|Player-1-0000000C'].events, 3);
  assert.equal(cloud.reqs.length, 2);
  assert.equal(cloud.reqs[1].final, true);
  assert.equal(cloud.reqs[1].from, H.T0 + 2000);
});

test('live: switching to a newer log uploads the old one first; files under 64 KB defer the upload', async () => {
  const cloud = fakeCloud(),
    dir = fakeDir(),
    client = newClient();
  let now = 0;
  const live = new TL.LiveSession({
    client,
    api: cloud.api,
    sha256,
    outbox: new C.Outbox(),
    listLogs: dir.list,
    now: () => now,
    intervalSec: () => 300,
  });
  dir.set('WoWCombatLog_a.txt', pad(700) + H.dmg(1, H.FOE, H.ALLY) + '\n');
  await live.sweep();
  dir.set('WoWCombatLog_b.txt', H.dmg(10, H.FOE2, H.ALLY) + '\n'); // tiny new file
  await live.sweep();
  assert.equal(cloud.players['pvp|Player-1-0000000C'].events, 1); // old file flushed on rotation
  now += 6 * 60000;
  await live.sweep();
  assert.ok(!cloud.players['pvp|Player-1-0000000D']); // b is < 64 KB: fingerprint unstable, upload deferred
  await live.stop(); // stop computes the fingerprint from what exists
  assert.equal(cloud.players['pvp|Player-1-0000000D'].events, 1);
});

test('live: upload failure keeps the batch and retries on the next due sweep', async () => {
  const cloud = fakeCloud(),
    dir = fakeDir(),
    client = newClient(),
    outbox = new C.Outbox();
  let now = 0;
  const live = new TL.LiveSession({
    client,
    api: cloud.api,
    sha256,
    outbox,
    listLogs: dir.list,
    now: () => now,
    intervalSec: () => 60,
  });
  dir.set('WoWCombatLog_a.txt', pad(700) + H.dmg(1, H.FOE, H.ALLY) + '\n');
  await live.sweep();
  cloud.failOn(1);
  now += 61000;
  await live.sweep();
  assert.equal(outbox.length, 1);
  assert.match(live.lastError, /network down/);
  now += 61000;
  await live.sweep();
  assert.equal(outbox.length, 0);
  assert.equal(cloud.players['pvp|Player-1-0000000C'].events, 1);
});

test('live: no logs yet and truncated file are handled', async () => {
  const dir = fakeDir(),
    client = newClient(),
    msgs = [];
  const cloud = fakeCloud();
  const live = new TL.LiveSession({
    client,
    api: cloud.api,
    sha256,
    outbox: new C.Outbox(),
    listLogs: dir.list,
    status: t => msgs.push(t),
    now: () => 0,
    intervalSec: () => 300,
  });
  await live.sweep();
  assert.match(msgs.pop(), /No WoWCombatLog/);
  dir.set('WoWCombatLog_a.txt', pad(700) + H.dmg(1, H.FOE, H.ALLY) + '\n');
  await live.sweep();
  // rewritten, same size -> not detected; shrink instead
  dir.set('WoWCombatLog_a.txt', pad(700) + H.dmg(2, H.FOE, H.ALLY) + '\n');
  dir.set('WoWCombatLog_a.txt', H.dmg(3, H.FOE, H.ALLY) + '\n');
  await live.sweep();
  assert.equal((await client.call({ type: 'snap' })).snap.pips.length, 2);
});

test('ingestFile reports the span of the file as UTC instants (offset-less lines use the engine zone)', async () => {
  const cloud = fakeCloud();
  const client = newClient();
  await client.call({ type: 'init', logClock: true });
  await client.call({ type: 'config', mode: 'auto', timezone: 'Asia/Kolkata' });
  const text = [H.dmg(1, H.FOE, H.ALLY), H.dmg(90, H.FOE, H.ALLY)].map(l => l.replace('-5  ', '  ')).join('\n') + '\n';
  const f = mkFile('WoWCombatLog_span.txt', 'x'.repeat(70000) + '\n' + text);
  const r = await I.ingestFile(f, { client, api: cloud.api, sha256, outbox: new C.Outbox() }, {});
  const kolkata = Date.UTC(2026, 10, 11, 8, 30, 0); // 14:00:00 at +5:30
  assert.equal(r.firstT, kolkata + 1000);
  assert.equal(r.lastT, kolkata + 90000);
});
