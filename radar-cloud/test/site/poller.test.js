"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const PL = require("../../site/poller.js");

// Fake timers + clock.
function clock() {
  let t = 1000000, id = 0; const q = [];
  return {
    now: () => t,
    setTimeout: (fn, ms) => { const h = ++id; q.push({ h, at: t + ms, fn }); return h; },
    clearTimeout: (h) => { const i = q.findIndex((x) => x.h === h); if (i >= 0) q.splice(i, 1); },
    pending: () => q.length,
    nextDelay: () => (q.length ? Math.min(...q.map((x) => x.at)) - t : null),
    async advance(ms) {
      const end = t + ms;
      for (;;) {
        q.sort((a, b) => a.at - b.at);
        if (!q.length || q[0].at > end) break;
        const x = q.shift(); t = Math.max(t, x.at); x.fn();
        for (let i = 0; i < 20; i++) await Promise.resolve();
      }
      t = end;
      for (let i = 0; i < 20; i++) await Promise.resolve();
    },
  };
}
// Harness: poller with scripted api responses.
function mk(opts) {
  opts = opts || {};
  const c = clock(), calls = [], got = [];
  const st = { hidden: false, scope: { scope: "all", rs: "pvp" }, serverNow: 5000000, changed: false, fail: false, hold: null, concurrent: 0, maxConcurrent: 0 };
  const p = new PL.Poller({
    api: async (path) => {
      calls.push(path); st.concurrent++; st.maxConcurrent = Math.max(st.maxConcurrent, st.concurrent);
      try {
        if (st.hold) await st.hold;
        if (st.fail) throw new Error("boom");
        st.serverNow += 5000;
        return st.changed ? { changed: true, v: 7, now: st.serverNow, players: [{ guid: "a", lastSeen: st.serverNow }] } : { changed: false, v: 7, now: st.serverNow };
      } finally { st.concurrent--; }
    },
    getScope: () => st.scope, onResponse: (r, sc, initial) => got.push([r, sc, initial]), onError: () => {},
    setTimeout: c.setTimeout, clearTimeout: c.clearTimeout, now: c.now, random: opts.random || (() => 0.5), hidden: () => st.hidden, getTrackTail: () => 4242,
  });
  return { c, p, st, calls, got };
}
const q = (path) => Object.fromEntries(new URL("http://x" + path).searchParams);

// ---- scope selection ----
test("pollScope: selected player > map selected (zone + grp from Blue/Red) > all", () => {
  const base = { rs: "pvp", track: null, viewMapId: null, showBlue: true, showRed: true };
  assert.deepEqual(PL.pollScope(base), { scope: "all", rs: "pvp" });
  assert.deepEqual(PL.pollScope({ ...base, viewMapId: 1429 }), { scope: "zone", rs: "pvp", mapId: 1429, grp: "any" });
  assert.equal(PL.pollScope({ ...base, viewMapId: 1429, showRed: false }).grp, "friendly");
  assert.equal(PL.pollScope({ ...base, viewMapId: 1429, showBlue: false }).grp, "hostile");
  assert.equal(PL.pollScope({ ...base, viewMapId: 1429, showBlue: false, showRed: false }), null);
  const pl = PL.pollScope({ ...base, viewMapId: 1429, track: { guid: "Player-1", fixes: [] } });
  assert.deepEqual(pl, { scope: "player", rs: "pvp", guid: "Player-1", track: 1 });
});

test("a player-scope poll never requests zone or all data", async () => {
  const h = mk(); h.st.scope = PL.pollScope({ rs: "rp", track: { guid: "Player-9", fixes: [] }, viewMapId: 12, showBlue: true, showRed: true });
  h.p.start();
  await h.c.advance(60000);
  assert.ok(h.calls.length >= 2);
  for (const path of h.calls) {
    const o = q(path);
    assert.equal(o.scope, "player"); assert.equal(o.guid, "Player-9"); assert.equal(o.mapId, undefined); assert.equal(o.grp, undefined); assert.equal(o.rs, "rp"); assert.equal(o.track, "1"); assert.equal(o.tsince, "4242");
  }
});

