'use strict';
/**
 * tail.js - single responsibility: follow the newest WoWCombatLog*.txt of a folder and ping the cloud on an interval.
 *
 * Public API (Radar.tail): LiveSession(deps)
 *   deps: {client, api, sha256, outbox, listLogs() -> [{name,lastModified,getFile()}], status(text), now(),
 * intervalSec()}
 *   sweep()  one step: pick the newest log, feed new bytes, upload when due (overlapping calls are ignored)
 *   stop()  final read + upload
 * Fields read by the UI: cur (current log entry), lastError. No DOM; the folder listing is injected (adapters.js
 * supplies it).
 */
(function (g) {
  const dep = (name, file) =>
    typeof require !== 'undefined' && typeof module !== 'undefined' ? require(file) : g.Radar[name];
  const { fingerprint, loadLedger } = dep('ledger', './ledger.js');
  const { flushAndUpload, headBytes } = dep('ping', './ping.js');
  const M = dep('messages', './messages.js');
  const CFG = dep('config', './config.js');
  const UP = CFG.ping;
  const MS_PER_SEC = CFG.units.MS_PER_SEC;
  const SLICE = UP.SLICE_BYTES,
    HEAD = UP.HEAD_BYTES,
    MAX_PENDING_FIXES = UP.MAX_PENDING_FIXES;

  /**
   * Newest log wins; equal modification times fall back to the name so the choice is stable.
   * @param {Array} logs log entries
   * @returns {*} the computed value
   */
  function pickNewest(logs) {
    let newest = logs[0];
    for (const l of logs)
      if (l.lastModified > newest.lastModified || (l.lastModified === newest.lastModified && l.name > newest.name))
        newest = l;
    return newest;
  }

  class LiveSession {
    /**
     * Create a session over the injected engine client, api, outbox and folder listing; lastUpload starts now.
     * @param {Object} deps the deps
     */
    constructor(deps) {
      this.d = deps;
      this.cur = null;
      this.offset = 0;
      this.fp = null;
      this.lastUpload = this.now();
      this.busy = false;
      this.finishing = false;
      this.uploaded = { received: 0, changed: 0 };
      this.lastError = '';
    }
    /**
     * Current time in unix ms (injectable for tests).
     * @returns {*} the computed value
     */
    now() {
      return (this.d.now || Date.now)();
    }
    /**
     * Forward a status text to the UI callback, if any.
     * @param {number} t time in unix ms
     */
    status(t) {
      if (this.d.status) this.d.status(t);
    }
    /**
     * The ledger fingerprint needs the first 64 KB; a younger log has no stable identity yet (unless it is being
     * finished).
     * @param {string} file log file
     * @returns {Promise<*>} resolves with the value
     */
    async ensureFp(file) {
      if (this.fp) return this.fp;
      if (file.size < HEAD && !this.finishing) return null;
      this.fp = await fingerprint(await headBytes(file), file.name, this.d.sha256);
      return this.fp;
    }
    /**
     * Begin a log file: fingerprint, ledger watermarks, engine begin.
     * @param {Object} entry log entry
     * @returns {Promise<void>} resolves when done
     */
    async startFile(entry) {
      this.cur = entry;
      this.offset = 0;
      this.fp = null;
      this.finishing = false;
      const file = await entry.getFile();
      let watermarks = {};
      if (await this.ensureFp(file)) {
        try {
          watermarks = await loadLedger(this.d.api, this.fp);
        } catch (e) {
          this.lastError = e.message;
        }
      }
      await this.d.client.call({ type: 'begin', file: entry.name, wm: watermarks });
    }
    /**
     * Flush the engine into the outbox and upload it.
     * @param {boolean} final this is the last flush
     * @returns {Promise<void>} resolves when done
     */
    async upload(final) {
      if (!this.cur) return;
      if (final) this.finishing = true;
      const file = await this.cur.getFile();
      if (!(await this.ensureFp(file))) {
        this.status(M.waitingForHead);
        return;
      }
      try {
        const r = await flushAndUpload(this.d.client, this.d.api, this.d.outbox, this.fp, t => this.status(t));
        this.uploaded.received += r.received;
        this.uploaded.changed += r.changed;
        this.lastError = '';
        this.status(M.uploaded(r.received, r.changed));
      } catch (e) {
        this.lastError = e.message;
        this.status(M.uploadRetry(e));
      }
      this.lastUpload = this.now();
    }
    /**
     * Finish the current log and start the newer one.
     * @param {Object} newest the newest
     * @returns {Promise<void>} resolves when done
     */
    async switchTo(newest) {
      if (this.cur) {
        await this.readNew(true);
        await this.upload(true);
      }
      await this.startFile(newest);
    }
    /**
     * Uploads when the interval elapsed and there is something to send.
     * @returns {Promise<void>} resolves when done
     */
    async uploadIfDue() {
      if (this.now() - this.lastUpload < this.d.intervalSec() * MS_PER_SEC) return;
      const p = await this.d.client.call({ type: 'pending' });
      if (p.players > 0 || this.d.outbox.length) await this.upload(false);
      else this.lastUpload = this.now();
    }
    /**
     * One live step: pick the newest log, feed new bytes, upload when due.
     * @returns {Promise<void>} resolves when done
     */
    async sweep() {
      if (this.busy) return;
      this.busy = true;
      try {
        const logs = await this.d.listLogs();
        if (!logs.length) {
          this.status(M.noLogsYet);
          return;
        }
        const newest = pickNewest(logs);
        if (!this.cur || this.cur.name !== newest.name) await this.switchTo(newest);
        if (await this.readNew(false)) this.status(M.liveFile(this.cur.name));
        await this.uploadIfDue();
      } catch (e) {
        this.lastError = e.message;
        this.status(M.liveError(e));
      } finally {
        this.busy = false;
      }
    }
    /**
     * Feeds bytes appended since the last call, at most LIVE_SLICES_PER_SWEEP slices. Returns true when anything was
     * read.
     * @param {boolean} last this is the last chunk
     * @returns {Promise<*>} resolves with the value
     */
    async readNew(last) {
      const file = await this.cur.getFile();
      if (file.size < this.offset) {
        await this.startFile(this.cur);
        return true;
      } // truncated or rewritten
      let grew = false,
        budget = UP.LIVE_SLICES_PER_SWEEP;
      while (this.offset < file.size && budget-- > 0) {
        const end = Math.min(file.size, this.offset + SLICE);
        const buf = await file.slice(this.offset, end).arrayBuffer();
        this.offset = end;
        const ack = await this.d.client.call({ type: 'chunk', buf, last: last && end >= file.size }, [buf]);
        grew = true;
        if (ack.pendingFixes > MAX_PENDING_FIXES) await this.upload(false);
      }
      if (last && !grew) await this.d.client.call({ type: 'chunk', buf: new ArrayBuffer(0), last: true });
      return grew;
    }
    /**
     * Stop tailing: read what is left of the current log and upload it (final flush).
     * @returns {Promise<void>} resolves when done
     */
    async stop() {
      if (this.cur) {
        try {
          await this.readNew(true);
        } catch (e) {
          this.lastError = e.message;
        }
        await this.upload(true);
      }
    }
  }

  const api = { LiveSession };
  (g.Radar || (g.Radar = {})).tail = api;
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
})(typeof self !== 'undefined' ? self : globalThis);
