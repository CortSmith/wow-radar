// Stage 11 U3: search chips. Picking a type-ahead result adds a chip (not a single-select fill of the
// box); each tracked player gets its own chip with a stable track colour, an x that removes just that
// one, and a "Clear all" link that only shows at 2+ chips. Keyboard: Backspace on an empty box removes
// the last chip, Enter with exactly one result showing adds it. See deselect.test.js for the UI
// behaviour this must not regress (Escape, the text-clear x, list/blip selection).
import test from 'node:test';
import assert from 'node:assert/strict';
import { createApp } from '../js/core.js';
import { loaders } from '../js/features.js';
import { demoApi } from '../dev-demo.mjs';
import { createFakeDocument } from './fake-dom.js';
import { createFakeTimers } from './fake-timers.js';
import { trackColorClass } from '../js/trackcolors.js';

const T0 = 1_760_000_000_000;
const DEBOUNCE_MS = 250; // config.SEARCH_DEBOUNCE_MS
const settle = async (n = 6) => { for (let i = 0; i < n; i++) await new Promise((r) => setImmediate(r)); };

async function boot() {
  const features = await Promise.all(loaders.map(async (l) => (await l()).default));
  const timers = createFakeTimers(T0);
  const doc = createFakeDocument();
  const requests = [];
  const env = {
    document: doc, window: null, timers, now: timers.now, features, storage: null, logs: [],
    log: (m, e) => env.logs.push([m, e && e.message]),
    fetch: async (url) => {
      requests.push(url);
      const u = new URL(url, 'http://x');
      if (!u.pathname.startsWith('/api/')) return { ok: false, status: 404, json: async () => null };
      const out = demoApi(url, timers.now());
      return { ok: out.status < 400, status: out.status, json: async () => out.body };
    },
  };
  const app = createApp(env);
  app.start();
  await timers.advance(0); await settle();
  const top = doc.querySelector('[data-mount="side-top"]');
  return {
    app, env, timers, doc, requests, top,
    input: top.find((n) => n.tagName === 'INPUT'),
    results: top.find((n) => n.className === 'results'),
    chipsEl: top.find((n) => n.className === 'chips'),
    clearAllBtn: top.find((n) => n.className === 'clearAll'),
    chips: () => top.findAll((n) => n.className === 'chip'),
    resultRows: () => top.findAll((n) => n.tagName === 'LI'),
    async type(q) { this.input.value = q; this.input.dispatch('input'); await this.timers.advance(DEBOUNCE_MS); await settle(); },
  };
}

function firstContact(h) {
  return [...h.app.state.contacts.values()][0];
}

test('typing and picking a result adds a chip and clears the input', async () => {
  const h = await boot();
  const c = firstContact(h);
  await h.type(c.name);
  const row = h.resultRows().find((n) => n.textContent.includes(c.name));
  assert.ok(row, 'the type-ahead result is rendered');
  row.dispatch('click');
  assert.equal(h.input.value, '', 'the input is cleared right after picking, ready for the next name');
  assert.equal(h.app.state.tracked.has(c.guid), true);
  assert.equal(h.results.hidden, true, 'the dropdown closes after a pick');
  h.app.stop();
});

test('picking a second result adds a second chip without removing the first', async () => {
  const h = await boot();
  const [a, b] = [...h.app.state.contacts.values()];
  h.app.ctx.addPlayer({ guid: a.guid, name: a.name });
  h.app.ctx.addPlayer({ guid: b.guid, name: b.name });
  assert.equal(h.app.state.tracked.size, 2);
  const chips = h.chips();
  assert.equal(chips.length, 2);
  assert.ok(chips[0].textContent.includes(a.name));
  assert.ok(chips[1].textContent.includes(b.name));
  h.app.stop();
});

