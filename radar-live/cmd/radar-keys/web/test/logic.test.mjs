import test from 'node:test';
import assert from 'node:assert/strict';
import {
  validateEmail, parseTags, parseLimit, expiresToIso, isoToDateInput, validDate, validateEntry, formFromKey,
  maskKey, statusLabel, isEnabled, isReadOnly, formatCount, formatTime, formatDay, formatLimits, limitPlaceholder,
  allTags, filterKeys, sortKeys, errorMessage, MAX_LIMIT,
} from '../logic.js';

const blank = { email: 'a@b.co', player: '', tags: '', notes: '', expires: '', limits: {} };

test('validateEmail', () => {
  assert.equal(validateEmail('a@b.co'), '');
  assert.equal(validateEmail('  a.b+c@x.example.org '), '');
  assert.match(validateEmail(''), /required/);
  assert.match(validateEmail('   '), /required/);
  assert.match(validateEmail(undefined), /required/);
  for (const bad of ['a', 'a@b', '@b.co', 'a@@b.co', 'a b@c.co', 'a@b.co,c@d.co', `${'x'.repeat(250)}@b.co`]) {
    assert.match(validateEmail(bad), /valid/, bad);
  }
});

test('parseTags splits, trims, dedupes case-insensitively', () => {
  assert.deepEqual(parseTags('a, b,a ;C\nc'), ['a', 'b', 'C']);
  assert.deepEqual(parseTags(''), []);
  assert.deepEqual(parseTags(' , ,'), []);
  assert.deepEqual(parseTags(null), []);
  assert.deepEqual(parseTags('Raid Lead, beta'), ['Raid Lead', 'beta']);
});

test('parseLimit', () => {
  assert.deepEqual(parseLimit(''), { value: null });
  assert.deepEqual(parseLimit('  '), { value: null });
  assert.deepEqual(parseLimit(undefined), { value: null });
  assert.deepEqual(parseLimit('250'), { value: 250 });
  assert.deepEqual(parseLimit(' 7 '), { value: 7 });
  assert.ok(parseLimit('0').error);
  assert.ok(parseLimit('-5').error);
  assert.ok(parseLimit('1.5').error);
  assert.ok(parseLimit('abc').error);
  assert.ok(parseLimit('1e3').error);
  assert.ok(parseLimit(String(MAX_LIMIT + 1)).error);
  assert.deepEqual(parseLimit(String(MAX_LIMIT)), { value: MAX_LIMIT });
});

test('date helpers', () => {
  assert.equal(expiresToIso(''), null);
  assert.equal(expiresToIso('2031-02-03'), '2031-02-03T23:59:59Z');
  assert.equal(isoToDateInput('2031-02-03T23:59:59Z'), '2031-02-03');
  assert.equal(isoToDateInput(null), '');
  assert.equal(validDate(''), true);
  assert.equal(validDate('2031-02-03'), true);
  assert.equal(validDate('2031-02-30'), false);
  assert.equal(validDate('03/02/2031'), false);
});

test('validateEntry builds the payload', () => {
  const r = validateEntry({
    email: ' a@b.co ', player: ' Thrall ', tags: 'x, y', notes: ' hi ', expires: '2031-01-01',
    limits: { lineRate: '100', lineBurst: '', byteRate: '2000', byteBurst: ' ', maxStreams: '2' }, keepCopy: true,
  });
  assert.equal(r.ok, true);
  assert.deepEqual(r.payload, {
    email: 'a@b.co', player: 'Thrall', tags: ['x', 'y'], notes: 'hi', expiresAt: '2031-01-01T23:59:59Z',
    limits: { lineRate: 100, lineBurst: null, byteRate: 2000, byteBurst: null, maxStreams: 2 }, keepCopy: true,
  });
});

test('validateEntry omits keepCopy when undefined (edit) and defaults limits to null', () => {
  const r = validateEntry(blank);
  assert.equal(r.ok, true);
  assert.equal('keepCopy' in r.payload, false);
  assert.deepEqual(r.payload.limits, { lineRate: null, lineBurst: null, byteRate: null, byteBurst: null, maxStreams: null });
  assert.equal(r.payload.expiresAt, null);
  assert.equal(validateEntry({ ...blank, keepCopy: false }).payload.keepCopy, false);
});

test('validateEntry reports every error and no payload', () => {
  const r = validateEntry({ ...blank, email: 'nope', expires: 'x', limits: { lineRate: '0', lineBurst: '-1', maxStreams: 'q' } });
  assert.equal(r.ok, false);
  assert.equal(r.payload, null);
  assert.deepEqual(Object.keys(r.errors).sort(), ['email', 'expires', 'lineBurst', 'lineRate', 'maxStreams']);
});

