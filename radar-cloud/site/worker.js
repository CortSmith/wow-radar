'use strict';
/**
 * worker.js - single responsibility: run the engine (parser + tracker) inside a Web Worker and relay messages.
 * All logic lives in engine.js createHandler(), which is also used in-process and by the tests.
 *
 * Public API: the worker message protocol, the single documentation of it. Every request is {type, id, ...}; the
 * reply carries
 * the same id (engine-client.js matches them). Field names are frozen: tests and the client assert on them.
 *
 *  request                                          reply
 *  {type:"init", logClock?:boolean}                 {type:"ready"}   live engines use the wall clock; bulk engines
 *                                                   (logClock:true) use the log's own clock so nothing expires on
 * replay
 *  {type:"config", mode:"auto"|"manual", manual,  {type:"ok"}      rule-set choice ("manual" is a rule-set id);
 *   timezone?}                                                      timezone = IANA zone applied to log lines
 *                                                                   without an offset (default UTC)
 *  {type:"begin", file, wm:{rs:lastTs}, bountyOnly?}{type:"ok"}      start a log file; wm = ledger watermarks, older
 * events are skipped
 *  {type:"chunk", buf:ArrayBuffer, last:boolean}    {type:"ack", lines, pendingPlayers, pendingFixes, ruleset,
 * firstT, lastT}   buf is transferred; firstT/lastT = UTC unix ms span of the current file (0: none)
 *  {type:"snap"}                                    {type:"snap", snap:Snapshot}     live view for the radar
 *  {type:"flush"}                                   {type:"flushed", batches:UploadBatch[], ruleset}   deltas to
 * upload; watermarks advance
 *  {type:"pending"}                                 {type:"pending", players, fixes}  how much is waiting for a flush
 *  any failing request                              {type:"error", error:string}
 * ruleset = {active, mode, detected}. Types are documented in types.js.
 */
importScripts('config.js', 'time-zone.js', 'parser.js', 'tracker.js', 'analyzer.js', 'engine.js', 'engine-protocol.js');
const handle = self.Radar.engineProtocol.createHandler(new TextDecoder('utf-8'));
onmessage = e => {
  const reply = handle(e.data);
  if (reply) postMessage(reply);
};
