'use strict';
/**
 * dom.js - single responsibility: cached element lookup and safe element building.
 *
 * Public API (Radar.dom):
 *  - $(id) -> Element     getElementById, looked up once and cached (ids come from index.html)
 *  - el(tag, {className, text, attrs}, children?) -> Element   builds a node; text always goes through textContent
 *  - replaceChildren(node, children) -> node    clears and refills a list without innerHTML
 *  - setHidden(node, hidden)
 * Names from combat logs are untrusted: nothing here, and nothing in the UI modules, assigns innerHTML.
 */
(function (g) {
  const cache = new Map();
  function $(id) {
    let node = cache.get(id);
    if (!node) {
      node = g.document.getElementById(id);
      cache.set(id, node);
    }
    return node;
  }

  function el(tag, opts, children) {
    const node = g.document.createElement(tag);
    const o = opts || {};
    if (o.className) node.className = o.className;
    if (o.text !== undefined) node.textContent = o.text;
    for (const k of Object.keys(o.attrs || {})) node.setAttribute(k, o.attrs[k]);
    for (const child of children || []) node.appendChild(child);
    return node;
  }

  function replaceChildren(node, children) {
    node.textContent = '';
    for (const child of children) node.appendChild(child);
    return node;
  }

  const setHidden = (node, hidden) => {
    node.hidden = hidden;
  };

  (g.Radar || (g.Radar = {})).dom = { $, el, replaceChildren, setHidden };
})(typeof self !== 'undefined' ? self : globalThis);
