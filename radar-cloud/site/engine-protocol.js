'use strict';
/**
 * engine-protocol.js - single responsibility: the engine's message protocol (request -> reply), independent of
 * transport.
 *
 * Public API (Radar.engineProtocol): createHandler(decoder?) -> handle(request) -> reply|null. The same handler runs
 * inside the Worker (worker.js documents every message) and in-process (engine-client LocalClient, tests). No DOM.
 */
(function (g) {
  const dep = (name, file) =>
    typeof require !== 'undefined' && typeof module !== 'undefined' ? require(file) : g.Radar[name];
  const { Engine } = dep('engine', './engine.js');

  /**
   * Text decoder honouring an injected streaming decoder (multi-byte characters may straddle chunks).
   * @param {TextDecoder|undefined} dec injected decoder
   * @returns {function((ArrayBuffer|Uint8Array|string), boolean): string} decode(buf, stream)
   */
  function makeDecode(dec) {
    return (buf, stream) => {
      if (dec) return dec.decode(buf, { stream });
      return typeof buf === 'string' ? buf : new TextDecoder('utf-8').decode(buf, { stream });
    };
  }

  /**
   * The {type:"ack"} reply of a chunk: parse progress, what is waiting for a flush, and the span of the current file
   * (firstT/lastT: UTC unix ms of its oldest and newest parsed event, 0 when none yet).
   * @param {Object} engine engine after feeding the chunk
   * @param {number} id request id
   * @returns {Object} ack message
   */
  function ackReply(engine, id) {
    const pending = engine.pending();
    return {
      type: 'ack',
      id,
      lines: engine.lines,
      pendingPlayers: pending.players,
      pendingFixes: pending.fixes,
      ruleset: engine.rulesetInfo(),
      firstT: engine.fileFirstT,
      lastT: engine.fileLastT,
    };
  }

  /**
   * Build the message handler of the engine protocol (documented in worker.js); shared by the Worker and the tests.
   * @param {TextDecoder} [dec] streaming decoder
   * @returns {function(Object): (Object|null)} handle(request) -> reply; failures become {type:"error"} replies
   */
  function createHandler(dec) {
    let engine = null;
    const decode = makeDecode(dec);
    const start = request => {
      const useLogClock = !!(request && request.logClock);
      engine = new Engine({ clock: useLogClock ? () => (engine ? engine.maxT : 0) : Date.now });
    };
    // One handler per request type; a handler returns its reply, or nothing for the plain {type:"ok"} answer.
    const handlers = {
      config: m => engine.setConfig(m),
      begin: m => engine.begin(m),
      chunk: m => {
        engine.feedText(decode(m.buf, !m.last), !!m.last);
        return ackReply(engine, m.id);
      },
      snap: m => ({ type: 'snap', id: m.id, snap: engine.snapshot() }),
      flush: m => ({ type: 'flushed', id: m.id, batches: engine.flush(), ruleset: engine.rulesetInfo() }),
      pending: m => Object.assign({ type: 'pending', id: m.id }, engine.pending()),
    };
    return function handle(m) {
      try {
        if (m.type === 'init') {
          start(m);
          return { type: 'ready', id: m.id };
        }
        if (!engine) start({});
        const handler = handlers[m.type];
        return (handler && handler(m)) || { type: 'ok', id: m.id };
      } catch (err) {
        return { type: 'error', id: m.id, error: String((err && err.message) || err) };
      }
    };
  }

  const api = { createHandler };
  (g.Radar || (g.Radar = {})).engineProtocol = api;
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
})(typeof self !== 'undefined' ? self : globalThis);
