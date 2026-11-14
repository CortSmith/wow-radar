// Page glue for radar-keys: renders the key table and dialogs from the local JSON API.
// Every piece of text from the server goes in through textContent; no HTML is ever built from data.
// Raw keys live only in memory (the reveal map and the one-time dialog) and are cleared from the DOM after use.
import {
  LIMIT_FIELDS, REVEAL_MS, validateEntry, formFromKey, maskKey, statusLabel, isEnabled, isReadOnly,
  formatCount, formatTime, formatDay, formatLimits, limitPlaceholder, allTags, filterKeys, sortKeys, errorMessage,
} from './logic.js';

const FIELD_IDS = {
  email: 'fEmail', player: 'fPlayer', tags: 'fTags', notes: 'fNotes', expires: 'fExpires',
  lineRate: 'fLineRate', lineBurst: 'fLineBurst', byteRate: 'fByteRate', byteBurst: 'fByteBurst', maxStreams: 'fMaxStreams',
};
const ERROR_IDS = {
  email: 'eEmail', expires: 'eExpires', lineRate: 'eLineRate', lineBurst: 'eLineBurst',
  byteRate: 'eByteRate', byteBurst: 'eByteBurst', maxStreams: 'eMaxStreams',
};

/**
 * Wires the page. All browser objects are passed in so the logic can be tested without a browser.
 * env: { document, fetch, timers: { setTimeout, clearTimeout }, confirm, prompt, copy }
 */
