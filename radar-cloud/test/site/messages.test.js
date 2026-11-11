'use strict';
// messages.js: error text rule and the historic wording of user-visible messages.
const test = require('node:test');
const assert = require('node:assert/strict');
const M = require('../../site/messages.js');

test('messages: errorText accepts Errors and plain values; builders keep the historic wording', () => {
  assert.equal(M.errorText(new Error('boom')), 'boom');
  assert.equal(M.errorText('plain'), 'plain');
  assert.equal(M.serverError(502), 'Server error (502)');
  assert.equal(M.uploadError(new Error('x'), true), 'Error: x (will retry on the next upload)');
  assert.equal(M.uploadError(new Error('x'), false), 'Error: x');
  assert.equal(M.uploading('pvp', 1, 3), 'Uploading pvp: request 1/3');
});
