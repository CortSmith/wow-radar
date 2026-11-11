'use strict';
// adapters.js: storage, static JSON and hashing degrade to fallbacks when platform APIs are missing.
const test = require('node:test');
const assert = require('node:assert/strict');

test('adapters: storage and static JSON degrade to their fallbacks when the platform API is missing', async () => {
  require('../../site/config.js');
  require('../../site/engine-client.js');
  require('../../site/adapters.js');
  const adapters = globalThis.Radar.adapters;
  assert.equal(adapters.storage.read('k', 'fallback'), 'fallback'); // no localStorage in node: must not throw
  adapters.storage.write('k', 'v');
  adapters.storage.remove('k');
  const realFetch = globalThis.fetch;
  globalThis.fetch = async () => {
    throw new Error('offline');
  };
  try {
    assert.equal(await adapters.loadStaticJson('x.json'), null);
  } finally {
    globalThis.fetch = realFetch;
  }
  assert.equal(
    await adapters.sha256(new Uint8Array([1, 2, 3])),
    require('node:crypto')
      .createHash('sha256')
      .update(Buffer.from([1, 2, 3]))
      .digest('hex'),
  );
  assert.equal(adapters.canPickDirectory(), false);
});