export function createApp(env) {
  const { document, fetch, timers, confirm, prompt, copy } = env;
  const token = document.querySelector('meta[name="radar-token"]').content;
  const $ = (id) => document.getElementById(id);

  let keys = [];
  let defaults = {};
  let editing = null; // null = creating, else the Key being edited
  let busy = false;
  const revealed = new Map(); // id -> { key, timer }
  let oneTimeKey = '';

  async function api(method, path, body) {
    const init = { method, headers: { 'X-Radar-Token': token } };
    if (body !== undefined) {
      init.headers['Content-Type'] = 'application/json';
      init.body = JSON.stringify(body);
    }
    try {
      const res = await fetch(path, init);
      const data = res.status === 204 ? {} : await res.json().catch(() => ({}));
      return { ok: res.ok, status: res.status, data };
    } catch (err) {
      return { ok: false, status: 0, data: { message: 'Cannot reach radar-keys.' } };
    }
  }

  function el(tag, props = {}, ...children) {
    const e = document.createElement(tag);
    for (const [k, v] of Object.entries(props)) {
      if (k === 'attrs') for (const [a, b] of Object.entries(v)) e.setAttribute(a, b);
      else e[k] = v;
    }
    if (children.length) e.append(...children.map((c) => (typeof c === 'string' ? el('span', { textContent: c }) : c)));
    return e;
  }

  function td(className, ...children) {
    return el('td', className ? { className } : {}, ...children);
  }

  function setPageMsg(text) { $('pageMsg').textContent = text || ''; }

  function replaceKey(entry) {
    const i = keys.findIndex((k) => k.id === entry.id);
    if (i >= 0) keys[i] = entry; else keys.push(entry);
  }

  // ---- reveal ----
  function hideKey(id) {
    const r = revealed.get(id);
    if (!r) return;
    timers.clearTimeout(r.timer);
    revealed.delete(id);
    r.key = '';
  }

  function hideAll() {
    for (const id of [...revealed.keys()]) hideKey(id);
  }

  async function onReveal(k) {
    if (revealed.has(k.id)) { hideKey(k.id); render(); return; }
    const res = await api('GET', `/api/keys/${encodeURIComponent(k.id)}/reveal`);
    if (!res.ok || !res.data.key) {
      setPageMsg(res.status === 404 ? 'No stored copy of this key. Use Regenerate.' : errorMessage(res.data));
      return;
    }
    setPageMsg('');
    const timer = timers.setTimeout(() => { hideKey(k.id); render(); }, REVEAL_MS);
    revealed.set(k.id, { key: res.data.key, timer });
    render();
  }

  async function onCopy(text, msgTarget) {
    try {
      await copy(text);
      msgTarget.textContent = 'Copied.';
    } catch (err) {
      msgTarget.textContent = 'Copy failed. Select the key and copy it manually.';
    }
  }

  // ---- table ----
  function keyCell(k) {
    const r = revealed.get(k.id);
    const text = el('span', { className: 'mono', textContent: r ? r.key : (k.masked || maskKey('')) });
    const eye = el('button', { className: 'btn small', type: 'button', textContent: r ? 'Hide' : 'Show', attrs: { 'data-action': 'reveal', 'aria-label': 'Reveal key' } });
    const cell = el('div', { className: 'keycell' }, text, eye);
    if (!k.canReveal) {
      eye.className = 'btn small off';
      eye.title = 'not stored, use Regenerate';
      eye.setAttribute('aria-disabled', 'true');
      eye.disabled = true;
    } else {
      eye.addEventListener('click', () => onReveal(k));
    }
    if (r) {
      const cp = el('button', { className: 'btn small', type: 'button', textContent: 'Copy', attrs: { 'data-action': 'copy-revealed' } });
      const msg = el('span', { className: 'hint' });
      cp.addEventListener('click', () => onCopy(r.key, msg));
      cell.append(cp, msg);
    }
    return td('', cell);
  }

  function row(k) {
    const ro = isReadOnly(k);
    const enabled = el('input', { type: 'checkbox', checked: isEnabled(k), disabled: ro, attrs: { 'data-action': 'toggle', 'aria-label': 'Enabled' } });
    if (!ro) enabled.addEventListener('change', () => onToggle(k, enabled));
    const status = statusLabel(k);
    const statusEl = el('span', { className: `status ${status}`, textContent: status });
    if (k.disabled && k.disabledReason) statusEl.title = k.disabledReason;
    const tags = el('span', {}, ...(k.tags || []).map((t) => el('span', { className: 'tag', textContent: t })));
    const actions = el('div', { className: 'actions' });
    if (!ro) {
      const edit = el('button', { className: 'btn small', type: 'button', textContent: 'Edit', attrs: { 'data-action': 'edit' } });
      const regen = el('button', { className: 'btn small', type: 'button', textContent: 'Regenerate', attrs: { 'data-action': 'regenerate' } });
      const del = el('button', { className: 'btn small danger', type: 'button', textContent: 'Delete', attrs: { 'data-action': 'delete' } });
      edit.addEventListener('click', () => openDialog(k));
      regen.addEventListener('click', () => onRegenerate(k));
      del.addEventListener('click', () => onDelete(k));
      actions.append(edit, regen, del);
    }
    const usage = k.usage || {};
    const tr = el('tr', { className: isEnabled(k) ? '' : 'disabledrow', attrs: { 'data-id': k.id } },
      td('', enabled),
      keyCell(k),
      td('', k.email || ''),
      td('', k.player || ''),
      td('', tags),
      td('', formatDay(k.createdAt)),
      td('', formatTime(usage.lastSeen)),
      td('num', formatCount(usage.linesToday)),
      td('', formatLimits(k.limits)),
      td('', statusEl),
      td('', actions));
    return tr;
  }

  function render() {
    const tagSel = $('tagFilter');
    const current = tagSel.value || '';
    const opts = [el('option', { value: '', textContent: 'All tags' })];
    for (const t of allTags(keys)) opts.push(el('option', { value: t, textContent: t }));
    tagSel.replaceChildren(...opts);
    tagSel.value = opts.some((o) => o.value === current) ? current : '';

    const shown = filterKeys(sortKeys(keys), $('search').value, tagSel.value);
    $('rows').replaceChildren(...shown.map(row));
    $('emptyText').hidden = shown.length > 0;
    $('countText').textContent = `${shown.length} of ${keys.length} keys`;
  }

  async function load() {
    const res = await api('GET', '/api/keys');
    if (!res.ok) { setPageMsg(errorMessage(res.data, 'Could not load keys.')); return; }
    setPageMsg('');
    keys = Array.isArray(res.data.keys) ? res.data.keys : [];
    defaults = res.data.defaults || {};
    for (const id of [...revealed.keys()]) if (!keys.some((k) => k.id === id && k.canReveal)) hideKey(id);
    render();
  }

  // ---- row actions ----
  async function onToggle(k, box) {
    const wantEnabled = box.checked;
    let res;
    if (wantEnabled) {
      res = await api('POST', `/api/keys/${encodeURIComponent(k.id)}/enable`);
    } else {
      const reason = prompt('Reason for blocking this key (optional):', '');
      if (reason === null || reason === undefined) { render(); return; }
      res = await api('POST', `/api/keys/${encodeURIComponent(k.id)}/disable`, { reason: String(reason).trim() });
    }
    if (res.ok) { setPageMsg(''); replaceKey(res.data); } else setPageMsg(errorMessage(res.data));
    render();
  }

  async function onRegenerate(k) {
    if (!confirm(`Regenerate the key for ${k.email || k.id}? The old key stops working immediately.`)) return;
    const keepCopy = confirm('Keep a recoverable copy of the new key so you can view it later? (OK = yes, Cancel = no)');
    const res = await api('POST', `/api/keys/${encodeURIComponent(k.id)}/regenerate`, { keepCopy });
    if (!res.ok) { setPageMsg(errorMessage(res.data)); return; }
    setPageMsg('');
    hideKey(k.id);
    replaceKey(res.data.entry);
    render();
    showOneTime(res.data.key);
  }

  async function onDelete(k) {
    if (!confirm(`Delete the key for ${k.email || k.id}? This cannot be undone.`)) return;
    const res = await api('DELETE', `/api/keys/${encodeURIComponent(k.id)}`);
    if (!res.ok) { setPageMsg(errorMessage(res.data)); return; }
    setPageMsg('');
    hideKey(k.id);
    keys = keys.filter((x) => x.id !== k.id);
    render();
  }

  // ---- edit / create dialog ----
  function clearErrors() {
    for (const id of Object.values(ERROR_IDS)) $(id).textContent = '';
    $('dlgErr').textContent = '';
  }

  function openDialog(k) {
    editing = k || null;
    const form = k ? formFromKey(k) : { email: '', player: '', tags: '', notes: '', expires: '', limits: {} };
    $('dlgTitle').textContent = k ? 'Edit key' : 'New key';
    $('dlgSave').textContent = k ? 'Save' : 'Create';
    $('fEmail').value = form.email;
    $('fPlayer').value = form.player;
    $('fTags').value = form.tags;
    $('fNotes').value = form.notes;
    $('fExpires').value = form.expires;
    for (const f of LIMIT_FIELDS) {
      $(FIELD_IDS[f]).value = form.limits[f] || '';
      $(FIELD_IDS[f]).placeholder = limitPlaceholder(defaults, f);
    }
    $('fKeepCopy').checked = true;
    $('keepCopyRow').hidden = !!k;
    clearErrors();
    $('editDlg').hidden = false;
  }

  function closeDialog() {
    $('editDlg').hidden = true;
    editing = null;
  }

  function readForm() {
    const limits = {};
    for (const f of LIMIT_FIELDS) limits[f] = $(FIELD_IDS[f]).value;
    return {
      email: $('fEmail').value, player: $('fPlayer').value, tags: $('fTags').value, notes: $('fNotes').value,
      expires: $('fExpires').value, limits, keepCopy: editing ? undefined : $('fKeepCopy').checked,
    };
  }

  async function onSave() {
    if (busy) return;
    clearErrors();
    const v = validateEntry(readForm());
    if (!v.ok) {
      for (const [f, msg] of Object.entries(v.errors)) if (ERROR_IDS[f]) $(ERROR_IDS[f]).textContent = msg;
      return;
    }
    busy = true;
    $('dlgSave').disabled = true;
    const res = editing
      ? await api('PUT', `/api/keys/${encodeURIComponent(editing.id)}`, v.payload)
      : await api('POST', '/api/keys', v.payload);
    busy = false;
    $('dlgSave').disabled = false;
    if (!res.ok) { $('dlgErr').textContent = errorMessage(res.data); return; }
    if (editing) {
      replaceKey(res.data);
      closeDialog();
      render();
    } else {
      replaceKey(res.data.entry);
      closeDialog();
      render();
      showOneTime(res.data.key);
    }
  }

  function onTagsKey(ev) {
    if (ev && ev.key === 'Enter') {
      if (ev.preventDefault) ev.preventDefault();
      $('fTags').value = parseTagsText($('fTags').value);
    }
  }

  function parseTagsText(text) {
    const seen = new Set();
    const out = [];
    for (const t of String(text).split(/[,;\n]/).map((x) => x.trim()).filter(Boolean)) {
      if (!seen.has(t.toLowerCase())) { seen.add(t.toLowerCase()); out.push(t); }
    }
    return out.length ? `${out.join(', ')}, ` : '';
  }

  // ---- one-time key ----
  function showOneTime(raw) {
    oneTimeKey = raw;
    $('newKeyText').textContent = raw;
    $('copyMsg').textContent = '';
    $('keyDlg').hidden = false;
  }

  function closeOneTime() {
    oneTimeKey = '';
    $('newKeyText').textContent = '';
    $('copyMsg').textContent = '';
    $('keyDlg').hidden = true;
  }

  async function init() {
    $('newBtn').addEventListener('click', () => openDialog(null));
    $('refreshBtn').addEventListener('click', async () => { hideAll(); await load(); });
    $('search').addEventListener('input', render);
    $('tagFilter').addEventListener('change', render);
    $('dlgSave').addEventListener('click', onSave);
    $('dlgCancel').addEventListener('click', closeDialog);
    $('fTags').addEventListener('keydown', onTagsKey);
    $('copyNewKey').addEventListener('click', () => onCopy(oneTimeKey, $('copyMsg')));
    $('closeKey').addEventListener('click', closeOneTime);
    await load();
  }

  return { init };
}

if (typeof document !== 'undefined') {
  createApp({
    document,
    fetch: (path, init) => fetch(path, init),
    timers: { setTimeout: (fn, ms) => setTimeout(fn, ms), clearTimeout: (h) => clearTimeout(h) },
    confirm: (msg) => window.confirm(msg),
    prompt: (msg, def) => window.prompt(msg, def),
    copy: (text) => navigator.clipboard.writeText(text),
  }).init();
}
