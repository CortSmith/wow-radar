/**
 * Black-box smoke test of the LOCAL production imitation (static site + API over real HTTP).
 * Usage: node dev/server.mjs --demo --fresh      (terminal 1: demo operator + dev-key-0001)
 *        node dev/smoke.mjs [baseUrl]            (terminal 2; default http://127.0.0.1:8787)
 * Exits non-zero when any check fails.
 */
import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert/strict';
import { fileURLToPath } from 'node:url';
import { LOCAL_URL, ACTIVE_BEACON_KEY, INACTIVE_BEACON_KEY, DEMO_USERNAME, DEMO_PASSWORD } from './harbor.mjs';

const BASE_URL = process.argv[2] || LOCAL_URL;
const SITE_DIR = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', 'site');
/** The demo simulator uploads every 5 s; wait longer than that so a later sweep sees a settled state. */
const SIMULATOR_SETTLE_MS = 7000;
/** Function URLs reject bodies above 6 MB. */
const OVERSIZE_BODY_BYTES = 6.2 * 1024 * 1024;
const MIN_DEMO_CONTACTS = 40;
const BARRENS_MAP_ID = 1413;

let passed = 0;
async function check(title, body) {
  try {
    await body();
    passed++;
    console.log('ok  ', title);
  } catch (error) {
    console.log('FAIL', title, '\n   ', error.message);
    process.exitCode = 1;
  }
}

/** JSON request helper: returns { code, json, headers }. */
async function api(method, urlPath, { body, token } = {}) {
  const response = await fetch(BASE_URL + urlPath, {
    method,
    headers: {
      ...(token && { authorization: 'Bearer ' + token }),
      ...(body && { 'content-type': 'application/json' }),
    },
    body: body && JSON.stringify(body),
  });
  return { code: response.status, json: await response.json().catch(() => null), headers: response.headers };
}

const fetchText = async urlPath => (await fetch(BASE_URL + urlPath)).text();
const fetchStatus = async urlPath => (await fetch(BASE_URL + urlPath)).status;
const demoLogin = { username: DEMO_USERNAME, password: DEMO_PASSWORD, apiKey: ACTIVE_BEACON_KEY };

