// Run: ELECTRON_RUN_AS_NODE=1 "<VS Code>/Code.exe" --test test/   (or `node --test test/`)
// Uses an in-memory DynamoDB stub (test/stub-lib.mjs); lambda/index.mjs is imported unchanged.
import { register } from 'node:module';
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createHash, createHmac } from 'node:crypto';
import { gunzipSync, gzipSync } from 'node:zlib';

process.env.TABLE = 'T'; process.env.MAX_ACCOUNTS_PER_KEY = '5';
register('./hooks.mjs', import.meta.url);
const stub = await import('./stub-lib.mjs');
const { handler } = await import('../lambda/index.mjs');

const call = async (method, path, { q, body, raw, token, gz, headers } = {}) => {
  const r = await handler({
    requestContext: { http: { method } }, rawPath: path, queryStringParameters: q && Object.fromEntries(Object.entries(q).map(([k, v]) => [k, String(v)])),
    headers: { ...(token && { authorization: 'Bearer ' + token }), ...(gz && { 'accept-encoding': 'gzip' }), ...headers },
    body: raw ?? (body !== undefined ? JSON.stringify(body) : undefined),
  });
  const text = r.isBase64Encoded ? gunzipSync(Buffer.from(r.body, 'base64')).toString() : r.body;
  return { code: r.statusCode, json: JSON.parse(text), gz: r.headers['content-encoding'] === 'gzip' };
};
const sha = s => createHash('sha256').update(s).digest('hex');
const signup = (u, k = 'goodkey-1234') => call('POST', '/api/signup', { body: { username: u, password: 'password-' + u, apiKey: k } });
const G = i => 'Player-1-' + String(i).padStart(8, '0');
const mkp = (i, extra = {}) => ({
  guid: G(i), name: 'Bob' + i, reaction: 'hostile', level: 60, x: 10.04, y: 20.06, mapId: 1, zone: 'Barrens', posTime: 1000, firstSeen: 1000, lastSeen: 2000,
  d: { events: 5, srcEvents: 2, dmgTotal: 100, dmgHits: 2, dmgEvents: 2, hpSum: 50, hpSamples: 1, maxHp: 900, kb: 0, assists: 0 }, ...extra,
});
const fx = (t, x, y, m = 1) => [t, m, x, y, 'exact'];

stub.seedKey(sha('goodkey-1234'), { active: true, cnt: 0 });
stub.seedKey(sha('deadkey-1234'), { active: false, cnt: 0 });
stub.seedKey(sha('racekey-1234'), { active: true, cnt: 0 });
stub.seedKey(sha('otherkey-1234'), { active: true, cnt: 0 });

let tok;
const up = (body, t = tok) => call('POST', '/api/upload', { token: t, body: { rs: 'normal', fp: 'fp1', from: 0, to: 5000, batchId: 'b1', final: false, ...body } });
const list = async (q = {}) => (await call('GET', '/api/players', { token: tok, q: { rs: 'normal', ...q } })).json;
const ledger = async (rs = 'normal') => (await call('GET', '/api/ledger', { token: tok, q: { rs, fp: 'fp1' } })).json.lastTs;
const trackOf = async (g, extra = {}) => (await call('GET', '/api/track', { token: tok, q: { rs: 'normal', guid: g, ...extra } })).json;

test('signup: 5-account cap, inactive key, taken name, validation', async () => {
  for (let i = 1; i <= 5; i++) {
    const r = await signup('user' + i);
    assert.equal(r.code, 200);
    if (i === 1) tok = r.json.token;
  }
  let r = await signup('user6');
  assert.equal(r.code, 403); assert.match(r.json.error, /already has 5/);
  r = await signup('ghost', 'deadkey-1234');
  assert.equal(r.code, 403); assert.match(r.json.error, /inactive/i);
  r = await signup('ghost', 'unknown-key-1');
  assert.equal(r.code, 403);
  assert.equal((await signup('user1', 'otherkey-1234')).code, 409);
  assert.equal((await call('POST', '/api/signup', { body: { username: 'a', password: 'x', apiKey: 'y' } })).code, 400);
});

test('signup: concurrent signups never exceed the cap', async () => {
  const rs = await Promise.all(Array.from({ length: 9 }, (_, i) => signup('race' + i, 'racekey-1234')));
  assert.equal(rs.filter(r => r.code === 200).length, 5);
  assert.equal(rs.filter(r => r.code === 403).length, 4);
});

