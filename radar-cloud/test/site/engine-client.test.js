'use strict';
// engine-client.js: LocalClient and WorkerClient request/reply matching and failure handling.
const test = require('node:test');
const assert = require('node:assert/strict');
const EC = require('../../site/engine-client.js');

test('engine client: LocalClient numbers requests and rejects engine errors', async () => {
  const seen = [];
  const client = new EC.LocalClient(m => {
    seen.push(m.id);
    return m.type === 'bad' ? { type: 'error', error: 'nope' } : { type: 'ok', id: m.id };
  });
  assert.deepEqual(await client.call({ type: 'a' }), { type: 'ok', id: 1 });
  await assert.rejects(client.call({ type: 'bad' }), /nope/);
  assert.deepEqual(seen, [1, 2]);
});

test('engine client: WorkerClient matches replies by id and fails every waiting call on a worker error', async () => {
  const worker = {
    postMessage(m) {
      this.last = m;
    },
    terminate() {
      this.stopped = true;
    },
  };
  const client = new EC.WorkerClient(worker);
  const first = client.call({ type: 'snap' });
  worker.onmessage({ data: { id: worker.last.id, type: 'snap', snap: 1 } });
  assert.equal((await first).snap, 1);
  const second = client.call({ type: 'flush' });
  worker.onerror({ message: 'crashed' });
  await assert.rejects(second, /crashed/);
  client.terminate();
  assert.ok(worker.stopped);
});
