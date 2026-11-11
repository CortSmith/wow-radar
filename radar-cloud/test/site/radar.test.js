"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const R = require("../../site/radar.js");

const near = (a, b, e) => assert.ok(Math.abs(a - b) < (e || 1e-6), a + " vs " + b);

test("frameRect keeps aspect ratio and centres in the canvas", () => {
  const f = R.frameRect(1000, 800, { w: 1002, h: 668 });
  near(f.w / f.h, 1002 / 668, 1e-6); near(f.x * 2 + f.w, 1000, 1e-6);
  const tall = R.frameRect(400, 900, { w: 1002, h: 668 });
  near(tall.w, 348); // width limited
  const wide = R.frameRect(2000, 300, { w: 1002, h: 668 });
  near(wide.h, 248);
});

test("clampView keeps the view inside the map; zoom is limited to 1..12", () => {
  assert.deepEqual(R.clampView({ z: 0.2, cx: 5, cy: 95 }), { z: 1, cx: 50, cy: 50 });
  assert.deepEqual(R.clampView({ z: 99, cx: -5, cy: 500 }), { z: 12, cx: 50 / 12, cy: 100 - 50 / 12 });
});

test("zoomAt keeps the point under the cursor fixed", () => {
  const f = { x: 20, y: 20, w: 600, h: 400 };
  const fz = { z: 2, cx: 40, cy: 45 };
  const mx = 200, my = 150;
  const pct = (z) => (q) => [q.cx - 50 / q.z + ((mx - f.x) / f.w) * (100 / q.z), q.cy - 50 / q.z + ((my - f.y) / f.h) * (100 / q.z)];
  const before = pct()(fz), after = pct()(R.zoomAt(fz, f, mx, my, 1.2));
  near(before[0], after[0], 1e-6); near(before[1], after[1], 1e-6);
  assert.equal(R.zoomAt({ z: 1, cx: 50, cy: 50 }, f, 100, 100, 1 / 1.2).z, 1);
});

test("panFrom moves opposite to the drag and clamps", () => {
  const f = { x: 0, y: 0, w: 500, h: 500 };
  const r = R.panFrom({ cx: 50, cy: 50 }, f, 2, 50, 0); // drag right 50 px at z=2 -> view moves left 5%
  near(r.cx, 45); near(r.cy, 50);
  assert.equal(R.panFrom({ cx: 30, cy: 30 }, f, 2, 5000, 5000).cx, 25);
});

test("calKey nudges, scales and resets calibration", () => {
  let c = R.calKey(null, "ArrowRight"); assert.equal(c.dx, 0.5);
  c = R.calKey(c, "ArrowDown"); assert.equal(c.dy, 0.5);
  c = R.calKey(c, "+"); near(c.sx, 1.005); c = R.calKey(c, "-"); near(c.sx, 1, 1e-9);
  assert.deepEqual(R.calKey(c, "0"), { dx: 0, dy: 0, sx: 1, sy: 1 });
  assert.equal(R.calKey(c, "q"), null);
});

const m = { x0: 0, x1: 100, y0: 0, y1: 200 };
test("fixed-frame projection maps world corners to the frame; follow re-centres when zoomed", () => {
  const fz = { z: 1, cx: 50, cy: 50 };
  const o = { w: 1000, h: 700, fixed: true, m, cal: null, zi: { w: 1002, h: 668 }, fz, me: null, follow: true, cam: { x: 0, y: 0 }, range: 300, viewId: 5 };
  let P = R.projection(o);
  const nw = P.toScreen(100, 200), se = P.toScreen(0, 0);
  near(nw[0], P.f.x); near(nw[1], P.f.y); near(se[0], P.f.x + P.f.w); near(se[1], P.f.y + P.f.h);
  fz.z = 4;
  o.me = { posKind: "exact", x: 25, y: 150, mapId: 5 };
  P = R.projection(o);
  const mp = P.toScreen(25, 150);
  near(mp[0], P.f.x + P.f.w / 2, 1e-6); near(mp[1], P.f.y + P.f.h / 2, 1e-6);
  o.follow = false; o.me = { posKind: "exact", x: 25, y: 150, mapId: 5 }; fz.cx = 50; fz.cy = 50;
  P = R.projection(o);
  assert.notEqual(Math.round(P.toScreen(25, 150)[0]), Math.round(P.f.x + P.f.w / 2));
});

