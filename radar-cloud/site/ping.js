'use strict';
/**
 * ping.js - single responsibility: manual upload of one whole combat-log file (parse in the engine, ping the cloud).
 *
 * Flow: fingerprint the file -> load the ledger watermarks -> stream 4 MB slices to the engine -> flush the engine's
 * deltas into the outbox -> drain the outbox to POST /api/upload. Flushing early (MAX_PENDING_FIXES) bounds memory on
 * huge logs.
 * Public API (Radar.ping):
 *  - ingestFile(file, deps, opts) -> Promise<{fp, received, changed, firstT, lastT}>
 *      deps: {client, api, sha256, outbox, status(text), progress(fraction)}; opts: {ignoreLedger}
 *  - flushAndUpload(client, api, outbox, fp, status) -> Promise<{received, changed}>   shared with tail.js
 *  - headBytes(file) -> Promise<Uint8Array>   the bytes that identify a log
 * No DOM; client and api are injected.
 */
(function (g) {
  const dep = (name, file) =>
    typeof require !== 'undefined' && typeof module !== 'undefined' ? require(file) : g.Radar[name];
  const { fingerprint, loadLedger } = dep('ledger', './ledger.js');
  const { mb } = dep('format', './format.js');
  const M = dep('messages', './messages.js');
  const UP = dep('config', './config.js').ping;
  const SLICE = UP.SLICE_BYTES,
    HEAD = UP.HEAD_BYTES,
    MAX_PENDING_FIXES = UP.MAX_PENDING_FIXES;

  async function headBytes(file) {
    return new Uint8Array(await file.slice(0, HEAD).arrayBuffer());
  }

  /**
   * Move whatever the engine accumulated into the outbox and upload the outbox.
   * @param {{call: function(Object): Promise<Object>}} client  engine client
   * @param {function} api
   * @param {Outbox} outbox
   * @param {string} fp  file fingerprint
   * @param {function(string): void} [status]
   * @returns {Promise<{received: number, changed: number}>}
   */
  async function flushAndUpload(client, api, outbox, fp, status) {
    const r = await client.call({ type: 'flush' });
    if (r.batches.length) outbox.add(fp, r.batches);
    return outbox.drain(api, (n, t, b) => status && status(M.uploading(b.rs, n, t)));
  }

  /**
   * Streams the file through the engine slice by slice; flushes early when too many fixes are pending.
   * @param {string} file log file
   * @param {Object} deps the deps
   * @param {string} fp file fingerprint
   * @param {Function} status the status
   * @param {Function} progress the progress
   * @returns {Promise<*>} resolves with the value
   */
  async function parseFile(file, deps, fp, status, progress) {
    const { client, api, outbox } = deps;
    let received = 0,
      changed = 0,
      span = { firstT: 0, lastT: 0 };
    for (let off = 0; off < file.size || off === 0; off += SLICE) {
      const end = Math.min(file.size, off + SLICE),
        isLast = end >= file.size;
      const buf = await file.slice(off, end).arrayBuffer();
      const ack = await client.call({ type: 'chunk', buf, last: isLast }, [buf]);
      span = { firstT: ack.firstT, lastT: ack.lastT };
      progress(file.size ? end / file.size : 1);
      status(M.parsing(file.name, mb(end), mb(file.size), ack.pendingPlayers));
      if (ack.pendingFixes > MAX_PENDING_FIXES && !isLast) {
        const r = await flushAndUpload(client, api, outbox, fp, status);
        received += r.received;
        changed += r.changed;
      }
      if (isLast) break;
    }
    return { received, changed, span };
  }

  /**
   * Parse and upload one whole log file (the ledger makes re-uploads free).
   * @param {{name: string, size: number, slice: function(number, number): Blob}} file
   * @param {{client: Object, api: function, sha256: function, outbox: Outbox, status?: function(string), progress?:
   * function(number)}} deps
   * @param {{ignoreLedger?: boolean}} [opts]
   * @returns {Promise<{fp: string, received: number, changed: number, firstT: number, lastT: number}>} firstT/lastT: UTC
   *   unix ms of the oldest and newest event in the file (0 when it had none)
   */
  async function ingestFile(file, deps, opts) {
    opts = opts || {};
    const { client, api, sha256, outbox } = deps;
    const status = deps.status || (() => {}),
      progress = deps.progress || (() => {});
    const fp = await fingerprint(await headBytes(file), file.name, sha256);
    status(M.checkingLedger(file.name));
    const watermarks = opts.ignoreLedger ? {} : await loadLedger(api, fp);
    await client.call({ type: 'begin', file: file.name, wm: watermarks, bountyOnly: false });
    const parsed = await parseFile(file, deps, fp, status, progress);
    const rest = await flushAndUpload(client, api, outbox, fp, status);
    return {
      fp,
      received: parsed.received + rest.received,
      changed: parsed.changed + rest.changed,
      firstT: parsed.span.firstT,
      lastT: parsed.span.lastT,
    };
  }

  const api = { ingestFile, flushAndUpload, headBytes, SLICE, HEAD };
  (g.Radar || (g.Radar = {})).ping = api;
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
})(typeof self !== 'undefined' ? self : globalThis);