test('login: ok, wrong password, wrong api key, inactive key, missing fields', async () => {
  const ok = await call('POST', '/api/login', { body: { username: 'user1', password: 'password-user1', apiKey: 'goodkey-1234' } });
  assert.equal(ok.code, 200); assert.ok(ok.json.token);
  assert.equal((await call('POST', '/api/login', { body: { username: 'user1', password: 'nope-nope', apiKey: 'goodkey-1234' } })).code, 401);
  assert.equal((await call('POST', '/api/login', { body: { username: 'user1', password: 'password-user1', apiKey: 'otherkey-1234' } })).code, 401);
  assert.equal((await call('POST', '/api/login', { body: { username: 'nobody', password: 'password-user1', apiKey: 'goodkey-1234' } })).code, 401);
  assert.equal((await call('POST', '/api/login', { body: { username: 'user1' } })).code, 400);
  // deactivate the key after signup: login must be refused
  stub.seedKey(sha('goodkey-1234'), { active: false, cnt: 5 });
  assert.equal((await call('POST', '/api/login', { body: { username: 'user1', password: 'password-user1', apiKey: 'goodkey-1234' } })).code, 403);
  stub.seedKey(sha('goodkey-1234'), { active: true, cnt: 5 });
});

test('auth and request errors: bad JSON, bad/expired token, oversize, bad gzip, 404', async () => {
  assert.equal((await call('GET', '/api/players')).code, 401);
  assert.equal((await call('GET', '/api/players', { token: 'garbage.token' })).code, 401);
  assert.equal((await call('GET', '/api/players', { token: tok.split('.')[0] + '.AAAA' })).code, 401);
  const secret = stub.tables.get('CFG').get('SECRET').v;
  const p = Buffer.from(JSON.stringify({ u: 'user1', e: Date.now() - 1000 })).toString('base64url');
  const expired = p + '.' + createHmac('sha256', secret).update(p).digest('base64url');
  assert.equal((await call('GET', '/api/me', { token: expired })).code, 401);
  assert.equal((await call('POST', '/api/upload', { token: tok, raw: '{nope' })).code, 400);
  assert.equal((await call('POST', '/api/login', { raw: '{nope' })).code, 400);
  assert.equal((await call('POST', '/api/upload', { token: tok, raw: ' '.repeat(6 * 1024 * 1024) })).code, 413);
  assert.equal((await call('POST', '/api/upload', { token: tok, raw: 'not gzip', headers: { 'content-encoding': 'gzip' } })).code, 400);
  assert.equal((await call('GET', '/api/nothing', { token: tok })).code, 404);
  assert.equal((await call('DELETE', '/api/players', { token: tok })).code, 404);
});

test('/api/me', async () => {
  const r = await call('GET', '/api/me', { token: tok });
  assert.equal(r.json.username, 'user1'); assert.deepEqual(r.json.rulesets, ['normal', 'pvp', 'rp', 'rppvp']);
});

test('settings: get/put, >8KB rejected, bad body, bad ruleset, default rs from settings', async () => {
  assert.deepEqual((await call('GET', '/api/settings', { token: tok })).json, {});
  assert.equal((await call('PUT', '/api/settings', { token: tok, body: { ruleset: 'pvp', cal: { 1: [1, 2] } } })).code, 200);
  assert.equal((await call('GET', '/api/settings', { token: tok })).json.ruleset, 'pvp');
  assert.equal((await call('PUT', '/api/settings', { token: tok, body: { big: 'x'.repeat(9000) } })).code, 413);
  assert.equal((await call('PUT', '/api/settings', { token: tok, body: { ruleset: 'pvp', v: 'x'.repeat(7000) } })).code, 200);
  assert.equal((await call('PUT', '/api/settings', { token: tok, body: [1] })).code, 400);
  assert.equal((await call('PUT', '/api/settings', { token: tok, body: { ruleset: 'bogus' } })).code, 400);
  // rs omitted -> settings.ruleset (pvp)
  await up({ rs: undefined, batchId: 'dflt', players: [mkp(90)] });
  assert.equal((await list({ rs: 'pvp' })).players.length, 1);
  assert.equal((await list({ rs: 'normal' })).players.length, 0);
  await call('PUT', '/api/settings', { token: tok, body: { ruleset: 'normal' } });
  await call('POST', '/api/reset', { token: tok, body: { rs: 'pvp', what: 'all' } });
});

