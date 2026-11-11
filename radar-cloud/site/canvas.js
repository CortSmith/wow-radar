'use strict';
/**
 * canvas.js - single responsibility: paint the radar canvas (chart image, grid or range rings, trail, blips) and answer
 * "what is under the cursor".
 *
 * It reads the shared state and view-model; it never changes state except its own hit lists. All sizes and colors
 * come from
 * config.draw. Public API (Radar.ui.canvas):
 *  - init(state)            bind the canvas, size it, and keep it sized (window resize + ResizeObserver)
 *  - draw()                 repaint
 *  - resize()               re-read the CSS size and repaint
 *  - projection()           current Projection (see blips.projection) incl. w, h and chart rectangle m
 *  - blipAt(x, y) / fixAt(x, y) -> hit under the cursor (CSS px relative to the canvas) or null
 */
(function (g) {
  const radar = g.Radar,
    { $ } = radar.dom,
    D = radar.config.draw,
    VM = radar.viewModel,
    blips = radar.blips;
  const TWO_PI = Math.PI * 2;
  const PCT = radar.config.units.PCT_FULL;
  let st = null,
    canvas = null,
    ctx = null,
    dpr = 1;
  let blipHits = [],
    fixHits = [];
  const imageCache = new Map();

  // ---------- sizing ----------
  /**
   * Re-read the canvas CSS size (device pixel ratio aware) and repaint.
   */
  function resize() {
    dpr = g.devicePixelRatio || 1;
    canvas.width = Math.max(1, Math.round(canvas.clientWidth * dpr));
    canvas.height = Math.max(1, Math.round(canvas.clientHeight * dpr));
    draw();
  }
  /**
   * Build the world-to-screen projection for the current view mode.
   * @returns {*} the computed value
   */
  function projection() {
    const w = canvas.width / dpr,
      h = canvas.height / dpr;
    const input = {
      w,
      h,
      fixed: VM.isFixedChartActive(st),
      m: VM.currentChart(st),
      cal: VM.calibrationOf(st),
      zi: VM.viewZone(st),
      fz: st.fz,
      me: st.snap && st.snap.me,
      follow: st.flags.follow,
      cam: st.cam,
      range: st.rangeYd,
      viewId: VM.viewId(st),
    };
    const V = blips.projection(input);
    V.w = w;
    V.h = h;
    V.m = input.m;
    return V;
  }

  /**
   * Chart images load lazily; a finished load repaints. Returns the image once ready, else null.
   * @param {string} url url
   * @returns {*} the computed value
   */
  function loadImage(url) {
    let entry = imageCache.get(url);
    if (entry) return entry.ready ? entry.img : null;
    const img = new Image();
    entry = { img, ready: false };
    imageCache.set(url, entry);
    img.onload = () => {
      entry.ready = true;
      draw();
    };
    img.src = url;
    return null;
  }

  /**
   * Trace a full circle path at (x, y).
   * @param {number} x world x in yards
   * @param {number} y world y in yards
   * @param {number} r row or response
   */
  const circle = (x, y, r) => {
    ctx.beginPath();
    ctx.arc(x, y, r, 0, TWO_PI);
  };
  /**
   * Stroke a circle outline in the given color.
   * @param {number} x world x in yards
   * @param {number} y world y in yards
   * @param {number} r row or response
   * @param {Object} color the color
   */
  const ring = (x, y, r, color) => {
    ctx.strokeStyle = color;
    circle(x, y, r);
    ctx.stroke();
  };

  // ---------- chart, grid, rings ----------
  function gridStep(zoom) {
    for (const [minZoom, step] of D.GRID_STEP_ZOOM) if (zoom >= minZoom) return step;
    return D.GRID_STEP_DEFAULT;
  }
  /**
   * Grid across the frame every `step` frame percent (0..100).
   * @param {Object} V projection
   * @param {Object} sx the sx
   * @param {Object} sy the sy
   * @param {number} step the step
   */
  function drawGridLines(V, sx, sy, step) {
    const f = V.f;
    ctx.strokeStyle = D.COLOR.grid;
    ctx.lineWidth = D.LINE_PX;
    for (let i = 0; i <= PCT; i += step) {
      ctx.beginPath();
      ctx.moveTo(sx(i), f.y);
      ctx.lineTo(sx(i), f.y + f.h);
      ctx.moveTo(f.x, sy(i));
      ctx.lineTo(f.x + f.w, sy(i));
      ctx.stroke();
    }
  }
  /**
   * Percent labels along the top and left edges, only for grid lines that are inside the frame.
   * @param {Object} V projection
   * @param {Object} sx the sx
   * @param {Object} sy the sy
   * @param {number} step the step
   */
  function drawAxisLabels(V, sx, sy, step) {
    const f = V.f;
    ctx.fillStyle = D.COLOR.axisLabel;
    ctx.font = D.LABEL_FONT_FRAME;
    for (let i = 0; i <= PCT; i += step) {
      const gx = sx(i),
        gy = sy(i);
      if (gx >= f.x - 1 && gx <= f.x + f.w + 1) {
        ctx.textAlign = 'center';
        ctx.fillText(String(i), gx, f.y - D.AXIS_LABEL_TOP_PX);
      }
      if (gy >= f.y - 1 && gy <= f.y + f.h + 1) {
        ctx.textAlign = 'right';
        ctx.fillText(String(i), f.x - D.AXIS_LABEL_LEFT_PX, gy + D.AXIS_LABEL_BASELINE_PX);
      }
    }
    ctx.textAlign = 'start';
  }
  function clipToFrame(f) {
    ctx.beginPath();
    ctx.rect(f.x, f.y, f.w, f.h);
    ctx.clip();
  }

  /**
   * Fixed chart mode: the chart fills a frame with a 0..100 grid. Leaves the frame clip active (V.clipped) for the
   * blips.
   * @param {Object} V projection
   */
  function drawFrame(V) {
    const f = V.f,
      z = V.z,
      zone = VM.viewZone(st),
      img = zone ? loadImage(zone.url) : null;
    /**
     * Frame percent to canvas x (CSS px), honouring zoom and pan.
     * @param {Object} blip blip or contact
     * @returns {*} the computed value
     */
    const sx = pct => f.x + (((pct - V.ox) * z) / PCT) * f.w,
      sy = pct => f.y + (((pct - V.oy) * z) / PCT) * f.h;
    const step = gridStep(z);
    ctx.save();
    clipToFrame(f);
    if (img) {
      ctx.globalAlpha = D.IMAGE_ALPHA_FIXED;
      ctx.drawImage(img, sx(0), sy(0), f.w * z, f.h * z);
      ctx.globalAlpha = 1;
    }
    drawGridLines(V, sx, sy, step);
    ctx.restore();
    drawAxisLabels(V, sx, sy, step);
    ctx.strokeStyle = D.COLOR.frame;
    ctx.strokeRect(f.x, f.y, f.w, f.h);
    ctx.save();
    clipToFrame(f);
    V.clipped = true;
  }

  /**
   * Free (range) mode: chart image placed in world coordinates.
   * @param {Object} V projection
   */
  function drawChartImage(V) {
    const zone = VM.viewZone(st),
      chart = VM.currentChart(st);
    if (!zone || !chart) return;
    const img = loadImage(zone.url);
    if (!img) return;
    const [sx, sy] = V.toScreen(chart.x1, chart.y1);
    const iw = (chart.y1 - chart.y0) * V.scale,
      ih = (chart.x1 - chart.x0) * V.scale;
    ctx.globalAlpha = D.IMAGE_ALPHA_FREE;
    ctx.drawImage(img, sx, sy, iw, ih);
    ctx.globalAlpha = 1;
    ctx.strokeStyle = D.COLOR.frame;
    ctx.strokeRect(sx, sy, iw, ih);
  }
  /**
   * Range rings every ringStep yards out to 1.5 x the range, the crosshair, and the N marker (X is north).
   * @param {Object} V projection
   */
  function drawRings(V) {
    const [cx, cy] = V.toScreen(V.c.x, V.c.y);
    ctx.strokeStyle = D.COLOR.ring;
    ctx.fillStyle = D.COLOR.ringLabel;
    ctx.lineWidth = D.LINE_PX;
    ctx.font = D.LABEL_FONT_RING;
    const step = blips.ringStep(V.range);
    for (let r = step; r <= V.range * D.RING_EXTENT; r += step) {
      circle(cx, cy, r * V.scale);
      ctx.stroke();
      ctx.fillText(r + 'y', cx + D.RING_LABEL_DX, cy - r * V.scale - D.RING_LABEL_DY);
    }
    ctx.beginPath();
    ctx.moveTo(cx - V.w, cy);
    ctx.lineTo(cx + V.w, cy);
    ctx.moveTo(cx, cy - V.h);
    ctx.lineTo(cx, cy + V.h);
    ctx.stroke();
    ctx.fillStyle = D.COLOR.north;
    ctx.font = D.LABEL_FONT_NORTH;
    ctx.fillText('N', V.w / 2 - D.NORTH_X_OFFSET_PX, D.NORTH_Y_PX);
  }

  // ---------- blips ----------
  /**
   * Gold ring plus a diamond above the blip marking a contact that has bounty.
   * @param {number} x world x in yards
   * @param {number} y world y in yards
   * @param {number} r row or response
   */
  function drawBountyMark(x, y, r) {
    const dy = y - r - D.BOUNTY_MARK_OFFSET;
    ctx.save();
    ctx.lineWidth = D.BOUNTY_LINE_PX;
    ctx.setLineDash([]);
    ring(x, y, r + D.BOUNTY_RING_PAD, D.COLOR.bounty);
    ctx.fillStyle = D.COLOR.bounty;
    ctx.beginPath();
    ctx.moveTo(x, dy - D.BOUNTY_MARK_H);
    ctx.lineTo(x + D.BOUNTY_MARK_W, dy);
    ctx.lineTo(x, dy + D.BOUNTY_MARK_H);
    ctx.lineTo(x - D.BOUNTY_MARK_W, dy);
    ctx.closePath();
    ctx.fill();
    ctx.restore();
  }
  /**
   * Filled dot (stale: outlined in the faction color); approximate positions are a dashed outline only.
   * @param {number} x world x in yards
   * @param {number} y world y in yards
   * @param {number} r row or response
   * @param {Object} blip blip or contact
   */
  function drawBlipBody(x, y, r, blip) {
    const faction = blips.COLORS[blip.reaction] || blips.COLORS.unknown;
    ctx.lineWidth = D.HP_LINE_PX;
    if (blip.posKind === 'approx') {
      ctx.setLineDash(D.APPROX_DASH);
      ring(x, y, r, faction);
      ctx.setLineDash([]);
      return;
    }
    ctx.fillStyle = blips.colorOf(blip);
    circle(x, y, r);
    ctx.fill();
    if (blip.state === 'stale') ring(x, y, r, faction);
  }
  function drawHpArc(x, y, r, blip) {
    const fraction = Math.max(0, Math.min(1, blip.hp / blip.maxHp));
    ctx.strokeStyle = D.COLOR.hp;
    ctx.lineWidth = D.HP_LINE_PX;
    ctx.beginPath();
    ctx.arc(x, y, r + D.RING_HP_PAD, D.HP_ARC_START, D.HP_ARC_START + TWO_PI * fraction);
    ctx.stroke();
  }
  function isCulled(V, x, y) {
    if (x < -D.PIP_CULL_PX || y < -D.PIP_CULL_PX || x > V.w + D.PIP_CULL_PX || y > V.h + D.PIP_CULL_PX) return true;
    const f = V.f;
    return !!(V.fit && (x < f.x || y < f.y || x > f.x + f.w || y > f.y + f.h));
  }
  /**
   * Blip with its decorations; skipped when outside the canvas. Registers a hit target (radius in CSS px) for the
   * tooltip.
   * @param {Object} V projection
   * @param {Object} blip blip or contact
   */
  function drawBlip(V, blip) {
    const [x, y] = V.toScreen(blip.x, blip.y);
    if (isCulled(V, x, y)) return;
    const r = blip.me ? D.PIP_ME_R : D.PIP_R;
    drawBlipBody(x, y, r, blip);
    if (blip.me) ring(x, y, r + D.RING_ME_PAD, D.COLOR.me);
    if (!blip.me && st.bounty.by[blip.guid]) drawBountyMark(x, y, r);
    if (blip.guid === st.selected) ring(x, y, r + D.RING_SEL_PAD, D.COLOR.selected);
    if (blip.hasHp && blip.maxHp > 0 && blip.state !== 'stale') drawHpArc(x, y, r, blip);
    blipHits.push({ x, y, r: r + D.HIT_PAD_PX, p: blip });
  }

  // ---------- trail ----------
  const trailColor = () => blips.COLORS[st.track && st.track.reaction] || blips.COLORS.unknown;
  const isInView = (V, x, y) =>
    V.fit ||
    (x >= -D.TRACK_CULL_PX && y >= -D.TRACK_CULL_PX && x <= V.w + D.TRACK_CULL_PX && y <= V.h + D.TRACK_CULL_PX);

  /**
   * The older part of the trail is drawn fainter: the points are split into fade buckets.
   * @param {Array} points the points
   */
  function drawTrailLine(points) {
    ctx.save();
    ctx.strokeStyle = trailColor();
    ctx.lineWidth = D.TRACK_LINE_PX;
    ctx.setLineDash(D.TRACK_DASH);
    ctx.lineCap = 'round';
    for (const b of blips.trackBuckets(points.length, radar.config.limits.TRACK_BUCKETS)) {
      ctx.globalAlpha = b.alpha;
      ctx.beginPath();
      ctx.moveTo(points[b.lo - 1][0], points[b.lo - 1][1]);
      for (let i = b.lo; i < b.hi; i++) ctx.lineTo(points[i][0], points[i][1]);
      ctx.stroke();
    }
    ctx.restore();
  }
  /**
   * One dot per fix, newer ones more opaque and larger at the end; approximate fixes are outlines. Registers hit
   * targets.
   * @param {Object} V projection
   * @param {Array} fixes trail fixes
   * @param {Array} points the points
   */
  function drawTrailDots(V, fixes, points) {
    const last = fixes.length - 1;
    ctx.save();
    ctx.setLineDash([]);
    ctx.fillStyle = trailColor();
    ctx.strokeStyle = trailColor();
    ctx.lineWidth = D.TRACK_LINE_PX;
    for (let i = 0; i <= last; i++) {
      const [x, y] = points[i];
      if (!isInView(V, x, y)) continue;
      const r = i === last ? D.TRACK_LAST_R : D.TRACK_DOT_R;
      ctx.globalAlpha = D.TRACK_ALPHA_MIN + (D.TRACK_ALPHA_SPAN * i) / Math.max(1, last);
      circle(x, y, r);
      if (blips.isApprox(fixes[i])) ctx.stroke();
      else ctx.fill();
      fixHits.push({ x, y, r: r + D.TRACK_HIT_PAD_PX, f: fixes[i] });
    }
    ctx.globalAlpha = 1;
    if (isInView(V, points[last][0], points[last][1]))
      ring(points[last][0], points[last][1], D.TRACK_LAST_RING_R, D.COLOR.me);
    ctx.restore();
  }
  function drawTrail(V) {
    const fixes = VM.trailFixesOnView(st);
    if (!fixes.length) return;
    const points = fixes.map(f => V.toScreen(f.x, f.y));
    if (points.length > 1) drawTrailLine(points);
    drawTrailDots(V, fixes, points);
  }

  // ---------- frame ----------
  /**
   * Repaint the whole radar canvas from the current state.
   */
  function draw() {
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, canvas.width / dpr, canvas.height / dpr);
    blipHits = [];
    fixHits = [];
    if (!st.snap || !st.flags.bg) return;
    const V = projection();
    if (V.fit) drawFrame(V);
    else {
      drawChartImage(V);
      drawRings(V);
    }
    drawTrail(V);
    // stale blips first so active ones are drawn on top
    const mine = st.snap.pips.filter(blip => VM.isOnMap(st, blip) && VM.isVisible(st, blip));
    mine.sort((a, b) => (a.state === 'stale' ? 0 : 1) - (b.state === 'stale' ? 0 : 1));
    for (const blip of mine) drawBlip(V, blip);
    const me = st.snap.me;
    if (me && VM.isOnMap(st, me) && VM.isVisible(st, me) && !mine.includes(me)) drawBlip(V, me);
    if (V.clipped) ctx.restore();
  }

  // ---------- hit testing ----------
  /**
   * The last drawn hit within reach wins (it was painted on top).
   * @param {Array} hits the hits
   * @param {number} mx pointer x in CSS px
   * @param {number} my pointer y in CSS px
   * @returns {*} the computed value
   */
  function nearest(hits, mx, my) {
    let best = null;
    for (const h of hits) if (Math.hypot(h.x - mx, h.y - my) <= h.r) best = h;
    return best;
  }
  const blipAt = (mx, my) => nearest(blipHits, mx, my);
  const fixAt = (mx, my) => nearest(fixHits, mx, my);

  /**
   * Bind the module to the shared state (and bus) and attach its DOM listeners.
   * @param {Object} state shared UI state
   */
  function init(state) {
    st = state;
    canvas = $('radar');
    ctx = canvas.getContext('2d');
    g.addEventListener('resize', resize);
    if (g.ResizeObserver) new g.ResizeObserver(resize).observe($('radar-wrap'));
    resize();
  }

  (radar.ui || (radar.ui = {})).canvas = { init, draw, resize, projection, blipAt, fixAt };
})(typeof self !== 'undefined' ? self : globalThis);
