'use strict';
/**
 * ledger.js - single responsibility: resumable, deduplicated upload of flushed pings (batches of contacts).
 *
 * Why a ledger: the same combat log is often uploaded again (re-run, overlapping live sessions). The file fingerprint
 * plus
 * a per-ruleset watermark (lastTs, kept server-side) lets the engine skip every event at or before it, so a re-upload
 * adds
 * nothing. Each ping carries a batchId; a retry reuses it so the server can skip contacts it already applied.
 *
 * Public API (Radar.ledger):
 *  - fingerprint(bytes, name, sha256) -> hex id of a log file (first 64 KB + name)
 *  - loadLedger(api, fp) -> {rulesetId: lastTs}   watermarks of every ruleset
 *  - chunkPlayers(players, maxPlayers?, maxFixes?) -> players[][]   request-sized slices (a contact is never split)
 *  - uploadBatch(api, fp, batch, onProgress) -> batch   resumable; see UploadBatch in types.js
 *  - Outbox: pings waiting for upload; keeps failed ones for the next attempt
 *  - RULESET_IDS, MAX_PLAYERS_PER_REQ, MAX_FIXES_PER_REQ
 * No DOM; the api function is injected.
 */
(function (g) {
  const dep = (name, file) =>
    typeof require !== 'undefined' && typeof module !== 'undefined' ? require(file) : g.Radar[name];
  const CFG = dep('config', './config.js');
  const { qs } = dep('format', './format.js');
  const { JSON_HEADERS } = dep('http', './http.js');

  const RULESET_IDS = CFG.rulesets.LIST.map(r => r[0]);
  const MAX_PLAYERS_PER_REQ = CFG.ping.MAX_CONTACTS_PER_REQ,
    MAX_FIXES_PER_REQ = CFG.ping.MAX_FIXES_PER_REQ;
  const BATCH_ID_PREFIX_CHARS = 8,
    RANDOM_SUFFIX_CHARS = 6,
    BASE36 = 36;

  /**
   * Stable id of a log file: SHA-256 over its first 64 KB plus its name, shortened.
   * @param {Uint8Array} bytes  head of the file
   * @param {string} name
   * @param {function(Uint8Array): Promise<string>} sha256
   * @returns {Promise<string>}
   */
  async function fingerprint(bytes, name, sha256) {
    const suffix = new TextEncoder().encode('|' + name);
    const all = new Uint8Array(bytes.length + suffix.length);
    all.set(bytes, 0);
    all.set(suffix, bytes.length);
    return (await sha256(all)).slice(0, CFG.ping.FINGERPRINT_HEX);
  }

  /**
   * Ledger watermarks of every rule-set for one fingerprint (events at or before them were already uploaded).
   * @param {function(string): Promise<Object>} api
   * @param {string} fp
   * @returns {Promise<Object<string, number>>} rule-set id -> lastTs (0 when unknown)
   */
  async function loadLedger(api, fp) {
    const watermarks = {};
    await Promise.all(
      RULESET_IDS.map(async rs => {
        const d = await api('/api/ledger?' + qs({ rs, fp }));
        watermarks[rs] = Number(d && d.lastTs) || 0;
      }),
    );
    return watermarks;
  }

  /**
   * Split contacts into request-sized slices; one contact is never split across requests.
   * @param {Object[]} players
   * @param {number} [maxPlayers]
   * @param {number} [maxFixes]
   * @returns {Object[][]}
   */
  function chunkPlayers(players, maxPlayers, maxFixes) {
    maxPlayers = maxPlayers || MAX_PLAYERS_PER_REQ;
    maxFixes = maxFixes || MAX_FIXES_PER_REQ;
    const out = [];
    let cur = [],
      fixCount = 0;
    for (const p of players) {
      const n = p.fixes ? p.fixes.length : 0;
      if (cur.length && (cur.length >= maxPlayers || fixCount + n > maxFixes)) {
        out.push(cur);
        cur = [];
        fixCount = 0;
      }
      cur.push(p);
      fixCount += n;
    }
    if (cur.length) out.push(cur);
    return out;
  }

  /**
   * Id sent with every request of a ping; unique per attempt series so the server can skip contacts it already
   * applied when a request is retried.
   * @param {string} fp file fingerprint
   * @param {string} rs ruleset id
   */
  const newBatchId = (fp, rs) =>
    fp.slice(0, BATCH_ID_PREFIX_CHARS) +
    '-' +
    rs +
    '-' +
    Date.now().toString(BASE36) +
    Math.random()
      .toString(BASE36)
      .slice(2, 2 + RANDOM_SUFFIX_CHARS);

  // Resumable: on failure the batch keeps its batchId and the index of the next unsent request, so calling
  // uploadBatch again
  // retries with the SAME batchId. Chart rectangles ride on the first request only.
  /**
   * Upload one Ping (see types.js), request by request. Throws on the first failure; call again to resume.
   * @param {function(string, Object): Promise<Object>} api
   * @param {string} fp  file fingerprint
   * @param {Ping} batch  mutated: batchId, chunks, next, received, changed
   * @param {function(number, number, Ping): void} [onProgress]  (requestsDone, requestsTotal, batch)
   * @returns {Promise<Ping>}
   */
  async function uploadBatch(api, fp, batch, onProgress) {
    if (!batch.batchId) batch.batchId = newBatchId(fp, batch.rs);
    if (!batch.chunks) batch.chunks = chunkPlayers(batch.players);
    batch.next = batch.next || 0;
    batch.received = batch.received || 0;
    batch.changed = batch.changed || 0;
    while (batch.next < batch.chunks.length) {
      const i = batch.next,
        isFinal = i === batch.chunks.length - 1;
      const d = await api('/api/upload', {
        method: 'POST',
        headers: JSON_HEADERS,
        body: JSON.stringify({
          rs: batch.rs,
          fp,
          from: batch.from,
          to: batch.to,
          batchId: batch.batchId,
          final: isFinal,
          players: batch.chunks[i],
          maps: i === 0 ? batch.maps : [],
        }),
      });
      batch.received += d.received || 0;
      batch.changed += d.changed || 0;
      batch.next++;
      if (onProgress) onProgress(batch.next, batch.chunks.length, batch);
    }
    return batch;
  }

  class Outbox {
    /**
     * Empty queue; items keep their file fingerprint so each ping uploads under its own ledger entry.
     */
    constructor() {
      this.items = [];
    }
    /**
     * Queue pings for upload.
     * @param {string} fp file fingerprint
     * @param {Array} batches the batches
     */
    add(fp, batches) {
      for (const b of batches) this.items.push({ fp, batch: b });
    }
    get length() {
      return this.items.length;
    }
    /**
     * Uploads in order; a throw leaves the failing item at the head so the next drain() resumes it.
     * @param {Function} api authenticated api(path, options)
     * @param {Function} onProgress the on progress
     * @returns {Promise<*>} resolves with the value
     */
    async drain(api, onProgress) {
      let received = 0,
        changed = 0;
      while (this.items.length) {
        const it = this.items[0];
        await uploadBatch(api, it.fp, it.batch, onProgress);
        received += it.batch.received;
        changed += it.batch.changed;
        this.items.shift();
      }
      return { received, changed };
    }
  }

  const api = {
    RULESET_IDS,
    MAX_PLAYERS_PER_REQ,
    MAX_FIXES_PER_REQ,
    fingerprint,
    loadLedger,
    chunkPlayers,
    uploadBatch,
    Outbox,
  };
  (g.Radar || (g.Radar = {})).ledger = api;
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
})(typeof self !== 'undefined' ? self : globalThis);
