'use strict';
// view-model.js: header texts, contact rows, filtering, trail text, bounty text, chart list, sweep input.
const test = require('node:test');
const assert = require('node:assert/strict');
const VM = require('../../site/view-model.js');
const { createState } = require('../../site/state.js');
const contacts = require('../../site/contacts.js');

const charts = [{ id: 1429, name: 'Elwynn Forest', x0: -500, x1: 500, y0: -500, y1: 500 }];
const row = (guid, name, extra) =>
  Object.assign(
    { guid, name, reaction: 'friendly', lastSeen: 1000, lastX: 10, lastY: 20, lastMap: 1429, level: 60, events: 3 },
    extra || {},
  );

function cloudState(rows) {
  const st = createState();
  st.cloudRows = rows;
  st.cloudMaps = charts;
  st.snap = contacts.cloudSnapshot(rows, charts, 61000);
  st.snap.view = {
    orientation: 'std',
    zone: null,
    available: [{ mapId: 1429, name: 'Elwynn Forest', url: 'maps/e.webp', w: 1002, h: 668 }],
    noImage: [{ mapId: 7, name: 'Hidden Vale' }],
  };
  return st;
}

test('rulesetName and currentRuleset: manual choice beats detection, detection beats the stored default', () => {
  const st = createState();
  assert.equal(VM.rulesetName('rppvp'), 'RP-PvP');
  assert.equal(VM.rulesetName('zzz'), 'zzz');
  assert.equal(VM.currentRuleset(st), 'pvp');
  st.detectedRuleset = 'rp';
  assert.equal(VM.currentRuleset(st), 'rp');
  st.settings.mode = 'manual';
  st.settings.ruleset = 'normal';
  assert.equal(VM.currentRuleset(st), 'normal');
});

test('describeHeader: empty before a snapshot, cloud summary and footer afterwards', () => {
  const st = createState();
  assert.deepEqual(VM.describeHeader(st, { canPickDirectory: true }), {
    zone: '-',
    summary: '',
    footer: '',
    sourceTag: null,
    emptyHidden: false,
    emptyText: null,
  });
  st.signedIn = true;
  assert.equal(VM.describeHeader(st, {}).footer, 'Not loaded');
  const full = cloudState([row('Player-1', 'Ann'), row('Player-2', 'Bob', { lastMap: 0, lastX: null })]);
  const h = VM.describeHeader(full, { canPickDirectory: false });
  assert.match(h.zone, /Elwynn Forest \(map 1429\)/);
  assert.equal(h.summary, '2 stored, 1 on this map');
  assert.match(h.footer, /^Cloud snapshot \| 2 players, 1 maps \| rule-set PvP$/);
  assert.equal(h.sourceTag, 'upload only');
  assert.equal(h.emptyHidden, true);
});

test('describeContact never returns markup: hostile-looking names stay plain text', () => {
  const st = cloudState([row('Player-1', '<img src=x onerror=alert(1)>', { level: 12, lastSeen: 1000 })]);
  st.bounty = { rows: [], by: { 'Player-1': { score: 30 } } };
  const d = VM.describeContact(st, st.snap.pips[0]);
  assert.equal(d.label, '<img src=x onerror=alert(1)>');
  assert.equal(d.bountyScore, 30);
  assert.match(d.meta, /^Lv 12 /);
});

test('isVisible and isOnMap honour the flags, the trail and the viewed chart', () => {
  const st = cloudState([row('Player-1', 'Ann'), row('Player-2', 'Foe', { reaction: 'hostile' })]);
  const [ann, foe] = st.snap.pips;
  assert.ok(VM.isVisible(st, ann) && VM.isVisible(st, foe));
  st.flags.showRed = false;
  assert.ok(!VM.isVisible(st, foe));
  st.track = { guid: 'Player-2', fixes: [] };
  assert.ok(!VM.isVisible(st, ann));
  assert.ok(VM.isOnMap(st, ann));
  st.viewMapId = 99;
  assert.ok(!VM.isOnMap(st, ann));
});

test('describeTrail: no trail, no positions, off-chart and multi-chart wording', () => {
  const st = cloudState([row('Player-1', 'Ann')]);
  assert.equal(VM.describeTrail(st), null);
  st.track = { guid: 'Player-1', name: 'Ann', total: 0, maps: [], fixes: [] };
  assert.equal(VM.describeTrail(st), 'Ann: no recorded positions');
  st.track = {
    guid: 'Player-1',
    name: 'Ann',
    total: 2,
    maps: [{ mapId: 5, zone: 'Hills', count: 2 }],
    fixes: [{ mapId: 5, t: 1, x: 1, y: 1 }],
  };
  assert.match(VM.describeTrail(st), /Not on the viewed map\. Recorded on: Hills \(5\): 2/);
  st.track.fixes = [{ mapId: 1429, t: 1, x: 1, y: 1 }];
  st.track.maps.push({ mapId: 1429, zone: '', count: 1 });
  assert.match(VM.describeTrail(st), /All maps: Hills \(5\): 2, map 1429 \(1429\): 1 \*/);
});

