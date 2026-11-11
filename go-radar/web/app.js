"use strict";
(function () {
  const $ = (id) => document.getElementById(id);
  const COLORS = { friendly: "#3b82f6", hostile: "#ef4444", neutral: "#ef4444", unknown: "#ef4444", stale: "#4b5563" };
  const cv = $("radar"), ctx = cv.getContext("2d");
  let S = null;            // last state
  let selected = null;     // selected guid
  let hit = [];            // [{x,y,r,pip}] for hover
  let cam = { x: 0, y: 0 };// world centre when not following
  let imgCache = {};       // url -> Image
  let dpr = 1;
  let B = { rows: [], by: {}, stats: null }; // bounty leaderboard: rows (ranked) and by[guid] = row
  let RS = null;           // active server rule-set {active, mode, detected} (from /api/ruleset and view.ruleset)

  // ---- prefs (best effort) ----
  const pref = {
    get(k, d) { try { const v = localStorage.getItem("nr." + k); return v === null ? d : v; } catch (e) { return d; } },
    set(k, v) { try { localStorage.setItem("nr." + k, v); } catch (e) { /* ignore */ } },
  };
  for (const id of ["follow", "stale", "bg", "fit", "showBlue", "showRed"]) {
    $(id).checked = pref.get(id, "1") === "1";
    $(id).addEventListener("change", () => { pref.set(id, $(id).checked ? "1" : "0"); draw(); });
  }
  $("range").value = pref.get("range", "300");
  $("range").addEventListener("input", () => { pref.set("range", $("range").value); draw(); });
  // The history panel is hidden by default and is NOT remembered across runs.
  $("showHistory").checked = false;
  $("showHistory").addEventListener("change", () => setHistory($("showHistory").checked));
  window.addEventListener("keydown", (e) => {
    if ((e.key === "h" || e.key === "H") && !e.ctrlKey && !e.altKey && !e.metaKey && e.target.tagName !== "INPUT") {
      $("showHistory").checked = !$("showHistory").checked;
      setHistory($("showHistory").checked);
    }
  });

  // ---- data ----
  let es = null, pollTimer = null;
  function connect() {
    if (!window.EventSource) { startPolling(); return; }
    es = new EventSource("/api/events");
    es.addEventListener("state", (e) => { stopPolling(); setConn("ok"); onState(JSON.parse(e.data)); });
    es.onerror = () => { setConn("poll"); startPolling(); };
  }
  function startPolling() {
    if (pollTimer) return;
    const tick = () => fetch("/api/state").then((r) => r.json()).then(onState).catch(() => setConn("off"));
    tick();
    pollTimer = setInterval(tick, 1000);
  }
  function stopPolling() { if (pollTimer) { clearInterval(pollTimer); pollTimer = null; } }
  function setConn(k) { $("conn").className = "dot " + (k === "off" ? "" : k); }

  function onState(s) {
    S = s;
    if (s.view && s.view.ruleset) applyRuleset(s.view.ruleset);
    if (viewMapId !== null && !availById(viewMapId)) viewMapId = null; // the chosen map vanished: back to auto
    syncView(); renderList(); renderHeader(); renderTrackInfo(); renderMapInput(); draw();
  }

  // ---- viewed map: null = follow the game (S.mapId), otherwise a user-chosen uiMapID ----
  let viewMapId = null, lastVid = null;
  function viewId() { return viewMapId !== null ? viewMapId : (S ? S.mapId : 0); }
  function availById(id) {
    const a = S && S.view && S.view.available;
    if (!a) return null;
    for (const m of a) if (m.mapId === id) return m;
    return null;
  }
  // Image info of the viewed map: the server's current-zone view in auto mode, else the chosen entry.
  function viewZone() {
    if (!S || !S.view) return null;
    return viewMapId === null ? (S.view.zone || null) : availById(viewMapId);
  }
  function viewName() {
    const m = S && S.maps && S.maps[String(viewId())];
    if (viewMapId !== null) { const a = availById(viewMapId); return (a && a.name) || (m && m.name) || ""; }
    return (S && S.zone) || (m && m.name) || "";
  }
  // Zoom/pan restart whenever the viewed map changes (user choice or the game changing zone in auto mode).
  function syncView() {
    const id = viewId();
    if (id !== lastVid) { lastVid = id; fz.z = 1; fz.cx = 50; fz.cy = 50; }
  }

  // ---- helpers ----
  function colorOf(p) { return p.state === "stale" ? COLORS.stale : (COLORS[p.reaction] || COLORS.unknown); }
  function fmtAge(sec) {
    sec = Math.max(0, Math.round(sec));
    if (sec < 60) return sec + "s";
    const m = Math.floor(sec / 60);
    return m < 60 ? m + "m" : Math.floor(m / 60) + "h" + (m % 60) + "m";
  }
  function esc(s) { return String(s).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c])); }
  function onMap(p) { const id = viewId(); return p.posKind !== "none" && (!id || p.mapId === id); }
  function visible(p) {
    if (track && p.guid !== track.guid) return false; // a searched player is selected: hide every other dot
    if (p.me) return $("stale").checked || p.state !== "stale";
    if (!$(p.reaction === "friendly" ? "showBlue" : "showRed").checked) return false; // blue = friendly, red = everything else
    return $("stale").checked || p.state !== "stale";
  }

  function renderHeader() {
    const mm = curMap();
    const vid = viewId();
    $("zone").textContent = (viewName() || "-") + (vid ? " (map " + vid + ")" : "") + (viewMapId !== null ? " [viewing]" : "") +
      (mm && S.me && S.me.posKind !== "none" && S.me.mapId === vid && fitActive() ? " | me " + fmtPct(worldToPct(mm, S.me.x, S.me.y)) : "");
    const act = S.pips.filter((p) => p.state === "active").length;
    $("summary").textContent = S.pips.length + " tracked, " + act + " active, " + (S.pips.length - act) +
      " stale" + (S.me ? " | me: " + S.me.name : "");
    $("empty").hidden = S.pips.length > 0;
    const v = S.view || {};
    $("foot").textContent = (v.logFile ? "Log: " + v.logFile : "No log file yet") + " | events " + S.stats.events +
      (S.advanced === false ? " | ADVANCED LOGGING IS OFF (no positions)" : "");
  }

  function renderList() {
    const ul = $("list");
    const all = S.pips.filter(visible), rows = all.slice(0, 400); // list is capped; the radar shows everything
    ul.innerHTML = "";
    for (const p of rows) {
      const li = document.createElement("li");
      li.className = (p.state === "stale" ? "stale " : "") + (p.guid === selected ? "sel" : "");
      const hp = p.hasHp && p.maxHp > 0 ? Math.round((100 * p.hp) / p.maxHp) + "% " : "";
      const pos = p.posKind === "none" ? "no fix" : p.posKind === "approx" ? "~" : "";
      const bo = B.by[p.guid];
      li.innerHTML = '<span class="sw" style="background:' + colorOf(p) + '"></span><span class="nm">' +
        esc(p.name) + (p.me ? " (me)" : "") + '</span><span class="meta">' + (bo ? '<span class="bty">bounty ' + bo.bounty + "</span> " : "") +
        hp + pos + " " + fmtAge(p.ageSec) + "</span>";
      li.addEventListener("click", () => { selected = p.guid; if (p.posKind !== "none") { cam = { x: p.x, y: p.y }; $("follow").checked = false; } renderList(); draw(); });
      ul.appendChild(li);
    }
  }

  // ---- radar drawing ----
  function resize() {
    dpr = window.devicePixelRatio || 1;
    const w = cv.clientWidth, h = cv.clientHeight;
    cv.width = Math.max(1, Math.round(w * dpr));
    cv.height = Math.max(1, Math.round(h * dpr));
    draw();
  }
  window.addEventListener("resize", resize);
  if (window.ResizeObserver) new ResizeObserver(resize).observe($("radarWrap"));

  function centre() {
    if ($("follow").checked && S && S.me && S.me.posKind !== "none" && onMap(S.me)) return { x: S.me.x, y: S.me.y };
    return cam;
  }
  // World -> screen. Standard orientation: X is north (up), Y is west (left).
  // ---- fixed map frame: top-left = (0,0), bottom-right = (100,100) ----
  // Per-map calibration {dx,dy,sx,sy} (in frame percent) corrects maps whose MAP_CHANGE rectangle
  // does not match the artwork. Stored per uiMapID; Shift+arrows nudge, Shift +/- scale, Shift+0 resets.
  function curMap() { return S && S.maps && viewId() ? S.maps[String(viewId())] || null : null; }
  function fitActive() {
    const zi = viewZone();
    return !!(zi && curMap() && $("fit").checked && $("bg").checked);
  }
  function getCal() {
    try { const c = JSON.parse(pref.get("cal." + viewId(), "")); if (c && isFinite(c.dx)) return c; } catch (e) { /* default */ }
    return { dx: 0, dy: 0, sx: 1, sy: 1 };
  }
  function setCal(c) { pref.set("cal." + viewId(), JSON.stringify(c)); draw(); }
  // world -> frame percent (Y counted from the top, X from the left), after calibration
  function worldToPct(m, x, y) {
    const c = getCal();
    const px = ((m.y1 - y) / (m.y1 - m.y0)) * 100, py = ((m.x1 - x) / (m.x1 - m.x0)) * 100;
    return [px * c.sx + c.dx, py * c.sy + c.dy];
  }
  function frameRect() {
    const zi = viewZone(), w = cv.width / dpr, h = cv.height / dpr, pad = 26;
    const ar = (zi.w || 1002) / (zi.h || 668), aw = w - pad * 2, ah = h - pad * 2;
    let fw = aw, fh = aw / ar;
    if (fh > ah) { fh = ah; fw = ah * ar; }
    return { x: (w - fw) / 2, y: (h - fh) / 2 + 4, w: fw, h: fh };
  }
  // Zoom/pan state for the fixed frame: zoom z >= 1, view centre (cx,cy) in frame percent.
  // The view is always clamped so it never shows anything outside the map (0..100).
  const fz = { z: 1, cx: 50, cy: 50 };
  const ZMAX = 12;
  function clampView() {
    fz.z = Math.max(1, Math.min(ZMAX, fz.z));
    const half = 50 / fz.z;
    fz.cx = Math.max(half, Math.min(100 - half, fz.cx));
    fz.cy = Math.max(half, Math.min(100 - half, fz.cy));
  }
  function fitPan(dx, dy, from) {
    const f = frameRect(), k = 100 / (f.w * fz.z), kv = 100 / (f.h * fz.z);
    fz.cx = from.cx - dx * k; fz.cy = from.cy - dy * kv;
    if (Math.abs(dx) + Math.abs(dy) > 2) { dragMoved = true; $("follow").checked = false; }
    clampView(); draw();
  }
  function fitZoom(mx, my, factor) {
    const f = frameRect(), half = 50 / fz.z;
    // percent under the cursor stays put while zooming
    const px = fz.cx - half + ((mx - f.x) / f.w) * (100 / fz.z), py = fz.cy - half + ((my - f.y) / f.h) * (100 / fz.z);
    const nz = Math.max(1, Math.min(ZMAX, fz.z * factor)), nh = 50 / nz;
    fz.z = nz;
    fz.cx = px - ((mx - f.x) / f.w) * (100 / nz) + nh; fz.cy = py - ((my - f.y) / f.h) * (100 / nz) + nh;
    $("follow").checked = false;
    clampView(); draw();
  }
  function fmtPct(p) { return p[0].toFixed(1) + ", " + p[1].toFixed(1); }
  window.addEventListener("keydown", (e) => {
    if (!e.shiftKey || !S || !viewId() || !fitActive() || e.target.tagName === "INPUT") return;
    const c = getCal(), st = 0.5, k = e.key;
    if (k === "ArrowLeft") c.dx -= st; else if (k === "ArrowRight") c.dx += st;
    else if (k === "ArrowUp") c.dy -= st; else if (k === "ArrowDown") c.dy += st;
    else if (k === "+" || k === "=") { c.sx *= 1.005; c.sy *= 1.005; }
    else if (k === "_" || k === "-") { c.sx /= 1.005; c.sy /= 1.005; }
    else if (k === ")" || k === "0") { c.dx = 0; c.dy = 0; c.sx = 1; c.sy = 1; }
    else return;
    e.preventDefault(); setCal(c);
  });

  function view() {
    const w = cv.width / dpr, h = cv.height / dpr;
    if (fitActive()) {
      const m = curMap(), f = frameRect(), z = fz.z;
      if ($("follow").checked && z > 1 && S.me && S.me.posKind !== "none" && S.me.mapId === viewId()) {
        const mp = worldToPct(m, S.me.x, S.me.y); fz.cx = mp[0]; fz.cy = mp[1];
      }
      clampView();
      const half = 50 / z, ox = fz.cx - half, oy = fz.cy - half;
      const toScreen = (x, y) => { const p = worldToPct(m, x, y); return [f.x + ((p[0] - ox) * z / 100) * f.w, f.y + ((p[1] - oy) * z / 100) * f.h]; };
      return { w, h, fit: true, f, m, z, ox, oy, scale: 1, range: 0, c: { x: 0, y: 0 }, toScreen, alt: false };
    }
    const c = centre();
    const range = Number($("range").value);
    const scale = Math.min(w, h) / 2 / range;
    const alt = S && S.view && S.view.orientation === "alt";
    const toScreen = alt
      ? (x, y) => [w / 2 + (x - c.x) * scale, h / 2 - (y - c.y) * scale]
      : (x, y) => [w / 2 - (y - c.y) * scale, h / 2 - (x - c.x) * scale];
    return { w, h, c, range, scale, toScreen, alt };
  }

  function loadImage(url, cb) {
    let e = imgCache[url];
    if (e) return e.ok ? e.img : null;
    const img = new Image();
    e = imgCache[url] = { img, ok: false };
    img.onload = () => { e.ok = true; draw(); };
    img.onerror = () => { e.err = true; };
    img.src = url;
    return null;
  }

  function draw() {
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    const w = cv.width / dpr, h = cv.height / dpr;
    ctx.clearRect(0, 0, w, h);
    hit = []; thit = [];
    if (!S) return;
    if (!$("bg").checked) return; // Map off hides the whole radar view: map, dots and tracks
    const V = view();
    if (V.fit) drawFrame(V); else { drawMap(V); drawRings(V); }
    drawTrack(V);
    const mine = S.pips.filter((p) => onMap(p) && visible(p));
    // stale first so active pips draw on top
    mine.sort((a, b) => (a.state === "stale" ? 0 : 1) - (b.state === "stale" ? 0 : 1));
    for (const p of mine) drawPip(V, p);
    if (S.me && onMap(S.me) && visible(S.me) && !mine.includes(S.me)) drawPip(V, S.me);
    if (V.clipped) ctx.restore();
  }

  function drawFrame(V) {
    const f = V.f, z = V.z, img = loadImage(viewZone().url);
    const sxp = (p) => f.x + ((p - V.ox) * z / 100) * f.w, syp = (p) => f.y + ((p - V.oy) * z / 100) * f.h;
    ctx.save();
    ctx.beginPath(); ctx.rect(f.x, f.y, f.w, f.h); ctx.clip();   // nothing is ever drawn outside the map
    if (img) { ctx.globalAlpha = 0.9; ctx.drawImage(img, sxp(0), syp(0), f.w * z, f.h * z); ctx.globalAlpha = 1; }
    ctx.strokeStyle = "rgba(148,163,184,.25)"; ctx.lineWidth = 1;
    const step = z >= 6 ? 2 : z >= 3 ? 5 : 10;
    for (let i = 0; i <= 100; i += step) {
      ctx.beginPath(); ctx.moveTo(sxp(i), f.y); ctx.lineTo(sxp(i), f.y + f.h); ctx.moveTo(f.x, syp(i)); ctx.lineTo(f.x + f.w, syp(i)); ctx.stroke();
    }
    ctx.restore();
    ctx.fillStyle = "#6b7280"; ctx.font = "10px sans-serif";
    for (let i = 0; i <= 100; i += step) {
      const gx = sxp(i), gy = syp(i);
      if (gx >= f.x - 1 && gx <= f.x + f.w + 1) { ctx.textAlign = "center"; ctx.fillText(String(i), gx, f.y - 5); }
      if (gy >= f.y - 1 && gy <= f.y + f.h + 1) { ctx.textAlign = "right"; ctx.fillText(String(i), f.x - 4, gy + 3); }
    }
    ctx.textAlign = "start";
    ctx.strokeStyle = "#2f3b52"; ctx.strokeRect(f.x, f.y, f.w, f.h);
    // pips are clipped to the map as well (restored after the pip pass in draw())
    ctx.save(); ctx.beginPath(); ctx.rect(f.x, f.y, f.w, f.h); ctx.clip(); V.clipped = true;
  }

  function drawMap(V) {
    const zi = viewZone();
    const m = curMap();
    if (!$("bg").checked || !zi || !m || V.alt) return;
    const img = loadImage(zi.url);
    if (!img) return;
    // image: left = west (y1), right = east (y0), top = north (x1), bottom = south (x0)
    const [sx, sy] = V.toScreen(m.x1, m.y1);
    const iw = (m.y1 - m.y0) * V.scale, ih = (m.x1 - m.x0) * V.scale;
    ctx.globalAlpha = 0.55;
    ctx.drawImage(img, sx, sy, iw, ih);
    ctx.globalAlpha = 1;
    ctx.strokeStyle = "#2f3b52";
    ctx.strokeRect(sx, sy, iw, ih);
  }

  function drawRings(V) {
    const [cx, cy] = V.toScreen(V.c.x, V.c.y);
    ctx.strokeStyle = "#232b39"; ctx.fillStyle = "#4a5568"; ctx.lineWidth = 1; ctx.font = "11px sans-serif";
    const step = V.range > 1500 ? 500 : V.range > 600 ? 200 : V.range > 200 ? 100 : 25;
    for (let r = step; r <= V.range * 1.5; r += step) {
      ctx.beginPath(); ctx.arc(cx, cy, r * V.scale, 0, Math.PI * 2); ctx.stroke();
      ctx.fillText(r + "y", cx + 3, cy - r * V.scale - 2);
    }
    ctx.beginPath(); ctx.moveTo(cx - V.w, cy); ctx.lineTo(cx + V.w, cy); ctx.moveTo(cx, cy - V.h); ctx.lineTo(cx, cy + V.h); ctx.stroke();
    ctx.fillStyle = "#6b7280"; ctx.font = "12px sans-serif";
    ctx.fillText("N", V.w / 2 - 4, 14);
  }

  function drawPip(V, p) {
    const [x, y] = V.toScreen(p.x, p.y);
    if (x < -20 || y < -20 || x > V.w + 20 || y > V.h + 20) return;
    if (V.fit) { const f = V.f; if (x < f.x || y < f.y || x > f.x + f.w || y > f.y + f.h) return; }
    const col = colorOf(p), r = p.me ? 7 : 5;
    ctx.lineWidth = 2;
    const fac = COLORS[p.reaction] || COLORS.unknown; // faction colour, kept on the outer edge when stale
    if (p.posKind === "approx") {
      ctx.strokeStyle = fac; ctx.setLineDash([3, 2]);
      ctx.beginPath(); ctx.arc(x, y, r, 0, Math.PI * 2); ctx.stroke(); ctx.setLineDash([]);
    } else {
      ctx.fillStyle = col; ctx.beginPath(); ctx.arc(x, y, r, 0, Math.PI * 2); ctx.fill();
      if (p.state === "stale") { ctx.strokeStyle = fac; ctx.lineWidth = 2; ctx.beginPath(); ctx.arc(x, y, r, 0, Math.PI * 2); ctx.stroke(); }
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

  // Hostile players on the bounty leaderboard get a gold ring and a small gold diamond above the dot
  // (no names on dots; the dot colour itself is unchanged).
  function drawBountyMark(x, y, r) {
    ctx.save();
    ctx.strokeStyle = "#f5b301"; ctx.lineWidth = 1.5; ctx.setLineDash([]);
    ctx.beginPath(); ctx.arc(x, y, r + 3.5, 0, Math.PI * 2); ctx.stroke();
    ctx.fillStyle = "#f5b301";
    const dy = y - r - 9;
    ctx.beginPath(); ctx.moveTo(x, dy - 3.5); ctx.lineTo(x + 3, dy); ctx.lineTo(x, dy + 3.5); ctx.lineTo(x - 3, dy); ctx.closePath(); ctx.fill();
    ctx.restore();
  }

  // ---- interaction ----
  const tip = $("tip");
  cv.addEventListener("mousemove", (e) => {
    const rect = cv.getBoundingClientRect(), mx = e.clientX - rect.left, my = e.clientY - rect.top;
    if (drag && fitActive()) { fitPan(mx - drag.mx, my - drag.my, drag.fz0); return; }
    if (drag) { cam.x = drag.cx; cam.y = drag.cy; panBy(mx - drag.mx, my - drag.my); return; }
    let best = null;
    for (const h of hit) if (Math.hypot(h.x - mx, h.y - my) <= h.r) best = h;
    if (!best) { showTrackTip(mx, my, rect); return; }
    const p = best.p;
    const m = curMap();
    tip.textContent = p.name + (p.me ? " (me)" : "") + (m && fitActive() ? "\nmap " + fmtPct(worldToPct(m, p.x, p.y)) : "") + "\n" + p.reaction + ", " + p.state + ", " + p.posKind + " fix\n" +
      (p.hasHp ? "HP " + Math.round(p.hp) + "/" + Math.round(p.maxHp) + "\n" : "") + "last event " + fmtAge(p.ageSec) + " ago" +
      (p.damageHits ? "\ndamage dealt " + Math.round(p.damageTotal) + " in " + p.damageHits + " hits" : "") +
      (B.by[p.guid] ? "\nBOUNTY " + B.by[p.guid].bounty + " (rank " + B.by[p.guid].rank + ": " + B.by[p.guid].killingBlows + " kills, " + B.by[p.guid].assists + " assists)" : "");
    tip.style.left = Math.min(mx + 12, rect.width - 200) + "px"; tip.style.top = my + 12 + "px"; tip.hidden = false;
  });
  function showTrackTip(mx, my, rect) {
    const th = trackHover(mx, my);
    if (!th) { tip.hidden = true; return; }
    tip.textContent = trackTipText(th.f);
    tip.style.left = Math.min(mx + 12, rect.width - 200) + "px"; tip.style.top = my + 12 + "px"; tip.hidden = false;
  }
  cv.addEventListener("mouseleave", () => { tip.hidden = true; drag = null; });
  cv.addEventListener("click", () => { if (dragMoved) return; });
  let drag = null, dragMoved = false;
  cv.addEventListener("mousedown", (e) => {
    const rect = cv.getBoundingClientRect();
    const c = centre();
    drag = { mx: e.clientX - rect.left, my: e.clientY - rect.top, cx: c.x, cy: c.y, fz0: { cx: fz.cx, cy: fz.cy } }; dragMoved = false;
  });
  window.addEventListener("mouseup", () => { drag = null; });
  function panBy(dx, dy) {
    if (fitActive()) return; // fixed frame: no panning
    if (Math.abs(dx) + Math.abs(dy) > 2) { dragMoved = true; $("follow").checked = false; }
    const V = view(); const k = 1 / V.scale;
    // screen -> world delta for the active orientation
    if (V.alt) { cam.x -= dx * k; cam.y += dy * k; } else { cam.y += dx * k; cam.x += dy * k; }
    draw();
  }
  cv.addEventListener("dblclick", () => { if (fitActive()) { fz.z = 1; fz.cx = 50; fz.cy = 50; draw(); } });
  cv.addEventListener("wheel", (e) => {
    e.preventDefault();
    if (fitActive()) {
      const rect = cv.getBoundingClientRect();
      fitZoom(e.clientX - rect.left, e.clientY - rect.top, e.deltaY > 0 ? 1 / 1.2 : 1.2);
      return;
    }
    const r = $("range"), v = Number(r.value) * (e.deltaY > 0 ? 1.15 : 1 / 1.15);
    r.value = Math.max(Number(r.min), Math.min(Number(r.max), Math.round(v)));
    pref.set("range", r.value); draw();
  }, { passive: false });
  $("range").addEventListener("input", () => { $("rangeVal").textContent = $("range").value; });
  $("rangeVal").textContent = $("range").value;

  // ---- player search + recorded track (from /api/players and /api/track) ----
  const TRACK_BUCKETS = 4;     // older segments are drawn fainter, in this many steps
  let track = null;            // {guid,name,reaction,total,maps,fixes} of the selected player
  let thit = [];               // [{x,y,r,f}] hover targets of the track dots
  let searchTimer = null, searchSeq = 0, trackTimer = null;

  function trackColor() { return COLORS[track && track.reaction] || COLORS.unknown; }
  function trackFixes() {
    if (!track || !S || !viewId()) return [];
    return track.fixes.filter((f) => f.mapId === viewId());
  }
  function inView(V, x, y) {
    if (V.fit) return true; // the frame clip handles it
    return x >= -10 && y >= -10 && x <= V.w + 10 && y <= V.h + 10;
  }
  function drawTrackLines(V, pts) {
    if (pts.length < 2) return;
    ctx.save();
    ctx.strokeStyle = trackColor(); ctx.lineWidth = 1.5; ctx.setLineDash([2, 5]); ctx.lineCap = "round";
    const n = pts.length;
    for (let b = 0; b < TRACK_BUCKETS; b++) {
      const lo = Math.max(1, Math.floor((b * n) / TRACK_BUCKETS)), hi = Math.floor(((b + 1) * n) / TRACK_BUCKETS);
      if (hi <= lo) continue;
      ctx.globalAlpha = 0.3 + (0.6 * (b + 1)) / TRACK_BUCKETS;
      ctx.beginPath(); ctx.moveTo(pts[lo - 1][0], pts[lo - 1][1]);
      for (let i = lo; i < hi; i++) ctx.lineTo(pts[i][0], pts[i][1]);
      ctx.stroke();
    }
    ctx.restore();
  }
  function drawTrackDots(V, fixes, pts) {
    const col = trackColor(), last = fixes.length - 1;
    ctx.save();
    ctx.setLineDash([]); ctx.fillStyle = col; ctx.strokeStyle = col; ctx.lineWidth = 1.5;
    for (let i = 0; i <= last; i++) {
      const x = pts[i][0], y = pts[i][1];
      if (!inView(V, x, y)) continue;
      const r = i === last ? 4 : 2.5;
      ctx.globalAlpha = 0.45 + (0.55 * i) / Math.max(1, last);
      ctx.beginPath(); ctx.arc(x, y, r, 0, Math.PI * 2);
      if (fixes[i].kind === "approx") ctx.stroke(); else ctx.fill();
      thit.push({ x, y, r: r + 3, f: fixes[i] });
    }
    ctx.globalAlpha = 1;
    if (last >= 0 && inView(V, pts[last][0], pts[last][1])) {
      ctx.strokeStyle = "#fff"; ctx.beginPath(); ctx.arc(pts[last][0], pts[last][1], 6.5, 0, Math.PI * 2); ctx.stroke();
    }
    ctx.restore();
  }
  function drawTrack(V) {
    thit = [];
    const fixes = trackFixes();
    if (!fixes.length) return;
    const pts = fixes.map((f) => V.toScreen(f.x, f.y));
    drawTrackLines(V, pts);
    drawTrackDots(V, fixes, pts);
  }
  function trackTipText(f) {
    const m = curMap(), where = m && fitActive() ? "map " + fmtPct(worldToPct(m, f.x, f.y)) : "x " + f.x + ", y " + f.y;
    return (track ? track.name : "") + "\n" + new Date(f.t).toLocaleString() + "\n" + where + "\n" + f.kind + " fix" + (f.zone ? ", " + f.zone : "");
  }
  function trackHover(mx, my) {
    let best = null;
    for (const h of thit) if (Math.hypot(h.x - mx, h.y - my) <= h.r) best = h;
    return best;
  }

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

  function loadTrack(guid) {
    return fetch("/api/track?guid=" + encodeURIComponent(guid)).then((r) => (r.ok ? r.json() : null)).then((t) => {
      if (!t || !track || track.guid !== guid) return;
      track = t; renderTrackInfo(); draw();
    }).catch(() => {});
  }
  function pickPlayer(row) {
    track = { guid: row.guid, name: row.name, reaction: row.reaction, total: 0, maps: [], fixes: [] };
    $("results").hidden = true;
    if (S) { renderList(); draw(); }
    loadTrack(row.guid);
    if (trackTimer) clearInterval(trackTimer);
    trackTimer = setInterval(() => { if (track) loadTrack(track.guid); }, 5000);
  }
  function clearSearch() {
    track = null; thit = [];
    if (trackTimer) { clearInterval(trackTimer); trackTimer = null; }
    if (searchTimer) { clearTimeout(searchTimer); searchTimer = null; }
    searchSeq++;
    $("q").value = ""; $("qclear").hidden = true; $("results").hidden = true;
    renderTrackInfo(); if (S) renderList(); draw();
  }

  function renderResults(rows) {
    const ul = $("results");
    ul.innerHTML = "";
    if (!rows.length) {
      const li = document.createElement("li"); li.className = "none"; li.textContent = "No matching players";
      ul.appendChild(li);
    }
    for (const r of rows) {
      const li = document.createElement("li");
      li.innerHTML = '<span class="sw" style="background:' + (COLORS[r.reaction] || COLORS.unknown) + '"></span><span class="nm">' +
        esc(r.name) + '</span><span class="meta">' + r.fixes + " fixes, " + new Date(r.lastSeen).toLocaleString() + "</span>";
      li.addEventListener("click", () => pickPlayer(r));
      ul.appendChild(li);
    }
    ul.hidden = false;
  }
  function runSearch(q) {
    const seq = ++searchSeq;
    fetch("/api/players?q=" + encodeURIComponent(q)).then((r) => r.json()).then((rows) => {
      if (seq === searchSeq) renderResults(rows);
    }).catch(() => {});
  }
  function onSearchInput() {
    const q = $("q").value.trim();
    $("qclear").hidden = $("q").value === "" && !track;
    if (searchTimer) clearTimeout(searchTimer);
    if (!q) { searchSeq++; $("results").hidden = true; return; }
    searchTimer = setTimeout(() => runSearch(q), 250);
  }
  $("q").addEventListener("input", onSearchInput);
  $("qclear").addEventListener("click", clearSearch);
  window.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && (track || $("q").value !== "")) clearSearch();
  });

  // ---- history panel (hidden by default) ----
  let histTimer = null;
  function setHistory(on) {
    $("history").hidden = !on;
    if (histTimer) { clearInterval(histTimer); histTimer = null; }
    if (on) { loadHistory(); histTimer = setInterval(loadHistory, 5000); }
    setTimeout(resize, 0);
  }
  function num(v) { return v ? (Math.round(v * 10) / 10).toString() : "-"; }
  function loadHistory() {
    fetch("/api/history").then((r) => r.json()).then((rows) => {
      const tb = $("htable").tBodies[0];
      tb.innerHTML = "";
      $("hcount").textContent = rows.length + " targets";
      for (const e of rows.slice(0, 500)) {
        const tr = document.createElement("tr");
        const name = (S && S.pips.find((p) => p.guid === e.guid)) ? S.pips.find((p) => p.guid === e.guid).name : e.name.replace(/-/g, " ");
        tr.innerHTML = "<td>" + esc(name) + "</td><td>" + num(e.avgDamagePerHit) + "</td><td>" + num(e.avgDamagePerEvent) +
          "</td><td>" + num(e.avgHp) + "</td><td>" + num(e.maxHp) + "</td><td>" + e.events + "</td><td>" +
          new Date(e.lastSeen).toLocaleString() + "</td><td>" + esc(e.lastZone || "") + "</td>";
        tb.appendChild(tr);
      }
    }).catch(() => {});
  }

  // ---- bounty: leaderboard of hostile players with the most kills (from /api/bounty) ----
  const BOUNTY_ROWS = 50;      // rows listed in the panel; the radar marks every ranked player (up to 500)
  let analyzeTimer = null, analyzeWasRunning = false;

  function fmtAgo(ms) {
    if (!ms) return "-";
    return fmtAge((Date.now() - ms) / 1000) + " ago";
  }
  function renderBounty() {
    const ul = $("bountyList");
    ul.innerHTML = "";
    $("bountyCount").textContent = B.rows.length ? "(" + B.rows.length + " ranked" + (B.stats ? ", " + B.stats.deaths + " deaths" : "") + ")" : "";
    if (!B.rows.length) {
      const li = document.createElement("li"); li.className = "none";
      li.textContent = "No bounties yet. Kills of friendly players are recorded live; use Analyze logs for older fights.";
      ul.appendChild(li); return;
    }
    for (const r of B.rows.slice(0, BOUNTY_ROWS)) {
      const li = document.createElement("li");
      li.className = track && track.guid === r.guid ? "sel" : "";
      li.title = r.raw + (r.lastZone ? "\nlast: " + r.lastZone : "") + "\ninvolved in " + r.involvedDeaths + " deaths";
      li.innerHTML = '<span class="rk">' + r.rank + '</span><span class="nm">' + esc(r.name) + '</span><span class="bv">' + r.bounty +
        '</span><span class="meta">' + r.killingBlows + " KB / " + r.assists + " A, " + fmtAgo(r.lastKill) + "</span>";
      li.addEventListener("click", () => pickBounty(r));
      ul.appendChild(li);
    }
  }
  // Click a leaderboard row: same flow as picking a search result (shows their recorded dots, hides the others).
  function pickBounty(r) {
    $("q").value = r.name; $("qclear").hidden = false;
    pickPlayer({ guid: r.guid, name: r.name, reaction: "hostile" });
    renderBounty();
  }
  let bountySeq = 0;           // a reply that belongs to a previous rule-set / request is ignored
  function loadBounty() {
    const seq = ++bountySeq;
    return fetch("/api/bounty?limit=500").then((r) => (r.ok ? r.json() : null)).then((d) => {
      if (!d || seq !== bountySeq) return;
      B.rows = (d.rows || []).filter((r) => r.bounty > 0); // only players who actually killed friendly players
      B.stats = d.stats || null; B.by = {};
      for (const r of B.rows) B.by[r.guid] = r;
      renderBounty(); if (S) { renderList(); draw(); }
    }).catch(() => {});
  }
  function mb(n) { return (n / 1048576).toFixed(1); }
  function showAnalyze(st) {
    const el = $("bStatus");
    if (!st || !st.jobId) { el.textContent = ""; return; }
    let txt = st.filesDone + "/" + st.filesTotal + " files, " + mb(st.bytes) + "/" + mb(st.bytesTotal) + " MB, " +
      st.deaths + " deaths (" + st.newDeaths + " new)";
    if (st.running) txt = "Analyzing " + txt + (st.currentFile ? " - " + st.currentFile : "");
    else txt = (st.cancelled ? "Stopped: " : "Done: ") + txt;
    if (st.errors && st.errors.length) txt += "; " + st.errors.length + " error(s): " + st.errors[0];
    el.textContent = txt;
  }
  function pollAnalyze() {
    fetch("/api/bounty/analyze").then((r) => r.json()).then((st) => {
      showAnalyze(st);
      $("bAnalyze").disabled = !!st.running;
      if (st.running) {
        analyzeWasRunning = true;
        if (!analyzeTimer) analyzeTimer = setInterval(pollAnalyze, 1000);
        return;
      }
      if (analyzeTimer) { clearInterval(analyzeTimer); analyzeTimer = null; }
      if (analyzeWasRunning) { analyzeWasRunning = false; loadBounty(); }
    }).catch(() => {
      if (analyzeTimer) { clearInterval(analyzeTimer); analyzeTimer = null; }
      $("bAnalyze").disabled = false;
    });
  }
  function startAnalyze() {
    $("bAnalyze").disabled = true;
    fetch("/api/bounty/analyze", { method: "POST", headers: { "X-Radar": "1", "Content-Type": "application/json" }, body: "{}" })
      .then((r) => {
        if (r.status === 202 || r.status === 409) { analyzeWasRunning = true; pollAnalyze(); return; } // 409: one is already running
        $("bAnalyze").disabled = false;
        return r.text().then((t) => { $("bStatus").textContent = "Could not start: " + t.trim(); });
      })
      .catch(() => { $("bAnalyze").disabled = false; $("bStatus").textContent = "Could not reach the radar."; });
  }
  $("bAnalyze").addEventListener("click", startAnalyze);
  loadBounty();
  setInterval(loadBounty, 10000);
  pollAnalyze(); // resumes the display if a startup analysis (--analyze-logs) is running


  // ---- server rule-set toggle: each rule-set (Normal/PvP/RP/RP-PvP) has its own history and bounty data ----
  const RULESETS = [["auto", "Auto"], ["normal", "Normal"], ["pvp", "PvP"], ["rp", "RP"], ["rppvp", "RP-PvP"]];
  function rsName(id) { for (const r of RULESETS) if (r[0] === id) return r[1]; return id || ""; }
  // Highlights the active choice ("Auto: PvP" while auto), tags the bounty panel, and, when the dataset
  // changed (a click, or auto-detection following the log), reloads everything that reads from it.
  function applyRuleset(rs) {
    if (!rs || !rs.active) return;
    const changed = RS !== null && RS.active !== rs.active;
    RS = { active: rs.active, mode: rs.mode, detected: rs.detected || null };
    for (const r of RULESETS) {
      const b = $("rs-" + r[0]);
      const on = r[0] === "auto" ? RS.mode === "auto" : (RS.mode === "manual" && RS.active === r[0]);
      b.className = on ? "on" : "";
      b.setAttribute("aria-pressed", on ? "true" : "false");
      b.textContent = r[0] === "auto" && RS.mode === "auto" ? "Auto: " + rsName(RS.active) : r[1];
    }
    $("bountyRs").textContent = rsName(RS.active);
    $("bountyRs").title = "Bounty ledger of the " + rsName(RS.active) + " rule-set (each rule-set keeps its own)";
    if (changed) onRulesetChanged();
  }
  function onRulesetChanged() {
    B = { rows: [], by: {}, stats: null }; renderBounty(); if (S) { renderList(); draw(); }
    loadBounty();
    if ($("showHistory").checked) loadHistory();
    const q = $("q").value.trim();
    if (q) runSearch(q); else { searchSeq++; $("results").hidden = true; }
    if (!track) return;
    const g = track.guid; // keep the selected track only if that player exists in the new dataset
    fetch("/api/track?guid=" + encodeURIComponent(g)).then((r) => (r.ok ? r.json() : null)).then((t) => {
      if (!track || track.guid !== g) return;
      if (!t) { clearSearch(); return; }
      track = t; renderTrackInfo(); draw();
    }).catch(() => {});
  }
  function selectRuleset(id) {
    fetch("/api/ruleset", { method: "POST", headers: { "X-Radar": "1", "Content-Type": "application/json" }, body: JSON.stringify({ ruleset: id }) })
      .then((r) => (r.ok ? r.json() : null)).then((rs) => { if (rs) applyRuleset(rs); }).catch(() => {});
  }
  for (const r of RULESETS) $("rs-" + r[0]).addEventListener("click", () => selectRuleset(r[0]));
  fetch("/api/ruleset").then((r) => (r.ok ? r.json() : null)).then((rs) => { if (rs) applyRuleset(rs); }).catch(() => {});
  // ---- map combobox: typeable box + filtered list; picking an entry switches the viewed map ----
  let mapOpen = false, mapEditing = false, mapItems = [], mapAct = -1;
  function normName(s) { return String(s).toLowerCase().replace(/[^a-z0-9]/g, ""); }
  // Entries for the current filter: Auto first, then maps with an image, then greyed maps without one.
  function buildMapItems(filter) {
    const f = normName(filter), v = (S && S.view) || {}, items = [];
    if (!f || "autofollowgame".indexOf(f) >= 0) items.push({ kind: "auto", label: "Auto (follow game)" });
    for (const m of v.available || []) if (!f || normName(m.name).indexOf(f) >= 0) items.push({ kind: "map", mapId: m.mapId, label: m.name });
    for (const m of v.noImage || []) if (!f || normName(m.name).indexOf(f) >= 0) items.push({ kind: "noimage", mapId: m.mapId, label: m.name, disabled: true });
    return items;
  }
  function mapLabel() {
    if (!S) return "";
    return viewMapId === null ? (viewName() ? viewName() + " (auto)" : "Auto (follow game)") : viewName();
  }
  function renderMapInput() { if (!mapEditing) $("mapq").value = mapLabel(); }
  function firstEnabled(from, step) {
    for (let i = from; i >= 0 && i < mapItems.length; i += step) if (!mapItems[i].disabled) return i;
    return -1;
  }
  function renderMapList() {
    const ul = $("maplist");
    ul.innerHTML = "";
    $("mapq").setAttribute("aria-expanded", mapOpen ? "true" : "false");
    ul.hidden = !mapOpen;
    if (!mapOpen) return;
    mapItems.forEach((it, i) => {
      const li = document.createElement("li");
      const cur = it.kind === "auto" ? viewMapId === null : (viewMapId === it.mapId);
      li.id = "mapopt" + i;
      li.className = (it.kind === "auto" ? "auto " : "") + (it.disabled ? "dis " : "") + (cur ? "cur " : "") + (i === mapAct ? "act" : "");
      li.setAttribute("role", "option");
      li.setAttribute("aria-selected", cur ? "true" : "false");
      if (it.disabled) li.setAttribute("aria-disabled", "true");
      li.innerHTML = "<span>" + esc(it.label) + "</span>" + (it.disabled ? '<span class="hint">no image</span>' : "");
      if (!it.disabled) li.addEventListener("click", () => chooseMap(it));
      ul.appendChild(li);
    });
    if (!mapItems.some((it) => it.kind !== "auto")) {
      const li = document.createElement("li"); li.className = "none"; li.setAttribute("role", "option");
      li.setAttribute("aria-disabled", "true"); li.textContent = "No matching maps";
      ul.appendChild(li);
    }
    if (mapAct >= 0) $("mapq").setAttribute("aria-activedescendant", "mapopt" + mapAct); else $("mapq").removeAttribute("aria-activedescendant");
  }
  function openMapList(filter) {
    mapItems = buildMapItems(filter);
    mapOpen = true;
    mapAct = firstEnabled(0, 1);
    renderMapList();
  }
  function closeMapList() {
    mapOpen = false; mapEditing = false; mapAct = -1;
    renderMapList(); renderMapInput();
  }
  function chooseMap(it) {
    if (!it || it.disabled) return;
    viewMapId = it.kind === "auto" ? null : it.mapId;
    closeMapList();
    if (!S) return;
    syncView(); renderList(); renderHeader(); renderTrackInfo(); renderMapInput(); draw();
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
    } else if (k === "Enter") {
      if (mapOpen) { e.preventDefault(); chooseMap(mapItems[mapAct]); }
    } else if (k === "Escape") {
      if (mapOpen) { e.preventDefault(); e.stopPropagation(); closeMapList(); }
    } else if (k === "Tab") {
      if (mapOpen) closeMapList();
    }
  });
  document.addEventListener("mousedown", (e) => {
    if (mapOpen && !$("mapbox").contains(e.target)) closeMapList();
  });

  resize();
  connect();
  setInterval(() => { if (S) draw(); }, 2000);
})();
