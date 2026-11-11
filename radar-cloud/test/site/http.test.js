'use strict';
// http.js: login/signup and the authenticated api client (tokens, 401, error messages).
const test = require('node:test');
const assert = require('node:assert/strict');
const C = require('../../site/http.js');

const res = (status, body) => ({
  status,
  ok: status >= 200 && status < 300,
  json: async () => {
    if (body === undefined) throw new Error('no json');
    return body;
  },
});

test('login posts credentials (trimmed) and returns token', async () => {
  const calls = [];
  const f = async (url, opt) => {
    calls.push([url, opt]);
    return res(200, { token: 'T', username: 'bob' });
  };
  const r = await C.authenticate(f, { signup: false, username: ' bob ', password: 'pw', apiKey: ' KEY ' });
  assert.deepEqual(r, { token: 'T', username: 'bob' });
  assert.equal(calls[0][0], '/api/login');
  assert.deepEqual(JSON.parse(calls[0][1].body), { username: 'bob', password: 'pw', apiKey: 'KEY' });
});

test('signup uses /api/signup; server error, bad json and network failure surface messages', async () => {
  let url;
  await C.authenticate(
    async u => {
      url = u;
      return res(200, { token: 'T' });
    },
    { signup: true, username: 'a', password: 'b', apiKey: 'c' },
  );
  assert.equal(url, '/api/signup');
  await assert.rejects(
    C.authenticate(async () => res(403, { error: 'Key inactive' }), { username: 'a', password: 'b', apiKey: 'c' }),
    /Key inactive/,
  );
  await assert.rejects(
    C.authenticate(async () => res(502), { username: 'a', password: 'b', apiKey: 'c' }),
    /Server error \(502\)/,
  );
  await assert.rejects(
    C.authenticate(
      async () => {
        throw new Error('net');
      },
      { username: 'a', password: 'b', apiKey: 'c' },
    ),
    /Could not reach/,
  );
});

test('api client sends bearer token, handles 401 and errors', async () => {
  let auth,
    out = 0;
  const api = C.makeApi(
    async (u, o) => {
      auth = o.headers.Authorization;
      return u === '/x' ? res(200, { a: 1 }) : u === '/401' ? res(401, {}) : res(500, { error: 'boom' });
    },
    () => 'TOK',
    () => {
      out++;
    },
  );
  assert.deepEqual(await api('/x'), { a: 1 });
  assert.equal(auth, 'Bearer TOK');
  await assert.rejects(api('/401'), /Session expired/);
  assert.equal(out, 1);
  await assert.rejects(api('/500'), /boom/);
});
