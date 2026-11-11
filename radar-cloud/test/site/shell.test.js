"use strict";
// Static-shell contract + a fake-DOM smoke test that loads every script and runs app.js end to end (login, cloud snapshot).
const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const SITE = path.join(__dirname, "..", "..", "site");
const html = fs.readFileSync(path.join(SITE, "index.html"), "utf8");
const appSrc = fs.readFileSync(path.join(SITE, "app.js"), "utf8");

test("index.html is a static shell: every id used by app.js exists, scripts exist, no external requests", () => {
  const ids = new Set(Array.from(html.matchAll(/\bid="([^"]+)"/g), (m) => m[1]));
  const used = new Set(Array.from(appSrc.matchAll(/\$\("([A-Za-z0-9_-]+)"\)/g), (m) => m[1]));
  for (const id of ["rs-auto", "rs-normal", "rs-pvp", "rs-rp", "rs-rppvp"]) used.add(id);
  for (const id of used) assert.ok(ids.has(id), "missing #" + id);
  for (const m of html.matchAll(/<script src="([^"]+)"/g)) assert.ok(fs.existsSync(path.join(SITE, m[1])), m[1]);
  assert.ok(!/https?:\/\//.test(html.replace(/<!doctype[^>]*>/i, "")), "external URL in index.html");
  for (const f of ["parser.js", "tracker.js", "core.js", "radar.js", "ingest.js", "poller.js", "worker.js", "app.js"]) assert.ok(!/https?:\/\/[a-z]/i.test(fs.readFileSync(path.join(SITE, f), "utf8").replace(/http:\/\/x/g, "")), f + " has an external URL");
  const order = Array.from(html.matchAll(/<script src="([^"]+)"/g), (m) => m[1]);
  assert.ok(order.indexOf("parser.js") < order.indexOf("tracker.js") && order.indexOf("core.js") < order.indexOf("ingest.js") && order[order.length - 1] === "app.js");
});

function fakeEl(id) {
  const el = {
    id, hidden: false, value: "", checked: true, textContent: "", innerHTML: "", className: "", title: "", disabled: false, style: {}, children: [], tBodies: [{ innerHTML: "", appendChild() {} }],
    clientWidth: 800, clientHeight: 600, width: 800, height: 600, min: "40", max: "3000", autocomplete: "",
    addEventListener(t, fn) { (el.h[t] = el.h[t] || []).push(fn); }, h: {},
    setAttribute() {}, removeAttribute() {}, appendChild(c) { el.children.push(c); }, contains: () => false, select() {}, click() {}, getBoundingClientRect: () => ({ left: 0, top: 0, width: 800, height: 600 }),
    classList: { add() {}, remove() {} }, getContext: () => new Proxy({}, { get: (t, k) => (k in t ? t[k] : () => {}), set: (t, k, v) => { t[k] = v; return true; } }),
  };
  return el;
}

function boot(fetchImpl, store) {
  const els = {};
  const ids = Array.from(html.matchAll(/\bid="([^"]+)"/g), (m) => m[1]);
  for (const id of ids) els[id] = fakeEl(id);
  els.range.value = "300"; els.upSec.value = "60";
  const timers = [];
  const doc = { getElementById: (id) => els[id], createElement: () => fakeEl("x"), addEventListener() {}, hidden: false };
  const ctx = {
    document: doc, console, URL, TextDecoder, TextEncoder, Promise, Date, Math, JSON, Object, Array, Map, Set, Number, String, Uint8Array, ArrayBuffer, Error,
    setTimeout: (f, ms) => { timers.push([f, ms]); return timers.length; }, clearTimeout() {}, setInterval: () => 1, clearInterval() {},
    localStorage: { getItem: (k) => (k in store ? store[k] : null), setItem: (k, v) => { store[k] = String(v); }, removeItem: (k) => { delete store[k]; } },
    fetch: fetchImpl, Image: function () {}, devicePixelRatio: 1, Worker: undefined, confirm: () => true, crypto: undefined,
    addEventListener() {},
  };
  ctx.window = ctx;
  vm.createContext(ctx);
  for (const f of ["parser.js", "tracker.js", "core.js", "radar.js", "ingest.js", "poller.js", "app.js"]) vm.runInContext(fs.readFileSync(path.join(SITE, f), "utf8"), ctx, { filename: f });
  return { els, ctx, timers };
}
const R = (status, body) => ({ status, ok: status < 300, json: async () => body });
const flush = async () => { for (let i = 0; i < 60; i++) await new Promise((r) => setImmediate(r)); };

test("app.js loads without a token: login overlay shown, nothing fetched with credentials", async () => {
  const calls = [];
  const { els } = boot(async (u) => { calls.push(u); return u.endsWith("index.json") ? R(200, { zones: {} }) : R(200, []); }, {});
  await flush();
  assert.equal(els.auth.hidden, false);
  assert.ok(!calls.some((u) => u.startsWith("/api/")));
});

test("with a stored token app.js loads /api/me + /api/players, renders cloud rows and the rule-set segmented control", async () => {
  const calls = [];
  const players = [{ guid: "Player-1-A", name: "Ann", reaction: "friendly", level: 60, lastSeen: Date.now() - 1000, lastX: 10, lastY: 20, lastMap: 1429, lastZone: "Elwynn", maxHp: 2000, events: 3 }];
  const fetchImpl = async (u, o) => {
    calls.push([u, o && o.headers && o.headers.Authorization]);
    if (u === "maps/index.json") return R(200, { zones: { elwynnforest: { file: "Elwynn.webp", w: 1002, h: 668, revealed: true } } });
    if (u === "ads/ads.json") return R(200, []);
    if (u === "/api/me") return R(200, { username: "bob", settings: { mode: "manual", ruleset: "rp" }, rulesets: ["normal", "pvp", "rp", "rppvp"] });
    if (u.startsWith("/api/players")) return R(200, { players, maps: [{ id: 1429, name: "Elwynn Forest", x0: -500, x1: 500, y0: -500, y1: 500 }] });
    if (u.startsWith("/api/bounty")) return R(200, { entries: [{ guid: "Player-1-X", name: "Foe", kb: 2, assists: 1, score: 20 }] });
    if (u.startsWith("/api/poll")) return R(200, { changed: false, v: 1, now: 5 });
    return R(404, {});
  };
  const { els, timers } = boot(fetchImpl, { "nrc.token": "TOK", "nrc.user": "bob" });
  await flush();
  assert.equal(els.auth.hidden, true); assert.equal(els.user.textContent, "bob");
  assert.ok(calls.every(([u, a]) => !u.startsWith("/api/") || a === "Bearer TOK"));
  const players1 = calls.find(([u]) => u.startsWith("/api/players"))[0];
  assert.match(players1, /rs=rp/); // settings.ruleset manual -> rp
  assert.equal(els["rs-rp"].className, "on"); assert.equal(els["rs-auto"].className, ""); assert.equal(els["rs-auto"].textContent, "Auto");
  assert.match(els.summary.textContent, /1 stored/);
  assert.match(els.zone.textContent, /Elwynn Forest/);
  assert.match(els.bountyCount.textContent, /1 ranked/);
  assert.ok(els.bountyList.children.length >= 1);
  assert.match(els.foot.textContent, /Cloud snapshot/);
  // the poll loop is armed (5 s chain) and polls scope=all because no map or player is selected
  assert.ok(timers.length >= 1);
  timers[timers.length - 1][0]();
  await flush();
  const poll = calls.find(([u]) => u.startsWith("/api/poll"));
  assert.ok(poll, "poll issued"); assert.match(poll[0], /scope=all/); assert.match(poll[0], /rs=rp/);
});

test("login form: bad key shows the server error, success enters the app", async () => {
  let fail = true;
  const fetchImpl = async (u, o) => {
    if (u === "/api/login") return fail ? R(403, { error: "API key inactive" }) : R(200, { token: "T2", username: "ann" });
    if (u === "maps/index.json") return R(200, { zones: {} });
    if (u === "ads/ads.json") return R(200, []);
    if (u.startsWith("/api/players")) return R(200, { players: [], maps: [] });
    if (u === "/api/me") return R(200, { settings: {} });
    return R(200, { entries: [], changed: false, now: 1 });
  };
  const store = {};
  const { els } = boot(fetchImpl, store);
  els.authUser.value = "ann"; els.authPass.value = "pw"; els.authKey.value = "k";
  const ev = { preventDefault() {} };
  els.authForm.h.submit[0](ev); await flush();
  assert.equal(els.authErr.textContent, "API key inactive"); assert.equal(els.auth.hidden, false);
  fail = false;
  els.authForm.h.submit[0](ev); await flush();
  assert.equal(els.auth.hidden, true); assert.equal(store["nrc.token"], "T2");
});