test('upload: first upload, no-op re-upload, replayed batchId, additive deltas', async () => {
  const maps = [{ id: 1, name: 'Barrens', x0: 0, x1: 100, y0: 0, y1: 100 }];
  let r = await up({ players: [mkp(1), mkp(2)], maps });
  assert.deepEqual([r.json.received, r.json.changed], [2, 3]);
  const l = await list();
  assert.equal(l.maps.length, 1);
  const p1 = l.players.find(p => p.guid === G(1));
  assert.equal(p1.events, 5); assert.equal(p1.lastX, 10); assert.equal(p1.lastY, 20.1); assert.equal(p1.avgDamagePerHit, 50);

  stub.stats.writes = 0; // same data, no deltas, new batchId -> nothing written
  r = await up({ batchId: 'b2', players: [mkp(1, { d: undefined }), mkp(2, { d: {} })], maps });
  assert.equal(r.json.changed, 0); assert.equal(stub.stats.writes, 0);

  r = await up({ batchId: 'b1', players: [mkp(1), mkp(2)] }); // replay
  assert.equal(r.json.changed, 0);
  assert.equal((await list()).players.find(p => p.guid === G(1)).events, 5);

  await up({ batchId: 'b3', players: [mkp(1, { d: { events: 5, maxHp: 1200, hpSum: 10, hpSamples: 1 } })] });
  const q1 = (await list()).players.find(p => p.guid === G(1));
  assert.equal(q1.events, 10); assert.equal(q1.maxHp, 1200); assert.equal(q1.hpSum, 60); assert.equal(q1.dmgTotal, 100);
});

test('upload: newest wins for name/pos, firstSeen=min, pastNames, clamping', async () => {
  await up({ batchId: 'n1', players: [mkp(10, { name: 'Alpha', firstSeen: 500, lastSeen: 3000, posTime: 3000, x: 1, y: 1, d: {} })] });
  await up({ batchId: 'n2', players: [mkp(10, { name: 'Beta', firstSeen: 100, lastSeen: 4000, posTime: 4000, x: 2, y: 2, d: {} })] });
  await up({ batchId: 'n3', players: [mkp(10, { name: 'Stale', firstSeen: 900, lastSeen: 1500, posTime: 1500, x: 9, y: 9, d: {} })] });
  const p = (await list({ q: 'beta' })).players[0];
  assert.equal(p.name, 'Beta'); assert.equal(p.firstSeen, 100); assert.equal(p.lastSeen, 4000); assert.equal(p.lastX, 2);
  assert.deepEqual([...p.pastNames].sort(), ['Alpha', 'Stale']);
  await up({ batchId: 'c1', players: [mkp(11, { level: 99999, d: { events: -5, kb: 1e12, dmgTotal: 'x' } })] });
  const c = (await list()).players.find(x => p && x.guid === G(11));
  assert.equal(c.level, 200); assert.equal(c.events, 0); assert.equal(c.kb, 1e6); assert.equal(c.dmgTotal, 0);
  const bad = await up({ batchId: 'c2', players: [{ guid: 'nope' }, null, mkp(12)] });
  assert.equal(bad.json.received, 1);
});

test('ledger watermark: only on final, never decreases, per rs, validation', async () => {
  assert.equal(await ledger(), 0);
  await up({ batchId: 'l1', players: [], final: false, to: 7000 });
  assert.equal(await ledger(), 0);
  await up({ batchId: 'l2', players: [], final: true, to: 7000 });
  assert.equal(await ledger(), 7000);
  await up({ batchId: 'l3', players: [], final: true, to: 6000 });
  assert.equal(await ledger(), 7000);
  assert.equal(await ledger('pvp'), 0);
  assert.equal((await call('GET', '/api/ledger', { token: tok, q: { rs: 'normal' } })).code, 400);
  assert.equal((await call('GET', '/api/ledger', { token: tok, q: { rs: 'x', fp: 'a' } })).code, 400);
  assert.equal((await up({ batchId: 'l4', players: [], final: true, to: undefined })).code, 400);
});

