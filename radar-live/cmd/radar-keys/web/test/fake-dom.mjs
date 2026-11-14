// A tiny DOM for app.js: one element per id found in index.html, plus created elements. Only what the page uses.
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
    this.title = '';
    this.placeholder = '';
    this._text = '';
  }

  get textContent() {
    return this.children.length ? this.children.map((c) => c.textContent).join('') : this._text;
  }

  set textContent(v) {
    this.children = [];
    this._text = String(v);
  }

  setAttribute(k, v) { this.attributes[k] = String(v); }

  append(...nodes) { this.children.push(...nodes); }

  replaceChildren(...nodes) {
    this._text = '';
    this.children = [...nodes];
  }

  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); }

  async fire(type, extra = {}) {
    for (const fn of this.listeners[type] || []) await fn({ type, target: this, preventDefault() {}, ...extra });
  }

  /** All descendants (depth first) matching a predicate. */
  findAll(pred) {
    const out = [];
    for (const c of this.children) {
      if (!(c instanceof FakeElement)) continue;
      if (pred(c)) out.push(c);
      out.push(...c.findAll(pred));
    }
    return out;
  }

  /** Descendants with data-action="name". */
  action(name) {
    return this.findAll((e) => e.attributes['data-action'] === name);
  }
}

export function createFakeDocument(token = 'tok-123') {
  const html = readFileSync(new URL('../index.html', import.meta.url), 'utf8');
  const elements = new Map();
  for (const m of html.matchAll(/<(\w+)[^>]*\sid="([^"]+)"/g)) elements.set(m[2], new FakeElement(m[2], m[1]));
  // Elements that start hidden in the HTML.
  for (const m of html.matchAll(/<\w+[^>]*\sid="([^"]+)"[^>]*\shidden/g)) elements.get(m[1]).hidden = true;
  return {
    getElementById: (id) => elements.get(id) || null,
    createElement: (tag) => new FakeElement('', tag),
    querySelector: (sel) => (sel === 'meta[name="radar-token"]' ? { content: token } : null),
    elements,
  };
}