// ---------------------------------------------------------------- static site
await check('index.html is served and every local asset it references exists (200)', async () => {
  const html = await fetchText('/');
  const references = [...html.matchAll(/(?:src|href)="([^"#?]+)"/g)]
    .map(match => match[1])
    .filter(url => !/^(https?:)?\/\//.test(url));
  assert.ok(references.length >= 3, 'refs ' + references);
  for (const reference of references)
    assert.equal(await fetchStatus('/' + reference.replace(/^\//, '')), 200, reference);
});
await check('every site/*.js file is served and parses', async () => {
  for (const file of fs.readdirSync(SITE_DIR).filter(name => name.endsWith('.js'))) {
    const response = await fetch(BASE_URL + '/' + file);
    assert.equal(response.status, 200, file);
    new Function((await response.text()).replace(/^\s*(import|export)\b.*$/gm, '')); // syntax only
  }
});
await check('index.html is static: no player data baked in', async () => {
  assert.ok(!/Player-\d/.test(await fetchText('/')));
});
await check('ads config + an ad image are served', async () => {
  const ads = JSON.parse(await fetchText('/ads/ads.json'));
  assert.ok(ads.length >= 1);
  assert.equal(await fetchStatus('/' + ads[0].img.replace(/^\//, '')), 200);
});
await check('maps/index.json and a map image are served, webp is immutable-cached', async () => {
  const index = JSON.parse(await fetchText('/maps/index.json'));
  const response = await fetch(BASE_URL + '/maps/' + Object.values(index.zones)[0].file);
  assert.equal(response.status, 200);
  assert.match(response.headers.get('cache-control'), /immutable/);
});
await check('missing static key -> 403 like S3+OAC', async () => assert.equal(await fetchStatus('/nope.txt'), 403));

// ---------------------------------------------------------------- auth
await check('API requires auth', async () => assert.equal((await api('GET', '/api/players')).code, 401));

let token;
await check('login: wrong key rejected; inactive key rejected; good login OK', async () => {
  assert.equal((await api('POST', '/api/login', { body: { ...demoLogin, apiKey: 'wrong-key-123' } })).code, 401);
  const response = await api('POST', '/api/login', { body: demoLogin });
  assert.equal(response.code, 200);
  token = response.json.token;
  assert.ok(token);
});
await check('signup with inactive key -> 403', async () => {
  const body = { username: 'zed1', password: 'password-zed1', apiKey: INACTIVE_BEACON_KEY };
  assert.equal((await api('POST', '/api/signup', { body })).code, 403);
});
await check('5-account cap enforced on one key (demo + 4 ok, 6th rejected)', async () => {
  const codes = [];
  const tag = Date.now().toString(36);
  for (let index = 0; index < 5; index++) {
    const body = { username: `cap${tag}${index}`, password: 'password-cap1', apiKey: ACTIVE_BEACON_KEY };
    codes.push((await api('POST', '/api/signup', { body })).code);
  }
  assert.ok(codes.filter(code => code === 200).length <= 4, codes.join());
  assert.ok(codes.includes(403), codes.join());
});

// ---------------------------------------------------------------- data endpoints
let rows;
await check('/api/players rows carry every field the frontend reads', async () => {
  const response = await api('GET', '/api/players?rs=normal', { token });
  assert.equal(response.code, 200);
  rows = response.json.players;
  assert.ok(rows.length >= MIN_DEMO_CONTACTS);
  assert.ok(response.json.maps.length >= 2);
  const required = [
    'guid',
    'name',
    'reaction',
    'level',
    'maxHp',
    'firstSeen',
    'lastSeen',
    'events',
    'srcEvents',
    'dmgTotal',
    'dmgHits',
    'hpSum',
    'hpSamples',
  ];
  assert.deepEqual(
    required.filter(field => !(field in rows[0])),
    [],
  );
  const positionFields = ['lastX', 'lastY', 'lastMap', 'lastZone'];
  assert.deepEqual(
    positionFields.filter(field => !(field in rows[0])),
    [],
    'position fields: ' + Object.keys(rows[0]).join(),
  );
});
await check('/api/poll zone scope: since=0 full, then unchanged poll = changed:false', async () => {
  const first = await api('GET', `/api/poll?rs=normal&scope=zone&mapId=${BARRENS_MAP_ID}&grp=any&since=0`, { token });
  assert.equal(first.code, 200);
  assert.equal(first.json.changed, true);
  assert.ok(first.json.players.length >= 20);
  assert.ok(first.json.players.every(player => player.lastMap === BARRENS_MAP_ID));
  await new Promise(resolve => setTimeout(resolve, SIMULATOR_SETTLE_MS));
  const later = await api(
    'GET',
    `/api/poll?rs=normal&scope=zone&mapId=${BARRENS_MAP_ID}&grp=any&since=${first.json.now + 60000}`,
    { token },
  );
  assert.equal(later.json.changed, false);
});
await check('/api/poll hostile group returns only hostile', async () => {
  const response = await api('GET', `/api/poll?rs=normal&scope=zone&mapId=${BARRENS_MAP_ID}&grp=hostile&since=0`, {
    token,
  });
  assert.ok(response.json.players.length > 0 && response.json.players.every(player => player.reaction === 'hostile'));
});
await check('/api/poll player scope returns that player (+track tail)', async () => {
  const response = await api('GET', `/api/poll?rs=normal&scope=player&guid=${rows[0].guid}&since=0&track=1`, { token });
  assert.equal(response.code, 200);
  assert.equal(response.json.changed, true);
  assert.equal(response.json.player.guid, rows[0].guid);
});
await check('bounty ranking works', async () => {
  const response = await api('GET', '/api/bounty?rs=normal', { token });
  assert.equal(response.code, 200);
  assert.ok(response.json.entries.length > 0);
  assert.ok(response.json.entries[0].score >= response.json.entries.at(-1).score);
});
await check('settings round-trip', async () => {
  const saved = { cal: { [BARRENS_MAP_ID]: [1, 2] } };
  assert.equal((await api('PUT', '/api/settings', { token, body: saved })).code, 200);
  assert.deepEqual((await api('GET', '/api/settings', { token })).json.cal, saved.cal);
});
await check('oversize body -> 413 (Function URL 6MB cap)', async () => {
  const response = await fetch(BASE_URL + '/api/upload', {
    method: 'POST',
    headers: { authorization: 'Bearer ' + token },
    body: 'x'.repeat(OVERSIZE_BODY_BYTES),
  });
  assert.equal(response.status, 413);
});

console.log(`\n${passed} passed`);
