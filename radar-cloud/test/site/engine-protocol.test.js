'use strict';
// engine-protocol.js: the request/reply messages of the engine (documented in worker.js).
const test = require('node:test');
const assert = require('node:assert/strict');
const { createHandler } = require('../../site/engine-protocol.js');
const H = require('./helpers.js');

test('init answers ready; chunk answers an ack with pending counts; snap and flush follow', () => {
  const handle = createHandler(new TextDecoder());
  assert.deepEqual(handle({ type: 'init', id: 1, logClock: true }), { type: 'ready', id: 1 });
  assert.equal(handle({ type: 'begin', id: 2, file: 'log', wm: {} }).type, 'ok');
  const text = H.dmg(1, H.FOE, H.ALLY) + '\n';
  const ack = handle({ type: 'chunk', id: 3, buf: new TextEncoder().encode(text), last: true });
  assert.equal(ack.type, 'ack');
  assert.equal(ack.pendingPlayers > 0, true);
  assert.equal(handle({ type: 'snap', id: 4 }).snap.pips.length > 0, true);
  const flushed = handle({ type: 'flush', id: 5 });
  assert.equal(flushed.type, 'flushed');
  assert.equal(flushed.batches[0].players.length > 0, true);
  assert.equal(handle({ type: 'pending', id: 6 }).players, 0);
});

test('a failing request answers an error message instead of throwing', () => {
  const handle = createHandler();
  const reply = handle({ type: 'chunk', id: 9, buf: null, last: true });
  assert.equal(reply.type, 'error');
  assert.equal(reply.id, 9);
});

const noOffset = line => line.replace(/(\d)-5( {2})/, '$1$2'); // the same log line as written without a UTC offset

test('config timezone: lines without an offset are read in that zone; the ack reports the file span in UTC', () => {
  const handle = createHandler(new TextDecoder());
  handle({ type: 'init', id: 1, logClock: true });
  assert.equal(handle({ type: 'config', id: 2, mode: 'auto', timezone: 'America/New_York' }).type, 'ok');
  handle({ type: 'begin', id: 3, file: 'log', wm: {} });
  const text = [H.dmg(1, H.FOE, H.ALLY), H.dmg(61, H.FOE, H.ALLY)].map(noOffset).join('\n') + '\n';
  const ack = handle({ type: 'chunk', id: 4, buf: new TextEncoder().encode(text), last: true });
  assert.equal(ack.firstT, H.T0 + 1000); // 14:00:01 EST = 19:00:01 UTC
  assert.equal(ack.lastT, H.T0 + 61000);
  const flushed = handle({ type: 'flush', id: 5 });
  assert.equal(flushed.batches[0].to, H.T0 + 61000);
});

test('without a zone, offset-less lines are UTC; an unknown zone is ignored; the span restarts for each file', () => {
  const handle = createHandler(new TextDecoder());
  handle({ type: 'init', id: 1, logClock: true });
  handle({ type: 'config', id: 2, timezone: 'Not/AZone' });
  handle({ type: 'begin', id: 3, file: 'a', wm: {} });
  const feed = (id, line) =>
    handle({ type: 'chunk', id, buf: new TextEncoder().encode(noOffset(line) + '\n'), last: true });
  assert.equal(feed(4, H.dmg(1, H.FOE, H.ALLY)).firstT, H.T0 - 5 * 3600000 + 1000); // 14:00:01 read as UTC
  handle({ type: 'begin', id: 5, file: 'b', wm: {} });
  const second = feed(6, H.dmg(9, H.FOE, H.ALLY));
  assert.equal(second.firstT, second.lastT);
  assert.equal(second.firstT, H.T0 - 5 * 3600000 + 9000);
});