// ---- since / overlap / v ----
test("first poll has since=0; later polls send previous response now minus 2000 ms and the version", async () => {
  const h = mk(); h.p.start();
  await h.c.advance(10);
  assert.equal(q(h.calls[0]).since, "0"); assert.equal(q(h.calls[0]).v, undefined);
  await h.c.advance(5600);
  assert.equal(h.calls.length, 2);
  assert.equal(q(h.calls[1]).since, String(5005000 - PL.OVERLAP_MS)); assert.equal(q(h.calls[1]).v, "7");
  assert.equal(q(h.calls[1]).scope, "all");
});

test("changing scope resets since to 0 (full load of the new scope) and polls immediately", async () => {
  const h = mk(); h.p.start();
  await h.c.advance(6000);
  assert.notEqual(q(h.calls[1]).since, "0");
  h.st.scope = { scope: "zone", rs: "pvp", mapId: 5, grp: "any" };
  h.p.poke(true);
  await h.c.advance(50);
  const last = q(h.calls[h.calls.length - 1]);
  assert.equal(last.scope, "zone"); assert.equal(last.since, "0"); assert.equal(last.mapId, "5"); assert.equal(last.grp, "any");
  assert.equal(h.got.length, 0); // nothing changed
});

// ---- delta merge ----
test("mergeRows merges by guid, keeps older fields, never regresses to an older lastSeen, maps zone projection fields", () => {
  const m = new Map();
  PL.mergeRows(m, [{ guid: "a", name: "A", lastSeen: 100, level: 60, events: 5 }, { guid: "b", name: "B", lastSeen: 50 }]);
  PL.mergeRows(m, [{ guid: "a", lastSeen: 200, x: 1, y: 2, mapId: 7, zone: "Z" }, { guid: "b", name: "OLD", lastSeen: 10 }, { name: "noguid" }]);
  const a = m.get("a");
  assert.equal(a.name, "A"); assert.equal(a.level, 60); assert.equal(a.events, 5); assert.equal(a.lastSeen, 200);
  assert.deepEqual([a.lastX, a.lastY, a.lastMap, a.lastZone], [1, 2, 7, "Z"]);
  assert.equal(m.get("b").name, "B"); assert.equal(m.size, 2);
});
test("mergeFixes dedupes and keeps chronological order", () => {
  const out = PL.mergeFixes([{ t: 1, x: 0, y: 0 }, { t: 3, x: 1, y: 1 }], [{ t: 2, x: 5, y: 5 }, { t: 3, x: 1, y: 1 }, { t: 4, x: 9, y: 9 }]);
  assert.deepEqual(out.map((f) => f.t), [1, 2, 3, 4]);
  assert.deepEqual(PL.mergeFixes([{ t: 1 }], []).length, 1);
});
test("changed responses are handed to onResponse with the scope and the initial flag", async () => {
  const h = mk(); h.st.changed = true; h.p.start();
  await h.c.advance(5600);
  assert.equal(h.got.length, 2);
  assert.equal(h.got[0][2], true); assert.equal(h.got[1][2], false);
  assert.equal(h.got[0][0].players[0].guid, "a"); assert.equal(h.got[0][1].scope, "all");
});

// ---- cadence ----
test("5 s chain with ±10% jitter", async () => {
  const lo = mk({ random: () => 0 }), hi = mk({ random: () => 1 });
  for (const h of [lo, hi]) { h.p.start(); await h.c.advance(1); }
  assert.equal(lo.c.nextDelay(), 4500 - 1); assert.equal(hi.c.nextDelay(), 5500 - 1);
});

