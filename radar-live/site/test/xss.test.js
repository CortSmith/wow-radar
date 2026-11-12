// XSS audit (security task 8.5): greps the whole page source for dangerous sinks so a regression fails
// the build, and checks that attacker-controlled values (player names, the ?maps= override) stay inert.
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';
import { resolveConfig, CONFIG } from '../js/config.js';

const root = new URL('..', import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, '$1');
const files = [];
(function walk(dir) {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    if (e.name === 'test' || e.name === 'node_modules') continue;
    if (e.isDirectory()) walk(join(dir, e.name));
    else if (/\.(js|mjs|html)$/.test(e.name)) files.push(join(dir, e.name));
  }
})(root);

const stripComments = (src) => src.replace(/\/\*[\s\S]*?\*\//g, '').split('\n').map((l) => l.replace(/(^|\s)\/\/.*$/, '')).join('\n');

const SINKS = [
  [/\.innerHTML\b/, 'innerHTML'],
  [/\.outerHTML\b/, 'outerHTML'],
  [/insertAdjacentHTML/, 'insertAdjacentHTML'],
  [/insertAdjacentText\s*\(\s*['"]?\s*['"]?\s*afterbegin\s*['"]\s*,\s*[^)]*\+/, 'built insertAdjacentText'],
  [/document\s*\.\s*write(ln)?\s*\(/, 'document.write'],
  [/\beval\s*\(/, 'eval'],
  [/\bnew\s+Function\b|(^|[^.\w])Function\s*\(/, 'Function constructor'],
  [/set(Timeout|Interval)\s*\(\s*['"`]/, 'string timer'],
  [/createContextualFragment|DOMParser|\.srcdoc\b|\.parseFromString/, 'HTML parsing'],
  [/javascript\s*:/i, 'javascript: URL'],
  [/data\s*:\s*text\/html/i, 'data:text/html URL'],
  [/\.on[a-z]+\s*=[^=]/, 'handler property assignment'],
  [/setAttribute\s*\(\s*['"](on|style|srcdoc|href|src|formaction)/i, 'dangerous setAttribute'],
  [/\.(href|action|formAction)\s*=[^=]/, 'URL property assignment'],
  [/window\s*\.\s*open\s*\(|location\s*(\.\s*(href|assign|replace))?\s*=[^=]/, 'navigation'],
  [/postMessage\s*\(/, 'postMessage'],
  [/<\s*(iframe|object|embed)\b/i, 'embedded content'],
];

test('no dangerous sinks anywhere in the page source', () => {
  assert.ok(files.length > 20, `only ${files.length} files scanned`);
  for (const f of files) {
    const code = f.endsWith('.html') ? readFileSync(f, 'utf8') : stripComments(readFileSync(f, 'utf8'));
    for (const [re, name] of SINKS) {
      if (f.endsWith('dev-server.mjs') || f.endsWith('dev-demo.mjs')) continue; // dev tools, not shipped
      assert.doesNotMatch(code, re, `${name} in ${f}`);
    }
  }
});

test('html has no inline script, style, handler or javascript: link', () => {
  const html = readFileSync(join(root, 'index.html'), 'utf8');
  assert.doesNotMatch(html, /<script(?![^>]*\bsrc=)/i);
  assert.doesNotMatch(html, /\son[a-z]+\s*=/i);
  assert.doesNotMatch(html, /javascript\s*:/i);
  assert.doesNotMatch(html, /<a\b[^>]*\bhref\s*=\s*["']?\s*(javascript|data|vbscript):/i);
});

test('the sink scanner itself catches each bad pattern', () => {
  const bad = ['x.innerHTML = a', 'el.outerHTML=b', 'e.insertAdjacentHTML("x", y)', 'document.write(z)', 'eval(s)', 'new Function("a")',
    'setTimeout("alert(1)", 5)', 'a.onclick = f', 'a.href = u', 'location = u', 'location.href = u', 'window.open(u)',
    'x.setAttribute("onclick", y)', 'x = "javascript:alert(1)"', 'new DOMParser()'];
  for (const src of bad) assert.ok(SINKS.some(([re]) => re.test(src)), `not caught: ${src}`);
});

test('every rendering of a player name goes through textContent or a text node', async () => {
  const { makeEl } = await import('../js/dom.js');
  const { createFakeDocument } = await import('./fake-dom.js');
  const doc = createFakeDocument();
  const evil = '<img src=x onerror=alert(1)><script>alert(2)</script>';
  const li = makeEl(doc, 'li', {}, makeEl(doc, 'span', { text: evil }), evil);
  assert.equal(li.children.length, 2, 'markup in a name must not create elements');
  assert.equal(li.children[0].textContent, evil);
  assert.equal(li.children[1].textContent, evil);
});

test('?maps= accepts only same-origin relative folders', () => {
  const rejected = ['//evil.example/', 'https://evil.example/', 'http://evil/', 'javascript:alert(1)', 'data:text/html,x',
    '/\\evil.example/', '..//evil/', '../../x/', 'a/../../b/', 'maps/\n', 'maps/<x>', 'a b/', '\\\\evil\\x\\', 'file:///etc/', ' //evil/'];
  for (const m of rejected) {
    const c = resolveConfig(`?maps=${encodeURIComponent(m)}`);
    assert.equal(c.MAPS_BASE_URL, CONFIG.MAPS_BASE_URL, `accepted ${JSON.stringify(m)}`);
  }
  for (const ok of ['/m/', 'maps/', 'assets/zone-maps/', '/static/maps_v2/']) {
    assert.equal(resolveConfig(`?maps=${encodeURIComponent(ok)}`).MAPS_BASE_URL, ok);
  }
});