test('describeBountyDetail prefers the detail answer and falls back to the ranked row', () => {
  const r = { name: 'Foe', rank: 2, kb: 3, assists: 1, score: 30 };
  assert.equal(
    VM.describeBountyDetail({ entry: { name: 'Foe', kb: 4, score: 40 }, rank: 1 }, r),
    'Foe: rank 1, 4 killing blows, 1 assists, bounty 40',
  );
  assert.equal(VM.describeBountyDetail({}, r), 'Foe: rank 2, 3 killing blows, 1 assists, bounty 30');
});

test('buildChartItems: auto first, charts with an image, charts without are disabled; the filter narrows', () => {
  const st = cloudState([row('Player-1', 'Ann')]);
  const all = VM.buildChartItems(st, '');
  assert.deepEqual(
    all.map(i => i.kind),
    ['auto', 'map', 'noimage'],
  );
  assert.equal(all[0].label, 'Auto (most players)');
  assert.ok(all[2].disabled);
  assert.deepEqual(
    VM.buildChartItems(st, 'elw').map(i => i.kind),
    ['map'],
  );
  assert.deepEqual(
    VM.buildChartItems(st, 'follow').map(i => i.kind),
    ['auto'],
  );
  assert.equal(VM.chartInputText(st), 'Elwynn Forest (auto)');
  st.viewMapId = 1429;
  assert.equal(VM.chartInputText(st), 'Elwynn Forest');
});

test('sweepInput reflects the sector the sweep should watch', () => {
  const st = createState();
  st.viewMapId = 1429;
  st.flags.showRed = false;
  assert.deepEqual(VM.sweepInput(st), { rs: 'pvp', track: null, viewMapId: 1429, showBlue: true, showRed: false });
});

const SEEN = Date.UTC(2026, 10, 11, 19, 0, 0); // 14:00 in New York, 00:30 next day in Kolkata

test('describeTimestamp: operator-zone text plus an ISO UTC (+00:00) title, following st.zone', () => {
  const st = cloudState([]);
  st.zone = 'UTC';
  assert.deepEqual(VM.describeTimestamp(st, SEEN), {
    text: '2026-11-11 19:00:00 UTC',
    title: '2026-11-11T19:00:00.000+00:00',
  });
  st.zone = 'America/New_York';
  assert.equal(VM.describeTimestamp(st, SEEN).text, '2026-11-11 14:00:00 EST');
  st.zone = 'Asia/Kolkata';
  assert.equal(VM.describeTimestamp(st, SEEN).text, '2026-11-12 00:30:00 GMT+5:30');
  assert.equal(VM.describeTimestamp(st, SEEN).title, '2026-11-11T19:00:00.000+00:00');
});

test('trail, fix tooltip, blip tooltip and footer tooltip show times in the operator zone, ISO UTC beside raw ones', () => {
  const st = cloudState([row('Player-1', 'Ann', { lastSeen: SEEN })]);
  st.zone = 'America/New_York';
  st.track = {
    guid: 'Player-1',
    name: 'Ann',
    total: 2,
    maps: [{ mapId: 1429, zone: '', count: 2 }],
    fixes: [
      { mapId: 1429, t: SEEN, x: 1, y: 1 },
      { mapId: 1429, t: SEEN + 3600000, x: 2, y: 2 },
    ],
  };
  assert.match(VM.describeTrail(st), /\nFixes span 2026-11-11 14:00:00 EST to 2026-11-11 15:00:00 EST$/);
  const tip = VM.describeFixTip(st, st.track.fixes[0]);
  assert.match(tip, /2026-11-11 14:00:00 EST\n2026-11-11T19:00:00\.000\+00:00\n/);
  assert.match(VM.describeBlipTip(st, st.snap.pips[0]), /last event \S+ ago \(2026-11-11 14:00:00 EST\)/);
  assert.equal(
    VM.describeHeader(st, {}).footerTitle,
    'Newest event 2026-11-11 14:00:00 EST (UTC 2026-11-11T19:00:00.000+00:00)',
  );
  st.zone = 'Asia/Kolkata';
  assert.match(VM.describeTrail(st), /Fixes span 2026-11-12 00:30:00 GMT\+5:30 to 2026-11-12 01:30:00 GMT\+5:30$/);
  assert.match(VM.describeFixTip(st, st.track.fixes[0]), /^Ann\n2026-11-12 00:30:00 GMT\+5:30\n/);
});

test('describeUploadSpan: null without a parsed log, otherwise zone text and ISO UTC for both ends', () => {
  const st = cloudState([]);
  assert.equal(VM.describeUploadSpan(st), null);
  st.uploadSpan = { firstT: 0, lastT: 0 };
  assert.equal(VM.describeUploadSpan(st), null);
  st.uploadSpan = { firstT: SEEN, lastT: SEEN + 600000 };
  st.zone = 'America/New_York';
  assert.equal(
    VM.describeUploadSpan(st),
    'Log spans 2026-11-11 14:00:00 EST to 2026-11-11 14:10:00 EST ' +
      '(UTC 2026-11-11T19:00:00.000+00:00 to 2026-11-11T19:10:00.000+00:00)',
  );
});