test("backs off to 30 s after 2 minutes unchanged, snaps back on change", async () => {
  const h = mk(); h.p.start();
  await h.c.advance(125000);
  const n = h.calls.length;
  assert.ok(n >= 24 && n <= 26, "fast phase polls: " + n);
  assert.equal(h.p.interval, PL.SLOW_MS);
  await h.c.advance(60000);
  assert.ok(h.calls.length - n <= 3, "slow phase: " + (h.calls.length - n));
  h.st.changed = true;
  await h.c.advance(30500);
  assert.equal(h.p.interval, PL.BASE_MS);
  const m = h.calls.length;
  await h.c.advance(10100);
  assert.ok(h.calls.length - m >= 2, "fast again");
});

test("user input / scope change / visibility poke snaps the cadence back", async () => {
  const h = mk(); h.p.start();
  await h.c.advance(130000);
  assert.equal(h.p.interval, PL.SLOW_MS);
  h.p.poke(false);
  assert.equal(h.p.interval, PL.BASE_MS);
  assert.ok(h.c.nextDelay() <= 5500);
  await h.c.advance(130000); // slow again
  const n = h.calls.length; h.p.poke(true);
  await h.c.advance(10);
  assert.equal(h.calls.length, n + 1);
});

// ---- hidden pause ----
test("pauses while the document is hidden and resumes immediately when visible", async () => {
  const h = mk(); h.p.start();
  await h.c.advance(6000);
  const n = h.calls.length;
  h.st.hidden = true; h.p.onVisibility();
  assert.equal(h.c.pending(), 0);
  await h.c.advance(120000);
  assert.equal(h.calls.length, n);
  h.st.hidden = false; h.p.onVisibility();
  await h.c.advance(10);
  assert.equal(h.calls.length, n + 1);
  assert.equal(h.p.interval, PL.BASE_MS);
});
test("a timer that fires while hidden does not request", async () => {
  const h = mk(); h.p.start();
  await h.c.advance(1);
  h.st.hidden = true;
  await h.c.advance(20000);
  assert.equal(h.calls.length, 1); assert.ok(h.p.paused);
});

// ---- no overlap ----
test("never two requests in flight; next poll is scheduled only after the response", async () => {
  const h = mk(); let release; h.st.hold = new Promise((r) => { release = r; });
  h.p.start();
  await h.c.advance(60000); // request hangs for a minute
  assert.equal(h.calls.length, 1); assert.equal(h.st.maxConcurrent, 1); assert.equal(h.c.pending(), 0);
  h.p.poke(true); // input while in flight must not start a second request
  await h.c.advance(100);
  assert.equal(h.calls.length, 1);
  h.st.hold = null; release();
  await h.c.advance(10); await h.c.advance(6000);
  assert.ok(h.calls.length >= 2); assert.equal(h.st.maxConcurrent, 1);
});

// ---- errors ----
test("errors back off exponentially up to 30 s and recover", async () => {
  const h = mk({ random: () => 0.5 }); h.st.fail = true; h.p.start();
  const errs = []; h.p.d.onError = (e) => errs.push(e.message);
  const near = (a, b) => assert.ok(a <= b && a >= b - 2, a + " vs " + b);
  await h.c.advance(1); near(h.c.nextDelay(), 10000);          // 5s * 2^1
  await h.c.advance(h.c.nextDelay()); near(h.c.nextDelay(), 20000);
  await h.c.advance(h.c.nextDelay()); near(h.c.nextDelay(), 30000);
  await h.c.advance(h.c.nextDelay()); near(h.c.nextDelay(), 30000); // capped
  assert.deepEqual(errs, ["boom", "boom", "boom", "boom"]);
  h.st.fail = false;
  await h.c.advance(h.c.nextDelay() + 1);
  assert.equal(h.p.errors, 0); near(h.c.nextDelay(), 5000);
});

test("no scope (nothing visible) means no request; stop cancels the timer", async () => {
  const h = mk(); h.st.scope = null; h.p.start();
  await h.c.advance(30000);
  assert.equal(h.calls.length, 0);
  h.p.stop(); assert.equal(h.c.pending(), 0);
});
