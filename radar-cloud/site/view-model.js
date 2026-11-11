'use strict';
/**
 * view-model.js - single responsibility: derive everything the screen shows from the shared state, as plain data and
 * text.
 *
 * Pure functions of (state): no DOM, no timers, no network, so they are unit-tested in node. UI modules call these and
 * only copy the results into elements (always through textContent, never innerHTML, because names come from combat
 * logs).
 *
 * Public API (Radar.viewModel), st = the state object of state.js:
 *  - view selection: viewId, chartById, viewZone, viewName, currentChart, isFixedChartActive, calibrationOf, percentOf
 *  - filtering: isVisible, isOnMap, trailFixesOnView, currentRuleset, rulesetName
 *  - texts: describeHeader, describeContact, describeBlipTip, describeFixTip, describeTrail, describeBounty,
 * describeBountyDetail, describeTimestamp, describeUploadSpan (every time is shown in st.zone, see time-zone.js)
 *  - lists: buildChartItems, chartInputText, sweepInput
 */
(function (g) {
  const dep = (name, file) =>
    typeof require !== 'undefined' && typeof module !== 'undefined' ? require(file) : g.Radar[name];
  const blips = dep('blips', './blips.js');
  const charts = dep('charts', './charts.js');
  const F = dep('format', './format.js');
  const TZ = dep('timeZone', './time-zone.js');
  const M = dep('messages', './messages.js');
  const CFG = dep('config', './config.js');

  const IDENTITY_CAL = { dx: 0, dy: 0, sx: 1, sy: 1 };

  // ---------- rule-set ----------
  /** Label of a rule-set id ("auto" and unknown ids fall back to the id). */
  function rulesetName(id) {
    for (const r of CFG.rulesets.LIST) if (r[0] === id) return r[1];
    return id || '';
  }
  /** The rule-set data is shown for: manual choice, else the one the engine detected, else the stored default. */
  function currentRuleset(st) {
    if (st.settings.mode === 'manual' && st.settings.ruleset) return st.settings.ruleset;
    return st.detectedRuleset || st.settings.ruleset || CFG.rulesets.DEFAULT_ID;
  }

  // ---------- which chart is viewed ----------
  const viewId = st => blips.viewId(st.snap, st.viewMapId);
  function chartById(st, id) {
    const available = st.snap && st.snap.view && st.snap.view.available;
    if (!available) return null;
    for (const m of available) if (m.mapId === id) return m;
    return null;
  }
  function viewZone(st) {
    if (!st.snap || !st.snap.view) return null;
    return chartById(st, viewId(st)) || (st.viewMapId === null ? st.snap.view.zone : null);
  }
  function viewName(st) {
    const snap = st.snap,
      info = snap && snap.maps && snap.maps[String(viewId(st))];
    if (st.viewMapId !== null) {
      const a = chartById(st, st.viewMapId);
      return (a && a.name) || (info && info.name) || '';
    }
    return (snap && snap.zone) || (info && info.name) || '';
  }
  function currentChart(st) {
    const snap = st.snap;
    return snap && snap.maps && viewId(st) ? snap.maps[String(viewId(st))] || null : null;
  }
  const isFixedChartActive = st => !!(viewZone(st) && currentChart(st) && st.flags.fit && st.flags.bg);
  const calibrationOf = st => st.settings.cal[viewId(st)] || IDENTITY_CAL;
  const percentOf = (st, chart, x, y) => charts.worldToPct(chart, x, y, calibrationOf(st));

  // ---------- filtering ----------
  const isVisible = (st, blip) => blips.visible(blip, st.flags, st.track);
  const isOnMap = (st, blip) => blips.onMap(blip, viewId(st));
  function trailFixesOnView(st) {
    if (!st.track || !st.snap || !viewId(st)) return [];
    return st.track.fixes.filter(f => f.mapId === viewId(st));
  }

  // ---------- timestamps ----------
  /**
   * A stored UTC instant as shown to the operator: text in the operator's zone, title as ISO UTC with +00:00.
   * @param {Object} st shared UI state (st.zone is the display zone)
   * @param {number} unixMs UTC instant in unix ms
   * @returns {{text: string, title: string}} text 'YYYY-MM-DD HH:MM:SS GMT-5'; title 'YYYY-MM-DDTHH:MM:SS.mmm+00:00'
   */
  function describeTimestamp(st, unixMs) {
    return { text: TZ.formatInZone(unixMs, st.zone), title: TZ.toUtcIso(unixMs) };
  }
  /**
   * The span of the last uploaded log, in the operator's zone and as ISO UTC.
   * @param {Object} st shared UI state (st.uploadSpan = {firstT, lastT} in UTC unix ms, or null)
   * @returns {string|null} text for the upload panel; null when no log was parsed or it had no events
   */
  function describeUploadSpan(st) {
    const span = st.uploadSpan;
    if (!span || !span.firstT) return null;
    const first = describeTimestamp(st, span.firstT),
      last = describeTimestamp(st, span.lastT);
    return M.logSpan(first.text, last.text, first.title, last.title);
  }
  /**
   * Tooltip for the footer naming the newest event among the shown contacts.
   * @param {Object} st shared UI state with a snapshot
   * @returns {string} text with the time in the operator's zone and as ISO UTC; '' when no contact has an event time
   */
  function describeFreshness(st) {
    let newest = 0;
    for (const blip of st.snap.pips) if (blip.lastEvent > newest) newest = blip.lastEvent;
    if (!newest) return '';
    const t = describeTimestamp(st, newest);
    return M.newestEvent(t.text, t.title);
  }

  // ---------- header, summary, footer ----------
  /**
   * Zone line: name, map id, "[viewing]" when chosen, and the operator position in frame percent in the fixed view.
   * @param {Object} st shared UI state
   * @returns {*} the computed value
   */
  function describeZone(st) {
    const snap = st.snap,
      chart = currentChart(st),
      vid = viewId(st);
    let text = (viewName(st) || '-') + (vid ? ' (map ' + vid + ')' : '') + (st.viewMapId !== null ? ' [viewing]' : '');
    if (chart && snap.me && snap.me.posKind !== 'none' && snap.me.mapId === vid && isFixedChartActive(st)) {
      text += ' | me ' + F.fmtPct(percentOf(st, chart, snap.me.x, snap.me.y));
    }
    return text;
  }
  /**
   * Count line: stored vs on this map for cloud data; tracked/active/stale (+ operator name) for live data.
   * @param {Object} st shared UI state
   * @returns {*} the computed value
   */
  function describeSummary(st) {
    const snap = st.snap;
    if (snap.cloud)
      return snap.pips.length + ' stored, ' + snap.pips.filter(blip => isOnMap(st, blip)).length + ' on this map';
    const active = snap.pips.filter(blip => blip.state === 'active').length;
    return (
      snap.pips.length +
      ' tracked, ' +
      active +
      ' active, ' +
      (snap.pips.length - active) +
      ' stale' +
      (snap.me ? ' | me: ' + snap.me.name : '')
    );
  }
  /**
   * Footer: source, counts, advanced-logging warning, ruleset and pending uploads.
   * @param {Object} st shared UI state
   * @returns {*} the computed value
   */
  function describeFooter(st) {
    const snap = st.snap;
    const source = st.live.active ? 'Live: ' + (st.live.fileName || 'waiting for log') : 'Cloud snapshot';
    const counts = snap.cloud
      ? st.cloudRows.length + ' players, ' + st.cloudMaps.length + ' maps'
      : 'events ' + snap.stats.events + ', units ' + snap.stats.units;
    const pending = st.outbox.length ? ' | ' + st.outbox.length + ' upload(s) pending' : '';
    const advanced = snap.advanced === false ? ' | ADVANCED LOGGING IS OFF (no positions)' : '';
    return source + ' | ' + counts + advanced + ' | rule-set ' + rulesetName(currentRuleset(st)) + pending;
  }
  /**
   * Texts of the header area. ctx: {canPickDirectory}. Returns {zone, summary, footer, sourceTag, emptyHidden,
   * emptyText, footerTitle}; footerTitle (tooltip naming the newest event time) is only present once a snapshot exists.
   */
  function describeHeader(st, ctx) {
    if (!st.snap)
      return {
        zone: '-',
        summary: '',
        footer: st.signedIn ? M.notLoaded : '',
        sourceTag: null,
        emptyHidden: false,
        emptyText: null,
      };
    return {
      zone: describeZone(st),
      summary: describeSummary(st),
      footer: describeFooter(st),
      footerTitle: describeFreshness(st),
      sourceTag: st.live.active ? 'live' : ctx.canPickDirectory ? 'idle' : 'upload only',
      emptyHidden: st.snap.pips.some(blip => isOnMap(st, blip)),
      emptyText: st.live.active ? M.waitingForEvents : M.noContactsYet,
    };
  }

  // ---------- contact list rows ----------
  /** Row of the contact list: {guid, isStale, color, label, bountyScore|null, meta}. */
  function describeContact(st, blip) {
    const hp = blip.hasHp && blip.maxHp > 0 ? Math.round((CFG.units.PCT_FULL * blip.hp) / blip.maxHp) + '% ' : '';
    const pos = blip.posKind === 'none' ? 'no fix' : blip.posKind === 'approx' ? '~' : '';
    const bounty = st.bounty.by[blip.guid];
    return {
      guid: blip.guid,
      isStale: blip.state === 'stale',
      color: blips.colorOf(blip),
      label: blip.name + (blip.me ? ' (me)' : ''),
      bountyScore: bounty ? bounty.score : null,
      meta: (st.snap.cloud && blip.level ? 'Lv ' + blip.level + ' ' : '') + hp + pos + ' ' + F.fmtAge(blip.ageSec),
    };
  }

  // ---------- tooltips ----------
  /**
   * Tooltip text of a blip: name, position, faction/state, hp, last event age, estimate spread (yards), damage and
   * bounty.
   * @param {Object} st shared UI state
   * @param {Object} blip blip or contact
   * @returns {*} the computed value
   */
  function describeBlipTip(st, blip) {
    const chart = currentChart(st);
    const where = chart && isFixedChartActive(st) ? '\nmap ' + F.fmtPct(percentOf(st, chart, blip.x, blip.y)) : '';
    const lines = [
      blip.name + (blip.me ? ' (me)' : '') + where,
      blip.reaction + ', ' + blip.state + ', ' + blip.posKind + ' fix' + (blip.level ? ', level ' + blip.level : ''),
    ];
    if (blip.hasHp) lines.push('HP ' + Math.round(blip.hp) + '/' + Math.round(blip.maxHp));
    else if (blip.maxHp) lines.push('Max HP ' + blip.maxHp);
    const lastClock = blip.lastEvent ? ' (' + describeTimestamp(st, blip.lastEvent).text + ')' : '';
    lines.push('last event ' + F.fmtAge(blip.ageSec) + ' ago' + lastClock);
    if (blip.est) lines.push('estimate ±' + blip.est.spread + 'y');
    if (blip.damageHits)
      lines.push('damage dealt ' + Math.round(blip.damageTotal) + ' in ' + blip.damageHits + ' hits');
    const bounty = st.bounty.by[blip.guid];
    if (bounty) {
      lines.push(
        'BOUNTY ' +
          bounty.score +
          ' (rank ' +
          bounty.rank +
          ': ' +
          bounty.kb +
          ' kills, ' +
          bounty.assists +
          ' assists)',
      );
    }
    return lines.join('\n');
  }
  /**
   * Tooltip text of a trail fix: contact, time, position (frame percent or world yards) and exact/approx kind.
   * @param {Object} st shared UI state
   * @param {Object} f fix
   * @returns {*} the computed value
   */
  function describeFixTip(st, f) {
    const chart = currentChart(st),
      where =
        chart && isFixedChartActive(st) ? 'map ' + F.fmtPct(percentOf(st, chart, f.x, f.y)) : 'x ' + f.x + ', y ' + f.y;
    const when = describeTimestamp(st, f.t);
    return (
      (st.track ? st.track.name : '') +
      '\n' +
      when.text +
      '\n' +
      when.title +
      '\n' +
      where +
      '\n' +
      (blips.isApprox(f) ? 'approx' : 'exact') +
      ' fix' +
      (f.zone ? ', ' + f.zone : '')
    );
  }

  // ---------- trail panel ----------
  /**
   * Text under the search box for the followed trail: fix totals, how many are on the viewed chart, and where the
   * rest are.
   * @param {Object} st shared UI state
   * @returns {*} the computed value
   */
  function describeTrail(st) {
    const track = st.track;
    if (!track) return null;
    if (track.total === 0) return M.noRecordedPositions(track.name);
    const here = trailFixesOnView(st).length;
    const maps = track.maps.map(
      m =>
        (m.zone || 'map ' + m.mapId) +
        ' (' +
        m.mapId +
        '): ' +
        m.count +
        (st.snap && m.mapId === viewId(st) ? ' *' : ''),
    );
    let text = track.name + ': ' + track.total + ' fixes, ' + here + ' on the viewed map (drawn)';
    if (here === 0) text += '\nNot on the viewed map. Recorded on: ' + maps.join(', ');
    else if (track.maps.length > 1) text += '\nAll maps: ' + maps.join(', ');
    return text + describeFixSpan(st, track.fixes);
  }
  /**
   * Line naming the first and last fix time of the followed trail.
   * @param {Object} st shared UI state
   * @param {Array} fixes trail fixes sorted oldest first (t = UTC unix ms)
   * @returns {string} newline plus 'Fixes span A to B' in the operator's zone; '' when there are no fixes
   */
  function describeFixSpan(st, fixes) {
    if (!fixes.length) return '';
    return (
      '\n' + M.fixSpan(describeTimestamp(st, fixes[0].t).text, describeTimestamp(st, fixes[fixes.length - 1].t).text)
    );
  }

  // ---------- bounty ----------
  /** Row texts of one leaderboard entry. */
  function describeBounty(r) {
    return {
      title: r.name + '\n' + r.kb + ' killing blows, ' + r.assists + ' assists',
      rank: String(r.rank),
      name: r.name,
      score: String(r.score),
      meta: r.kb + ' KB / ' + r.assists + ' A',
    };
  }
  /** Detail line from GET /api/bounty/player (d) falling back to the leaderboard row r. */
  function describeBountyDetail(d, r) {
    const e = d.entry || d,
      pick = key => (e[key] !== undefined ? e[key] : r[key]);
    return (
      (e.name || r.name) +
      ': rank ' +
      (d.rank || e.rank || r.rank) +
      ', ' +
      pick('kb') +
      ' killing blows, ' +
      pick('assists') +
      ' assists, bounty ' +
      pick('score')
    );
  }

  // ---------- chart combobox ----------
  /** Entries of the chart list narrowed by the typed filter: auto first, then charts with an image, then charts
   * without. */
  function buildChartItems(st, filter) {
    const f = F.normName(filter),
      view = (st.snap && st.snap.view) || {},
      items = [];
    const matches = m => !f || F.normName(m.name).indexOf(f) >= 0;
    if (!f || 'autofollowgame'.indexOf(f) >= 0)
      items.push({ kind: 'auto', label: st.snap && st.snap.cloud ? 'Auto (most players)' : 'Auto (follow game)' });
    for (const m of view.available || []) if (matches(m)) items.push({ kind: 'map', mapId: m.mapId, label: m.name });
    for (const m of view.noImage || [])
      if (matches(m)) items.push({ kind: 'noimage', mapId: m.mapId, label: m.name, disabled: true });
    return items;
  }
  function chartInputText(st) {
    if (!st.snap) return '';
    if (st.viewMapId !== null) return viewName(st);
    return viewName(st) ? viewName(st) + ' (auto)' : 'Auto';
  }

  /** The UI state the sweep needs to pick its Sector (see sweep.pickSector). */
  const sweepInput = st => ({
    rs: currentRuleset(st),
    track: st.track,
    viewMapId: st.viewMapId,
    showBlue: st.flags.showBlue,
    showRed: st.flags.showRed,
  });

  const api = {
    rulesetName,
    currentRuleset,
    viewId,
    chartById,
    viewZone,
    viewName,
    currentChart,
    isFixedChartActive,
    calibrationOf,
    percentOf,
    isVisible,
    isOnMap,
    trailFixesOnView,
    describeHeader,
    describeContact,
    describeBlipTip,
    describeFixTip,
    describeTrail,
    describeBounty,
    describeBountyDetail,
    describeTimestamp,
    describeUploadSpan,
    buildChartItems,
    chartInputText,
    sweepInput,
  };
  (g.Radar || (g.Radar = {})).viewModel = api;
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
})(typeof self !== 'undefined' ? self : globalThis);
