'use strict';
/**
 * chart-box.js - single responsibility: the chart (map) combobox in the header: type to narrow, arrows/Enter to choose,
 * Esc/Tab/outside click to close. "Auto" follows the game (live) or the busiest chart (cloud).
 *
 * Public API (Radar.ui.chartBox):
 *  - init(state, bus)
 *  - renderInput()   show the viewed chart's name in the box (ignored while the operator is typing)
 * Choosing a chart sets state.viewMapId and emits the redraw events plus "sector" (the sweep follows the viewed chart).
 * ARIA: the input is role=combobox controlling the listbox; the active option is announced via aria-activedescendant.
 */
(function (g) {
  const radar = g.Radar,
    { $, el, replaceChildren } = radar.dom,
    VM = radar.viewModel;
  let st = null,
    bus = null;
  let isOpen = false,
    isEditing = false,
    items = [],
    activeIndex = -1;

  function firstEnabled(from, step) {
    for (let i = from; i >= 0 && i < items.length; i += step) if (!items[i].disabled) return i;
    return -1;
  }

  function selectText(input) {
    try {
      input.select();
    } catch (e) {
      /* not selectable in this browser: harmless */
    }
  }

  /**
   * One option of the listbox with ARIA state (selected, disabled); disabled charts have no image and cannot be chosen.
   * @param {Object} item list item
   * @param {number} i index
   * @returns {*} the computed value
   */
  function buildOption(item, i) {
    const isCurrent = item.kind === 'auto' ? st.viewMapId === null : st.viewMapId === item.mapId;
    const classes =
      (item.kind === 'auto' ? 'auto ' : '') +
      (item.disabled ? 'disabled ' : '') +
      (isCurrent ? 'current ' : '') +
      (i === activeIndex ? 'active' : '');
    const attrs = { role: 'option', 'aria-selected': isCurrent ? 'true' : 'false' };
    if (item.disabled) attrs['aria-disabled'] = 'true';
    const children = [el('span', { text: item.label })];
    if (item.disabled) children.push(el('span', { className: 'hint', text: 'no image' }));
    const li = el('li', { className: classes, attrs }, children);
    li.id = 'chart-option-' + i;
    if (!item.disabled) li.addEventListener('click', () => choose(item));
    return li;
  }

  /**
   * Repaint the list from the shared state.
   */
  function renderList() {
    const list = $('chart-list'),
      input = $('chart-input');
    input.setAttribute('aria-expanded', isOpen ? 'true' : 'false');
    list.hidden = !isOpen;
    if (!isOpen) {
      replaceChildren(list, []);
      return;
    }
    const options = items.map(buildOption);
    if (!items.some(it => it.kind !== 'auto')) {
      options.push(
        el('li', {
          className: 'empty-row',
          text: radar.messages.noMatchingCharts,
          attrs: { role: 'option', 'aria-disabled': 'true' },
        }),
      );
    }
    replaceChildren(list, options);
    if (activeIndex >= 0) input.setAttribute('aria-activedescendant', 'chart-option-' + activeIndex);
    else input.removeAttribute('aria-activedescendant');
  }

  /**
   * Show the viewed chart name in the combobox input.
   */
  function renderInput() {
    if (!isEditing) $('chart-input').value = VM.chartInputText(st);
  }
  function open(filter) {
    items = VM.buildChartItems(st, filter);
    isOpen = true;
    activeIndex = firstEnabled(0, 1);
    renderList();
  }
  function close() {
    isOpen = false;
    isEditing = false;
    activeIndex = -1;
    renderList();
    renderInput();
  }

  /**
   * Apply a chart choice: set the viewed chart (null = follow the game), close the list, repaint, re-aim the sweep.
   * @param {Object} item list item
   */
  function choose(item) {
    if (!item || item.disabled) return;
    st.viewMapId = item.kind === 'auto' ? null : item.mapId;
    close();
    if (st.snap) {
      radar.ui.view.syncView();
      for (const event of ['list', 'header', 'trail-info', 'chart-input', 'draw']) bus.emit(event);
    }
    bus.emit('sector');
  }

  /**
   * Arrow keys: open the list, or move the active option to the next enabled one in that direction.
   * @param {string} key key
   */
  function moveActive(key) {
    const input = $('chart-input');
    if (!isOpen) {
      open(isEditing ? input.value : '');
      return;
    }
    const down = key === 'ArrowDown';
    const next = firstEnabled(down ? activeIndex + 1 : activeIndex - 1, down ? 1 : -1);
    if (next >= 0) {
      activeIndex = next;
      renderList();
    }
  }

  /**
   * Keyboard contract of the combobox: arrows move, Enter chooses, Esc closes (without bubbling to the search box),
   * Tab closes.
   * @param {Object} e the DOM event
   */
  function onKeyDown(e) {
    const k = e.key;
    if (k === 'ArrowDown' || k === 'ArrowUp') {
      e.preventDefault();
      moveActive(k);
    } else if (k === 'Enter') {
      if (isOpen) {
        e.preventDefault();
        choose(items[activeIndex]);
      }
    } else if (k === 'Escape') {
      if (isOpen) {
        e.preventDefault();
        e.stopPropagation();
        close();
      }
    } else if (k === 'Tab') {
      if (isOpen) close();
    }
  }

  /**
   * Bind the module to the shared state (and bus) and attach its DOM listeners.
   * @param {Object} state shared UI state
   * @param {Object} eventBus event bus
   */
  function init(state, eventBus) {
    st = state;
    bus = eventBus;
    const input = $('chart-input');
    input.addEventListener('focus', () => selectText(input));
    input.addEventListener('click', () => {
      isEditing = false;
      open('');
      selectText(input);
    });
    input.addEventListener('input', () => {
      isEditing = true;
      open(input.value);
    });
    input.addEventListener('keydown', onKeyDown);
    g.document.addEventListener('mousedown', e => {
      if (isOpen && !$('chart-box').contains(e.target)) close();
    });
  }

  (radar.ui || (radar.ui = {})).chartBox = { init, renderInput };
})(typeof self !== 'undefined' ? self : globalThis);