test('rs validation and isolation; upload validation', async () => {
  for (const [m, p, extra] of [['GET', '/api/players', {}], ['GET', '/api/bounty', {}], ['GET', '/api/track', { guid: G(1) }], ['GET', '/api/bounty/player', { guid: G(1) }]])
    assert.equal((await call(m, p, { token: tok, q: { rs: 'zzz', ...extra } })).code, 400, p);
  assert.equal((await up({ rs: 'zzz', players: [] })).code, 400);
  assert.equal((await call('POST', '/api/reset', { token: tok, body: { rs: 'zzz', what: 'all' } })).code, 400);
  assert.equal((await list({ rs: 'rppvp' })).players.length, 0);
  assert.equal((await call('POST', '/api/upload', { token: tok, body: { rs: 'normal', players: [] } })).code, 400); // no batchId
  assert.equal((await call('POST', '/api/upload', { token: tok, body: { rs: 'normal', batchId: 'x' } })).code, 400);
  assert.equal((await up({ batchId: 'big', players: Array.from({ length: 501 }, (_, i) => mkp(i)) })).code, 400);
  assert.equal((await up({ batchId: 'bad id!', players: [] })).code, 400);
  assert.equal((await call('GET', '/api/players', { token: tok, q: { rs: 'normal', mapId: 'abc' } })).code, 400);
  assert.equal((await call('GET', '/api/players', { token: tok, q: { rs: 'normal', reaction: 'zzz' } })).code, 400);
  assert.equal((await call('GET', '/api/track', { token: tok, q: { rs: 'normal' } })).code, 400);
});

test('filters q / mapId / reaction incl. pastNames', async () => {
  await up({ batchId: 'f1', players: [mkp(20, { name: 'Zedd', pastNames: ['Oldname'], reaction: 'friendly', mapId: 2, d: {} })] });
  assert.deepEqual((await list({ q: 'oLdNa' })).players.map(p => p.guid), [G(20)]);
  assert.deepEqual((await list({ q: 'ZEDD' })).players.map(p => p.guid), [G(20)]);
  assert.equal((await list({ q: 'nomatch' })).players.length, 0);
  assert.deepEqual((await list({ mapId: 2 })).players.map(p => p.guid), [G(20)]);
  assert.deepEqual((await list({ reaction: 'friendly' })).players.map(p => p.guid), [G(20)]);
  assert.deepEqual((await list({ reaction: 'friendly', mapId: 1 })).players, []);
});

test('tracks: dedupe 2yd/5s, junk dropped, chunking, older fixes ignored, map filter, gzip', async () => {
  const f1 = Array.from({ length: 700 }, (_, i) => fx(100000 + i * 10000, i * 5, 0));
  const near = [fx(101000, 0.5, 0), fx(102000, 1.0, 0)]; // <2yd and <5s from fix 0
  await up({ batchId: 't1', players: [mkp(30, { d: {}, fixes: [...f1, ...near, ['x'], [1, 2, NaN, 4]] })] });
  let tr = await trackOf(G(30));
  assert.equal(tr.fixes.length, 700); assert.equal(tr.total, 700);
  assert.deepEqual(tr.fixes[0], { t: 100000, mapId: 1, zone: 'Barrens', x: 0, y: 0, kind: 'exact' });
  const keys = [...stub.tables.get('T#user1#normal').keys()].filter(k => k.startsWith(G(30)));
  assert.deepEqual(keys, [1, 2, 3].map(n => G(30) + '#00000' + n));
  assert.equal(stub.tables.get('T#user1#normal').get(keys[0]).f.length, 300);
  assert.equal((await list()).players.find(p => p.guid === G(30)).fixes, 700);

  await up({ batchId: 't2', players: [mkp(30, { d: {}, fixes: f1.slice(600) })] });
  assert.equal((await trackOf(G(30))).fixes.length, 700);

  // approx kind + rounding + other map; dedupe never crosses maps
  await up({ batchId: 't2b', players: [mkp(30, { d: {}, fixes: [[1e9, 9, 3.04, 4.06, 'approx'], [1e9 + 100, 8, 3.04, 4.06, 'exact']] })] });
  tr = await trackOf(G(30), { map: 9 });
  assert.deepEqual(tr.fixes.map(f => [f.mapId, f.x, f.y, f.kind]), [[9, 3, 4.1, 'approx']]);
  assert.deepEqual(tr.maps.map(m => m.mapId), [1, 8, 9]); assert.equal(tr.total, 702);
  assert.equal((await trackOf(G(31))).fixes.length, 0);

  const big = await call('GET', '/api/track', { token: tok, q: { rs: 'normal', guid: G(30) }, gz: true });
  assert.ok(big.gz); assert.equal(big.json.fixes.length, 702);
  const small = await call('GET', '/api/me', { token: tok, gz: true });
  assert.equal(small.gz, false);
  assert.ok((await call('GET', '/api/players', { token: tok, q: { rs: 'normal' }, gz: true })).gz);
});

