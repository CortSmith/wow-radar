'use strict';
/**
 * canvas-input.js - single responsibility: pointer input on the radar canvas (drag to pan, wheel to zoom or change
 * range,
 * double-click to reset zoom) and the hover tooltip.
 *
 * Public API (Radar.ui.canvasInput): init(state, bus). Reads hit lists from canvas.js, texts from view-model.js,
 * and emits "draw". Moving the view turns "Follow me" off through settings-ui so the choice is remembered.
 */
(function (g) {
  const radar = g.Radar,
    { $ } = radar.dom,
    D = radar.config.draw,
    GEO = radar.config.geometry,
    VM = radar.viewModel,
    blips = radar.blips;
  const FRAME_CENTRE = radar.state.FRAME_CENTRE_PCT;
  let st = null,
    bus = null,
    canvas = null,
    tip = null,
    drag = null;

  const pointer = e => {
    const rect = canvas.getBoundingClientRect();
    return { x: e.clientX - rect.left, y: e.clientY - rect.top, rect };
  };

  function showTip(text, p) {
    tip.textContent = text;
    tip.style.left = Math.min(p.x + D.TIP_OFFSET_PX, p.rect.width - D.TIP_MAX_W_PX) + 'px';
    tip.style.top = p.y + D.TIP_OFFSET_PX + 'px';
    tip.hidden = false;
  }

  function releaseFollow() {
    if (st.flags.follow) radar.ui.settings.setFlag('follow', false);
  }

  /**
   * Drag: moves the fixed-chart zoom window (frame percent) or the free-mode camera (world yards); any real movement
   * turns Follow me off.
   * @param {Object} p blip or contact
   */
  function pan(p) {
    const dx = p.x - drag.x,
      dy = p.y - drag.y;
    if (Math.abs(dx) + Math.abs(dy) > D.DRAG_MIN_PX) releaseFollow();
    if (VM.isFixedChartActive(st)) {
      const r = blips.panFrom(drag.fz0, radar.ui.canvas.projection().f, st.fz.z, dx, dy);
      st.fz.cx = r.cx;
      st.fz.cy = r.cy;
    } else {
      const k = 1 / radar.ui.canvas.projection().scale;
      st.cam = { x: drag.cx + dy * k, y: drag.cy + dx * k };
    }
    bus.emit('draw');
  }

  /**
   * Tooltip for the blip under the pointer, else for the trail fix under it, else hide it.
   * @param {Object} p blip or contact
   */
  function hover(p) {
    const hit = radar.ui.canvas.blipAt(p.x, p.y);
    if (hit) {
      showTip(VM.describeBlipTip(st, hit.p), p);
      return;
    }
    const fix = radar.ui.canvas.fixAt(p.x, p.y);
    if (fix) showTip(VM.describeFixTip(st, fix.f), p);
    else tip.hidden = true;
  }

  function onMouseMove(e) {
    const p = pointer(e);
    if (drag) pan(p);
    else hover(p);
  }
  function onMouseDown(e) {
    const p = pointer(e),
      c = blips.centre({ follow: st.flags.follow, me: st.snap && st.snap.me, cam: st.cam, viewId: VM.viewId(st) });
    drag = { x: p.x, y: p.y, cx: c.x, cy: c.y, fz0: { cx: st.fz.cx, cy: st.fz.cy } };
    if (!VM.isFixedChartActive(st)) st.cam = { x: c.x, y: c.y };
  }
  /**
   * Wheel zooms the fixed chart around the pointer, or changes the radar range (yards) in free mode.
   * @param {Object} e the DOM event
   */
  function onWheel(e) {
    e.preventDefault();
    if (VM.isFixedChartActive(st)) {
      const p = pointer(e),
        factor = e.deltaY > 0 ? 1 / GEO.WHEEL_ZOOM : GEO.WHEEL_ZOOM;
      const r = blips.zoomAt(st.fz, radar.ui.canvas.projection().f, p.x, p.y, factor);
      st.fz.z = r.z;
      st.fz.cx = r.cx;
      st.fz.cy = r.cy;
    } else {
      const range = $('range');
      radar.ui.settings.setRange(blips.rangeWheel(Number(range.value), Number(range.min), Number(range.max), e.deltaY));
    }
    bus.emit('draw');
  }
  function onDoubleClick() {
    if (!VM.isFixedChartActive(st)) return;
    st.fz.z = 1;
    st.fz.cx = FRAME_CENTRE;
    st.fz.cy = FRAME_CENTRE;
    bus.emit('draw');
  }

  /**
   * Bind the module to the shared state (and bus) and attach its DOM listeners.
   * @param {Object} state shared UI state
   * @param {Object} eventBus event bus
   */
  function init(state, eventBus) {
    st = state;
    bus = eventBus;
    canvas = $('radar');
    tip = $('tooltip');
    canvas.addEventListener('mousemove', onMouseMove);
    canvas.addEventListener('mouseleave', () => {
      tip.hidden = true;
      drag = null;
    });
    canvas.addEventListener('mousedown', onMouseDown);
    g.addEventListener('mouseup', () => {
      drag = null;
    });
    canvas.addEventListener('dblclick', onDoubleClick);
    canvas.addEventListener('wheel', onWheel, { passive: false });
  }

  (radar.ui || (radar.ui = {})).canvasInput = { init };
})(typeof self !== 'undefined' ? self : globalThis);
