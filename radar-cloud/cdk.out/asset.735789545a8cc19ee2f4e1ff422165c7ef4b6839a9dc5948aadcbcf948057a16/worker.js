"use strict";
// Web Worker glue around parser.js + tracker.js (all logic lives in tracker.js createHandler so it is testable in node).
// The live worker uses the wall clock; bulk workers ({type:"init", logClock:true}) use the log clock so nothing expires on replay.
importScripts("parser.js", "tracker.js");
const handle = RadarTracker.createHandler(new TextDecoder("utf-8"));
onmessage = (e) => {
  const r = handle(e.data);
  if (r) postMessage(r);
};