test('tracks: 5000 cap deletes oldest chunks, newest kept', async () => {
  const all = Array.from({ length: 5200 }, (_, i) => fx(2e9 + i * 10000, i * 5, 7, 3));
  for (let k = 0; k < 4; k++) await up({ batchId: 'cap' + k, players: [mkp(40, { d: {}, fixes: all.slice(k * 1300, k * 1300 + 1300) })] });
  const p = (await list()).players.find(x => x.guid === G(40));
  const tr = await trackOf(G(40));
  assert.ok(p.fixes <= 5000 && p.fixes >= 4700, 'fixes=' + p.fixes);
  assert.equal(tr.total, p.fixes);
  assert.equal(tr.fixes.at(-1).t, 2e9 + 5199 * 10000);
  assert.equal(tr.fixes[0].t, 2e9 + (5200 - p.fixes) * 10000); // whole oldest chunks removed
  const keys = [...stub.tables.get('T#user1#normal').keys()].filter(k => k.startsWith(G(40)));
  assert.ok(keys[0] > G(40) + '#000001'); // oldest chunk(s) gone
  assert.equal(keys.length, Math.ceil(p.fixes / 300));
});

test('bounty ranking, player rank, 404, reset bounty/all', async () => {
  await up({ batchId: 'k1', players: [
    mkp(50, { d: { kb: 3, assists: 1 }, lastSeen: 100 }), mkp(51, { d: { kb: 3, assists: 4 }, lastSeen: 50 }),
    mkp(52, { d: { kb: 1 } }), mkp(53, { d: { kb: 3, assists: 4 }, lastSeen: 90 }), mkp(54, { d: { assists: 2 } })] });
  const bo = (await call('GET', '/api/bounty', { token: tok, q: { rs: 'normal' } })).json.entries.filter(e => e.guid !== G(11));
  assert.deepEqual(bo.map(e => e.guid), [G(53), G(51), G(50), G(52), G(54)]);
  assert.deepEqual(bo.map(e => e.score), [30, 30, 30, 10, 0]);
  assert.deepEqual(bo.map(e => e.rank).slice(0, 5), [2, 3, 4, 5, 6]); // rank 1 = G(11) from the clamp test
  const bp = (await call('GET', '/api/bounty/player', { token: tok, q: { rs: 'normal', guid: G(50) } })).json;
  assert.equal(bp.rank, 4); assert.equal(bp.kb, 3); assert.equal(bp.assists, 1);
  assert.equal((await call('GET', '/api/bounty/player', { token: tok, q: { rs: 'normal', guid: G(1) } })).code, 404);
  assert.equal((await call('GET', '/api/bounty/player', { token: tok, q: { rs: 'normal', guid: 'bad' } })).code, 400);

  await up({ batchId: 'k2', players: [], final: true, to: 9000 });
  assert.equal(await ledger(), 9000);
  assert.equal((await call('POST', '/api/reset', { token: tok, body: { rs: 'normal', what: 'nope' } })).code, 400);
  assert.equal((await call('POST', '/api/reset', { token: tok, body: { rs: 'normal', what: 'bounty' } })).code, 200);
  assert.equal((await call('GET', '/api/bounty', { token: tok, q: { rs: 'normal' } })).json.entries.length, 0);
  assert.equal(await ledger(), 0);
  assert.ok((await list()).players.find(p => p.guid === G(1)).events > 0); // other data kept

  assert.equal((await call('POST', '/api/reset', { token: tok, body: { rs: 'normal', what: 'all' } })).code, 200);
  assert.equal((await list()).players.length, 0);
  assert.equal((await list()).maps.length, 1); // map calibration kept
  assert.equal(stub.tables.get('T#user1#normal').size, 0);
});

test('users are isolated', async () => {
  const t2 = (await call('POST', '/api/login', { body: { username: 'user2', password: 'password-user2', apiKey: 'goodkey-1234' } })).json.token;
  await up({ batchId: 'u2', players: [mkp(60)] }, t2);
  assert.equal((await list()).players.length, 0);
  assert.equal((await call('GET', '/api/players', { token: t2, q: { rs: 'normal' } })).json.players.length, 1);
});

