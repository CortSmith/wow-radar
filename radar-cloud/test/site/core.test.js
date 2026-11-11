"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const crypto = require("node:crypto");
const C = require("../../site/core.js");

const res = (status, body) => ({ status, ok: status >= 200 && status < 300, json: async () => { if (body === undefined) throw new Error("no json"); return body; } });
const sha256 = async (u8) => crypto.createHash("sha256").update(u8).digest("hex");

// ---- login / signup ----
test("login posts credentials (trimmed) and returns token", async () => {
  const calls = [];
  const f = async (url, opt) => { calls.push([url, opt]); return res(200, { token: "T", username: "bob" }); };
  const r = await C.authenticate(f, { signup: false, username: " bob ", password: "pw", apiKey: " KEY " });
  assert.deepEqual(r, { token: "T", username: "bob" });
  assert.equal(calls[0][0], "/api/login");
  assert.deepEqual(JSON.parse(calls[0][1].body), { username: "bob", password: "pw", apiKey: "KEY" });
});
test("signup uses /api/signup; server error, bad json and network failure surface messages", async () => {
  let url;
  await C.authenticate(async (u) => { url = u; return res(200, { token: "T" }); }, { signup: true, username: "a", password: "b", apiKey: "c" });
  assert.equal(url, "/api/signup");
  await assert.rejects(C.authenticate(async () => res(403, { error: "Key inactive" }), { username: "a", password: "b", apiKey: "c" }), /Key inactive/);
  await assert.rejects(C.authenticate(async () => res(502), { username: "a", password: "b", apiKey: "c" }), /Server error \(502\)/);
  await assert.rejects(C.authenticate(async () => { throw new Error("net"); }, { username: "a", password: "b", apiKey: "c" }), /Could not reach/);
});
test("api client sends bearer token, handles 401 and errors", async () => {
  let auth, out = 0;
  const api = C.makeApi(async (u, o) => { auth = o.headers.Authorization; return u === "/x" ? res(200, { a: 1 }) : u === "/401" ? res(401, {}) : res(500, { error: "boom" }); }, () => "TOK", () => { out++; });
  assert.deepEqual(await api("/x"), { a: 1 }); assert.equal(auth, "Bearer TOK");
  await assert.rejects(api("/401"), /Session expired/); assert.equal(out, 1);
  await assert.rejects(api("/500"), /boom/);
});
test("qs skips empty values and encodes", () => {
  assert.equal(C.qs({ rs: "pvp", q: "a b", guid: "", x: undefined }), "rs=pvp&q=a%20b");
});

// ---- ledger / upload ----
const mkPlayers = (n, fixes) => Array.from({ length: n }, (_, i) => ({ guid: "Player-" + i, name: "P" + i, fixes: Array.from({ length: fixes || 0 }, () => [0, 1, 0, 0, "exact"]) }));

test("fingerprint depends on content and file name", async () => {
  const a = new Uint8Array([1, 2, 3]);
  const f1 = await C.fingerprint(a, "WoWCombatLog_1.txt", sha256), f2 = await C.fingerprint(a, "WoWCombatLog_2.txt", sha256), f3 = await C.fingerprint(new Uint8Array([1, 2, 4]), "WoWCombatLog_1.txt", sha256);
  assert.equal(f1.length, 32); assert.notEqual(f1, f2); assert.notEqual(f1, f3);
  assert.equal(f1, await C.fingerprint(a, "WoWCombatLog_1.txt", sha256));
});

test("loadLedger asks every rule-set and defaults to 0", async () => {
  const seen = [];
  const api = async (path) => { seen.push(path); return path.includes("rs=rp&") ? { lastTs: 1234 } : {}; };
  const wm = await C.loadLedger(api, "abc");
  assert.deepEqual(wm, { normal: 0, pvp: 0, rp: 1234, rppvp: 0 });
  assert.equal(seen.length, 4); assert.ok(seen.every((p) => p.startsWith("/api/ledger?") && p.includes("fp=abc")));
});

test("chunkPlayers: <= 500 players and <= fix cap per request, never splits a player", () => {
  const ch = C.chunkPlayers(mkPlayers(1200));
  assert.deepEqual(ch.map((c) => c.length), [500, 500, 200]);
  const byFix = C.chunkPlayers(mkPlayers(10, 30), 500, 100);
  assert.deepEqual(byFix.map((c) => c.length), [3, 3, 3, 1]);
  assert.deepEqual(C.chunkPlayers([]), []);
  assert.equal(C.chunkPlayers(mkPlayers(1, 500), 500, 100).length, 1);
});

