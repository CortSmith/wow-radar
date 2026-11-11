'use strict';
/**
 * engine-client.js - single responsibility: a promise-based client for the engine message protocol (see worker.js),
 * either over a real Web Worker or in-process.
 *
 * Public API (Radar.engineClient):
 *  - WorkerClient(worker): call(msg, transfer?) -> Promise<reply>, terminate()
 *  - LocalClient(handler): same interface around engine.createHandler (tests, browsers without Worker)
 * Both reject with Error(reply.error) when the engine answers {type:"error"}. No DOM.
 */
(function (g) {
  const M =
    typeof require !== 'undefined' && typeof module !== 'undefined' ? require('./messages.js') : g.Radar.messages;

  class WorkerClient {
    /**
     * Wrap a Worker: replies are matched to requests by id; a worker error rejects every waiting call.
     * @param {Object} worker Worker
     */
    constructor(worker) {
      this.worker = worker;
      this.seq = 0;
      this.waiting = new Map();
      worker.onmessage = e => this.onReply(e.data);
      worker.onerror = e => this.failAll(new Error(e.message || M.workerFailed));
    }
    onReply(msg) {
      const pending = this.waiting.get(msg.id);
      if (!pending) return;
      this.waiting.delete(msg.id);
      if (msg.type === 'error') pending.reject(new Error(msg.error));
      else pending.resolve(msg);
    }
    failAll(err) {
      for (const p of this.waiting.values()) p.reject(err);
      this.waiting.clear();
    }
    /**
     * Send one request to the engine and resolve with its reply.
     * @param {Object} msg the msg
     * @param {Object} transfer the transfer
     * @returns {*} the computed value
     */
    call(msg, transfer) {
      return new Promise((resolve, reject) => {
        msg.id = ++this.seq;
        this.waiting.set(msg.id, { resolve, reject });
        this.worker.postMessage(msg, transfer || []);
      });
    }
    /**
     * Stop the engine and release its resources.
     */
    terminate() {
      this.worker.terminate();
    }
  }

  class LocalClient {
    /**
     * Wrap an in-process protocol handler (tests, browsers without Worker) with the same call() contract.
     * @param {Function} handle message handler
     */
    constructor(handle) {
      this.handle = handle;
      this.seq = 0;
    }
    /**
     * Send one request to the engine and resolve with its reply.
     * @param {Object} msg the msg
     * @returns {Promise<*>} resolves with the value
     */
    async call(msg) {
      msg.id = ++this.seq;
      const reply = this.handle(msg);
      if (reply && reply.type === 'error') throw new Error(reply.error);
      return reply;
    }
    terminate() {}
  }

  const api = { WorkerClient, LocalClient };
  (g.Radar || (g.Radar = {})).engineClient = api;
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
})(typeof self !== 'undefined' ? self : globalThis);