test('formFromKey', () => {
  const f = formFromKey({ email: 'a@b.co', player: 'P', tags: ['x', 'y'], notes: 'n', expiresAt: '2031-02-03T00:00:00Z', limits: { lineRate: 5, lineBurst: null } });
  assert.deepEqual(f, {
    email: 'a@b.co', player: 'P', tags: 'x, y', notes: 'n', expires: '2031-02-03',
    limits: { lineRate: '5', lineBurst: '', byteRate: '', byteBurst: '', maxStreams: '' },
  });
  assert.equal(formFromKey({}).limits.maxStreams, '');
});

test('maskKey', () => {
  assert.equal(maskKey('abcdefghijklmnop'), 'abcd••••••mnop');
  assert.equal(maskKey('abc'), '•'.repeat(8));
  assert.equal(maskKey(''), '•'.repeat(8));
  assert.equal(maskKey(undefined), '•'.repeat(8));
});

test('status helpers', () => {
  assert.equal(statusLabel({ status: 'blocked' }), 'blocked');
  assert.equal(statusLabel({ status: 'weird' }), 'active');
  assert.equal(statusLabel(null), 'active');
  assert.equal(isEnabled({ status: 'active', disabled: false }), true);
  assert.equal(isEnabled({ status: 'blocked', disabled: true }), false);
  assert.equal(isEnabled({ status: 'expired', disabled: false }), true);
  assert.equal(isReadOnly({ status: 'legacy' }), true);
  assert.equal(isReadOnly({ status: 'active' }), false);
});

test('formatters', () => {
  assert.equal(formatCount(1234567), '1,234,567');
  assert.equal(formatCount(undefined), '0');
  assert.equal(formatCount('x'), '0');
  assert.equal(formatTime('2030-01-02T03:04:59Z'), '2030-01-02 03:04');
  assert.equal(formatTime(null), 'never');
  assert.equal(formatTime('garbage', '-'), '-');
  assert.equal(formatDay('2030-01-02T03:04:59Z'), '2030-01-02');
  assert.equal(formatDay(null), '-');
  assert.equal(formatDay('nope'), '-');
});

test('formatLimits shows default or custom values', () => {
  assert.equal(formatLimits(null), 'default');
  assert.equal(formatLimits({ lineRate: null, lineBurst: null, byteRate: null, byteBurst: null, maxStreams: null }), 'default');
  assert.equal(formatLimits({ lineRate: 1000, lineBurst: null, byteRate: null, byteBurst: null, maxStreams: 2 }), '1,000 lines/s, 2 streams');
});

test('limitPlaceholder', () => {
  assert.equal(limitPlaceholder({ lineRate: 20000 }, 'lineRate'), 'default 20,000');
  assert.equal(limitPlaceholder({}, 'lineRate'), 'default');
  assert.equal(limitPlaceholder(undefined, 'lineRate'), 'default');
});

const K = [
  { id: 'k1', email: 'Alice@x.co', player: 'Thrall', tags: ['raid', 'Beta'], notes: 'friend', masked: 'abcd', createdAt: '2030-01-01T00:00:00Z' },
  { id: 'k2', email: 'bob@x.co', player: '', tags: ['beta'], notes: '', masked: 'wxyz', createdAt: '2030-03-01T00:00:00Z' },
  { id: 'k3', email: 'cy@x.co', player: 'Cy', tags: [], notes: '', masked: 'qqqq' },
];

test('allTags is unique, case-insensitive and sorted', () => {
  assert.deepEqual(allTags(K), ['Beta', 'raid']);
  assert.deepEqual(allTags([]), []);
});

test('filterKeys', () => {
  assert.equal(filterKeys(K, '', '').length, 3);
  assert.deepEqual(filterKeys(K, 'ALICE', '').map((k) => k.id), ['k1']);
  assert.deepEqual(filterKeys(K, 'thrall', '').map((k) => k.id), ['k1']);
  assert.deepEqual(filterKeys(K, 'friend', '').map((k) => k.id), ['k1']);
  assert.deepEqual(filterKeys(K, 'wxyz', '').map((k) => k.id), ['k2']);
  assert.deepEqual(filterKeys(K, '', 'BETA').map((k) => k.id), ['k1', 'k2']);
  assert.deepEqual(filterKeys(K, 'bob', 'beta').map((k) => k.id), ['k2']);
  assert.deepEqual(filterKeys(K, 'bob', 'raid'), []);
  assert.deepEqual(filterKeys([], 'x', ''), []);
});

test('sortKeys newest first, stable, missing dates last, input untouched', () => {
  assert.deepEqual(sortKeys(K).map((k) => k.id), ['k2', 'k1', 'k3']);
  assert.deepEqual(K.map((k) => k.id), ['k1', 'k2', 'k3']);
});

test('errorMessage', () => {
  assert.equal(errorMessage({ error: 'x', message: 'Bad thing' }), 'Bad thing');
  assert.equal(errorMessage({}), 'Request failed.');
  assert.equal(errorMessage(null, 'fb'), 'fb');
});