test("uploadBatch: sequential requests, final only on the last, maps once, same batchId, totals", async () => {
  const bodies = [];
  const api = async (p, o) => { const b = JSON.parse(o.body); bodies.push(b); return { received: b.players.length, changed: b.players.length - 1 }; };
  const batch = { rs: "rp", from: 5, to: 99, players: mkPlayers(1100), maps: [{ id: 1 }] };
  const prog = [];
  await C.uploadBatch(api, "f".repeat(32), batch, (n, t) => prog.push([n, t]));
  assert.equal(bodies.length, 3);
  assert.deepEqual(bodies.map((b) => b.players.length), [500, 500, 100]);
  assert.deepEqual(bodies.map((b) => b.final), [false, false, true]);
  assert.deepEqual(bodies.map((b) => b.maps.length), [1, 0, 0]);
  assert.equal(new Set(bodies.map((b) => b.batchId)).size, 1);
  assert.ok(bodies.every((b) => b.rs === "rp" && b.fp === "f".repeat(32) && b.from === 5 && b.to === 99));
  assert.equal(batch.received, 1100); assert.equal(batch.changed, 1097);
  assert.deepEqual(prog, [[1, 3], [2, 3], [3, 3]]);
});

test("uploadBatch retry resumes at the failed request with the SAME batchId", async () => {
  const ids = [], sizes = [];
  let fail = true;
  const api = async (p, o) => {
    const b = JSON.parse(o.body);
    if (b.players.length === 500 && ids.length === 1 && fail) { fail = false; throw new Error("net"); }
    ids.push(b.batchId); sizes.push(b.players.length);
    return { received: b.players.length, changed: 0 };
  };
  const batch = { rs: "pvp", from: 0, to: 1, players: mkPlayers(1100), maps: [] };
  await assert.rejects(C.uploadBatch(api, "fp12345678", batch), /net/);
  assert.equal(batch.next, 1); const id = batch.batchId;
  await C.uploadBatch(api, "fp12345678", batch);
  assert.deepEqual(sizes, [500, 500, 100]);
  assert.ok(ids.every((x) => x === id));
  assert.equal(batch.received, 1100);
});

test("Outbox keeps failed batches and resumes; completed ones are dropped", async () => {
  const ob = new C.Outbox();
  ob.add("fp", [{ rs: "pvp", from: 0, to: 1, players: mkPlayers(2), maps: [] }, { rs: "rp", from: 0, to: 2, players: mkPlayers(3), maps: [] }]);
  let calls = 0;
  const api = async (p, o) => { calls++; if (calls === 2) throw new Error("down"); const b = JSON.parse(o.body); return { received: b.players.length, changed: b.players.length }; };
  await assert.rejects(ob.drain(api), /down/);
  assert.equal(ob.length, 1); assert.equal(ob.items[0].batch.rs, "rp");
  const r = await ob.drain(api);
  assert.equal(r.received, 3); assert.equal(ob.length, 0);
});

test("resume from watermark: ledger -> engine skips old events -> only new data is uploaded", async () => {
  const T = require("../../site/tracker.js"), H = require("./helpers.js");
  const lines = [H.dmg(1, H.FOE, H.ALLY), H.dmg(2, H.FOE, H.ALLY), H.dmg(3, H.FOE, H.ALLY)];
  const run = async (ledgerTs) => {
    const sent = [];
    const api = async (p, o) => {
      if (p.startsWith("/api/ledger")) return { lastTs: p.includes("rs=pvp") ? ledgerTs : 0 };
      const b = JSON.parse(o.body); sent.push(b); return { received: b.players.length, changed: b.players.length };
    };
    const wm = await C.loadLedger(api, "fp");
    const e = new T.Engine({ clock: () => H.T0 }); e.begin({ file: "f", wm });
    e.feedText(lines.join("\n") + "\n", true);
    const ob = new C.Outbox(); ob.add("fp", e.flush());
    await ob.drain(api);
    return sent;
  };
  const first = await run(0);
  assert.equal(first.length, 1); assert.equal(first[0].from, 0); assert.equal(first[0].to, H.T0 + 3000);
  assert.equal(first[0].players.find((p) => p.guid === H.FOE.g).d.events, 3);
  const second = await run(H.T0 + 3000); // everything already ingested
  assert.equal(second.length, 0);
  const partial = await run(H.T0 + 1000);
  assert.equal(partial[0].players.find((p) => p.guid === H.FOE.g).d.events, 2); assert.equal(partial[0].from, H.T0 + 1000);
});

// ---- render helpers ----
test("fmtAge / esc / num / normName / fmtPct / mb", () => {
  assert.equal(C.fmtAge(5), "5s"); assert.equal(C.fmtAge(125), "2m"); assert.equal(C.fmtAge(3700), "1h1m"); assert.equal(C.fmtAge(3 * 86400), "3d"); assert.equal(C.fmtAge(-4), "0s");
  assert.equal(C.esc('<a href="x">&</a>'), "&lt;a href=&quot;x&quot;&gt;&amp;&lt;/a&gt;");
  assert.equal(C.num(0), "-"); assert.equal(C.num(12.345), "12.3");
  assert.equal(C.normName("Un'Goro Crater"), "ungorocrater");
  assert.equal(C.fmtPct([1.234, 56.78]), "1.2, 56.8"); assert.equal(C.mb(1048576), "1.0");
});