test("each chip's x removes only that player", async () => {
  const h = await boot();
  const [a, b, c] = [...h.app.state.contacts.values()];
  for (const p of [a, b, c]) h.app.ctx.addPlayer({ guid: p.guid, name: p.name });
  assert.equal(h.app.state.tracked.size, 3);
  const bChip = h.chips().find((n) => n.textContent.includes(b.name));
  const x = bChip.children.find((n) => n.tagName === 'BUTTON');
  assert.ok(x, 'the chip has an x button');
  assert.equal(x.getAttribute('aria-label'), `Remove ${b.name}`);
  x.dispatch('click');
  assert.equal(h.app.state.tracked.has(b.guid), false);
  assert.equal(h.app.state.tracked.has(a.guid), true);
  assert.equal(h.app.state.tracked.has(c.guid), true);
  assert.equal(h.chips().length, 2);
  h.app.stop();
});

test('clicking a chip body (not the x) focuses that player without removing anyone', async () => {
  const h = await boot();
  const [a, b] = [...h.app.state.contacts.values()];
  h.app.ctx.addPlayer({ guid: a.guid, name: a.name });
  h.app.ctx.addPlayer({ guid: b.guid, name: b.name });
  assert.deepEqual(h.app.state.player, { guid: b.guid, name: b.name });
  const aChip = h.chips().find((n) => n.textContent.includes(a.name));
  const body = aChip.children.find((n) => n.className === 'chipBody');
  body.dispatch('click');
  assert.equal(h.app.state.tracked.size, 2, 'focusing never adds or removes anyone');
  assert.equal(h.app.state.player.guid, a.guid, 'a is now focused (most recent)');
  h.app.stop();
});

test('"Clear all" is hidden below two chips and clears everyone when clicked', async () => {
  const h = await boot();
  assert.equal(h.clearAllBtn.hidden, true, 'nothing tracked yet');
  const [a, b] = [...h.app.state.contacts.values()];
  h.app.ctx.addPlayer({ guid: a.guid, name: a.name });
  assert.equal(h.clearAllBtn.hidden, true, 'only one chip: no Clear all');
  h.app.ctx.addPlayer({ guid: b.guid, name: b.name });
  assert.equal(h.clearAllBtn.hidden, false, 'two chips: Clear all appears');
  h.clearAllBtn.dispatch('click');
  assert.equal(h.app.state.tracked.size, 0);
  assert.equal(h.chips().length, 0);
  assert.equal(h.clearAllBtn.hidden, true);
  h.app.stop();
});

test('a single tracked player looks reasonable: one chip, no Clear all, chips container visible', async () => {
  const h = await boot();
  const c = firstContact(h);
  h.app.ctx.addPlayer({ guid: c.guid, name: c.name });
  assert.equal(h.chipsEl.hidden, false);
  assert.equal(h.chips().length, 1);
  assert.equal(h.clearAllBtn.hidden, true);
  h.app.stop();
});

test('zero tracked players: chips container is empty/hidden, nothing broken', async () => {
  const h = await boot();
  assert.equal(h.chipsEl.hidden, true);
  assert.equal(h.chips().length, 0);
  assert.equal(h.clearAllBtn.hidden, true);
  h.app.stop();
});

test('Backspace on an empty input removes the last (most recently added/focused) chip', async () => {
  const h = await boot();
  const [a, b] = [...h.app.state.contacts.values()];
  h.app.ctx.addPlayer({ guid: a.guid, name: a.name });
  h.app.ctx.addPlayer({ guid: b.guid, name: b.name });
  h.input.value = '';
  h.input.dispatch('keydown', { key: 'Backspace' });
  assert.equal(h.app.state.tracked.has(b.guid), false, 'the most recent (b) was removed');
  assert.equal(h.app.state.tracked.has(a.guid), true);
  assert.equal(h.chips().length, 1);
  h.app.stop();
});

