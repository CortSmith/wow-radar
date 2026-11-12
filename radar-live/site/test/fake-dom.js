// A tiny DOM, just enough for the page's features. Mount points are read from the real index.html.
import { readFileSync } from 'node:fs';

class FakeNode {
  constructor(tagName, doc) {
    this.tagName = String(tagName).toUpperCase();
    this.ownerDocument = doc;
    this.children = [];
    this.parentNode = null;
    this.attributes = {};
    this.listeners = {};
    this.style = {};
    this.dataset = {};
    this.className = '';
    this.hidden = false;
    this.value = '';
    this.checked = false;
    this.disabled = false;
    this.nodeText = null;
    this.clientWidth = 800;
    this.clientHeight = 600;
  }

  get firstChild() {
    return this.children[0] || null;
  }

  get classList() {
    const node = this;
    const names = () => node.className.split(/\s+/).filter(Boolean);
    return {
      add: (n) => { if (!names().includes(n)) node.className = [...names(), n].join(' '); },
      remove: (n) => { node.className = names().filter((x) => x !== n).join(' '); },
      toggle: (n, on) => {
        const has = names().includes(n);
        const want = on === undefined ? !has : on;
        if (want && !has) node.className = [...names(), n].join(' ');
        if (!want && has) node.className = names().filter((x) => x !== n).join(' ');
      },
      contains: (n) => names().includes(n),
    };
  }

  get textContent() {
    if (this.nodeText !== null) return this.nodeText;
    return this.children.map((c) => c.textContent).join('');
  }

  set textContent(v) {
    this.children = [];
    this.nodeText = String(v);
  }

  appendChild(child) {
    if (child.parentNode) child.parentNode.removeChild(child);
    this.nodeText = this.nodeText !== null && this.children.length === 0 ? null : this.nodeText;
    child.parentNode = this;
    this.children.push(child);
    return child;
  }

  removeChild(child) {
    this.children = this.children.filter((c) => c !== child);
    child.parentNode = null;
    return child;
  }

  setAttribute(k, v) {
    this.attributes[k] = String(v);
  }

  getAttribute(k) {
    return k in this.attributes ? this.attributes[k] : null;
  }

  addEventListener(type, fn) {
    (this.listeners[type] ||= []).push(fn);
  }

  removeEventListener(type, fn) {
    this.listeners[type] = (this.listeners[type] || []).filter((f) => f !== fn);
  }

  dispatch(type, extra = {}) {
    const event = { type, target: this, preventDefault() {}, stopPropagation() {}, ...extra };
    for (const fn of this.listeners[type] || []) fn(event);
    return event;
  }

  focus() {}

  getContext() {
    return null;
  }

  getBoundingClientRect() {
    return { left: 0, top: 0, width: this.clientWidth, height: this.clientHeight };
  }

  walk(visit) {
    for (const c of this.children) {
      if (c.walk) {
        if (visit(c)) return true;
        if (c.walk(visit)) return true;
      }
    }
    return false;
  }

  find(predicate) {
    let found = null;
    this.walk((n) => {
      if (predicate(n)) {
        found = n;
        return true;
      }
      return false;
    });
    return found;
  }

  findAll(predicate) {
    const out = [];
    this.walk((n) => {
      if (predicate(n)) out.push(n);
      return false;
    });
    return out;
  }

  querySelector(selector) {
    const m = /^\[data-mount="([^"]+)"\]$/.exec(selector);
    if (!m) throw new Error(`fake dom: unsupported selector ${selector}`);
    return this.find((n) => n.attributes['data-mount'] === m[1]);
  }
}

class FakeText {
  constructor(text) {
    this.textContent = text;
    this.parentNode = null;
  }
}

/** Mount names declared in index.html (data-mount="..."). */
export function mountNames(htmlPath = new URL('../index.html', import.meta.url)) {
  const html = readFileSync(htmlPath, 'utf8');
  return [...html.matchAll(/data-mount="([^"]+)"/g)].map((m) => m[1]);
}

/** A document whose body holds one div per mount point of index.html. */
export function createFakeDocument(mounts = mountNames()) {
  const doc = { hidden: false, listeners: {} };
  doc.createElement = (tag) => new FakeNode(tag, doc);
  doc.createTextNode = (text) => new FakeText(text);
  doc.body = new FakeNode('body', doc);
  doc.addEventListener = (type, fn) => (doc.listeners[type] ||= []).push(fn);
  doc.dispatch = (type) => (doc.listeners[type] || []).forEach((fn) => fn({ type }));
  doc.querySelector = (selector) => doc.body.querySelector(selector);
  for (const name of mounts) {
    const div = doc.createElement('div');
    div.setAttribute('data-mount', name);
    doc.body.appendChild(div);
  }
  return doc;
}
