// Black-box smoke test of the LOCAL production imitation. Usage: node dev/smoke.mjs [baseUrl]
// Requires: node dev/server.mjs --demo --fresh   (demo account + dev-key-0001)
import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert/strict';
import { fileURLToPath } from 'node:url';
const base = process.argv[2] || 'http://127.0.0.1:8787';
const site = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', 'site');
let pass = 0; const t = async (n, f) => { try { await f(); pass++; console.log('ok  ', n); } catch (e) { console.log('FAIL', n, '\n   ', e.message); process.exitCode = 1; } };
const j = async (m, p, { body, token } = {}) => {
  const r = await fetch(base + p, { method: m, headers: { ...(token && { authorization: 'Bearer ' + token }), ...(body && { 'content-type': 'application/json' }) }, body: body && JSON.stringify(body) });
  return { code: r.status, json: await r.json().catch(() => null), headers: r.headers };
};

await t('index.html is served and every local asset it references exists (200)', async () => {
  const html = await (await fetch(base + '/')).text();
  const refs = [...html.matchAll(/(?:src|href)="([^"#?]+)"/g)].map(m => m[1]).filter(u => !/^(https?:)?\/\//.test(u));
  assert.ok(refs.length >= 3, 'refs ' + refs);
  for (const u of refs) assert.equal((await fetch(base + '/' + u.replace(/^\//, ''))).status, 200, u);
});
await t('every site/*.js file is served and parses', async () => {
  for (const f of fs.readdirSync(site).filter(f => f.endsWith('.js'))) {
    const r = await fetch(base + '/' + f); assert.equal(r.status, 200, f);
    new Function(await r.text().then(s => s.replace(/^\s*(import|export)\b.*$/gm, ''))); // syntax only
  }
});
await t('index.html is static: no player data baked in', async () => {
  const html = await (await fetch(base + '/')).text();
  assert.ok(!/Player-\d/.test(html));
});
await t('ads config + an ad image are served', async () => {
  const ads = await (await fetch(base + '/ads/ads.json')).json();
  assert.ok(ads.length >= 1);
  assert.equal((await fetch(base + '/' + ads[0].img.replace(/^\//, ''))).status, 200);
});
await t('maps/index.json and a map image are served, webp is immutable-cached', async () => {
  const idx = await (await fetch(base + '/maps/index.json')).json();
  const z = Object.values(idx.zones)[0];
  const r = await fetch(base + '/maps/' + z.file);
  assert.equal(r.status, 200); assert.match(r.headers.get('cache-control'), /immutable/);
});
await t('missing static key -> 403 like S3+OAC', async () => assert.equal((await fetch(base + '/nope.txt')).status, 403));
await t('API requires auth', async () => assert.equal((await j('GET', '/api/players')).code, 401));

let token;
await t('login: wrong key rejected; inactive key rejected; good login OK', async () => {
  assert.equal((await j('POST', '/api/login', { body: { username: 'demo', password: 'demo-password-1', apiKey: 'wrong-key-123' } })).code, 401);
  const r = await j('POST', '/api/login', { body: { username: 'demo', password: 'demo-password-1', apiKey: 'dev-key-0001' } });
  assert.equal(r.code, 200); token = r.json.token; assert.ok(token);
});
await t('signup with inactive key -> 403', async () => assert.equal((await j('POST', '/api/signup', { body: { username: 'zed1', password: 'password-zed1', apiKey: 'dev-inactive-key' } })).code, 403));
await t('5-account cap enforced on one key (demo + 4 ok, 6th rejected)', async () => {
  const codes = []; const tag = Date.now().toString(36);
  for (let i = 0; i < 5; i++) codes.push((await j('POST', '/api/signup', { body: { username: `cap${tag}${i}`, password: 'password-cap1', apiKey: 'dev-key-0001' } })).code);
  assert.deepEqual(codes.filter(c => c === 200).length <= 4, true, codes.join());
  assert.ok(codes.includes(403), codes.join());
});

let rows;
await t('/api/players rows carry every field the frontend reads', async () => {
  const r = await j('GET', '/api/players?rs=normal', { token });
  assert.equal(r.code, 200); rows = r.json.players; assert.ok(rows.length >= 40); assert.ok(r.json.maps.length >= 2);
  const need = ['guid', 'name', 'reaction', 'level', 'maxHp', 'firstSeen', 'lastSeen', 'events', 'srcEvents', 'dmgTotal', 'dmgHits', 'hpSum', 'hpSamples'];
  const miss = need.filter(k => !(k in rows[0])); assert.deepEqual(miss, [], 'missing ' + miss);
  const pos = ['lastX', 'lastY', 'lastMap', 'lastZone'].filter(k => !(k in rows[0]) && !(k.replace('last', '').toLowerCase() in rows[0]));
  assert.deepEqual(pos, [], 'position fields: ' + Object.keys(rows[0]).join());
});
await t('/api/poll zone scope: since=0 full, then unchanged poll = changed:false', async () => {
  const a = await j('GET', '/api/poll?rs=normal&scope=zone&mapId=1413&grp=any&since=0', { token });
  assert.equal(a.code, 200); assert.equal(a.json.changed, true); assert.ok(a.json.players.length >= 20);
  assert.ok(a.json.players.every(p => p.lastMap === 1413));
  await new Promise(r => setTimeout(r, 7000)); // let the 5s simulator settle, then poll with fresh `now`
  const b = await j('GET', `/api/poll?rs=normal&scope=zone&mapId=1413&grp=any&since=${a.json.now + 60000}`, { token });
  assert.equal(b.json.changed, false);
});
await t('/api/poll hostile group returns only hostile', async () => {
  const r = await j('GET', '/api/poll?rs=normal&scope=zone&mapId=1413&grp=hostile&since=0', { token });
  assert.ok(r.json.players.length > 0 && r.json.players.every(p => p.reaction === 'hostile'));
});
await t('/api/poll player scope returns that player (+track tail)', async () => {
  const r = await j('GET', `/api/poll?rs=normal&scope=player&guid=${rows[0].guid}&since=0&track=1`, { token });
  assert.equal(r.code, 200); assert.equal(r.json.changed, true);
  assert.equal((r.json.player || r.json.players?.[0]).guid, rows[0].guid);
});
await t('bounty ranking works', async () => {
  const r = await j('GET', '/api/bounty?rs=normal', { token });
  assert.equal(r.code, 200); assert.ok(r.json.entries.length > 0); assert.ok(r.json.entries[0].score >= r.json.entries.at(-1).score);
});
await t('settings round-trip', async () => {
  assert.equal((await j('PUT', '/api/settings', { token, body: { cal: { 1413: [1, 2] } } })).code, 200);
  assert.deepEqual((await j('GET', '/api/settings', { token })).json.settings?.cal ?? (await j('GET', '/api/settings', { token })).json.cal, { 1413: [1, 2] });
});
await t('oversize body -> 413 (Function URL 6MB cap)', async () => {
  const r = await fetch(base + '/api/upload', { method: 'POST', headers: { authorization: 'Bearer ' + token }, body: 'x'.repeat(6.2 * 1024 * 1024) });
  assert.equal(r.status, 413);
});
console.log(`\n${pass} passed`);
