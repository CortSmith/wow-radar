'use strict';
/**
 * adapters.js - single responsibility: the only place that touches browser platform APIs (localStorage, fetch,
 * File System Access, crypto, Worker, confirm). Every call is wrapped so a blocked or missing API degrades to a
 * harmless default instead of throwing into UI code.
 *
 * Public API (Radar.adapters):
 *  - storage.read(key, fallback) / write(key, value) / remove(key)       localStorage, never throws
 *  - httpFetch(path, options) -> Promise<Response>                        fetch, injected into http.makeApi
 *  - loadStaticJson(url) -> Promise<object|null>                          GET a static file; null on any failure
 *  - sha256(bytes) -> Promise<hex>                                        Web Crypto, with a non-secure-context
 * fallback
 *  - canPickDirectory(), pickLogDirectory() -> handle|null, listCombatLogs(dirHandle) -> [{name,lastModified,getFile}]
 *  - createEngineClient({logClock, mode, manual, timezone}) -> client              Worker, or in-process when Workers are
 * unavailable
 *  - confirmAction(text) -> boolean
 */
(function (g) {
  const engineClient = g.Radar.engineClient;
  const COMBAT_LOG_RE = /^WoWCombatLog.*\.txt$/i;
  const HEX_RADIX = 16,
    BYTE_HEX_DIGITS = 2,
    WORD_HEX_DIGITS = 8,
    HASH_REPEAT = 4;
  const FNV_OFFSET = 0x811c9dc5,
    FNV_PRIME = 16777619,
    MIX_SEED = 0x1234567,
    MIX_PRIME = 2246822519;

  const storage = {
    read(key, fallback) {
      try {
        const v = g.localStorage.getItem(key);
        return v === null ? fallback : v;
      } catch (e) {
        return fallback;
      }
    },
    write(key, value) {
      try {
        g.localStorage.setItem(key, value);
      } catch (e) {
        /* storage blocked: the feature simply is not persisted */
      }
    },
    remove(key) {
      try {
        g.localStorage.removeItem(key);
      } catch (e) {
        /* see write() */
      }
    },
  };

  const httpFetch = (path, options) => g.fetch(path, options);

  async function loadStaticJson(url) {
    try {
      return await (await g.fetch(url)).json();
    } catch (e) {
      return null;
    }
  }

  const toHex = bytes => Array.from(bytes, b => b.toString(HEX_RADIX).padStart(BYTE_HEX_DIGITS, '0')).join('');

  /**
   * Non-secure contexts (plain http on a LAN) have no crypto.subtle; a 64 bit FNV-style mix is enough to identify a
   * log.
   * @param {Object} u8 the u8
   * @returns {*} the computed value
   */
  function weakHash(u8) {
    let h1 = FNV_OFFSET,
      h2 = MIX_SEED;
    for (let i = 0; i < u8.length; i++) {
      h1 = Math.imul(h1 ^ u8[i], FNV_PRIME);
      h2 = Math.imul(h2 ^ u8[i], MIX_PRIME) ^ (h1 >>> 7);
    }
    return (
      (h1 >>> 0).toString(HEX_RADIX).padStart(WORD_HEX_DIGITS, '0') +
      (h2 >>> 0).toString(HEX_RADIX).padStart(WORD_HEX_DIGITS, '0')
    ).repeat(HASH_REPEAT);
  }
  async function sha256(u8) {
    if (g.crypto && g.crypto.subtle)
      return toHex(new Uint8Array(await g.crypto.subtle.digest(g.Radar.config.ping.HASH_ALGORITHM, u8)));
    return weakHash(u8);
  }

  const canPickDirectory = () => !!g.showDirectoryPicker;
  async function pickLogDirectory() {
    try {
      return await g.showDirectoryPicker({ mode: 'read' });
    } catch (e) {
      return null;
    } // the operator cancelled the picker
  }
  async function listCombatLogs(dir) {
    const out = [];
    for await (const h of dir.values()) {
      if (h.kind !== 'file' || !COMBAT_LOG_RE.test(h.name)) continue;
      const file = await h.getFile();
      out.push({ name: h.name, lastModified: file.lastModified, getFile: () => h.getFile() });
    }
    return out;
  }

  /**
   * Start an engine in a Worker (falling back to the in-process protocol handler) and send it init + config; the
   * worker holds the parse state of one log file.
   * @param {Object} opts options
   * @returns {*} the computed value
   */
  function createEngineClient(opts) {
    let client;
    try {
      client = new engineClient.WorkerClient(new g.Worker('worker.js'));
    } catch (e) {
      client = new engineClient.LocalClient(g.Radar.engineProtocol.createHandler(new TextDecoder()));
    }
    client.call({ type: 'init', logClock: !!opts.logClock });
    client.call({ type: 'config', mode: opts.mode, manual: opts.manual, timezone: opts.timezone });
    return client;
  }

  const confirmAction = text => g.confirm(text);

  (g.Radar || (g.Radar = {})).adapters = {
    storage,
    httpFetch,
    loadStaticJson,
    sha256,
    canPickDirectory,
    pickLogDirectory,
    listCombatLogs,
    createEngineClient,
    confirmAction,
  };
})(typeof self !== 'undefined' ? self : globalThis);
