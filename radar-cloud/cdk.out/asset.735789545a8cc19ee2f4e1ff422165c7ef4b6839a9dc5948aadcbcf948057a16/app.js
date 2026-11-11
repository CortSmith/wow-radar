"use strict";
// DOM glue only. Logic lives in parser.js / tracker.js (worker), core.js, radar.js, ingest.js (all unit-tested in node).
(function () {
  const $ = (id) => document.getElementById(id);
  const C = RadarCore, G = RadarGeo, I = RadarIngest;
  const COLORS = G.COLORS;
  const cv = $("radar"), ctx = cv.getContext("2d");
  const RULESETS = [["auto", "Auto"], ["normal", "Normal"], ["pvp", "PvP"], ["rp", "RP"], ["rppvp", "RP-PvP"]];
  const rsName = (id) => { for (const r of RULESETS) if (r[0] === id) return r[1]; return id || ""; };

  let S = null;                 // current snapshot (live engine or cloud rows) + S.view
  const PL = RadarPoller;
  let cloudRows = [], cloudMaps = [], cloudMap = new Map(), poller = null;
  let zoneIdx = {};             // normalised map name -> {file,w,h,revealed}
  let selected = null, track = null, hit = [], thit = [], imgCache = {}, dpr = 1;
  let B = { rows: [], by: {} };
  let viewMapId = null, lastVid = null;
  let cam = { x: 0, y: 0 };
  const fz = { z: 1, cx: 50, cy: 50 };
  let detected = null;          // rule-set detected by the engine (live or last parse)

  // ---------- prefs / settings ----------
  const pref = {
    get(k, d) { try { const v = localStorage.getItem("nr." + k); return v === null ? d : v; } catch (e) { return d; } },
    set(k, v) { try { localStorage.setItem("nr." + k, v); } catch (e) { /* ignore */ } },
  };
  let settings = { mode: "auto", ruleset: "pvp", cal: {}, prefs: {}, uploadSec: 60 };
  try { const s = JSON.parse(pref.get("settings", "")); if (s && typeof s === "object") settings = Object.assign(settings, s); } catch (e) { /* default */ }
  settings.cal = settings.cal || {}; settings.prefs = settings.prefs || {};
  let saveTimer = null;
  function saveSettings() {
    const compact = C.compactSettings(settings);
    pref.set("settings", JSON.stringify(compact));
    if (!token) return;
    if (saveTimer) clearTimeout(saveTimer);
    saveTimer = setTimeout(() => { saveTimer = null; api("/api/settings", { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(C.compactSettings(settings)) }).catch(() => {}); }, 1500);
  }
  const BOOL_PREFS = ["follow", "stale", "bg", "fit", "showBlue", "showRed"];
  function applyPrefs() {
    for (const id of BOOL_PREFS) {
      const v = settings.prefs[id] !== undefined ? settings.prefs[id] : pref.get(id, "1") === "1";
      $(id).checked = !!v;
    }
    const r = settings.prefs.range !== undefined ? settings.prefs.range : Number(pref.get("range", "300"));
    $("range").value = r; $("rangeVal").textContent = $("range").value;
    $("upSec").value = settings.uploadSec || 60;
  }
  function setPref(id, v) { settings.prefs[id] = v; pref.set(id, typeof v === "boolean" ? (v ? "1" : "0") : String(v)); saveSettings(); }
  for (const id of BOOL_PREFS) $(id).addEventListener("change", () => { setPref(id, $(id).checked); renderList(); draw(); });
  $("range").addEventListener("input", () => { $("rangeVal").textContent = $("range").value; setPref("range", Number($("range").value)); draw(); });
  $("upSec").addEventListener("change", () => { settings.uploadSec = Math.max(5, Math.min(3600, Number($("upSec").value) || 60)); $("upSec").value = settings.uploadSec; saveSettings(); });
  applyPrefs();
  $("showHistory").checked = false;
  $("showHistory").addEventListener("change", () => setHistory($("showHistory").checked));
  window.addEventListener("keydown", (e) => {
    if ((e.key === "h" || e.key === "H") && !e.ctrlKey && !e.altKey && !e.metaKey && e.target.tagName !== "INPUT") {
      $("showHistory").checked = !$("showHistory").checked; setHistory($("showHistory").checked);
    }
  });

  // ---------- auth ----------
  let token = null, username = "", signup = false;
  try { token = localStorage.getItem("nrc.token"); username = localStorage.getItem("nrc.user") || ""; } catch (e) { /* ignore */ }
  function saveAuth() {
    try {
      if (token) { localStorage.setItem("nrc.token", token); localStorage.setItem("nrc.user", username); }
      else { localStorage.removeItem("nrc.token"); localStorage.removeItem("nrc.user"); }
    } catch (e) { /* ignore */ }
  }
  const api = C.makeApi((p, o) => fetch(p, o), () => token, () => logout());
  function setConn(k) { $("conn").className = "dot " + (k === "off" ? "" : k); }
  function showAuth() { $("auth").hidden = false; $("user").textContent = ""; $("logout").hidden = true; setConn("off"); }
  function logout() {
    stopLive(true);
    token = null; username = ""; saveAuth(); S = null; selected = null; track = null; cloudRows = []; B = { rows: [], by: {} };
    stopTimers(); rebuild(); showAuth();
  }
  function setSignup(on) {
    signup = on;
    $("authTitle").textContent = $("authGo").textContent = on ? "Create account" : "Log in";
    $("authToggle").textContent = on ? "I already have an account" : "Create account";
    $("authPass").autocomplete = on ? "new-password" : "current-password";
    $("authErr").textContent = "";
  }
  $("authToggle").addEventListener("click", () => setSignup(!signup));
  $("authForm").addEventListener("submit", (e) => {
    e.preventDefault();
    $("authErr").textContent = ""; $("authGo").disabled = true;
    C.authenticate((p, o) => fetch(p, o), { signup, username: $("authUser").value, password: $("authPass").value, apiKey: $("authKey").value })
      .then((r) => { token = r.token; username = r.username; saveAuth(); $("authPass").value = ""; $("authKey").value = ""; enter(); })
      .catch((err) => { $("authErr").textContent = err.message; })
      .then(() => { $("authGo").disabled = false; });
  });
  $("logout").addEventListener("click", logout);

  // ---------- engine (Web Worker) ----------
  const sha256 = async (u8) => {
    if (window.crypto && crypto.subtle) {
      const d = new Uint8Array(await crypto.subtle.digest("SHA-256", u8));
      return Array.from(d, (b) => b.toString(16).padStart(2, "0")).join("");
    }
    let h1 = 0x811c9dc5, h2 = 0x1234567; // non-secure context fallback (FNV-style, 64 bit)
    for (let i = 0; i < u8.length; i++) { h1 = Math.imul(h1 ^ u8[i], 16777619); h2 = Math.imul(h2 ^ u8[i], 2246822519) ^ (h1 >>> 7); }
    return ((h1 >>> 0).toString(16).padStart(8, "0") + (h2 >>> 0).toString(16).padStart(8, "0")).repeat(4);
  };
  function newClient(logClock) {
    let c;
    try { c = new I.WorkerClient(new Worker("worker.js")); }
    catch (e) { c = new I.LocalClient(RadarTracker.createHandler(new TextDecoder())); }
    c.call({ type: "init", logClock: !!logClock });
    c.call({ type: "config", mode: settings.mode, manual: settings.ruleset });
    return c;
  }
  let liveClient = null;
  function sendConfig() { for (const c of [liveClient]) if (c) c.call({ type: "config", mode: settings.mode, manual: settings.ruleset }).catch(() => {}); }
  const outbox = new C.Outbox();

  function currentRs() {
    if (settings.mode === "manual" && settings.ruleset) return settings.ruleset;
    return detected || settings.ruleset || "pvp";
  }
  function noteRuleset(r) {
    if (!r) return;
    if (r.detected && r.detected !== detected) detected = r.detected;
    const prev = lastRs; lastRs = currentRs();
    paintRuleset();
    if (prev !== null && prev !== lastRs) onRulesetChanged();
  }
  let lastRs = null;
  function paintRuleset() {
    const rs = currentRs();
    for (const r of RULESETS) {
      const b = $("rs-" + r[0]);
      const on = r[0] === "auto" ? settings.mode !== "manual" : (settings.mode === "manual" && settings.ruleset === r[0]);
      b.className = on ? "on" : ""; b.setAttribute("aria-pressed", on ? "true" : "false");
      b.textContent = r[0] === "auto" && settings.mode !== "manual" ? "Auto: " + rsName(rs) : r[1];
    }
    $("bountyRs").textContent = rsName(rs);
  }
  function selectRuleset(id) {
    if (id === "auto") settings.mode = "auto"; else { settings.mode = "manual"; settings.ruleset = id; }
    sendConfig(); saveSettings();
    noteRuleset({}); lastRs = currentRs();
    onRulesetChanged();
  }
  for (const r of RULESETS) $("rs-" + r[0]).addEventListener("click", () => selectRuleset(r[0]));
  function onRulesetChanged() {
    B = { rows: [], by: {} }; renderBounty();
    loadCloud(); loadBounty();
    const q = $("q").value.trim();
    if (q) runSearch(q); else { searchSeq++; $("results").hidden = true; }
    if (track) { const g = track.guid; loadTrack(g, true); }
  }

  // ---------- data ----------
  function normName(s) { return C.normName(s); }
  fetch("maps/index.json").then((r) => r.json()).then((d) => { zoneIdx = d.zones || {}; if (S) rebuild(); }).catch(() => {});
  fetch("ads/ads.json").then((r) => r.json()).then((ads) => {
    const box = $("ads");
    for (const a of ads) {
      const l = document.createElement("a"), im = document.createElement("img");
      l.href = a.href; l.rel = "sponsored noopener"; l.target = "_blank";
      im.src = a.img; im.alt = a.alt || "";
      l.appendChild(im); box.appendChild(l);
    }
  }).catch(() => {});

  function decorate(snap) {
    snap.view = C.buildView(snap.maps, zoneIdx);
    snap.view.zone = snap.cloud ? null : C.currentZone(snap, zoneIdx);
    return snap;
  }
  function onState(snap) {
    S = decorate(snap);
    if (snap.ruleset) noteRuleset(snap.ruleset);
    if (viewMapId !== null && !availById(viewMapId) && !(S.maps && S.maps[String(viewMapId)])) viewMapId = null;
    rebuild();
  }
  function rebuild() {
    syncView(); renderList(); renderHeader(); renderTrackInfo(); renderMapInput();
    if ($("showHistory").checked) renderHistory();
    draw();
  }
  function loadCloud() {
    if (!token) return Promise.resolve();
    const rs = currentRs();
    return api("/api/players?" + C.qs({ rs })).then((d) => {
      if (rs !== currentRs()) return;
      cloudRows = d.players || []; cloudMaps = d.maps || [];
      cloudMap = new Map(); PL.mergeRows(cloudMap, cloudRows); cloudRows = Array.from(cloudMap.values());
      if (poller) poller.reset();
      if (!live) { setConn("ok"); onState(C.cloudSnapshot(cloudRows, cloudMaps, Date.now())); }
      else if ($("showHistory").checked) renderHistory();
    }).catch((e) => { setConn("off"); $("foot").textContent = e.message; });
  }

  // ---------- viewed map ----------
  function viewId() { return G.viewId(S, viewMapId); }
  function availById(id) { const a = S && S.view && S.view.available; if (!a) return null; for (const m of a) if (m.mapId === id) return m; return null; }
  function viewZone() { if (!S || !S.view) return null; return availById(viewId()) || (viewMapId === null ? S.view.zone : null); }
  function viewName() {
    const m = S && S.maps && S.maps[String(viewId())];
    if (viewMapId !== null) { const a = availById(viewMapId); return (a && a.name) || (m && m.name) || ""; }
    return (S && S.zone) || (m && m.name) || "";
  }
  function syncView() { const id = viewId(); if (id !== lastVid) { lastVid = id; fz.z = 1; fz.cx = 50; fz.cy = 50; } }
  function curMap() { return S && S.maps && viewId() ? S.maps[String(viewId())] || null : null; }
  function fitActive() { return !!(viewZone() && curMap() && $("fit").checked && $("bg").checked); }
  function getCal() { return settings.cal[viewId()] || { dx: 0, dy: 0, sx: 1, sy: 1 }; }
  function setCal(c) { settings.cal[viewId()] = c; saveSettings(); draw(); }
  const flags = () => ({ stale: $("stale").checked, showBlue: $("showBlue").checked, showRed: $("showRed").checked });
  const visible = (p) => G.visible(p, flags(), track);
  const onMap = (p) => G.onMap(p, viewId());
  const pctOf = (m, x, y) => C.worldToPct(m, x, y, getCal());

  // ---------- panels ----------
  function renderHeader() {
    if (!S) { $("zone").textContent = "-"; $("summary").textContent = ""; $("empty").hidden = false; $("foot").textContent = token ? "Not loaded" : ""; return; }
    const mm = curMap(), vid = viewId();
    $("zone").textContent = (viewName() || "-") + (vid ? " (map " + vid + ")" : "") + (viewMapId !== null ? " [viewing]" : "") +
      (mm && S.me && S.me.posKind !== "none" && S.me.mapId === vid && fitActive() ? " | me " + C.fmtPct(pctOf(mm, S.me.x, S.me.y)) : "");
    const act = S.pips.filter((p) => p.state === "active").length;
    $("summary").textContent = S.cloud ? S.pips.length + " stored, " + S.pips.filter(onMap).length + " on this map"
      : S.pips.length + " tracked, " + act + " active, " + (S.pips.length - act) + " stale" + (S.me ? " | me: " + S.me.name : "");
    $("empty").hidden = S.pips.some(onMap);
    $("empty").textContent = live ? "Waiting for combat log events..." : "No players yet. Start live or upload a combat log...";
    const src = live ? "Live: " + (liveSession && liveSession.cur ? liveSession.cur.name : "waiting for log") : "Cloud snapshot";
    $("foot").textContent = src + " | " + (S.cloud ? cloudRows.length + " players, " + (cloudMaps.length) + " maps" : "events " + S.stats.events + ", units " + S.stats.units) +
      (S.advanced === false ? " | ADVANCED LOGGING IS OFF (no positions)" : "") + " | rule-set " + rsName(currentRs()) + (outbox.length ? " | " + outbox.length + " upload(s) pending" : "");
    $("srcTag").textContent = live ? "live" : (window.showDirectoryPicker ? "idle" : "upload only");
  }
  function renderList() {
    const ul = $("list");
    ul.innerHTML = "";
    if (!S) return;
    const rows = S.pips.filter(visible).slice(0, 400);
    for (const p of rows) {
      const li = document.createElement("li");
      li.className = (p.state === "stale" ? "stale " : "") + (p.guid === selected ? "sel" : "");
      const hp = p.hasHp && p.maxHp > 0 ? Math.round((100 * p.hp) / p.maxHp) + "% " : "";
      const pos = p.posKind === "none" ? "no fix" : p.posKind === "approx" ? "~" : "";
      const bo = B.by[p.guid];
      li.innerHTML = '<span class="sw" style="background:' + G.colorOf(p) + '"></span><span class="nm">' + C.esc(p.name) + (p.me ? " (me)" : "") +
        '</span><span class="meta">' + (bo ? '<span class="bty">bounty ' + bo.score + "</span> " : "") + (S.cloud && p.level ? "Lv " + p.level + " " : "") + hp + pos + " " + C.fmtAge(p.ageSec) + "</span>";
      li.addEventListener("click", () => {
        selected = p.guid;
        if (p.posKind !== "none") {
          cam = { x: p.x, y: p.y }; $("follow").checked = false; setPref("follow", false);
          const m = S.maps[String(p.mapId)];
          if (m && fz.z > 1) { const pc = pctOf(m, p.x, p.y); fz.cx = pc[0]; fz.cy = pc[1]; G.clampView(fz); }
        }
        renderList(); draw();
      });
      ul.appendChild(li);
    }
  }

  // ---------- drawing ----------
  function resize() {
    dpr = window.devicePixelRatio || 1;
    cv.width = Math.max(1, Math.round(cv.clientWidth * dpr)); cv.height = Math.max(1, Math.round(cv.clientHeight * dpr));
    draw();
  }
  window.addEventListener("resize", resize);
  if (window.ResizeObserver) new ResizeObserver(resize).observe($("radarWrap"));
  window.addEventListener("keydown", (e) => {
    if (!e.shiftKey || !S || !viewId() || !fitActive() || e.target.tagName === "INPUT") return;
    const c = G.calKey(getCal(), e.key);
    if (!c) return;
    e.preventDefault(); setCal(c);
  });
  function view() {
    const w = cv.width / dpr, h = cv.height / dpr;
    const o = { w, h, fixed: fitActive(), m: curMap(), cal: getCal(), zi: viewZone(), fz, me: S && S.me, follow: $("follow").checked, cam, range: Number($("range").value), viewId: viewId() };
    const V = G.projection(o);
    V.w = w; V.h = h; V.m = o.m;
    return V;
  }
  function loadImage(url) {
    let e = imgCache[url];
    if (e) return e.ok ? e.img : null;
    const img = new Image();
    e = imgCache[url] = { img, ok: false };
    img.onload = () => { e.ok = true; draw(); };
    img.src = url;
    return null;
  }
  function draw() {
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    const w = cv.width / dpr, h = cv.height / dpr;
    ctx.clearRect(0, 0, w, h);
    hit = []; thit = [];
    if (!S || !$("bg").checked) return;
    const V = view();
    if (V.fit) drawFrame(V); else { drawMap(V); drawRings(V); }
    drawTrack(V);
    const mine = S.pips.filter((p) => onMap(p) && visible(p));
    mine.sort((a, b) => (a.state === "stale" ? 0 : 1) - (b.state === "stale" ? 0 : 1));
    for (const p of mine) drawPip(V, p);
    if (S.me && onMap(S.me) && visible(S.me) && !mine.includes(S.me)) drawPip(V, S.me);
    if (V.clipped) ctx.restore();
  }
  function drawFrame(V) {
    const f = V.f, z = V.z, zi = viewZone(), img = zi ? loadImage(zi.url) : null;
    const sxp = (p) => f.x + ((p - V.ox) * z / 100) * f.w, syp = (p) => f.y + ((p - V.oy) * z / 100) * f.h;
    ctx.save();
    ctx.beginPath(); ctx.rect(f.x, f.y, f.w, f.h); ctx.clip();
    if (img) { ctx.globalAlpha = 0.9; ctx.drawImage(img, sxp(0), syp(0), f.w * z, f.h * z); ctx.globalAlpha = 1; }
    ctx.strokeStyle = "rgba(148,163,184,.25)"; ctx.lineWidth = 1;
    const step = z >= 6 ? 2 : z >= 3 ? 5 : 10;
    for (let i = 0; i <= 100; i += step) { ctx.beginPath(); ctx.moveTo(sxp(i), f.y); ctx.lineTo(sxp(i), f.y + f.h); ctx.moveTo(f.x, syp(i)); ctx.lineTo(f.x + f.w, syp(i)); ctx.stroke(); }
    ctx.restore();
    ctx.fillStyle = "#6b7280"; ctx.font = "10px sans-serif";
    for (let i = 0; i <= 100; i += step) {
      const gx = sxp(i), gy = syp(i);
      if (gx >= f.x - 1 && gx <= f.x + f.w + 1) { ctx.textAlign = "center"; ctx.fillText(String(i), gx, f.y - 5); }
      if (gy >= f.y - 1 && gy <= f.y + f.h + 1) { ctx.textAlign = "right"; ctx.fillText(String(i), f.x - 4, gy + 3); }
    }
    ctx.textAlign = "start";
    ctx.strokeStyle = "#2f3b52"; ctx.strokeRect(f.x, f.y, f.w, f.h);
    ctx.save(); ctx.beginPath(); ctx.rect(f.x, f.y, f.w, f.h); ctx.clip(); V.clipped = true;
  }
  function drawMap(V) {
    const zi = viewZone(), m = curMap();
    if (!zi || !m) return;
    const img = loadImage(zi.url);
    if (!img) return;
    const [sx, sy] = V.toScreen(m.x1, m.y1);
    const iw = (m.y1 - m.y0) * V.scale, ih = (m.x1 - m.x0) * V.scale;
    ctx.globalAlpha = 0.55; ctx.drawImage(img, sx, sy, iw, ih); ctx.globalAlpha = 1;
    ctx.strokeStyle = "#2f3b52"; ctx.strokeRect(sx, sy, iw, ih);
  }
  function drawRings(V) {
    const [cx, cy] = V.toScreen(V.c.x, V.c.y);
    ctx.strokeStyle = "#232b39"; ctx.fillStyle = "#4a5568"; ctx.lineWidth = 1; ctx.font = "11px sans-serif";
    const step = G.ringStep(V.range);
    for (let r = step; r <= V.range * 1.5; r += step) { ctx.beginPath(); ctx.arc(cx, cy, r * V.scale, 0, Math.PI * 2); ctx.stroke(); ctx.fillText(r + "y", cx + 3, cy - r * V.scale - 2); }
    ctx.beginPath(); ctx.moveTo(cx - V.w, cy); ctx.lineTo(cx + V.w, cy); ctx.moveTo(cx, cy - V.h); ctx.lineTo(cx, cy + V.h); ctx.stroke();
    ctx.fillStyle = "#6b7280"; ctx.font = "12px sans-serif"; ctx.fillText("N", V.w / 2 - 4, 14);
  }
  function drawPip(V, p) {
    const [x, y] = V.toScreen(p.x, p.y);
    if (x < -20 || y < -20 || x > V.w + 20 || y > V.h + 20) return;
    if (V.fit) { const f = V.f; if (x < f.x || y < f.y || x > f.x + f.w || y > f.y + f.h) return; }
    const col = G.colorOf(p), r = p.me ? 7 : 5;
    ctx.lineWidth = 2;
    const fac = COLORS[p.reaction] || COLORS.unknown;
    if (p.posKind === "approx") {
      ctx.strokeStyle = fac; ctx.setLineDash([3, 2]); ctx.beginPath(); ctx.arc(x, y, r, 0, Math.PI * 2); ctx.stroke(); ctx.setLineDash([]);
    } else {
      ctx.fillStyle = col; ctx.beginPath(); ctx.arc(x, y, r, 0, Math.PI * 2); ctx.fill();
      if (p.state === "stale") { ctx.strokeStyle = fac; ctx.beginPath(); ctx.arc(x, y, r, 0, Math.PI * 2); ctx.stroke(); }
    }
    if (p.me) { ctx.strokeStyle = "#fff"; ctx.beginPath(); ctx.arc(x, y, r + 3, 0, Math.PI * 2); ctx.stroke(); }
    if (!p.me && B.by[p.guid]) drawBountyMark(x, y, r);
    if (p.guid === selected) { ctx.strokeStyle = "#fde047"; ctx.beginPath(); ctx.arc(x, y, r + 5, 0, Math.PI * 2); ctx.stroke(); }
    if (p.hasHp && p.maxHp > 0 && p.state !== "stale") {
      ctx.strokeStyle = "#22c55e"; ctx.lineWidth = 2;
      ctx.beginPath(); ctx.arc(x, y, r + 1.5, -Math.PI / 2, -Math.PI / 2 + Math.PI * 2 * Math.max(0, Math.min(1, p.hp / p.maxHp))); ctx.stroke();
    }
    hit.push({ x, y, r: r + 5, p });
  }
  function drawBountyMark(x, y, r) {
    ctx.save();
    ctx.strokeStyle = "#f5b301"; ctx.lineWidth = 1.5; ctx.setLineDash([]);
    ctx.beginPath(); ctx.arc(x, y, r + 3.5, 0, Math.PI * 2); ctx.stroke();
    ctx.fillStyle = "#f5b301";
    const dy = y - r - 9;
    ctx.beginPath(); ctx.moveTo(x, dy - 3.5); ctx.lineTo(x + 3, dy); ctx.lineTo(x, dy + 3.5); ctx.lineTo(x - 3, dy); ctx.closePath(); ctx.fill();
    ctx.restore();
  }

  // ---------- track drawing ----------
  const trackColor = () => COLORS[track && track.reaction] || COLORS.unknown;
  function trackFixes() { if (!track || !S || !viewId()) return []; return track.fixes.filter((f) => f.mapId === viewId()); }
  function inView(V, x, y) { return V.fit ? true : x >= -10 && y >= -10 && x <= V.w + 10 && y <= V.h + 10; }
  function drawTrack(V) {
    const fixes = trackFixes();
    if (!fixes.length) return;
    const pts = fixes.map((f) => V.toScreen(f.x, f.y));
    if (pts.length > 1) {
      ctx.save();
      ctx.strokeStyle = trackColor(); ctx.lineWidth = 1.5; ctx.setLineDash([2, 5]); ctx.lineCap = "round";
      for (const b of G.trackBuckets(pts.length, 4)) {
        ctx.globalAlpha = b.alpha;
        ctx.beginPath(); ctx.moveTo(pts[b.lo - 1][0], pts[b.lo - 1][1]);
        for (let i = b.lo; i < b.hi; i++) ctx.lineTo(pts[i][0], pts[i][1]);
        ctx.stroke();
      }
      ctx.restore();
    }
    const col = trackColor(), last = fixes.length - 1;
    ctx.save();
    ctx.setLineDash([]); ctx.fillStyle = col; ctx.strokeStyle = col; ctx.lineWidth = 1.5;
    for (let i = 0; i <= last; i++) {
      const x = pts[i][0], y = pts[i][1];
      if (!inView(V, x, y)) continue;
      const r = i === last ? 4 : 2.5;
      ctx.globalAlpha = 0.45 + (0.55 * i) / Math.max(1, last);
      ctx.beginPath(); ctx.arc(x, y, r, 0, Math.PI * 2);
      if (G.isApprox(fixes[i])) ctx.stroke(); else ctx.fill();
      thit.push({ x, y, r: r + 3, f: fixes[i] });
    }
    ctx.globalAlpha = 1;
    if (inView(V, pts[last][0], pts[last][1])) { ctx.strokeStyle = "#fff"; ctx.beginPath(); ctx.arc(pts[last][0], pts[last][1], 6.5, 0, Math.PI * 2); ctx.stroke(); }
    ctx.restore();
  }
  function trackTipText(f) {
    const m = curMap(), where = m && fitActive() ? "map " + C.fmtPct(pctOf(m, f.x, f.y)) : "x " + f.x + ", y " + f.y;
    return (track ? track.name : "") + "\n" + new Date(f.t).toLocaleString() + "\n" + where + "\n" + (G.isApprox(f) ? "approx" : "exact") + " fix" + (f.zone ? ", " + f.zone : "");
  }

  // ---------- interaction ----------
  const tip = $("tip");
  let drag = null, dragMoved = false;
  function showTip(text, mx, my, rect) { tip.textContent = text; tip.style.left = Math.min(mx + 12, rect.width - 200) + "px"; tip.style.top = my + 12 + "px"; tip.hidden = false; }
  cv.addEventListener("mousemove", (e) => {
    const rect = cv.getBoundingClientRect(), mx = e.clientX - rect.left, my = e.clientY - rect.top;
    if (drag) {
      const dx = mx - drag.mx, dy = my - drag.my;
      if (Math.abs(dx) + Math.abs(dy) > 2) { dragMoved = true; if ($("follow").checked) { $("follow").checked = false; setPref("follow", false); } }
      if (fitActive()) { const f = view().f; const r = G.panFrom(drag.fz0, f, fz.z, dx, dy); fz.cx = r.cx; fz.cy = r.cy; }
      else {
        const V = view(), k = 1 / V.scale;
        cam = { x: drag.cx + dy * k, y: drag.cy + dx * k };
      }
      draw(); return;
    }
    let best = null;
    for (const h of hit) if (Math.hypot(h.x - mx, h.y - my) <= h.r) best = h;
    if (!best) {
      let th = null;
      for (const h of thit) if (Math.hypot(h.x - mx, h.y - my) <= h.r) th = h;
      if (th) showTip(trackTipText(th.f), mx, my, rect); else tip.hidden = true;
      return;
    }
    const p = best.p, m = curMap(), bo = B.by[p.guid];
    showTip(p.name + (p.me ? " (me)" : "") + (m && fitActive() ? "\nmap " + C.fmtPct(pctOf(m, p.x, p.y)) : "") + "\n" + p.reaction + ", " + p.state + ", " + p.posKind + " fix" +
      (p.level ? ", level " + p.level : "") + "\n" + (p.hasHp ? "HP " + Math.round(p.hp) + "/" + Math.round(p.maxHp) + "\n" : p.maxHp ? "Max HP " + p.maxHp + "\n" : "") +
      "last event " + C.fmtAge(p.ageSec) + " ago" + (p.est ? "\nestimate ±" + p.est.spread + "y" : "") +
      (p.damageHits ? "\ndamage dealt " + Math.round(p.damageTotal) + " in " + p.damageHits + " hits" : "") +
      (bo ? "\nBOUNTY " + bo.score + " (rank " + bo.rank + ": " + bo.kb + " kills, " + bo.assists + " assists)" : ""), mx, my, rect);
  });
  cv.addEventListener("mouseleave", () => { tip.hidden = true; drag = null; });
  cv.addEventListener("mousedown", (e) => {
    const rect = cv.getBoundingClientRect(), c = G.centre({ follow: $("follow").checked, me: S && S.me, cam, viewId: viewId() });
    drag = { mx: e.clientX - rect.left, my: e.clientY - rect.top, cx: c.x, cy: c.y, fz0: { cx: fz.cx, cy: fz.cy } }; dragMoved = false;
    if (!fitActive()) cam = { x: c.x, y: c.y };
  });
  window.addEventListener("mouseup", () => { drag = null; });
  cv.addEventListener("dblclick", () => { if (fitActive()) { fz.z = 1; fz.cx = 50; fz.cy = 50; draw(); } });
  cv.addEventListener("wheel", (e) => {
    e.preventDefault();
    if (fitActive()) {
      const rect = cv.getBoundingClientRect(), r = G.zoomAt(fz, view().f, e.clientX - rect.left, e.clientY - rect.top, e.deltaY > 0 ? 1 / 1.2 : 1.2);
      fz.z = r.z; fz.cx = r.cx; fz.cy = r.cy;
      if ($("follow").checked && fz.z === 1) { /* follow only re-centres when zoomed */ }
      draw(); return;
    }
    const rg = $("range");
    rg.value = G.rangeWheel(Number(rg.value), Number(rg.min), Number(rg.max), e.deltaY);
    $("rangeVal").textContent = rg.value; setPref("range", Number(rg.value)); draw();
  }, { passive: false });

  // ---------- search + track ----------
  let searchTimer = null, searchSeq = 0, trackTimer = null;
  function renderTrackInfo() {
    const el = $("trackInfo");
    if (!track) { el.hidden = true; el.textContent = ""; return; }
    const here = trackFixes().length;
    const maps = track.maps.map((m) => (m.zone || "map " + m.mapId) + " (" + m.mapId + "): " + m.count + (S && m.mapId === viewId() ? " *" : ""));
    let txt = track.name + ": " + track.total + " fixes, " + here + " on the viewed map (drawn)";
    if (track.total === 0) txt = track.name + ": no recorded positions";
    else if (here === 0) txt += "\nNot on the viewed map. Recorded on: " + maps.join(", ");
    else if (track.maps.length > 1) txt += "\nAll maps: " + maps.join(", ");
    el.textContent = txt; el.hidden = false;
  }
  function loadTrack(guid, switchMap) {
    return api("/api/track?" + C.qs({ rs: currentRs(), guid })).then((d) => {
      if (!track || track.guid !== guid) return;
      const fixes = (d.fixes || []).slice().sort((a, b) => a.t - b.t);
      const sum = C.trackSummary(fixes);
      track = Object.assign(track, { fixes, total: d.total !== undefined ? d.total : sum.total, maps: d.maps || sum.maps });
      if (fixes.length && (switchMap || viewMapId === null) && !trackFixes().length) {
        const lastMap = fixes[fixes.length - 1].mapId;
        if (S && S.maps && S.maps[String(lastMap)]) { viewMapId = lastMap; syncView(); }
      }
      renderTrackInfo(); renderHeader(); renderMapInput(); draw();
    }).catch(() => { if (track && track.guid === guid && switchMap) clearSearch(); });
  }
  function pickPlayer(row) {
    track = { guid: row.guid, name: row.name, reaction: row.reaction || "unknown", total: 0, maps: [], fixes: [] };
    selected = row.guid;
    $("results").hidden = true; $("qclear").hidden = false;
    if (S) { renderList(); draw(); }
    loadTrack(row.guid, true);
    scopeMaybeChanged();
  }
  function clearSearch() {
    track = null; thit = [];
    if (searchTimer) { clearTimeout(searchTimer); searchTimer = null; }
    searchSeq++;
    $("q").value = ""; $("qclear").hidden = true; $("results").hidden = true;
    renderTrackInfo(); if (S) renderList(); draw(); scopeMaybeChanged();
  }
  function renderResults(rows) {
    const ul = $("results");
    ul.innerHTML = "";
    if (!rows.length) { const li = document.createElement("li"); li.className = "none"; li.textContent = "No matching players"; ul.appendChild(li); }
    for (const r of rows) {
      const li = document.createElement("li");
      li.innerHTML = '<span class="sw" style="background:' + (COLORS[r.reaction] || COLORS.unknown) + '"></span><span class="nm">' + C.esc(r.name) +
        '</span><span class="meta">' + (r.fixes || 0) + " fixes, " + new Date(r.lastSeen).toLocaleString() + "</span>";
      li.addEventListener("click", () => pickPlayer(r));
      ul.appendChild(li);
    }
    ul.hidden = false;
  }
  function runSearch(q) {
    const seq = ++searchSeq;
    api("/api/players?" + C.qs({ rs: currentRs(), q })).then((d) => { if (seq === searchSeq) renderResults((d.players || []).slice(0, 50)); }).catch(() => {});
  }
  $("q").addEventListener("input", () => {
    const q = $("q").value.trim();
    $("qclear").hidden = $("q").value === "" && !track;
    if (searchTimer) clearTimeout(searchTimer);
    if (!q) { searchSeq++; $("results").hidden = true; return; }
    searchTimer = setTimeout(() => runSearch(q), 250);
  });
  $("qclear").addEventListener("click", clearSearch);
  window.addEventListener("keydown", (e) => { if (e.key === "Escape" && (track || $("q").value !== "")) clearSearch(); });

  // ---------- history table ----------
  function setHistory(on) { $("history").hidden = !on; if (on) { renderHistory(); loadCloud(); } setTimeout(resize, 0); }
  function renderHistory() {
    const tb = $("htable").tBodies[0];
    tb.innerHTML = "";
    const rows = cloudRows.slice().sort((a, b) => b.lastSeen - a.lastSeen);
    $("hcount").textContent = rows.length + " targets";
    for (const p of rows.slice(0, 500)) {
      const h = C.historyRow(p), tr = document.createElement("tr");
      tr.innerHTML = "<td>" + C.esc(h.name) + "</td><td>" + C.num(h.avgDmgHit) + "</td><td>" + C.num(h.avgDmgEvent) + "</td><td>" + C.num(h.avgHp) + "</td><td>" + C.num(h.maxHp) +
        "</td><td>" + h.samples + "</td><td>" + new Date(h.lastSeen).toLocaleString() + "</td><td>" + C.esc(h.zone) + "</td>";
      tb.appendChild(tr);
    }
  }

  // ---------- bounty ----------
  const BOUNTY_ROWS = 50;
  let bountySeq = 0;
  function renderBounty() {
    const ul = $("bountyList");
    ul.innerHTML = "";
    $("bountyCount").textContent = B.rows.length ? "(" + B.rows.length + " ranked)" : "";
    if (!B.rows.length) {
      const li = document.createElement("li"); li.className = "none";
      li.textContent = "No bounties yet. Kills of friendly players are recorded from live logs; use Analyze logs for older fights.";
      ul.appendChild(li); return;
    }
    for (const r of B.rows.slice(0, BOUNTY_ROWS)) {
      const li = document.createElement("li");
      li.className = track && track.guid === r.guid ? "sel" : "";
      li.title = r.name + "\n" + r.kb + " killing blows, " + r.assists + " assists";
      li.innerHTML = '<span class="rk">' + r.rank + '</span><span class="nm">' + C.esc(r.name) + '</span><span class="bv">' + r.score + '</span><span class="meta">' + r.kb + " KB / " + r.assists + " A</span>";
      li.addEventListener("click", () => pickBounty(r));
      ul.appendChild(li);
    }
  }
  function pickBounty(r) {
    $("q").value = r.name; $("qclear").hidden = false;
    pickPlayer({ guid: r.guid, name: r.name, reaction: "hostile" });
    renderBounty();
    api("/api/bounty/player?" + C.qs({ rs: currentRs(), guid: r.guid })).then((d) => {
      const e = d.entry || d, el = $("bDetail");
      el.textContent = (e.name || r.name) + ": rank " + (d.rank || e.rank || r.rank) + ", " + (e.kb !== undefined ? e.kb : r.kb) + " killing blows, " + (e.assists !== undefined ? e.assists : r.assists) +
        " assists, bounty " + (e.score !== undefined ? e.score : r.score);
      el.hidden = false;
    }).catch(() => {});
  }
  function loadBounty() {
    if (!token) return Promise.resolve();
    const seq = ++bountySeq;
    return api("/api/bounty?" + C.qs({ rs: currentRs() })).then((d) => {
      if (seq !== bountySeq) return;
      B = C.rankBounty(d.entries);
      renderBounty(); if (S) { renderList(); draw(); }
    }).catch(() => {});
  }

  // ---------- upload (manual files) and Analyze logs ----------
  let busy = false;
  const status = (t) => { $("upStatus").textContent = t; };
  const bar = (f) => { $("bar").hidden = f === null; $("barFill").style.width = Math.round((f || 0) * 100) + "%"; };
  async function ingestFiles(files, label) {
    let sent = 0, changed = 0;
    for (const file of files) {
      const client = newClient(true);
      try {
        const r = await I.ingestFile(file, { client, api, sha256, outbox, status: (t) => { status(t); $("bStatus").textContent = label ? label + " " + t : $("bStatus").textContent; }, progress: bar }, {});
        sent += r.received; changed += r.changed;
        const rs = (await client.call({ type: "flush" })).ruleset; noteRuleset(rs);
      } finally { client.terminate(); }
    }
    return { sent, changed };
  }
  async function uploadFiles(files) {
    if (busy || !token) return;
    busy = true; $("pick").disabled = true;
    try {
      const r = await ingestFiles(files);
      status("Done: " + r.sent + " players sent, " + r.changed + " new or updated.");
    } catch (e) { status("Error: " + e.message + (outbox.length ? " (will retry on the next upload)" : "")); }
    busy = false; $("pick").disabled = false; setTimeout(() => bar(null), 1500);
    refreshAll();
  }
  $("pick").addEventListener("click", () => $("file").click());
  $("file").addEventListener("change", () => { const fs = Array.from($("file").files); $("file").value = ""; if (fs.length) uploadFiles(fs); });
  const drop = $("drop");
  for (const t of ["dragenter", "dragover"]) drop.addEventListener(t, (e) => { e.preventDefault(); drop.classList.add("over"); });
  for (const t of ["dragleave", "drop"]) drop.addEventListener(t, (e) => { e.preventDefault(); drop.classList.remove("over"); });
  drop.addEventListener("drop", (e) => { const fs = Array.from(e.dataTransfer.files); if (fs.length) uploadFiles(fs); });

  $("bAnalyze").addEventListener("click", () => $("bFiles").click());
  $("bFiles").addEventListener("change", async () => {
    const fs = Array.from($("bFiles").files); $("bFiles").value = "";
    if (!fs.length || busy || !token) return;
    const rs = currentRs();
    if (!window.confirm("This clears ALL stored data of the " + rsName(rs) + " rule-set (players, tracks, bounty, upload ledger) and rebuilds it from the " + fs.length + " log file(s) you chose. Continue?")) return;
    busy = true; $("bAnalyze").disabled = true;
    try {
      $("bStatus").textContent = "Resetting " + rsName(rs) + "...";
      await api("/api/reset", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ rs, what: "all" }) });
      const r = await ingestFiles(fs, "Analyzing:");
      $("bStatus").textContent = "Done: " + fs.length + " file(s), " + r.sent + " players sent.";
    } catch (e) { $("bStatus").textContent = "Analyze failed: " + e.message; }
    busy = false; $("bAnalyze").disabled = false; setTimeout(() => bar(null), 1500);
    refreshAll();
  });

  // ---------- live mode ----------
  let live = false, liveSession = null, liveDir = null, livePoll = null, liveSnap = null;
  async function listLogs() {
    const out = [];
    for await (const h of liveDir.values()) {
      if (h.kind === "file" && /^WoWCombatLog.*\.txt$/i.test(h.name)) {
        const f = await h.getFile();
        out.push({ name: h.name, lastModified: f.lastModified, getFile: () => h.getFile() });
      }
    }
    return out;
  }
  async function startLive() {
    if (!window.showDirectoryPicker || !token) return;
    try { liveDir = await window.showDirectoryPicker({ mode: "read" }); } catch (e) { return; }
    if (!liveClient) liveClient = newClient(false);
    sendConfig();
    liveSession = new I.LiveSession({ client: liveClient, api, sha256, outbox, listLogs, status, intervalSec: () => Math.max(5, settings.uploadSec || 60) });
    live = true; if (poller) poller.stop(); $("liveBtn").hidden = true; $("liveStop").hidden = false; setConn("ok"); status("Live: reading logs...");
    const tick = async () => {
      if (!live) return;
      await liveSession.poll();
      try { const r = await liveClient.call({ type: "snap" }); onState(r.snap); } catch (e) { /* ignore */ }
    };
    tick(); livePoll = setInterval(tick, 1000);
  }
  async function stopLive(silent) {
    if (!live) return;
    live = false; if (livePoll) { clearInterval(livePoll); livePoll = null; }
    $("liveBtn").hidden = false; $("liveStop").hidden = true;
    const ses = liveSession; liveSession = null;
    if (ses && !silent) { status("Stopping: final upload..."); try { await ses.stop(); } catch (e) { status("Error: " + e.message); } }
    if (liveClient) { liveClient.terminate(); liveClient = null; }
    if (!silent) { refreshAll(); if (token) startPoller(); }
  }
  $("liveBtn").addEventListener("click", startLive);
  $("liveStop").addEventListener("click", () => stopLive(false));
  if (!window.showDirectoryPicker) { $("liveBtn").disabled = true; $("liveBtn").title = "Live tailing needs a Chromium-based browser (Chrome, Edge). Use Choose files instead."; }
  window.addEventListener("beforeunload", (e) => { if (live || outbox.length) { e.preventDefault(); e.returnValue = ""; } });

  // ---------- map combobox ----------
  let mapOpen = false, mapEditing = false, mapItems = [], mapAct = -1;
  function buildMapItems(filter) {
    const f = normName(filter), v = (S && S.view) || {}, items = [];
    if (!f || "autofollowgame".indexOf(f) >= 0) items.push({ kind: "auto", label: S && S.cloud ? "Auto (most players)" : "Auto (follow game)" });
    for (const m of v.available || []) if (!f || normName(m.name).indexOf(f) >= 0) items.push({ kind: "map", mapId: m.mapId, label: m.name });
    for (const m of v.noImage || []) if (!f || normName(m.name).indexOf(f) >= 0) items.push({ kind: "noimage", mapId: m.mapId, label: m.name, disabled: true });
    return items;
  }
  function renderMapInput() {
    if (mapEditing) return;
    $("mapq").value = !S ? "" : viewMapId === null ? (viewName() ? viewName() + " (auto)" : "Auto") : viewName();
  }
  function firstEnabled(from, step) { for (let i = from; i >= 0 && i < mapItems.length; i += step) if (!mapItems[i].disabled) return i; return -1; }
  function renderMapList() {
    const ul = $("maplist");
    ul.innerHTML = "";
    $("mapq").setAttribute("aria-expanded", mapOpen ? "true" : "false");
    ul.hidden = !mapOpen;
    if (!mapOpen) return;
    mapItems.forEach((it, i) => {
      const li = document.createElement("li");
      const cur = it.kind === "auto" ? viewMapId === null : viewMapId === it.mapId;
      li.id = "mapopt" + i;
      li.className = (it.kind === "auto" ? "auto " : "") + (it.disabled ? "dis " : "") + (cur ? "cur " : "") + (i === mapAct ? "act" : "");
      li.setAttribute("role", "option"); li.setAttribute("aria-selected", cur ? "true" : "false");
      if (it.disabled) li.setAttribute("aria-disabled", "true");
      li.innerHTML = "<span>" + C.esc(it.label) + "</span>" + (it.disabled ? '<span class="hint">no image</span>' : "");
      if (!it.disabled) li.addEventListener("click", () => chooseMap(it));
      ul.appendChild(li);
    });
    if (!mapItems.some((it) => it.kind !== "auto")) {
      const li = document.createElement("li"); li.className = "none"; li.setAttribute("role", "option"); li.setAttribute("aria-disabled", "true"); li.textContent = "No matching maps"; ul.appendChild(li);
    }
    if (mapAct >= 0) $("mapq").setAttribute("aria-activedescendant", "mapopt" + mapAct); else $("mapq").removeAttribute("aria-activedescendant");
  }
  function openMapList(filter) { mapItems = buildMapItems(filter); mapOpen = true; mapAct = firstEnabled(0, 1); renderMapList(); }
  function closeMapList() { mapOpen = false; mapEditing = false; mapAct = -1; renderMapList(); renderMapInput(); }
  function chooseMap(it) {
    if (!it || it.disabled) return;
    viewMapId = it.kind === "auto" ? null : it.mapId;
    closeMapList();
    if (S) { syncView(); renderList(); renderHeader(); renderTrackInfo(); renderMapInput(); draw(); }
    scopeMaybeChanged();
  }
  $("mapq").addEventListener("focus", () => { try { $("mapq").select(); } catch (e) { /* ignore */ } });
  $("mapq").addEventListener("click", () => { mapEditing = false; openMapList(""); try { $("mapq").select(); } catch (e) { /* ignore */ } });
  $("mapq").addEventListener("input", () => { mapEditing = true; openMapList($("mapq").value); });
  $("mapq").addEventListener("keydown", (e) => {
    const k = e.key;
    if (k === "ArrowDown" || k === "ArrowUp") {
      e.preventDefault();
      if (!mapOpen) { openMapList(mapEditing ? $("mapq").value : ""); return; }
      const n = firstEnabled(k === "ArrowDown" ? mapAct + 1 : mapAct - 1, k === "ArrowDown" ? 1 : -1);
      if (n >= 0) { mapAct = n; renderMapList(); }
    } else if (k === "Enter") { if (mapOpen) { e.preventDefault(); chooseMap(mapItems[mapAct]); } }
    else if (k === "Escape") { if (mapOpen) { e.preventDefault(); e.stopPropagation(); closeMapList(); } }
    else if (k === "Tab") { if (mapOpen) closeMapList(); }
  });
  document.addEventListener("mousedown", (e) => { if (mapOpen && !$("mapbox").contains(e.target)) closeMapList(); });

  // ---------- cloud poll (GET /api/poll, scoped to what is viewed) ----------
  function pollUi() { return { rs: currentRs(), track, viewMapId: viewMapId !== null ? viewMapId : null, showBlue: $("showBlue").checked, showRed: $("showRed").checked }; }
  function onPoll(r, sc) {
    if (live) return;
    if (sc.scope === "player") {
      if (r.player) PL.mergeRows(cloudMap, [r.player]);
      if (track && track.guid === sc.guid && r.fixes && r.fixes.length) { track.fixes = PL.mergeFixes(track.fixes, r.fixes); track.total = track.fixes.length; track.maps = C.trackSummary(track.fixes).maps; renderTrackInfo(); }
    } else PL.mergeRows(cloudMap, r.players);
    cloudRows = Array.from(cloudMap.values());
    onState(C.cloudSnapshot(cloudRows, cloudMaps, Date.now()));
    if ($("showHistory").checked) renderHistory();
  }
  function startPoller() {
    if (poller) { poller.stop(); }
    poller = new PL.Poller({
      api, getScope: () => (live || !token ? null : PL.pollScope(pollUi())), onResponse: onPoll, onError: (e) => { $("foot").textContent = "Poll: " + e.message; },
      setTimeout: (f, ms) => setTimeout(f, ms), clearTimeout: (h) => clearTimeout(h), now: () => Date.now(), random: Math.random, hidden: () => document.hidden,
      getTrackTail: () => (track && track.fixes.length ? track.fixes[track.fixes.length - 1].t : 0),
    });
    poller.start();
  }
  let lastScopeKey = "";
  function scopeMaybeChanged() { if (!poller) return; const sc = PL.pollScope(pollUi()), k = sc ? PL.scopeKey(sc) : ""; if (k !== lastScopeKey) { lastScopeKey = k; poller.poke(true); } }
  document.addEventListener("visibilitychange", () => { if (poller) poller.onVisibility(); });
  for (const ev of ["mousedown", "keydown", "wheel"]) window.addEventListener(ev, () => { if (poller) poller.poke(false); scopeMaybeChanged(); }, { passive: true });
  for (const id of ["showBlue", "showRed", "stale"]) $(id).addEventListener("change", scopeMaybeChanged);

  // ---------- session ----------
  let timers = [];
  function stopTimers() { for (const t of timers) clearInterval(t); timers = []; if (poller) { poller.stop(); poller = null; } }
  function refreshAll() { return Promise.all([loadCloud(), loadBounty()]); }
  async function enter() {
    $("auth").hidden = true; $("user").textContent = username; $("logout").hidden = false; setConn("poll");
    try {
      const me = await api("/api/me");
      if (me && me.settings && typeof me.settings === "object") {
        const s = me.settings;
        settings = Object.assign({ mode: "auto", ruleset: "pvp", cal: {}, prefs: {}, uploadSec: 60 }, settings, s);
        settings.cal = Object.assign({}, JSON.parse(pref.get("settings", "{}") || "{}").cal || {}, s.cal || {});
        settings.prefs = Object.assign({}, settings.prefs, s.prefs || {});
        if (!P_ok(settings.ruleset)) settings.ruleset = "pvp";
        applyPrefs();
      }
    } catch (e) { /* keep local settings */ }
    lastRs = currentRs(); paintRuleset(); sendConfig();
    await refreshAll();
    stopTimers();
    startPoller();
    timers.push(setInterval(() => { loadBounty(); }, 60000));
    timers.push(setInterval(() => { if (S) draw(); }, 2000));
  }
  const P_ok = (id) => !!RadarParser.parseRuleset(id);

  resize();
  paintRuleset();
  if (token) enter(); else showAuth();
})();