// ------------------------------------------------------------------ v3: /api/poll
const sleep = ms => new Promise(r => setTimeout(r, ms));
let t3;
const polled = async (q, token = t3) => { stub.log.length = 0; const r = await call('GET', '/api/poll', { token, q: { rs: 'normal', ...q } }); return { ...r, ops: [...stub.log] }; };
const up3 = (body) => call('POST', '/api/upload', { token: t3, body: { rs: 'normal', fp: 'fp3', from: 0, to: 1, batchId: 'p' + Math.random(), final: false, ...body } });
const pl = (i, reaction, mapId, extra = {}) => mkp(i, { reaction, mapId, d: {}, ...extra });
const gsiQ = ops => ops.filter(o => o.op === 'Query' && o.index);

test('poll: setup + full loads (since=0) and read-op counts', async () => {
  t3 = (await call('POST', '/api/login', { body: { username: 'user3', password: 'password-user3', apiKey: 'goodkey-1234' } })).json.token;
  await up3({ maps: [{ id: 1, name: 'Barrens', x0: 0, x1: 1, y0: 0, y1: 1 }], players: [
    pl(101, 'hostile', 1, { fixes: [fx(1000, 1, 1), fx(20000, 50, 50), fx(40000, 90, 90)] }), pl(102, 'hostile', 1), pl(103, 'friendly', 1), pl(104, 'neutral', 1), pl(105, 'hostile', 2)] });
  // all scope since=0: version Get + one base-table Query (no GSI)
  let r = await polled({ scope: 'all', since: 0 });
  assert.equal(r.json.changed, true); assert.equal(r.json.players.length, 5); assert.equal(r.json.maps.length, 1); assert.ok(r.json.v >= 1);
  assert.deepEqual(r.ops.map(o => o.op), ['Get', 'Query']); assert.equal(gsiQ(r.ops).length, 0);
  assert.ok(r.ops.every(o => !o.cr), 'eventually consistent only');
  // zone since=0 any: version Get + 3 GSI queries (friendly/hostile/other)
  r = await polled({ scope: 'zone', mapId: 1, grp: 'any', since: 0 });
  assert.equal(r.json.players.length, 4);
  assert.deepEqual(r.ops.map(o => o.op), ['Get', 'Query', 'Query', 'Query']);
  assert.deepEqual(gsiQ(r.ops).map(o => o.pk.split('#').pop()).sort(), ['friendly', 'hostile', 'other']);
  // GSI projection really applied: counters are not visible in zone rows, projected attrs are
  const h = r.json.players.find(p => p.guid === G(101));
  assert.equal(h.name, 'Bob101'); assert.equal(h.lastMap, 1); assert.equal(h.events, undefined); assert.equal(h.kb, undefined);
  // grp=other holds neutral+unknown
  r = await polled({ scope: 'zone', mapId: 1, grp: 'other', since: 0 });
  assert.deepEqual(r.json.players.map(p => p.guid), [G(104)]);
  // player scope: one GetItem; with track=1 one extra Query (newest chunk, Limit 1)
  r = await polled({ scope: 'player', guid: G(101), since: 0 });
  assert.equal(r.json.player.guid, G(101)); assert.deepEqual(r.ops.map(o => o.op), ['Get']);
  r = await polled({ scope: 'player', guid: G(101), since: 0, track: 1, tsince: 0 });
  assert.deepEqual(r.ops.map(o => o.op), ['Get', 'Query']); assert.equal(r.json.fixes.length, 3);
  r = await polled({ scope: 'player', guid: G(101), since: 0, track: 1, tsince: 20000 });
  assert.deepEqual(r.json.fixes.map(f => f.t), [40000]);
  assert.equal((await polled({ scope: 'player', guid: G(199), since: 0 })).json.changed, false);
});

