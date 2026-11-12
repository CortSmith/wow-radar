// Safe DOM helpers: text only ever goes through textContent / text nodes.

/**
 * Creates an element. props: className, text, attrs{}, on{event: fn}, plus any plain property
 * (value, checked, type, title, hidden, ...). children: nodes or strings (strings become text nodes).
 */
export function makeEl(doc, tag, props = {}, ...children) {
  const node = doc.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (k === 'text') node.textContent = v;
    else if (k === 'attrs') for (const [a, av] of Object.entries(v)) node.setAttribute(a, av);
    else if (k === 'on') for (const [e, fn] of Object.entries(v)) node.addEventListener(e, fn);
    else node[k] = v;
  }
  for (const c of children.flat()) {
    if (c === null || c === undefined) continue;
    node.appendChild(typeof c === 'string' ? doc.createTextNode(c) : c);
  }
  return node;
}

/** Removes all children. */
export function clearNode(node) {
  while (node.firstChild) node.removeChild(node.firstChild);
}
