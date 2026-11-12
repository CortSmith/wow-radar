import test from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { createHandler, safeJoin } from '../dev-server.mjs';

async function withServer(opts, fn) {
  const server = createServer(createHandler(opts));
  await new Promise((r) => server.listen(0, '127.0.0.1', r));
  try {
    await fn(`http://127.0.0.1:${server.address().port}`);
  } finally {
    await new Promise((r) => server.close(r));
  }
}

test('serves the page, assets with types, demo api, healthz', async () => {
  await withServer({}, async (base) => {
    const page = await fetch(`${base}/`);
    assert.match(page.headers.get('content-type'), /text\/html/);
    assert.match(await page.text(), /<canvas|data-mount="radar"/);
    assert.match((await fetch(`${base}/js/core.js`)).headers.get('content-type'), /text\/javascript/);
    const sweep = await (await fetch(`${base}/api/v1/sweep?scope=all&since=0`)).json();
    assert.ok(sweep.contacts.length > 0);
    assert.equal(await (await fetch(`${base}/healthz`)).text(), 'ok');
    assert.equal((await fetch(`${base}/nope.js`)).status, 404);
    assert.equal((await fetch(`${base}/..%2f..%2fgo.mod`)).status, 404);
  });
});

test('VIEW_KEY makes the api answer 401 without the header', async () => {
  await withServer({ viewKey: 'k' }, async (base) => {
    assert.equal((await fetch(`${base}/api/v1/charts`)).status, 401);
    assert.equal((await fetch(`${base}/api/v1/charts`, { headers: { 'X-View-Key': 'k' } })).status, 200);
  });
});

test('safeJoin refuses to leave the root', () => {
  assert.equal(safeJoin('/a/b', '../c'), null);
  assert.ok(safeJoin('/a/b', 'x/y.js'));
});