test('poll: unchanged polls cost exactly one read op and no Query', async () => {
  await sleep(5);
  const a = await polled({ scope: 'all', since: 0 });
  await sleep(5);
  let r = await polled({ scope: 'all', since: a.json.now, v: a.json.v });
  assert.equal(r.json.changed, false); assert.equal(r.json.v, a.json.v);
  assert.deepEqual(r.ops.map(o => [o.op, o.sk]), [['Get', 'VER']]);
  const z = await polled({ scope: 'zone', mapId: 1, since: 0 });
  await sleep(5);
  r = await polled({ scope: 'zone', mapId: 1, grp: 'any', since: z.json.now });
  assert.equal(r.json.changed, false); assert.deepEqual(r.ops.map(o => [o.op, o.sk]), [['Get', 'VER']]);
  const p = await polled({ scope: 'player', guid: G(101), since: 0, track: 1 });
  await sleep(5);
  r = await polled({ scope: 'player', guid: G(101), since: p.json.now, track: 1 });
  assert.equal(r.json.changed, false); assert.equal(r.ops.length, 1); assert.equal(r.ops[0].op, 'Get'); // no track query when unchanged
  assert.equal(r.json.fixes, undefined);
  // zone never written: version gate only
  r = await polled({ scope: 'zone', mapId: 77, since: z.json.now });
  assert.deepEqual(r.ops.map(o => o.op), ['Get']);
  r = await polled({ scope: 'zone', mapId: 77, since: 0 });
  assert.deepEqual([r.json.changed, r.json.players, r.ops.length], [true, [], 1]);
});

