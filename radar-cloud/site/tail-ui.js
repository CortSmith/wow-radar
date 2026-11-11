'use strict';
/**
 * tail-ui.js - single responsibility: the "Log source" and "Analyze logs" controls: manual file upload (choose / drop),
 * rebuilding a rule-set from chosen logs, and live tailing of the WoW Logs folder.
 *
 * Parsing runs in an engine (Worker); results are pinged to the cloud through ping.js / tail.js. This module only wires
 * buttons to those flows and shows progress through feedback.js.
 * Public API (Radar.ui.tailUi):
 *  - init(state, bus)
 *  - stopLive(silent) -> Promise   stop tailing; unless silent: final upload, then "live-stopped"
 *  - sendEngineConfig()            tell the live engine the rule-set choice and the display zone
 *  - renderUploadSpan()            show the span of the last uploaded log (operator zone and ISO UTC)
 * Emits "refresh" (reload cloud data after an upload), "snapshot" (live snapshot), "live-started", "live-stopped",
 * "engine-ruleset" (rule-set info from the engine).
 */
(function (g) {
  const radar = g.Radar,
    { $ } = radar.dom,
    M = radar.messages,
    CFG = radar.config,
    feedback = radar.ui.feedback,
    adapters = radar.adapters,
    VM = radar.viewModel;
  let st = null,
    bus = null;
  let busy = false,
    liveClient = null,
    liveSession = null,
    liveTimer = null;

  const newClient = logClock =>
    adapters.createEngineClient({
      logClock,
      mode: st.settings.mode,
      manual: st.settings.ruleset,
      timezone: st.zone,
    });
  const intervalSec = () => Math.max(CFG.pingInterval.MIN_SEC, st.settings.uploadSec || CFG.pingInterval.DEFAULT_SEC);
  const hideProgressSoon = () => setTimeout(() => feedback.progress(null), CFG.timing.PROGRESS_HIDE_MS);
  const chosenFiles = input => {
    const files = Array.from(input.files);
    input.value = '';
    return files;
  };

  function sendEngineConfig() {
    if (liveClient)
      liveClient
        .call({ type: 'config', mode: st.settings.mode, manual: st.settings.ruleset, timezone: st.zone })
        .catch(() => {
          /* engine already gone */
        });
  }

  /**
   * Show the span of the last uploaded log in the operator's zone and as ISO UTC; hidden when there is none.
   */
  function renderUploadSpan() {
    const text = VM.describeUploadSpan(st);
    $('upload-summary').textContent = text === null ? '' : text;
    $('upload-summary').hidden = text === null;
  }

  // ---------- manual upload and analyze ----------
  /**
   * Grow the remembered upload span to cover another file (times are UTC unix ms, 0 means the file had no events).
   * @param {number} firstT oldest event of the file
   * @param {number} lastT newest event of the file
   */
  function widenSpan(firstT, lastT) {
    if (!firstT) return;
    const span = st.uploadSpan || { firstT, lastT };
    st.uploadSpan = { firstT: Math.min(span.firstT, firstT), lastT: Math.max(span.lastT, lastT) };
  }

  /**
   * One engine per file keeps memory flat; the log clock makes nothing expire during a replay.
   * @param {Array} files log files
   * @param {string} label the label
   * @returns {Promise<*>} resolves with the value
   */
  async function ingestFiles(files, label) {
    let sent = 0,
      changed = 0;
    st.uploadSpan = null;
    for (const file of files) {
      const client = newClient(true);
      const status = text => {
        feedback.uploadStatus(text);
        if (label) feedback.bountyStatus(M.analyzeProgress(label, text));
      };
      try {
        const r = await radar.ping.ingestFile(
          file,
          {
            client,
            api: radar.session.api,
            sha256: adapters.sha256,
            outbox: st.outbox,
            status,
            progress: feedback.progress,
          },
          {},
        );
        sent += r.received;
        changed += r.changed;
        widenSpan(r.firstT, r.lastT);
        bus.emit('engine-ruleset', (await client.call({ type: 'flush' })).ruleset);
      } finally {
        client.terminate();
      }
    }
    renderUploadSpan();
    return { sent, changed };
  }

  /**
   * Parse and upload the chosen files one engine at a time; one run at a time (busy guard), then reload cloud data.
   * @param {Array} files log files
   * @returns {Promise<void>} resolves when done
   */
  async function uploadFiles(files) {
    if (busy || !radar.session.isSignedIn()) return;
    busy = true;
    $('pick-files').disabled = true;
    try {
      const r = await ingestFiles(files);
      feedback.uploadStatus(M.uploadDone(r.sent, r.changed));
    } catch (e) {
      feedback.uploadStatus(M.uploadError(e, st.outbox.length > 0));
    }
    busy = false;
    $('pick-files').disabled = false;
    hideProgressSoon();
    bus.emit('refresh');
  }

  /**
   * Rebuild the ruleset from the chosen logs: confirm, reset the ruleset on the server, re-ingest, then reload data.
   * @param {Array} files log files
   * @returns {Promise<void>} resolves when done
   */
  async function analyzeFiles(files) {
    const rulesetName = VM.rulesetName(VM.currentRuleset(st));
    if (!adapters.confirmAction(M.analyzeConfirm(rulesetName, files.length))) return;
    busy = true;
    $('bounty-analyze').disabled = true;
    try {
      feedback.bountyStatus(M.analyzeResetting(rulesetName));
      await radar.session.api('/api/reset', {
        method: 'POST',
        headers: radar.http.JSON_HEADERS,
        body: JSON.stringify({ rs: VM.currentRuleset(st), what: 'all' }),
      });
      const r = await ingestFiles(files, M.analyzeLabel);
      feedback.bountyStatus(M.analyzeDone(files.length, r.sent));
    } catch (e) {
      feedback.bountyStatus(M.analyzeFailed(e));
    }
    busy = false;
    $('bounty-analyze').disabled = false;
    hideProgressSoon();
    bus.emit('refresh');
  }

  /**
   * Wire the file picker, the drop zone and the Analyze logs picker.
   */
  function bindUpload() {
    $('pick-files').addEventListener('click', () => $('log-file-input').click());
    $('log-file-input').addEventListener('change', () => {
      const files = chosenFiles($('log-file-input'));
      if (files.length) uploadFiles(files);
    });
    const zone = $('drop-zone');
    for (const type of ['dragenter', 'dragover'])
      zone.addEventListener(type, e => {
        e.preventDefault();
        zone.classList.add('dragover');
      });
    for (const type of ['dragleave', 'drop'])
      zone.addEventListener(type, e => {
        e.preventDefault();
        zone.classList.remove('dragover');
      });
    zone.addEventListener('drop', e => {
      const files = Array.from(e.dataTransfer.files);
      if (files.length) uploadFiles(files);
    });
    $('bounty-analyze').addEventListener('click', () => $('bounty-files').click());
    $('bounty-files').addEventListener('change', () => {
      const files = chosenFiles($('bounty-files'));
      if (files.length && !busy && radar.session.isSignedIn()) analyzeFiles(files);
    });
  }

  // ---------- live tail ----------
  /**
   * One live step each second: sweep the log folder, then show the engine snapshot.
   * @returns {Promise<void>} resolves when done
   */
  async function liveTick() {
    if (!st.live.active) return;
    await liveSession.sweep();
    try {
      st.live.fileName = liveSession && liveSession.cur ? liveSession.cur.name : '';
      bus.emit('snapshot', (await liveClient.call({ type: 'snap' })).snap);
    } catch (e) {
      /* the engine was stopped while this tick was running */
    }
  }

  /**
   * Ask for the logs folder and start tailing it.
   * @returns {Promise<void>} resolves when done
   */
  async function startLive() {
    if (!adapters.canPickDirectory() || !radar.session.isSignedIn()) return;
    const dir = await adapters.pickLogDirectory();
    if (!dir) return;
    if (!liveClient) liveClient = newClient(false);
    sendEngineConfig();
    liveSession = new radar.tail.LiveSession({
      client: liveClient,
      api: radar.session.api,
      sha256: adapters.sha256,
      outbox: st.outbox,
      listLogs: () => adapters.listCombatLogs(dir),
      status: feedback.uploadStatus,
      intervalSec,
    });
    st.live.active = true;
    st.live.fileName = '';
    bus.emit('live-started');
    $('tail-start').hidden = true;
    $('tail-stop').hidden = false;
    feedback.connection('ok');
    feedback.uploadStatus(M.liveReading);
    liveTick();
    liveTimer = setInterval(liveTick, CFG.timing.LIVE_TICK_MS);
  }

  /**
   * Stop tailing; unless silent, do the final read and upload.
   * @param {boolean} silent skip the final upload
   * @returns {Promise<void>} resolves when done
   */
  async function stopLive(silent) {
    if (!st.live.active) return;
    st.live.active = false;
    st.live.fileName = '';
    if (liveTimer) {
      clearInterval(liveTimer);
      liveTimer = null;
    }
    $('tail-start').hidden = false;
    $('tail-stop').hidden = true;
    const session = liveSession;
    liveSession = null;
    if (session && !silent) {
      feedback.uploadStatus(M.liveStopping);
      try {
        await session.stop();
      } catch (e) {
        feedback.uploadStatus(M.uploadError(e, false));
      }
    }
    if (liveClient) {
      liveClient.terminate();
      liveClient = null;
    }
    if (!silent) bus.emit('live-stopped');
  }

  /**
   * Bind the module to the shared state (and bus) and attach its DOM listeners.
   * @param {Object} state shared UI state
   * @param {Object} eventBus event bus
   */
  function init(state, eventBus) {
    st = state;
    bus = eventBus;
    bindUpload();
    $('tail-start').addEventListener('click', startLive);
    $('tail-stop').addEventListener('click', () => stopLive(false));
    if (!adapters.canPickDirectory()) {
      $('tail-start').disabled = true;
      $('tail-start').title = M.liveNeedsChromium;
    }
    g.addEventListener('beforeunload', e => {
      if (st.live.active || st.outbox.length) {
        e.preventDefault();
        e.returnValue = '';
      }
    });
  }

  (radar.ui || (radar.ui = {})).tailUi = { init, stopLive, sendEngineConfig, renderUploadSpan };
})(typeof self !== 'undefined' ? self : globalThis);
