"use strict";
(function () {
  const $ = (id) => document.getElementById(id);
  const COLORS = { friendly: "#3b82f6", hostile: "#ef4444", neutral: "#ef4444", unknown: "#ef4444", stale: "#4b5563" };
  const STALE_MS = 24 * 3600 * 1000;
  const cv = $("radar"), ctx = cv.getContext("2d");
  let S = null;            // {players, maps}
  let mapsById = {};       // uiMapID -> rect
  let zoneIdx = {};        // normalised name -> {file,w,h}
  let counts = {};         // mapId -> players
  let newest = 0;
  let selected = null, track = null; // selected guid; searched player
  let hit = [], imgCache = {}, dpr = 1;

  const pref = {
    get(k, d) { try { const v = localStorage.getItem("nr." + k); return v === null ? d : v; } catch (e) { return d; } },
    set(k, v) { try { localStorage.setItem("nr." + k, v); } catch (e) { /* ignore */ } },
  };
  for (const id of ["stale", "bg", "showBlue", "showRed"]) {
    $(id).checked = pref.get(id, "1") === "1";
    $(id).addEventListener("change", () => { pref.set(id, $(id).checked ? "1" : "0"); renderList(); draw(); });
  }
  $("showHistory").checked = false;
  $("showHistory").addEventListener("change", () => setHistory($("showHistory").checked));
  window.addEventListener("keydown", (e) => {
    if ((e.key === "h" || e.key === "H") && !e.ctrlKey && !e.altKey && !e.metaKey && e.target.tagName !== "INPUT") {
      $("showHistory").checked = !$("showHistory").checked;
      setHistory($("showHistory").checked);
    }
  });

  // ---- auth ----
  let token = null, username = "", signup = false;
  try { token = localStorage.getItem("nrc.token"); username = localStorage.getItem("nrc.user") || ""; } catch (e) { /* ignore */ }
  function saveAuth() {
    try {
      if (token) { localStorage.setItem("nrc.token", token); localStorage.setItem("nrc.user", username); }
      else { localStorage.removeItem("nrc.token"); localStorage.removeItem("nrc.user"); }
    } catch (e) { /* ignore */ }
  }
  function showAuth() { $("auth").hidden = false; $("user").textContent = ""; $("logout").hidden = true; setConn("off"); }
  function logout() {
    token = null; username = ""; saveAuth(); S = null; selected = null; track = null;
    rebuild(); showAuth();
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
    fetch(signup ? "/api/signup" : "/api/login", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ username: $("authUser").value.trim(), password: $("authPass").value, apiKey: $("authKey").value.trim() }),
    }).then((r) => r.json().catch(() => ({ error: "Server error (" + r.status + ")" })))
      .then((d) => {
        if (!d.token) { $("authErr").textContent = d.error || "Login failed"; return; }
        token = d.token; username = d.username || $("authUser").value.trim(); saveAuth();
        $("authPass").value = ""; $("authKey").value = "";
        enter();
      })
      .catch(() => { $("authErr").textContent = "Could not reach the server."; })
      .then(() => { $("authGo").disabled = false; });
  });
  $("logout").addEventListener("click", logout);
  function enter() {
    $("auth").hidden = true; $("user").textContent = username; $("logout").hidden = false;
    loadPlayers();
  }
  function api(path, opt) {
    opt = opt || {};
    opt.headers = Object.assign({ Authorization: "Bearer " + token }, opt.headers || {});
    return fetch(path, opt).then((r) => {
      if (r.status === 401) { logout(); throw new Error("Session expired, please log in again."); }
      return r.json().catch(() => ({})).then((d) => { if (!r.ok) throw new Error(d.error || "Server error (" + r.status + ")"); return d; });
    });
  }

  // ---- data ----
  function setConn(k) { $("conn").className = "dot " + (k === "off" ? "" : k); }
  function normName(s) { return String(s).toLowerCase().replace(/[^a-z0-9]/g, ""); }
  fetch("maps/index.json").then((r) => r.json()).then((d) => {
    for (const k in d.zones || {}) zoneIdx[k] = d.zones[k];
    if (S) rebuild();
  }).catch(() => {});
  fetch("ads/ads.json").then((r) => r.json()).then((ads) => {
    const box = $("ads");
    for (const a of ads) {
      const l = document.createElement("a"), im = document.createElement("img");
      l.href = a.href; l.rel = "sponsored noopener"; l.target = "_blank";
      im.src = a.img; im.alt = a.alt || "";
      l.appendChild(im); box.appendChild(l);
    }
  }).catch(() => {});

  function loadPlayers() {
    setConn("poll");
    return api("/api/players").then((d) => {
      S = { players: d.players || [], maps: d.maps || [] };
      setConn("ok");
      rebuild();
    }).catch((e) => { setConn("off"); $("foot").textContent = e.message; });
  }
  function rebuild() {
    mapsById = {}; counts = {}; newest = 0;
    if (S) {
      for (const m of S.maps) mapsById[m.id] = m;
      for (const p of S.players) { counts[p.mapId] = (counts[p.mapId] || 0) + 1; if (p.lastSeen > newest) newest = p.lastSeen; }
    }
    if (viewMapId !== null && !counts[viewMapId]) viewMapId = null;
    if (track && S && !S.players.includes(track)) { const t = S.players.find((p) => p.guid === track.guid); track = t || null; }
    syncView(); renderList(); renderHeader(); renderTrackInfo(); renderMapInput();
    if ($("showHistory").checked) loadHistory();
    draw();
  }

  // ---- viewed map: null = auto (the map with the most players), otherwise a user-chosen uiMapID ----
  let viewMapId = null, lastVid = null;
  function zoneOf(id) { const m = mapsById[id]; return m ? zoneIdx[normName(m.name)] || null : null; }
  function autoId() {
    let best = 0, bn = -1;
    for (const id in counts) { const n = counts[id] + (zoneOf(+id) ? 1e6 : 0); if (n > bn) { bn = n; best = +id; } }
    return best;
  }
  function viewId() { return viewMapId !== null ? viewMapId : autoId(); }
  function viewZone() { const z = zoneOf(viewId()); return z ? { url: "maps/" + z.file, w: z.w, h: z.h } : null; }
  function viewName() { const m = mapsById[viewId()]; return m ? m.name : ""; }
  const fz = { z: 1, cx: 50, cy: 50 };
  function syncView() {
    const id = viewId();
    if (id !== lastVid) { lastVid = id; fz.z = 1; fz.cx = 50; fz.cy = 50; }
  }

  // ---- helpers ----
  function isStale(p) { return p.lastSeen < newest - STALE_MS; }
  function colorOf(p) { return isStale(p) ? COLORS.stale : (COLORS[p.reaction] || COLORS.unknown); }
  function fmtAge(sec) {
    sec = Math.max(0, Math.round(sec));
    if (sec < 60) return sec + "s";
    const m = Math.floor(sec / 60);
    if (m < 60) return m + "m";
    const h = Math.floor(m / 60);
    return h < 48 ? h + "h" + (m % 60) + "m" : Math.floor(h / 24) + "d";
  }
  function esc(s) { return String(s).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c])); }
  function onMap(p) { const id = viewId(); return !id || p.mapId === id; }
  function visible(p) {
    if (track && p.guid !== track.guid) return false;
    if (!$(p.reaction === "friendly" ? "showBlue" : "showRed").checked) return false;
    return $("stale").checked || !isStale(p);
  }
  function mapPlayers() { return S ? S.players.filter((p) => onMap(p) && visible(p)) : []; }

  function renderHeader() {
    const vid = viewId();
    $("zone").textContent = (viewName() || "-") + (vid ? " (map " + vid + ")" : "") + (viewMapId !== null ? " [viewing]" : "");
    const all = S ? S.players.length : 0, here = S ? S.players.filter((p) => onMap(p)).length : 0;
    $("summary").textContent = all + " stored, " + here + " on this map";
    $("empty").hidden = here > 0;
    $("foot").textContent = S ? all + " players, " + S.maps.length + " maps | newest sighting " + (newest ? new Date(newest).toLocaleString() : "-") : "Not loaded";
  }

  function renderList() {
    const ul = $("list");
    ul.innerHTML = "";
    if (!S) return;
    const rows = mapPlayers().sort((a, b) => b.lastSeen - a.lastSeen).slice(0, 400);
    for (const p of rows) {
      const li = document.createElement("li");
      li.className = (isStale(p) ? "stale " : "") + (p.guid === selected ? "sel" : "");
      li.innerHTML = '<span class="sw" style="background:' + colorOf(p) + '"></span><span class="nm">' + esc(p.name) +
        '</span><span class="meta">' + (p.level ? "Lv " + p.level + " " : "") + fmtAge((Date.now() - p.lastSeen) / 1000) + "</span>";
      li.addEventListener("click", () => {
        selected = p.guid;
        const m = mapsById[p.mapId];
        if (m && fz.z > 1) { const pc = worldToPct(m, p.x, p.y); fz.cx = pc[0]; fz.cy = pc[1]; clampView(); }
        renderList(); draw();
      });
      ul.appendChild(li);
    }
  }

  // ---- radar drawing ----
  function resize() {
    dpr = window.devicePixelRatio || 1;
    cv.width = Math.max(1, Math.round(cv.clientWidth * dpr));
    cv.height = Math.max(1, Math.round(cv.clientHeight * dpr));
    draw();
  }
  window.addEventListener("resize", resize);
  if (window.ResizeObserver) new ResizeObserver(resize).observe($("radarWrap"));

  // Fixed map frame: top-left = (0,0), bottom-right = (100,100). Per-map calibration {dx,dy,sx,sy} in frame percent.
  function curMap() { return S && viewId() ? mapsById[viewId()] || null : null; }
  function getCal() {
    try { const c = JSON.parse(pref.get("cal." + viewId(), "")); if (c && isFinite(c.dx)) return c; } catch (e) { /* default */ }
    return { dx: 0, dy: 0, sx: 1, sy: 1 };
  }
  function setCal(c) { pref.set("cal." + viewId(), JSON.stringify(c)); draw(); }
  function worldToPct(m, x, y) {
    const c = getCal();
    const px = ((m.y1 - y) / (m.y1 - m.y0)) * 100, py = ((m.x1 - x) / (m.x1 - m.x0)) * 100;
    return [px * c.sx + c.dx, py * c.sy + c.dy];
  }
  function frameRect() {
    const zi = viewZone() || {}, w = cv.width / dpr, h = cv.height / dpr, pad = 26;
    const ar = (zi.w || 1002) / (zi.h || 668), aw = w - pad * 2, ah = h - pad * 2;
    let fw = aw, fh = aw / ar;
    if (fh > ah) { fh = ah; fw = ah * ar; }
    return { x: (w - fw) / 2, y: (h - fh) / 2 + 4, w: fw, h: fh };
  }
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
    clampView(); draw();
  }
  function fitZoom(mx, my, factor) {
    const f = frameRect(), half = 50 / fz.z;
    const px = fz.cx - half + ((mx - f.x) / f.w) * (100 / fz.z), py = fz.cy - half + ((my - f.y) / f.h) * (100 / fz.z);
    const nz = Math.max(1, Math.min(ZMAX, fz.z * factor)), nh = 50 / nz;
    fz.z = nz;
    fz.cx = px - ((mx - f.x) / f.w) * (100 / nz) + nh; fz.cy = py - ((my - f.y) / f.h) * (100 / nz) + nh;
    clampView(); draw();
  }
  function fmtPct(p) { return p[0].toFixed(1) + ", " + p[1].toFixed(1); }
  window.addEventListener("keydown", (e) => {
    if (!e.shiftKey || !S || !curMap() || e.target.tagName === "INPUT") return;
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
    const w = cv.width / dpr, h = cv.height / dpr, m = curMap(), f = frameRect(), z = fz.z;
    clampView();
    const half = 50 / z, ox = fz.cx - half, oy = fz.cy - half;
    const toScreen = (x, y) => { const p = worldToPct(m, x, y); return [f.x + ((p[0] - ox) * z / 100) * f.w, f.y + ((p[1] - oy) * z / 100) * f.h]; };
    return { w, h, f, m, z, ox, oy, toScreen };
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
    hit = [];
    if (!S || !$("bg").checked || !curMap()) return;
    const V = view();
    drawFrame(V);
    const mine = mapPlayers();
    mine.sort((a, b) => (isStale(a) ? 0 : 1) - (isStale(b) ? 0 : 1)); // stale first so recent pips draw on top
    for (const p of mine) drawPip(V, p);
    ctx.restore();
  }

  function drawFrame(V) {
    const f = V.f, z = V.z, zi = viewZone(), img = zi ? loadImage(zi.url) : null;
    const sxp = (p) => f.x + ((p - V.ox) * z / 100) * f.w, syp = (p) => f.y + ((p - V.oy) * z / 100) * f.h;
    ctx.save();
    ctx.beginPath(); ctx.rect(f.x, f.y, f.w, f.h); ctx.clip();
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
    ctx.save(); ctx.beginPath(); ctx.rect(f.x, f.y, f.w, f.h); ctx.clip(); // pips are clipped too (restored in draw())
  }

  function drawPip(V, p) {
    const [x, y] = V.toScreen(p.x, p.y), f = V.f;
    if (x < f.x || y < f.y || x > f.x + f.w || y > f.y + f.h) return;
    const col = colorOf(p), r = 5;
    ctx.lineWidth = 2;
    ctx.fillStyle = col; ctx.beginPath(); ctx.arc(x, y, r, 0, Math.PI * 2); ctx.fill();
    if (isStale(p)) { ctx.strokeStyle = COLORS[p.reaction] || COLORS.unknown; ctx.beginPath(); ctx.arc(x, y, r, 0, Math.PI * 2); ctx.stroke(); }
    if (p.guid === selected || (track && p.guid === track.guid)) { ctx.strokeStyle = "#fde047"; ctx.beginPath(); ctx.arc(x, y, r + 5, 0, Math.PI * 2); ctx.stroke(); }
    hit.push({ x, y, r: r + 5, p });
  }

  // ---- interaction ----
  const tip = $("tip");
  let drag = null;
  cv.addEventListener("mousemove", (e) => {
    const rect = cv.getBoundingClientRect(), mx = e.clientX - rect.left, my = e.clientY - rect.top;
    if (drag) { fitPan(mx - drag.mx, my - drag.my, drag.fz0); return; }
    let best = null;
    for (const h of hit) if (Math.hypot(h.x - mx, h.y - my) <= h.r) best = h;
    if (!best) { tip.hidden = true; return; }
    const p = best.p, m = curMap();
    tip.textContent = p.name + (m ? "\nmap " + fmtPct(worldToPct(m, p.x, p.y)) : "") + "\n" + p.reaction + (p.level ? ", level " + p.level : "") + "\n" +
      (p.maxHp ? "Max HP " + p.maxHp + "\n" : "") + "last seen " + fmtAge((Date.now() - p.lastSeen) / 1000) + " ago" + (p.zone ? "\n" + p.zone : "");
    tip.style.left = Math.min(mx + 12, rect.width - 200) + "px"; tip.style.top = my + 12 + "px"; tip.hidden = false;
  });
  cv.addEventListener("mouseleave", () => { tip.hidden = true; drag = null; });
  cv.addEventListener("mousedown", (e) => {
    const rect = cv.getBoundingClientRect();
    drag = { mx: e.clientX - rect.left, my: e.clientY - rect.top, fz0: { cx: fz.cx, cy: fz.cy } };
  });
  window.addEventListener("mouseup", () => { drag = null; });
  cv.addEventListener("dblclick", () => { fz.z = 1; fz.cx = 50; fz.cy = 50; draw(); });
  cv.addEventListener("wheel", (e) => {
    e.preventDefault();
    const rect = cv.getBoundingClientRect();
    fitZoom(e.clientX - rect.left, e.clientY - rect.top, e.deltaY > 0 ? 1 / 1.2 : 1.2);
  }, { passive: false });

  // ---- player search (client side, over the stored players) ----
  function renderTrackInfo() {
    const el = $("trackInfo");
    if (!track) { el.hidden = true; el.textContent = ""; return; }
    const m = mapsById[track.mapId];
    el.textContent = track.name + (track.level ? " (level " + track.level + ")" : "") + ", " + track.reaction +
      "\nLast seen " + new Date(track.lastSeen).toLocaleString() + (track.zone ? " in " + track.zone : "") +
      (m ? "\nMap: " + m.name + " " + fmtPct(worldToPct(m, track.x, track.y)) : "");
    el.hidden = false;
  }
  function pickPlayer(p) {
    track = p; selected = p.guid;
    $("results").hidden = true;
    if (mapsById[p.mapId]) viewMapId = p.mapId;
    rebuild();
  }
  function clearSearch() {
    track = null;
    $("q").value = ""; $("qclear").hidden = true; $("results").hidden = true;
    rebuild();
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
        esc(r.name) + '</span><span class="meta">' + (r.level ? "Lv " + r.level + ", " : "") + new Date(r.lastSeen).toLocaleDateString() + "</span>";
      li.addEventListener("click", () => pickPlayer(r));
      ul.appendChild(li);
    }
    ul.hidden = false;
  }
  $("q").addEventListener("input", () => {
    const q = $("q").value.trim().toLowerCase();
    $("qclear").hidden = $("q").value === "" && !track;
    if (!q || !S) { $("results").hidden = true; return; }
    renderResults(S.players.filter((p) => p.name.toLowerCase().includes(q)).sort((a, b) => b.lastSeen - a.lastSeen).slice(0, 50));
  });
  $("qclear").addEventListener("click", clearSearch);
  window.addEventListener("keydown", (e) => { if (e.key === "Escape" && (track || $("q").value !== "")) clearSearch(); });

  // ---- history table (hidden by default) ----
  function setHistory(on) {
    $("history").hidden = !on;
    if (on) loadHistory();
    setTimeout(resize, 0);
  }
  function loadHistory() {
    const tb = $("htable").tBodies[0];
    tb.innerHTML = "";
    const rows = S ? S.players.slice().sort((a, b) => b.lastSeen - a.lastSeen) : [];
    $("hcount").textContent = rows.length + " players";
    for (const p of rows.slice(0, 500)) {
      const tr = document.createElement("tr");
      tr.innerHTML = "<td>" + esc(p.name) + "</td><td>" + (p.level || "-") + "</td><td>" + esc(p.reaction) + "</td><td>" + (p.maxHp || "-") +
        "</td><td>" + new Date(p.lastSeen).toLocaleString() + "</td><td>" + esc(p.zone || "") + "</td>";
      tb.appendChild(tr);
    }
  }

  // ---- upload: parse in a Web Worker (parser.js), send deduplicated summaries in batches ----
  const BATCH = 1000;
  let busy = false;
  function status(t) { $("upStatus").textContent = t; }
  function bar(f) { $("bar").hidden = f === null; $("barFill").style.width = Math.round((f || 0) * 100) + "%"; }
  function mb(n) { return (n / 1048576).toFixed(1); }
  function parseFile(file) {
    return new Promise((resolve, reject) => {
      const w = new Worker("parser.js");
      w.onmessage = (e) => {
        const d = e.data;
        if (d.type === "progress") { bar(d.done / d.total); status("Parsing " + file.name + ": " + mb(d.done) + "/" + mb(d.total) + " MB, " + d.players + " players"); }
        else if (d.type === "done") { w.terminate(); resolve(d); }
        else if (d.type === "error") { w.terminate(); reject(new Error(d.error)); }
      };
      w.onerror = (e) => { w.terminate(); reject(new Error(e.message || "Parser failed")); };
      w.postMessage({ file });
    });
  }
  async function uploadFiles(files) {
    if (busy || !token) return;
    busy = true; $("pick").disabled = true;
    let changedTotal = 0, sentTotal = 0;
    try {
      for (const file of files) {
        const r = await parseFile(file);
        if (!r.players.length) { status(file.name + ": no player positions found (is advanced logging on?)"); continue; }
        const n = Math.ceil(r.players.length / BATCH);
        for (let i = 0; i < n; i++) {
          status("Uploading " + file.name + ": batch " + (i + 1) + "/" + n);
          bar(i / n);
          const d = await api("/api/upload", {
            method: "POST", headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ players: r.players.slice(i * BATCH, (i + 1) * BATCH), maps: r.maps }),
          });
          sentTotal += d.received || 0; changedTotal += d.changed || 0;
        }
        bar(1);
      }
      status("Done: " + sentTotal + " players sent, " + changedTotal + " new or updated.");
      await loadPlayers();
    } catch (e) {
      status("Error: " + e.message);
    }
    busy = false; $("pick").disabled = false; setTimeout(() => bar(null), 1500);
  }
  $("pick").addEventListener("click", () => $("file").click());
  $("file").addEventListener("change", () => { const fs = Array.from($("file").files); $("file").value = ""; if (fs.length) uploadFiles(fs); });
  const drop = $("drop");
  for (const t of ["dragenter", "dragover"]) drop.addEventListener(t, (e) => { e.preventDefault(); drop.classList.add("over"); });
  for (const t of ["dragleave", "drop"]) drop.addEventListener(t, (e) => { e.preventDefault(); drop.classList.remove("over"); });
  drop.addEventListener("drop", (e) => { const fs = Array.from(e.dataTransfer.files); if (fs.length) uploadFiles(fs); });

  // ---- map combobox ----
  let mapOpen = false, mapEditing = false, mapItems = [], mapAct = -1;
  function buildMapItems(filter) {
    const f = normName(filter), items = [];
    if (!f || "autofollowgame".indexOf(f) >= 0) items.push({ kind: "auto", label: "Auto (most players)" });
    const ids = Object.keys(counts).map(Number).filter((id) => mapsById[id]).sort((a, b) => mapsById[a].name.localeCompare(mapsById[b].name) || a - b);
    for (const pass of [true, false]) {
      for (const id of ids) {
        const m = mapsById[id];
        if ((!!zoneOf(id)) === pass && (!f || normName(m.name).indexOf(f) >= 0))
          items.push(pass ? { kind: "map", mapId: id, label: m.name, n: counts[id] } : { kind: "noimage", mapId: id, label: m.name, disabled: true });
      }
    }
    return items;
  }
  function renderMapInput() {
    if (mapEditing) return;
    $("mapq").value = !S ? "" : viewMapId === null ? (viewName() ? viewName() + " (auto)" : "Auto (most players)") : viewName();
  }
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
      li.innerHTML = "<span>" + esc(it.label) + "</span>" + (it.disabled ? '<span class="hint">no image</span>' : it.n ? '<span class="hint">' + it.n + "</span>" : "");
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
  function openMapList(filter) { mapItems = buildMapItems(filter); mapOpen = true; mapAct = firstEnabled(0, 1); renderMapList(); }
  function closeMapList() { mapOpen = false; mapEditing = false; mapAct = -1; renderMapList(); renderMapInput(); }
  function chooseMap(it) {
    if (!it || it.disabled) return;
    viewMapId = it.kind === "auto" ? null : it.mapId;
    closeMapList();
    syncView(); renderList(); renderHeader(); renderMapInput(); draw();
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
  document.addEventListener("mousedown", (e) => { if (mapOpen && !$("mapbox").contains(e.target)) closeMapList(); });

  resize();
  if (token) enter(); else showAuth();
  setInterval(() => { if (S) draw(); }, 60000);
})();
