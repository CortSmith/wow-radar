'use strict';
// ARCHITECTURE.md: every site script is documented, and every file it names exists.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const SITE = path.join(__dirname, '..', '..', 'site');
const doc = fs.readFileSync(path.join(SITE, 'ARCHITECTURE.md'), 'utf8');
const named = new Set(Array.from(doc.matchAll(/`([a-z0-9-]+\.(?:js|html|css))`/g), m => m[1]));

test('every site script is listed in ARCHITECTURE.md', () => {
  for (const f of fs.readdirSync(SITE).filter(x => x.endsWith('.js')))
    assert.ok(named.has(f), f + ' is not documented');
});

test('every file named in ARCHITECTURE.md exists in site/', () => {
  for (const f of named) assert.ok(fs.existsSync(path.join(SITE, f)), f + ' does not exist');
});