test('poll: changed zone returns only the delta, queries only the requested grp', async () => {
  const base = await polled({ scope: 'zone', mapId: 1, since: 0 });
  await sleep(5);
  await up3({ players: [pl(102, 'hostile', 1, { lastSeen: 9000, d: { events: 3 } })] });
  let r = await polled({ scope: 'zone', mapId: 1, grp: 'hostile', since: base.json.now });
  assert.equal(r.json.changed, true); assert.deepEqual(r.json.players.map(p => p.guid), [G(102)]); // not 101
  assert.deepEqual(r.ops.map(o => o.op), ['Get', 'Query']);
  assert.ok(gsiQ(r.ops).every(o => o.pk.endsWith('#1#hostile'))); // grp=hostile never touches friendly (or other)
  assert.ok(!r.ops.some(o => /friendly|#other/.test(o.pk || '')));
  // per-zone gate: map 2 unchanged; friendly poll on map 1 queries only friendly
  r = await polled({ scope: 'zone', mapId: 2, since: base.json.now });
  assert.deepEqual([r.json.changed, r.ops.length], [false, 1]);
  r = await polled({ scope: 'zone', mapId: 1, grp: 'friendly', since: base.json.now });
  assert.deepEqual(r.json.players, []); assert.ok(gsiQ(r.ops).length === 1 && gsiQ(r.ops).every(o => o.pk.endsWith('#friendly')));
  // overlap: since = now - 2000 returns the recent delta, one row per guid (client can dedupe)
  r = await polled({ scope: 'zone', mapId: 1, grp: 'any', since: base.json.now - 2000 });
  const guids = r.json.players.map(p => p.guid);
  assert.ok(guids.includes(G(102))); assert.equal(new Set(guids).size, guids.length);
  // all-scope gate sees the version change
  const all0 = await polled({ scope: 'all', since: 0 });
  await sleep(5);
  await up3({ players: [pl(103, 'friendly', 1, { d: { events: 1 } })] });
  r = await polled({ scope: 'all', since: all0.json.now, v: all0.json.v });
  assert.equal(r.json.changed, true); assert.equal(r.json.v, all0.json.v + 1); assert.deepEqual(r.ops.map(o => o.op), ['Get', 'Query']);
  assert.equal(r.json.players.find(p => p.guid === G(103)).events, 1);
});

test('poll: moved-zone behaviour (no removal, old partition drops the player)', async () => {
  const z1 = await polled({ scope: 'zone', mapId: 1, grp: 'hostile', since: 0 });
  assert.ok(z1.json.players.some(p => p.guid === G(101)));
  await sleep(5);
  await up3({ players: [pl(101, 'hostile', 2, { lastSeen: 99999, posTime: 99999, x: 5, y: 6 })] });
  let r = await polled({ scope: 'zone', mapId: 1, grp: 'hostile', since: z1.json.now });
  assert.deepEqual([r.json.changed, r.ops.length], [false, 1]); // no removal event for the old zone
  r = await polled({ scope: 'zone', mapId: 1, grp: 'hostile', since: 0 });
  assert.ok(!r.json.players.some(p => p.guid === G(101)));
  r = await polled({ scope: 'zone', mapId: 2, grp: 'hostile', since: z1.json.now });
  assert.ok(r.json.players.some(p => p.guid === G(101) && p.lastMap === 2 && p.lastX === 5));
  const items = stub.tables.get('P#user3#normal');
  assert.equal(items.get('G#' + G(101)).zk, 'user3#normal#2#hostile');
  await up3({ players: [{ guid: G(110), name: 'NoPos', reaction: 'hostile', lastSeen: 5, firstSeen: 5, d: { events: 1 } }] });
  const np = items.get('G#' + G(110));
  assert.ok(np && np.zk === undefined && np.ls === undefined && typeof np.ut === 'number'); // no position: no GSI keys
  assert.equal((await polled({ scope: 'player', guid: G(110), since: 0 })).json.changed, true);
});

test('poll: player scope sees changes; track tail only after a change', async () => {
  const p = await polled({ scope: 'player', guid: G(101), since: 0, track: 1 });
  await sleep(5);
  await up3({ players: [pl(101, 'hostile', 2, { d: { events: 1 }, fixes: [fx(60000, 5, 5, 2)] })] });
  const r = await polled({ scope: 'player', guid: G(101), since: p.json.now, track: 1, tsince: 40000 });
  assert.equal(r.json.changed, true); assert.deepEqual(r.json.fixes.map(f => f.t), [60000]);
  assert.deepEqual(r.ops.map(o => o.op), ['Get', 'Query']); assert.equal(r.json.player.events, 1);
});

test('poll: validation, clamping, auth', async () => {
  const bad = async q => (await polled(q)).code;
  assert.equal(await bad({}), 400);
  assert.equal(await bad({ scope: 'nope' }), 400);
  assert.equal(await bad({ scope: 'zone' }), 400);
  assert.equal(await bad({ scope: 'zone', mapId: 'abc' }), 400);
  assert.equal(await bad({ scope: 'zone', mapId: 1.5 }), 400);
  assert.equal(await bad({ scope: 'zone', mapId: -1 }), 400);
  assert.equal(await bad({ scope: 'zone', mapId: 1, grp: 'purple' }), 400);
  assert.equal(await bad({ scope: 'player' }), 400);
  assert.equal(await bad({ scope: 'player', guid: 'x' }), 400);
  assert.equal(await bad({ scope: 'all', since: 'abc' }), 400);
  assert.equal(await bad({ scope: 'all', rs: 'zzz' }), 400);
  assert.equal(await bad({ scope: 'all', v: 'abc' }), 400);
  assert.equal((await call('GET', '/api/poll', { q: { rs: 'normal', scope: 'all' } })).code, 401);
  assert.equal((await call('GET', '/api/poll', { token: 'bad.token', q: { rs: 'normal', scope: 'all' } })).code, 401);
  // since clamped to [0, now]: negative == full load; far future == nothing newer
  let r = await polled({ scope: 'zone', mapId: 2, since: -5 });
  assert.equal(r.json.changed, true); assert.ok(r.json.players.length > 0);
  r = await polled({ scope: 'zone', mapId: 2, since: 9e15 });
  assert.equal(r.json.changed, false); assert.ok(r.json.now < 9e15);
  // other users' data is not visible
  r = await polled({ scope: 'zone', mapId: 1, since: 0 }, tok);
  assert.deepEqual(r.json.players, []);
  // rs omitted -> settings default
  assert.equal((await call('GET', '/api/poll', { token: t3, q: { scope: 'all', since: 0 } })).code, 200);
});

test('poll: reset cleans version item and zone keys', async () => {
  assert.ok(stub.tables.get('V#user3#normal')?.get('VER'));
  const before = (await polled({ scope: 'all', since: 0 })).json;
  await sleep(5);
  assert.equal((await call('POST', '/api/reset', { token: t3, body: { rs: 'normal', what: 'bounty' } })).code, 200);
  let r = await polled({ scope: 'all', since: before.now, v: before.v });
  assert.equal(r.json.changed, true); assert.equal(r.json.v, before.v + 1); // bounty reset bumps the version
  assert.equal((await call('POST', '/api/reset', { token: t3, body: { rs: 'normal', what: 'all' } })).code, 200);
  assert.equal(stub.tables.get('V#user3#normal')?.get('VER'), undefined);
  const left = [...stub.tables.values()].flatMap(p => [...p.values()]).filter(i => i.zk && i.zk.startsWith('user3#normal#'));
  assert.equal(left.length, 0);
  r = await polled({ scope: 'zone', mapId: 1, since: 0 });
  assert.deepEqual([r.json.changed, r.json.players, r.ops.length], [true, [], 1]);
  r = await polled({ scope: 'all', since: before.now, v: before.v });
  assert.equal(r.json.changed, true); assert.deepEqual(r.json.players, []);
});