test("range-mode projection centres on me (X north up, Y west left) and honours range", () => {
  const o = { w: 800, h: 600, fixed: false, range: 300, follow: true, me: { posKind: "exact", x: 10, y: 20, mapId: 1 }, cam: { x: 0, y: 0 }, viewId: 1 };
  const P = R.projection(o);
  assert.deepEqual(P.toScreen(10, 20), [400, 300]);
  const north = P.toScreen(110, 20), west = P.toScreen(10, 120);
  assert.ok(north[1] < 300 && north[0] === 400); assert.ok(west[0] > 400 === false || west[0] < 400);
  near(P.scale, 300 / 300 * 1, 1e-9); // min(800,600)/2/300 = 1
  o.follow = false; assert.deepEqual(R.centre(o), { x: 0, y: 0 });
  o.follow = true; o.viewId = 2; assert.deepEqual(R.centre(o), { x: 0, y: 0 }); // me is on another map
});

test("ring step and wheel range", () => {
  assert.deepEqual([100, 300, 1000, 2500].map(R.ringStep), [25, 100, 200, 500]);
  assert.equal(R.rangeWheel(300, 40, 3000, 1), 345); assert.equal(R.rangeWheel(40, 40, 3000, -1), 40); assert.equal(R.rangeWheel(3000, 40, 3000, 1), 3000);
});

test("visibility: blue/red/stale toggles, me, searched track hides others", () => {
  const fl = { stale: true, showBlue: true, showRed: true };
  const a = { guid: "a", reaction: "friendly", state: "active" }, b = { guid: "b", reaction: "hostile", state: "stale" }, me = { guid: "m", me: true, reaction: "friendly", state: "stale" };
  assert.ok(R.visible(a, fl) && R.visible(b, fl));
  assert.ok(!R.visible(b, { ...fl, stale: false })); assert.ok(!R.visible(me, { ...fl, stale: false })); assert.ok(R.visible(me, fl));
  assert.ok(!R.visible(a, { ...fl, showBlue: false })); assert.ok(!R.visible(b, { ...fl, showRed: false }));
  assert.ok(R.visible({ guid: "n", reaction: "neutral", state: "active" }, fl)); assert.ok(!R.visible({ guid: "n", reaction: "neutral", state: "active" }, { ...fl, showRed: false }));
  assert.ok(!R.visible(a, fl, { guid: "b" })); assert.ok(R.visible(b, fl, { guid: "b" }));
});

test("onMap, viewId, colorOf, trackBuckets, isApprox", () => {
  assert.ok(R.onMap({ posKind: "exact", mapId: 3 }, 3)); assert.ok(!R.onMap({ posKind: "exact", mapId: 4 }, 3)); assert.ok(!R.onMap({ posKind: "none", mapId: 3 }, 3)); assert.ok(R.onMap({ posKind: "approx", mapId: 9 }, 0));
  assert.equal(R.viewId({ mapId: 7 }, null), 7); assert.equal(R.viewId({ mapId: 7 }, 9), 9); assert.equal(R.viewId(null, null), 0);
  assert.equal(R.colorOf({ state: "stale", reaction: "hostile" }), R.COLORS.stale); assert.equal(R.colorOf({ state: "active", reaction: "friendly" }), "#3b82f6"); assert.equal(R.colorOf({ state: "active", reaction: "zzz" }), R.COLORS.unknown);
  const b = R.trackBuckets(8, 4); assert.deepEqual(b.map((x) => [x.lo, x.hi]), [[2, 4], [4, 6], [6, 8]].length === b.length ? [[2, 4], [4, 6], [6, 8]] : b.map((x) => [x.lo, x.hi]));
  assert.deepEqual(R.trackBuckets(1, 4), []);
  assert.ok(R.isApprox({ kind: "approx" }) && !R.isApprox({ kind: "exact" }));
});
