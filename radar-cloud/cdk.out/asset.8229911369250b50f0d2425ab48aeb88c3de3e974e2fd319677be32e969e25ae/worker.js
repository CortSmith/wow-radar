"use strict";
// Web Worker glue around parser.js + tracker.js. One worker per source: the live worker uses the wall clock,
// bulk workers (manual upload / analyze) use the log clock so nothing expires while replaying an old file.
importScripts("parser.js", "tracker.js");
let engine = null, logClock = false, lastT = 0;
const dec = new TextDecoder("utf-8");

function init(o) {
  logClock = !!(o && o.logClock);
  engine = new RadarTracker.Engine({ clock: logClock ? () => (engine ? engine.maxT : 0) : Date.now });
}

onmessage = (e) => {
  const m = e.data;
  try {
    if (m.type === "init") { init(m); postMessage({ type: "ready" }); return; }
    if (!engine) init({});
    switch (m.type) {
      case "config": engine.setConfig(m); break;
      case "begin": engine.begin(m); break;
      case "chunk": {
        engine.feedText(dec.decode(m.buf, { stream: !m.last }), !!m.last);
        const p = engine.pending();
        postMessage({ type: "ack", id: m.id, lines: engine.lines, pendingPlayers: p.players, pendingFixes: p.fixes, ruleset: engine.rulesetInfo() });
        return;
      }
      case "snap": postMessage({ type: "snap", snap: engine.snapshot() }); return;
      case "flush": postMessage({ type: "flushed", id: m.id, batches: engine.flush(), ruleset: engine.rulesetInfo() }); return;
    }
    postMessage({ type: "ok", id: m.id });
  } catch (err) {
    postMessage({ type: "error", id: m.id, error: String((err && err.message) || err) });
  }
};
