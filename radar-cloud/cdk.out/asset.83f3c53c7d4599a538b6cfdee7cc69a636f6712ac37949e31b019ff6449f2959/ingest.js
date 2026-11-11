"use strict";
// DOM-free ingest flows: manual file upload (parse whole file, ledger flow) and live tailing of the newest combat log.
// Talks to the parser/tracker engine through a "client" (Worker or in-process handler) and to the cloud through api().
(function (g) {
  const C = typeof require !== "undefined" && typeof module !== "undefined" ? require("./core.js") : g.RadarCore;
  const SLICE = 4 * 1024 * 1024, HEAD = 65536, MAX_PENDING_FIXES = 150000;

  // Request/response wrapper around a real Worker.
  class WorkerClient {
    constructor(worker) {
      this.w = worker; this.seq = 0; this.waiting = new Map(); this.onSnap = null;
      worker.onmessage = (e) => {
        const m = e.data, p = this.waiting.get(m.id);
        if (!p) return;
        this.waiting.delete(m.id);
        if (m.type === "error") p.reject(new Error(m.error)); else p.resolve(m);
      };
      worker.onerror = (e) => { for (const p of this.waiting.values()) p.reject(new Error(e.message || "Worker failed")); this.waiting.clear(); };
    }
    call(msg, transfer) {
      return new Promise((resolve, reject) => { msg.id = ++this.seq; this.waiting.set(msg.id, { resolve, reject }); this.w.postMessage(msg, transfer || []); });
    }
    terminate() { this.w.terminate(); }
  }
  // In-process client around tracker.js createHandler (tests / no-Worker fallback).
  class LocalClient {
    constructor(handle) { this.handle = handle; this.seq = 0; }
    async call(msg) {
      msg.id = ++this.seq;
      const r = this.handle(msg);
      if (r && r.type === "error") throw new Error(r.error);
      return r;
    }
    terminate() {}
  }

  async function headBytes(file) { return new Uint8Array(await file.slice(0, HEAD).arrayBuffer()); }

  // Uploads whatever the engine has accumulated (flush -> outbox -> cloud). Returns {received, changed}.
  async function flushAndUpload(client, api, outbox, fp, status) {
    const r = await client.call({ type: "flush" });
    if (r.batches.length) outbox.add(fp, r.batches);
    return outbox.drain(api, (n, t, b) => status && status("Uploading " + b.rs + ": request " + n + "/" + t));
  }

  // Manual upload of one whole file. deps: {client, api, sha256, outbox, status(text), progress(frac)}
  async function ingestFile(file, deps, opts) {
    opts = opts || {};
    const { client, api, sha256, outbox } = deps;
    const status = deps.status || (() => {}), progress = deps.progress || (() => {});
    const fp = await C.fingerprint(await headBytes(file), file.name, sha256);
    status("Checking ledger for " + file.name + "...");
    const wm = opts.ignoreLedger ? {} : await C.loadLedger(api, fp);
    await client.call({ type: "begin", file: file.name, wm, bountyOnly: false });
    let received = 0, changed = 0;
    for (let off = 0; off < file.size || off === 0; off += SLICE) {
      const end = Math.min(file.size, off + SLICE), last = end >= file.size;
      const buf = await file.slice(off, end).arrayBuffer();
      const ack = await client.call({ type: "chunk", buf, last }, [buf]);
      progress(file.size ? end / file.size : 1);
      status("Parsing " + file.name + ": " + C.mb(end) + "/" + C.mb(file.size) + " MB, " + ack.pendingPlayers + " players pending");
      if (ack.pendingFixes > MAX_PENDING_FIXES && !last) {
        const r = await flushAndUpload(client, api, outbox, fp, status);
        received += r.received; changed += r.changed;
      }
      if (last) break;
    }
    const r = await flushAndUpload(client, api, outbox, fp, status);
    received += r.received; changed += r.changed;
    return { fp, received, changed };
  }

  // Live tailing. deps: {client, api, sha256, outbox, listLogs() -> [{name,lastModified,getFile()}], status, now(), intervalMin() }
  class LiveSession {
    constructor(deps) {
      this.d = deps; this.cur = null; this.offset = 0; this.fp = null; this.lastUpload = (deps.now || Date.now)(); this.running = false; this.busy = false;
      this.uploaded = { received: 0, changed: 0 }; this.lastError = "";
    }
    now() { return (this.d.now || Date.now)(); }
    status(t) { if (this.d.status) this.d.status(t); }
    async ensureFp(file) {
      if (this.fp) return this.fp;
      if (file.size < HEAD && !this.finishing) return null; // the first 64 KB are not stable yet
      this.fp = await C.fingerprint(await headBytes(file), file.name, this.d.sha256);
      return this.fp;
    }
    async startFile(entry) {
      this.cur = entry; this.offset = 0; this.fp = null; this.finishing = false;
      const file = await entry.getFile();
      let wm = {};
      if (await this.ensureFp(file)) { try { wm = await C.loadLedger(this.d.api, this.fp); } catch (e) { this.lastError = e.message; } }
      await this.d.client.call({ type: "begin", file: entry.name, wm });
    }
    async upload(final) {
      if (!this.cur) return;
      if (final) this.finishing = true;
      const file = await this.cur.getFile();
      if (!(await this.ensureFp(file))) { this.status("Waiting for the log to reach 64 KB before uploading"); return; }
      try {
        const r = await flushAndUpload(this.d.client, this.d.api, this.d.outbox, this.fp, (t) => this.status(t));
        this.uploaded.received += r.received; this.uploaded.changed += r.changed; this.lastError = "";
        this.status("Uploaded " + r.received + " players (" + r.changed + " new or updated)");
      } catch (e) { this.lastError = e.message; this.status("Upload failed, will retry: " + e.message); }
      this.lastUpload = this.now();
    }
    // One poll step: pick the newest log, feed new bytes, upload when due. Safe to call from a timer; overlapping calls are ignored.
    async poll() {
      if (this.busy) return; this.busy = true;
      try {
        const logs = await this.d.listLogs();
        if (!logs.length) { this.status("No WoWCombatLog*.txt in the chosen folder yet"); return; }
        let newest = logs[0];
        for (const l of logs) if (l.lastModified > newest.lastModified || (l.lastModified === newest.lastModified && l.name > newest.name)) newest = l;
        if (!this.cur || this.cur.name !== newest.name) {
          if (this.cur) { await this.readNew(true); await this.upload(true); }
          await this.startFile(newest);
        }
        const grew = await this.readNew(false);
        if (grew) this.status("Live: " + this.cur.name);
        if (this.now() - this.lastUpload >= this.d.intervalMin() * 60000) {
          const p = await this.d.client.call({ type: "pending" });
          if (p.players > 0 || this.d.outbox.length) await this.upload(false); else this.lastUpload = this.now();
        }
      } catch (e) { this.lastError = e.message; this.status("Live error: " + e.message); }
      finally { this.busy = false; }
    }
    async readNew(last) {
      const file = await this.cur.getFile();
      if (file.size < this.offset) { await this.startFile(this.cur); return true; } // truncated / rewritten
      let grew = false, budget = 4;
      while (this.offset < file.size && budget-- > 0) {
        const end = Math.min(file.size, this.offset + SLICE);
        const buf = await file.slice(this.offset, end).arrayBuffer();
        this.offset = end;
        const ack = await this.d.client.call({ type: "chunk", buf, last: last && end >= file.size }, [buf]);
        grew = true;
        if (ack.pendingFixes > MAX_PENDING_FIXES) await this.upload(false);
      }
      if (last && !grew) await this.d.client.call({ type: "chunk", buf: new ArrayBuffer(0), last: true });
      return grew;
    }
    async stop() { // final read + upload
      if (this.cur) { try { await this.readNew(true); } catch (e) { this.lastError = e.message; } await this.upload(true); }
      this.running = false;
    }
  }

  const api = { WorkerClient, LocalClient, ingestFile, LiveSession, flushAndUpload, SLICE, HEAD };
  g.RadarIngest = api;
  if (typeof module !== "undefined" && module.exports) module.exports = api;
})(typeof self !== "undefined" ? self : globalThis);