test("worldToPct: X north/up, Y west/left, with calibration", () => {
  const m = { x0: 0, x1: 100, y0: 0, y1: 200 };
  assert.deepEqual(C.worldToPct(m, 100, 200), [0, 0]);       // north-west corner
  assert.deepEqual(C.worldToPct(m, 0, 0), [100, 100]);
  assert.deepEqual(C.worldToPct(m, 50, 100), [50, 50]);
  assert.deepEqual(C.worldToPct(m, 50, 100, { dx: 2, dy: -1, sx: 1, sy: 1 }), [52, 49]);
});

test("buildView joins MAP_CHANGE rectangles with the maps index (available vs noImage, sorted)", () => {
  const zones = { elwynnforest: { file: "Elwynn.webp", w: 1002, h: 668, revealed: true } };
  const v = C.buildView({ 2: { name: "Zeta", x0: 0, x1: 1, y0: 0, y1: 1 }, 1: { name: "Elwynn Forest", x0: 0, x1: 1, y0: 0, y1: 1 }, 3: { name: "Bad", x0: 1, x1: 1, y0: 0, y1: 1 } }, zones);
  assert.deepEqual(v.available.map((m) => [m.mapId, m.url]), [[1, "maps/Elwynn.webp"]]);
  assert.deepEqual(v.noImage.map((m) => m.mapId), [2]);
  assert.equal(C.currentZone({ zone: "x", mapId: 1, maps: { 1: { name: "Elwynn Forest" } } }, zones).url, "maps/Elwynn.webp");
  assert.equal(C.currentZone({ zone: "Nowhere", mapId: 0, maps: {} }, zones), null);
});

test("cloudSnapshot: pips from history rows, 24 h stale rule, auto map = busiest with image", () => {
  const now = 1e12, day = 86400000;
  const rows = [
    { guid: "a", name: "A", reaction: "friendly", lastX: 1, lastY: 2, lastMap: 10, lastSeen: now - 1000, level: 60 },
    { guid: "b", name: "B", reaction: "hostile", lastX: 3, lastY: 4, lastMap: 10, lastSeen: now - 2 * day },
    { guid: "c", name: "C", reaction: "hostile", lastSeen: now },
  ];
  const s = C.cloudSnapshot(rows, [{ id: 10, name: "Z", x0: 0, x1: 1, y0: 0, y1: 1 }], now);
  assert.equal(s.mapId, 10); assert.equal(s.me, null); assert.ok(s.cloud);
  assert.deepEqual(s.pips.map((p) => [p.guid, p.state, p.posKind]), [["a", "active", "exact"], ["b", "stale", "exact"], ["c", "active", "none"]]);
  assert.equal(s.maps["10"].name, "Z");
});

test("historyRow computes the original columns from additive counters", () => {
  const r = C.historyRow({ name: "N", dmgTotal: 1000, dmgHits: 4, srcEvents: 10, hpSum: 600, hpSamples: 3, maxHp: 2000, events: 25, lastSeen: 5, lastZone: "Z" });
  assert.deepEqual(r, { name: "N", avgDmgHit: 250, avgDmgEvent: 100, avgHp: 200, maxHp: 2000, samples: 25, lastSeen: 5, zone: "Z" });
  const z = C.historyRow({ name: "Q" });
  assert.equal(z.avgDmgHit, 0); assert.equal(z.avgHp, 0);
});

test("trackSummary counts fixes per map ascending", () => {
  const s = C.trackSummary([{ mapId: 5, zone: "B" }, { mapId: 2, zone: "A" }, { mapId: 5 }]);
  assert.equal(s.total, 3); assert.deepEqual(s.maps, [{ mapId: 2, zone: "A", count: 1 }, { mapId: 5, zone: "B", count: 2 }]);
});

test("compactSettings drops identity calibrations, rounds, and stays under 8 KB", () => {
  const cal = { 1: { dx: 0, dy: 0, sx: 1, sy: 1 }, 2: { dx: 1.23456, dy: -2, sx: 1.0049999, sy: 0.9 } };
  const out = C.compactSettings({ mode: "auto", ruleset: "pvp", cal, prefs: { follow: true } });
  assert.ok(!out.cal[1]); assert.deepEqual(out.cal[2], { dx: 1.23, dy: -2, sx: 1.005, sy: 0.9 }); assert.equal(out.ruleset, "pvp");
  for (let i = 100; i < 600; i++) cal[i] = { dx: i / 7, dy: i / 3, sx: 1.01, sy: 0.99 };
  const big = C.compactSettings({ mode: "auto", ruleset: "pvp", cal });
  assert.ok(JSON.stringify(big).length <= 8000); assert.ok(Object.keys(big.cal).length > 50);
});
