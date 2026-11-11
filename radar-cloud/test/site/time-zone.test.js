'use strict';
// time-zone.js: log timestamps to UTC instants, UTC ISO text, and display in a zone (DST aware).
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { execFileSync } = require('node:child_process');
const TZ = require('../../site/time-zone.js');
const P = require('../../site/parser.js');

const NY = 'America/New_York';
const HOUR_MS = 3600000;

test('offsets written in the line win over any fallback zone (whole, positive, half-hour, negative half-hour, zero)', () => {
  const noon = (offset, zone) => TZ.parseLogTimestamp('11/11/2026 14:00:00.000' + offset, zone);
  assert.equal(noon('-5', 'Asia/Tokyo'), Date.UTC(2026, 10, 11, 19, 0, 0));
  assert.equal(noon('+1', NY), Date.UTC(2026, 10, 11, 13, 0, 0));
  assert.equal(noon('+5:30', NY), Date.UTC(2026, 10, 11, 8, 30, 0));
  assert.equal(noon('-9:30', undefined), Date.UTC(2026, 10, 11, 23, 30, 0));
  assert.equal(noon('+0', NY), Date.UTC(2026, 10, 11, 14, 0, 0));
  assert.equal(noon('-05:00', NY), Date.UTC(2026, 10, 11, 19, 0, 0));
});

test('milliseconds are kept and malformed timestamps are NaN', () => {
  assert.equal(TZ.parseLogTimestamp('1/2/2026 03:04:05.25+0', 'UTC'), Date.UTC(2026, 0, 2, 3, 4, 5, 250));
  for (const bad of ['', 'garbage', '13/1/2026 00:00:00.000', '1/2/2026 25:00:00.000-5', '1/2/2026 03:04:05.000+x'])
    assert.ok(Number.isNaN(TZ.parseLogTimestamp(bad, NY)), bad);
});

test('no offset: the wall clock is read in the fallback zone (New York winter and summer, Kolkata, UTC)', () => {
  assert.equal(TZ.parseLogTimestamp('11/11/2026 14:00:00.000', NY), Date.UTC(2026, 10, 11, 19, 0, 0)); // EST
  assert.equal(TZ.parseLogTimestamp('7/4/2026 14:00:00.000', NY), Date.UTC(2026, 6, 4, 18, 0, 0)); // EDT
  assert.equal(TZ.parseLogTimestamp('11/11/2026 14:00:00.000', 'Asia/Kolkata'), Date.UTC(2026, 10, 11, 8, 30, 0));
  assert.equal(TZ.parseLogTimestamp('11/11/2026 14:00:00.000', 'UTC'), Date.UTC(2026, 10, 11, 14, 0, 0));
  assert.equal(TZ.parseLogTimestamp('11/11/2026 14:00:00.000', 'Not/AZone'), Date.UTC(2026, 10, 11, 14, 0, 0));
});

test('no offset in New York on the November change (2026-11-01 02:00 EDT -> 01:00 EST): ambiguous hour = first pass', () => {
  const at = text => TZ.parseLogTimestamp(text, NY);
  const firstOneThirty = Date.UTC(2026, 10, 1, 5, 30, 0); // 01:30 EDT
  assert.equal(at('11/1/2026 00:59:59.000'), Date.UTC(2026, 10, 1, 4, 59, 59)); // EDT
  assert.equal(at('11/1/2026 01:30:00.000'), firstOneThirty); // ambiguous: earlier instant
  assert.equal(at('11/1/2026 01:59:59.999'), Date.UTC(2026, 10, 1, 5, 59, 59, 999));
  assert.equal(at('11/1/2026 02:00:00.000'), Date.UTC(2026, 10, 1, 7, 0, 0)); // first unambiguous EST reading
  assert.equal(at('11/1/2026 03:00:00.000') - at('11/1/2026 00:00:00.000'), 4 * HOUR_MS); // 4 elapsed hours, 3 on clock
});

test('no offset in New York on the March change (2026-03-08 02:00 EST -> 03:00 EDT): the missing hour moves forward', () => {
  const at = text => TZ.parseLogTimestamp(text, NY);
  assert.equal(at('3/8/2026 01:59:59.000'), Date.UTC(2026, 2, 8, 6, 59, 59)); // EST
  assert.equal(at('3/8/2026 02:30:00.000'), Date.UTC(2026, 2, 8, 7, 30, 0)); // nonexistent: pre-change offset, = 03:30 EDT
  assert.equal(at('3/8/2026 03:00:00.000'), Date.UTC(2026, 2, 8, 7, 0, 0)); // EDT
  assert.equal(TZ.formatInZone(at('3/8/2026 02:30:00.000'), NY, { label: false }), '2026-03-08 03:30:00');
});

test('a run of lines in one minute resolves identically (the per-minute memo adds the milliseconds)', () => {
  const a = TZ.parseLogTimestamp('11/11/2026 14:00:01.000', NY),
    b = TZ.parseLogTimestamp('11/11/2026 14:00:59.500', NY);
  assert.equal(b - a, 58500);
});

test('toUtcIso: UTC with an explicit +00:00 offset, and it parses back to the same instant', () => {
  const t = Date.UTC(2026, 10, 11, 19, 0, 0, 7);
  assert.equal(TZ.toUtcIso(t), '2026-11-11T19:00:00.007+00:00');
  assert.equal(TZ.toUtcIso(NaN), '');
  for (const instant of [0, t, Date.UTC(2026, 2, 8, 7, 30, 0, 999), Date.UTC(1999, 11, 31, 23, 59, 59, 1)]) {
    const iso = TZ.toUtcIso(instant);
    assert.equal(Date.parse(iso), instant, iso);
    const m = iso.match(/^(\d{4})-(\d\d)-(\d\d)T(\d\d):(\d\d):(\d\d)\.(\d{3})\+00:00$/);
    const logText = `${+m[2]}/${+m[3]}/${m[1]} ${m[4]}:${m[5]}:${m[6]}.${m[7]}+0`;
    assert.equal(TZ.parseLogTimestamp(logText, NY), instant, logText); // the offset in the text wins
  }
});

