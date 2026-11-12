import test from 'node:test';
import assert from 'node:assert/strict';
import { createScheduler } from '../js/scheduler.js';
import { createFakeTimers } from './fake-timers.js';

const deferred = () => {
  let resolve;
  const promise = new Promise((a) => {
    resolve = a;
  });
  return { promise, resolve };
};
const flush = () => new Promise((r) => setImmediate(r));

function make(over = {}) {
  const timers = createFakeTimers(1000);
  const calls = [];
  const sched = createScheduler({
    run: async (signal) => {
      calls.push({ at: timers.now(), signal });
      if (over.run) return over.run(signal, calls.length);
      return undefined;
    },
    intervalMs: 60_000,
    debounceMs: 300,
    retryBaseMs: 5000,
    maxBackoffMs: 300_000,
    timers,
    now: timers.now,
    ...over.opts,
  });
  return { timers, calls, sched };
}

test('runs immediately on start then every 60 s', async () => {
  const { timers, calls, sched } = make();
  sched.start();
  await timers.advance(0);
  await flush();
  assert.equal(calls.length, 1);
  await timers.advance(59_999);
  await flush();
  assert.equal(calls.length, 1);
  await timers.advance(1);
  await flush();
  assert.equal(calls.length, 2);
  assert.equal(calls[1].at - calls[0].at, 60_000);
  assert.equal(sched.nextAt(), timers.now() + 60_000);
});

test('requestNow debounces and restarts the 60 s timer', async () => {
  const { timers, calls, sched } = make();
  sched.start();
  await timers.advance(0);
  await flush();
  await timers.advance(10_000);
  sched.requestNow();
  sched.requestNow();
  sched.requestNow();
  await timers.advance(299);
  await flush();
  assert.equal(calls.length, 1);
  await timers.advance(1);
  await flush();
  assert.equal(calls.length, 2);
  await timers.advance(59_999);
  await flush();
  assert.equal(calls.length, 2);
  await timers.advance(1);
  await flush();
  assert.equal(calls.length, 3);
});

test('never overlaps: requestNow during a run aborts it and reruns afterwards', async () => {
  const gate = deferred();
  let first = true;
  const { timers, calls, sched } = make({
    run: async () => {
      if (first) {
        first = false;
        await gate.promise;
      }
    },
  });
  sched.start();
  await timers.advance(0);
  await flush();
  assert.equal(calls.length, 1);
  sched.requestNow();
  assert.equal(calls[0].signal.aborted, true);
  await timers.advance(1000);
  await flush();
  assert.equal(calls.length, 1, 'no second run while the first is in flight');
  gate.resolve();
  await flush();
  await timers.advance(0);
  await flush();
  assert.equal(calls.length, 2);
});

test('errors back off exponentially, capped at 5 min, and recover', async () => {
  let fail = true;
  const errors = [];
  const { timers, calls, sched } = make({
    run: async () => {
      if (fail) throw new Error('boom');
    },
    opts: { onError: (e) => errors.push(e.message) },
  });
  sched.start();
  await timers.advance(0);
  await flush();
  const delays = [];
  for (let i = 0; i < 9; i++) {
    const wait = sched.nextAt() - timers.now();
    delays.push(wait);
    await timers.advance(wait);
    await flush();
  }
  assert.deepEqual(delays.slice(0, 4), [5000, 10_000, 20_000, 40_000]);
  assert.equal(Math.max(...delays), 300_000);
  assert.ok(errors.length >= 9);
  fail = false;
  await timers.advance(sched.nextAt() - timers.now());
  await flush();
  assert.equal(sched.nextAt() - timers.now(), 60_000);
  assert.ok(calls.length > 9);
});

test('aborted runs do not count as failures', async () => {
  let n = 0;
  const { timers, sched } = make({
    run: async (signal) => {
      n++;
      if (n === 1) {
        await new Promise((_, rej) => signal.addEventListener('abort', () => rej(new Error('aborted'))));
      }
    },
  });
  sched.start();
  await timers.advance(0);
  await flush();
  sched.requestNow();
  await flush();
  await flush();
  assert.equal(sched.failures(), 0);
});

test('pauses while hidden and sweeps immediately on return', async () => {
  const { timers, calls, sched } = make();
  sched.start();
  await timers.advance(0);
  await flush();
  sched.setHidden(true);
  await timers.advance(300_000);
  await flush();
  assert.equal(calls.length, 1);
  sched.setHidden(false);
  await timers.advance(0);
  await flush();
  assert.equal(calls.length, 2);
});

test('stop cancels everything', async () => {
  const { timers, calls, sched } = make();
  sched.start();
  await timers.advance(0);
  await flush();
  sched.stop();
  await timers.advance(600_000);
  await flush();
  assert.equal(calls.length, 1);
  assert.equal(sched.nextAt(), 0);
});
