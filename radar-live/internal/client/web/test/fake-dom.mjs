// A tiny DOM for app.js: one element per id found in index.html. Only what the page uses is implemented.
import { readFileSync } from 'node:fs';

class FakeElement {
  constructor(id, tag) {
    this.id = id;
    this.tagName = tag.toUpperCase();
    this.children = [];
    this.listeners = {};
    this.attributes = {};
    this.value = '';
    this.checked = false;
    this.disabled = false;
    this.hidden = false;
    this.className = '';
    this.scrollTop = 0;
    this.scrollHeight = 1000;
    this._text = '';
  }

  get textContent() {
    return this.children.length ? this.children.map((c) => c.textContent).join('') : this._text;
  }

  set textContent(v) {
    this.children = [];
    this._text = String(v);
  }

  get classList() {
    const el = this;
    const names = () => el.className.split(/\s+/).filter(Boolean);
    return {
      add: (n) => { if (!names().includes(n)) el.className = [...names(), n].join(' '); },
      remove: (n) => { el.className = names().filter((x) => x !== n).join(' '); },
      contains: (n) => names().includes(n),
    };
  }

  setAttribute(k, v) { this.attributes[k] = String(v); }

  append(...nodes) { this.children.push(...nodes); }

  replaceChildren(...nodes) {
    this._text = '';
    this.children = [...nodes];
  }

  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); }

  async fire(type) {
    for (const fn of this.listeners[type] || []) await fn({ type, target: this, preventDefault() {} });
  }
}

/** A document with an element for every id="..." of the given HTML file, plus a token meta tag. */
export function createFakeDocument(token = 'tok-123') {
  const html = readFileSync(new URL('../index.html', import.meta.url), 'utf8');
  const elements = new Map();
  for (const m of html.matchAll(/<(\w+)[^>]*\sid="([^"]+)"/g)) elements.set(m[2], new FakeElement(m[2], m[1]));
  return {
    getElementById: (id) => elements.get(id) || null,
    createElement: (tag) => new FakeElement('', tag),
    querySelector: (sel) => (sel === 'meta[name="radar-token"]' ? { content: token } : null),
    elements,
  };
}
