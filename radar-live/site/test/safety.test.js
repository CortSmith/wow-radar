// Static checks: strict-CSP friendly (no inline script/style/handlers), safe DOM, no external requests.
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';

const root = new URL('..', import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, '$1');
const jsFiles = [];
(function walk(dir) {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    if (e.isDirectory()) walk(join(dir, e.name));
    else if (e.name.endsWith('.js')) jsFiles.push(join(dir, e.name));
  }
})(join(root, 'js'));
const html = readFileSync(join(root, 'index.html'), 'utf8');
const css = readFileSync(join(root, 'style.css'), 'utf8');

test('no unsafe HTML sinks or eval in page code', () => {
  assert.ok(jsFiles.length > 10);
  const banned = /\.innerHTML|\.outerHTML|insertAdjacentHTML|document\.write|\beval\(|new Function\(|setAttribute\(\s*['"]on|setAttribute\(\s*['"]style/;
  for (const f of jsFiles) assert.doesNotMatch(readFileSync(f, 'utf8'), banned, f);
});

test('page code makes no external requests', () => {
  for (const f of jsFiles) {
    const code = readFileSync(f, 'utf8').split('\n').map((l) => l.replace(/(^|\s)\/\/.*$/, '')).join('\n');
    assert.doesNotMatch(code, /https?:\/\/(?!\S*localhost)/, f);
  }
  assert.doesNotMatch(html + css, /https?:\/\//);
});

test('index.html: no inline script, style or handlers; scripts are same-origin modules', () => {
  assert.doesNotMatch(html, /<script(?![^>]*\bsrc=)/i);
  assert.doesNotMatch(html, /\sstyle\s*=/i);
  assert.doesNotMatch(html, /\son[a-z]+\s*=/i);
  assert.doesNotMatch(html, /<style/i);
  for (const m of html.matchAll(/<script[^>]*src="([^"]+)"/g)) assert.doesNotMatch(m[1], /^(https?:)?\/\//);
  assert.match(html, /<script type="module" src="js\/main\.js">/);
});

test('css has no @import or remote urls', () => {
  assert.doesNotMatch(css, /@import|url\(/);
});