test('formatInZone: wall clock plus short offset label across zones and DST', () => {
  const winter = Date.UTC(2026, 10, 11, 19, 0, 0),
    summer = Date.UTC(2026, 6, 4, 18, 0, 0);
  assert.equal(TZ.formatInZone(winter, NY), '2026-11-11 14:00:00 EST');
  assert.equal(TZ.formatInZone(summer, NY), '2026-07-04 14:00:00 EDT');
  assert.equal(TZ.formatInZone(winter, 'UTC'), '2026-11-11 19:00:00 UTC');
  assert.equal(TZ.formatInZone(winter, 'Asia/Kolkata'), '2026-11-12 00:30:00 GMT+5:30');
  assert.equal(TZ.formatInZone(winter, 'Europe/Berlin'), '2026-11-11 20:00:00 GMT+1');
  assert.equal(TZ.formatInZone(summer, 'Europe/Berlin'), '2026-07-04 20:00:00 GMT+2');
  assert.equal(TZ.formatInZone(winter, 'Asia/Tokyo', { label: false }), '2026-11-12 04:00:00');
  assert.equal(TZ.formatInZone(0, 'Not/AZone'), '1970-01-01 00:00:00 UTC');
  assert.equal(TZ.formatInZone(NaN, NY), '-');
});

test('formatInZone shows midnight as 00 and the same instant twice around a clock change', () => {
  assert.equal(TZ.formatInZone(Date.UTC(2026, 10, 11, 5, 0, 0), NY, { label: false }), '2026-11-11 00:00:00');
  const first = Date.UTC(2026, 10, 1, 5, 30, 0),
    second = first + HOUR_MS;
  assert.equal(TZ.formatInZone(first, NY), '2026-11-01 01:30:00 EDT');
  assert.equal(TZ.formatInZone(second, NY), '2026-11-01 01:30:00 EST');
});

test('zone helpers: validation, offsets, the picker list and the effective zone', () => {
  assert.ok(TZ.isValidZone('UTC') && TZ.isValidZone(NY) && TZ.isValidZone('Asia/Kolkata'));
  assert.ok(!TZ.isValidZone('') && !TZ.isValidZone('Nope/Zone') && !TZ.isValidZone(5) && !TZ.isValidZone(undefined));
  assert.equal(TZ.offsetMs(Date.UTC(2026, 10, 11, 19), NY), -5 * HOUR_MS);
  assert.equal(TZ.offsetMs(Date.UTC(2026, 6, 4, 18), NY), -4 * HOUR_MS);
  assert.equal(TZ.offsetMs(Date.UTC(2026, 6, 4, 18), 'Asia/Kolkata'), 5.5 * HOUR_MS);
  const zones = TZ.supportedZones();
  assert.equal(zones[0], 'UTC');
  assert.ok(zones.includes(NY) && zones.length > 100);
  assert.equal(TZ.effectiveZone('Asia/Kolkata'), 'Asia/Kolkata');
  assert.equal(TZ.effectiveZone(undefined), TZ.hostZone());
  assert.equal(TZ.effectiveZone('bogus'), TZ.hostZone());
});

test('parser.parseTime and parseLine take the fallback zone; an offset in the line still wins', () => {
  assert.equal(P.parseTime('11/11/2026 14:00:00.000', NY), Date.UTC(2026, 10, 11, 19, 0, 0));
  assert.equal(P.parseTime('11/11/2026 14:00:00.000-5', 'Asia/Tokyo'), Date.UTC(2026, 10, 11, 19, 0, 0));
  const line = '11/11/2026 14:00:00.000  ZONE_CHANGE,1,"Barrens",0';
  assert.equal(P.parseLine(line, 'Asia/Kolkata').t, Date.UTC(2026, 10, 11, 8, 30, 0));
  assert.equal(P.parseLine(line, 'UTC').t, Date.UTC(2026, 10, 11, 14, 0, 0));
});

test('dev/make-sample-log.cjs --no-offset writes lines without the offset suffix (and by default keeps it)', () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'sample-log-'));
  const script = path.join(__dirname, '..', '..', 'dev', 'make-sample-log.cjs');
  const plain = path.join(dir, 'plain.txt'),
    offset = path.join(dir, 'offset.txt');
  try {
    execFileSync(process.execPath, [script, plain, '--no-offset'], { stdio: 'pipe' });
    execFileSync(process.execPath, [script, offset], { stdio: 'pipe' });
    const plainLines = fs.readFileSync(plain, 'utf8').trim().split('\n');
    const offsetLines = fs.readFileSync(offset, 'utf8').trim().split('\n');
    assert.equal(plainLines.length, offsetLines.length);
    assert.ok(plainLines.every(l => /^\d+\/\d+\/\d+ \d+:\d+:\d+\.\d{3} {2}[A-Z]/.test(l)));
    assert.ok(offsetLines.every(l => /^\d+\/\d+\/\d+ \d+:\d+:\d+\.\d{3}-5 {2}[A-Z]/.test(l)));
    assert.equal(P.parseLine(plainLines[0], 'America/Chicago').t, Date.UTC(2026, 10, 11, 20, 0, 0));
    assert.equal(P.parseLine(offsetLines[0], 'America/Chicago').t, Date.UTC(2026, 10, 11, 19, 0, 0));
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});