test('Backspace with text still in the input does nothing to the tracked set', async () => {
  const h = await boot();
  const a = firstContact(h);
  h.app.ctx.addPlayer({ guid: a.guid, name: a.name });
  h.input.value = 'something';
  h.input.dispatch('keydown', { key: 'Backspace' });
  assert.equal(h.app.state.tracked.size, 1, 'untouched: the input was not empty');
  h.app.stop();
});

test('Backspace on an empty input with nothing tracked is a no-op', async () => {
  const h = await boot();
  h.input.value = '';
  h.input.dispatch('keydown', { key: 'Backspace' });
  assert.equal(h.app.state.tracked.size, 0);
  h.app.stop();
});

test('Enter adds the result when exactly one is showing', async () => {
  const h = await boot();
  const c = firstContact(h);
  await h.type(c.name);
  assert.equal(h.resultRows().filter((n) => !n.className.includes('none')).length, 1, 'the query matches exactly one contact');
  h.input.dispatch('keydown', { key: 'Enter' });
  assert.equal(h.app.state.tracked.has(c.guid), true);
  h.app.stop();
});

test('Enter does nothing when zero or several results are showing', async () => {
  const h = await boot();
  await h.type('zzzz-no-such-player-zzzz');
  h.input.dispatch('keydown', { key: 'Enter' });
  assert.equal(h.app.state.tracked.size, 0, 'no results: Enter adds nothing');
  h.app.stop();
});

test('Escape still clears everyone (chips gone) and does not double-fire alongside the keydown deselect', async () => {
  const h = await boot();
  const [a, b] = [...h.app.state.contacts.values()];
  h.app.ctx.addPlayer({ guid: a.guid, name: a.name });
  h.app.ctx.addPlayer({ guid: b.guid, name: b.name });
  h.input.value = 'typed text';
  h.input.dispatch('input');
  h.doc.dispatch('keydown', { key: 'Escape' });
  assert.equal(h.app.state.tracked.size, 0, 'Escape clears everyone');
  assert.equal(h.input.value, '', 'typed text is cleared too');
  assert.equal(h.chips().length, 0);
  h.app.stop();
});

test('track colours are stable across re-render: the same guid gets the same colour class every time', async () => {
  const h = await boot();
  const [a, b] = [...h.app.state.contacts.values()];
  h.app.ctx.addPlayer({ guid: a.guid, name: a.name });
  h.app.ctx.addPlayer({ guid: b.guid, name: b.name });
  const swatchClass = (guid) => {
    const chip = h.chips().find((n) => n.textContent.includes(guid === a.guid ? a.name : b.name));
    return chip.children.find((n) => n.className === 'chipBody').children.find((n) => n.className.startsWith('sw')).className;
  };
  const before = { a: swatchClass(a.guid), b: swatchClass(b.guid) };
  // Force a second render (focusPlayer re-renders chips from scratch via the 'selection' bus event).
  h.app.ctx.focusPlayer(a.guid);
  const after = { a: swatchClass(a.guid), b: swatchClass(b.guid) };
  assert.deepEqual(after, before, 'same guid -> same track colour class after a second render');
  assert.equal(before.a, `sw ${trackColorClass(a.guid)}`);
  assert.equal(before.b, `sw ${trackColorClass(b.guid)}`);
  assert.notEqual(before.a, before.b, 'two different guids should not usually collide (demo guids chosen to differ)');
  h.app.stop();
});

test('a malicious player name renders as plain text in a chip, never as markup', async () => {
  const h = await boot();
  const evil = '<img src=x onerror=alert(1)><b>x</b>';
  h.app.ctx.addPlayer({ guid: 'evil-guid', name: evil });
  const chip = h.chips()[0];
  assert.ok(chip, 'a chip was rendered for the malicious name');
  const nm = chip.children.find((n) => n.className === 'chipBody').children.find((n) => n.className === 'nm');
  assert.equal(nm.textContent, evil, 'the raw string is present only as text');
  assert.equal(nm.children.length, 0, 'no child elements were created from the name (textContent, not HTML)');
  h.app.stop();
});
