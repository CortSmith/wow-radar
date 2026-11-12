import test from 'node:test';
import assert from 'node:assert/strict';
import { createClient, buildUrl, ApiError } from '../js/client.js';

const reply = (status, body) => async () => ({ ok: status < 400, status, json: async () => body });

test('buildUrl skips empty values and encodes', () => {
  assert.equal(buildUrl('', '/api/v1/sweep', { scope: 'all', q: '', rs: undefined, since: 0, guid: 'Player-1 x' }),
    '/api/v1/sweep?scope=all&since=0&guid=Player-1+x');
  assert.equal(buildUrl('http://h:1', '/api/v1/status', {}), 'http://h:1/api/v1/status');
});

test('get sends the view key header and returns json', async () => {
  const seen = [];
  const client = createClient({
    base: '', getKey: () => 'k1',
    fetch: async (url, init) => { seen.push({ url, init }); return { ok: true, status: 200, json: async () => ({ now: 5 }) }; },
  });
  assert.deepEqual(await client.get('/api/v1/sweep', { scope: 'all' }), { now: 5 });
  assert.equal(seen[0].init.headers['X-View-Key'], 'k1');
  assert.equal(seen[0].init.headers.Accept, 'application/json');
});

test('no key header when no key', async () => {
  let init;
  const client = createClient({ base: '', getKey: () => '', fetch: async (u, i) => { init = i; return { ok: true, status: 200, json: async () => ({}) }; } });
  await client.get('/x');
  assert.equal('X-View-Key' in init.headers, false);
});

test('errors carry status and code; 401 calls onAuth', async () => {
  let auth = 0;
  const client = createClient({ base: '', getKey: () => '', onAuth: () => { auth++; }, fetch: reply(401, { error: 'unauthorized', message: 'no' }) });
  await assert.rejects(client.get('/x'), (e) => e instanceof ApiError && e.status === 401 && e.code === 'unauthorized');
  assert.equal(auth, 1);
  const bad = createClient({ base: '', getKey: () => '', fetch: reply(500, null) });
  await assert.rejects(bad.get('/x'), (e) => e.status === 500 && e.code === 'http_500');
});

test('network failures become ApiError status 0; aborts pass through', async () => {
  const net = createClient({ base: '', getKey: () => '', fetch: async () => { throw new TypeError('fail'); } });
  await assert.rejects(net.get('/x'), (e) => e instanceof ApiError && e.status === 0);
  const abort = createClient({ base: '', getKey: () => '', fetch: async () => { throw new DOMException('x', 'AbortError'); } });
  await assert.rejects(abort.get('/x'), (e) => e.name === 'AbortError');
});
