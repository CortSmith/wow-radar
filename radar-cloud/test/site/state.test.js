'use strict';
// state.js: independent state objects with documented defaults, and the event bus.
const test = require('node:test');
const assert = require('node:assert/strict');
const { createBus, createState } = require('../../site/state.js');
const config = require('../../site/config.js');

test('bus: handlers run in subscription order with the emitted arguments; unknown events are ignored', () => {
  const bus = createBus(),
    seen = [];
  bus.on('x', (a, b) => seen.push(['first', a, b]));
  bus.on('x', () => seen.push(['second']));
  bus.emit('x', 1, 2);
  bus.emit('nobody');
  assert.deepEqual(seen, [['first', 1, 2], ['second']]);
});

test('state: every state owns its own collections and starts with the documented defaults', () => {
  const a = createState(),
    b = createState();
  assert.notEqual(a.fz, b.fz);
  assert.notEqual(a.outbox, b.outbox);
  assert.equal(a.rangeYd, config.geometry.RANGE_DEFAULT_YD);
  assert.deepEqual(a.flags, {
    follow: true,
    stale: true,
    bg: true,
    fit: true,
    showBlue: true,
    showRed: true,
    showHistory: false,
  });
});
