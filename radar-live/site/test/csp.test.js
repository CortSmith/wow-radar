// Parses index.html as markup and proves it loads under the server's strict CSP
// (default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'self').
import test from 'node:test';
import assert from 'node:assert/strict';
import { existsSync, readFileSync, readdirSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';

const root = new URL('..', import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, '$1');
const html = readFileSync(join(root, 'index.html'), 'utf8');

/** Minimal tag scanner: [{name, attrs:{}, raw, selfContained}] (comments removed). */
function tags(source) {
  const out = [];
  const clean = source.replace(/<!--[\s\S]*?-->/g, '');
  for (const m of clean.matchAll(/<([a-zA-Z][a-zA-Z0-9-]*)((?:\s+[^\s=>/]+(?:\s*=\s*(?:"[^"]*"|'[^']*'|[^\s>]+))?)*)\s*\/?>/g)) {
    const attrs = {};
    for (const a of m[2].matchAll(/([^\s=>/]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+)))?/g)) {
      attrs[a[1].toLowerCase()] = a[2] ?? a[3] ?? a[4] ?? '';
    }
    out.push({ name: m[1].toLowerCase(), attrs, raw: m[0] });
  }
  return out;
}

const isExternal = (u) => /^([a-z][a-z0-9+.-]*:|\/\/)/i.test(u.trim());

test('index.html: no inline script, style or event-handler attributes', () => {
  const all = tags(html);
  assert.ok(all.length > 8, 'parsed the page');
  for (const t of all) {
    if (t.name === 'script') assert.ok(t.attrs.src, 'inline <script> body: ' + t.raw);
    assert.notEqual(t.name, 'style', 'inline <style>');
    assert.ok(!('style' in t.attrs), 'style attribute: ' + t.raw);
    for (const a of Object.keys(t.attrs)) assert.doesNotMatch(a, /^on/, 'event handler ' + a + ' in ' + t.raw);
  }
  // A script element with a src must have an empty body.
  for (const m of html.matchAll(/<script\b[^>]*>([\s\S]*?)<\/script>/gi)) assert.equal(m[1].trim(), '');
  assert.doesNotMatch(html, /javascript:/i);
});

test('index.html: no external URLs and no elements the CSP would block', () => {
  for (const t of tags(html)) {
    for (const attr of ['src', 'href', 'action', 'data', 'poster', 'srcset', 'formaction']) {
      if (attr in t.attrs) assert.ok(!isExternal(t.attrs[attr]), `external ${attr} in ${t.raw}`);
    }
    assert.ok(!['iframe', 'frame', 'object', 'embed', 'base', 'form', 'applet'].includes(t.name), 'blocked element ' + t.name);
    assert.ok(!('http-equiv' in t.attrs), 'meta http-equiv is the server header job');
  }
});

test('index.html: every local script and stylesheet exists and imports resolve inside the site', () => {
  const refs = tags(html).filter((t) => (t.name === 'script' && t.attrs.src) || (t.name === 'link' && t.attrs.href));
  assert.ok(refs.some((t) => t.name === 'script') && refs.some((t) => t.name === 'link'));
  const seen = new Set();
  const visit = (file) => {
    if (seen.has(file)) return;
    seen.add(file);
    assert.ok(existsSync(file), 'missing ' + file);
    if (!file.endsWith('.js')) return;
    const code = readFileSync(file, 'utf8');
    for (const m of code.matchAll(/(?:from\s+|import\s*\(\s*|import\s+)['"]([^'"]+)['"]/g)) {
      assert.ok(!isExternal(m[1]), `external import ${m[1]} in ${file}`);
      const target = resolve(dirname(file), m[1]);
      assert.ok(target.startsWith(resolve(root)), 'import escapes site: ' + m[1]);
      visit(target);
    }
  };
  for (const t of refs) visit(join(root, t.attrs.src ?? t.attrs.href));
});

test('page code never creates inline-style attributes, workers, blobs or sockets to other origins', () => {
  const files = [];
  (function walk(dir) {
    for (const e of readdirSync(dir, { withFileTypes: true })) {
      if (e.isDirectory()) walk(join(dir, e.name));
      else if (e.name.endsWith('.js')) files.push(join(dir, e.name));
    }
  })(join(root, 'js'));
  for (const f of files) {
    const code = readFileSync(f, 'utf8');
    assert.doesNotMatch(code, /attrs:\s*\{[^}]*\bstyle\s*:/, 'style attribute via attrs in ' + f);
    assert.doesNotMatch(code, /new\s+(Worker|SharedWorker|WebSocket|EventSource)\b|createObjectURL|document\.cookie|sendBeacon/, f);
  }
});

test('page code never reads or writes cookies', () => {
  for (const f of ['js/main.js', 'js/core.js', 'js/client.js']) {
    assert.doesNotMatch(readFileSync(join(root, f), 'utf8'), /cookie|credentials\s*:\s*['"]include/i, f);
  }
});
