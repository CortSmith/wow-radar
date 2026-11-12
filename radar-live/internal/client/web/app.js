// Page glue for radar-client: reads the form, talks to the local JSON API and renders the status.
// Every piece of text from the server goes in through textContent; no HTML is ever built from data.
import {
  validateForm, startPayload, describeState, statusRows, eventLines, serverWarning,
} from './logic.js';

const POLL_MS = 1000;
const LOCKED_WHILE_RUNNING = ['server', 'key', 'ruleset', 'logPath', 'browse', 'followNewest', 'includeExisting'];
const LOG_LABEL_FILE = 'Log file';
const LOG_LABEL_FOLDER = 'Log folder (or any file in it)';

/**
 * Wires the page. All browser objects are passed in so the logic can be tested without a browser.
 * env: { document, fetch, timers: { setInterval }, now }
 */
export function createApp(env) {
  const { document, fetch, timers, now } = env;
  const token = document.querySelector('meta[name="radar-token"]').content;
  const $ = (id) => document.getElementById(id);
  let view = { keySource: 'none' };

  async function api(method, path, body) {
    const init = { method, headers: { 'X-Radar-Token': token } };
    if (body !== undefined) {
      init.headers['Content-Type'] = 'application/json';
      init.body = JSON.stringify(body);
    }
    try {
      const res = await fetch(path, init);
      const data = await res.json().catch(() => ({}));
      return { ok: res.ok, status: res.status, data };
    } catch (err) {
      return { ok: false, status: 0, data: { message: 'Cannot reach radar-client.' } };
    }
  }

  function readForm() {
    return {
      server: $('server').value, key: $('key').value, ruleset: $('ruleset').value, logPath: $('logPath').value,
      followNewest: $('followNewest').checked, includeExisting: $('includeExisting').checked,
    };
  }

  function fillForm(s) {
    $('server').value = s.server;
    $('ruleset').value = s.ruleset;
    $('logPath').value = s.logPath;
    $('followNewest').checked = s.followNewest;
    $('includeExisting').checked = s.includeExisting;
    updateLogLabel();
  }

  function updateLogLabel() {
    $('logLabel').textContent = $('followNewest').checked ? LOG_LABEL_FOLDER : LOG_LABEL_FILE;
  }

  function renderCapabilities() {
    $('browse').hidden = !view.canBrowse;
    $('quitBtn').hidden = !view.canQuit;
    $('keyHint').textContent = view.keySource === 'env'
      ? 'Using the key from RADAR_CLIENT_INGEST_KEY. Type a key here to use another one for this run.'
      : 'The key is kept in memory for this run only and is never saved.';
    const warning = view.warning || '';
    $('loadWarning').hidden = !warning;
    $('loadWarning').textContent = warning;
  }

  function showMessages(result) {
    $('serverMsg').textContent = result.errors.server || serverWarning($('server').value);
    $('rulesetMsg').textContent = result.errors.ruleset || '';
    $('logMsg').textContent = result.errors.logPath || '';
    const box = $('formWarnings');
    box.replaceChildren(...result.warnings.map((w) => {
      const p = document.createElement('p');
      p.textContent = w;
      return p;
    }));
  }

  function renderStatus(status) {
    const state = describeState(status);
    $('stateBadge').className = `badge ${state.tone}`;
    $('stateText').textContent = state.label;
    $('stateHint').textContent = state.hint;
    const cells = [];
    for (const [label, value] of statusRows(status, now())) {
      const dt = document.createElement('dt');
      dt.textContent = label;
      const dd = document.createElement('dd');
      dd.textContent = value;
      cells.push(dt, dd);
    }
    $('statusRows').replaceChildren(...cells);
    const log = $('eventLog');
    log.textContent = eventLines(status).join('\n');
    log.scrollTop = log.scrollHeight;
    $('startBtn').disabled = status.running;
    $('stopBtn').disabled = !status.running;
    for (const id of LOCKED_WHILE_RUNNING) $(id).disabled = status.running;
  }

  function renderLostContact() {
    $('stateBadge').className = 'badge error';
    $('stateText').textContent = 'Lost contact with radar-client';
  }

  async function refreshStatus() {
    const res = await api('GET', '/api/status');
    if (res.ok) renderStatus(res.data);
    else renderLostContact();
  }

  async function onStart() {
    $('formMsg').textContent = '';
    const values = readForm();
    const result = validateForm(values, { keySource: view.keySource });
    showMessages(result);
    if (!result.ok) return;
    const res = await api('POST', '/api/start', startPayload(values));
    if (!res.ok) {
      $('formMsg').textContent = res.data.message || 'Could not start.';
      return;
    }
    $('key').value = ''; // the server holds the key in memory now
    if (res.data.warning) $('formMsg').textContent = res.data.warning;
    renderStatus(res.data);
    await loadSettings();
  }

  async function onStop() {
    const res = await api('POST', '/api/stop');
    if (res.ok) renderStatus(res.data);
  }

  async function onBrowse() {
    const res = await api('POST', '/api/pick-file', { current: $('logPath').value });
    if (res.ok && res.data.path) {
      $('logPath').value = res.data.path;
    } else if (res.status === 501) {
      $('browse').hidden = true;
      $('logMsg').textContent = res.data.message;
    } else if (!res.ok) {
      $('logMsg').textContent = res.data.message || 'The file dialog failed.';
    }
  }

  async function onQuit() {
    await api('POST', '/api/quit');
    $('stateBadge').className = 'badge idle';
    $('stateText').textContent = 'radar-client has closed. You can close this tab.';
  }

  async function loadSettings() {
    const res = await api('GET', '/api/settings');
    if (!res.ok) return;
    view = res.data;
    renderCapabilities();
    return res.data;
  }

  async function init() {
    const settings = await loadSettings();
    if (settings) fillForm(settings);
    $('startBtn').addEventListener('click', onStart);
    $('stopBtn').addEventListener('click', onStop);
    $('browse').addEventListener('click', onBrowse);
    $('quitBtn').addEventListener('click', onQuit);
    $('followNewest').addEventListener('change', updateLogLabel);
    $('server').addEventListener('input', () => showMessages(validateForm(readForm(), { keySource: view.keySource })));
    await refreshStatus();
    timers.setInterval(refreshStatus, POLL_MS);
  }

  return { init };
}

if (typeof document !== 'undefined') {
  createApp({
    document,
    fetch: (path, init) => fetch(path, init),
    timers: { setInterval: (fn, ms) => setInterval(fn, ms) },
    now: () => Date.now(),
  }).init();
}
