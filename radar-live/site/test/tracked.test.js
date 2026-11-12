// Stage 11 U1: state.tracked (ordered Map<guid,{guid,name}>) + derived state.player getter, and the
// addPlayer/removePlayer/clearPlayers/focusPlayer/setPlayer/deselect ctx API. No rendering here; see
// deselect.test.js for the single-select UI behaviour this must not regress.
import test from 'node:test';
import assert from 'node:assert/strict';
import { createApp } from '../js/core.js';
import { loaders } from '../js/features.js';
import { demoApi } from '../dev-demo.mjs';
import { createFakeDocument } from './fake-dom.js';
import { createFakeTimers } from './fake-timers.js';
import { CONFIG } from '../js/config.js';

const T0 = 1_760_000_000_000;
const settle = async (n = 6) => { for (let i = 0; i < n; i++) await new Promise((r) => setImmediate(r)); };

/** The default fake backend: answers via demoApi, same contract as dev-server.mjs. */
function demoFetch(url, now) {
  const u = new URL(url, 'http://x');
  if (!u.pathname.startsWith('/api/')) return { ok: false, status: 404, json: async () => null };
  const out = demoApi(url, now);
  return { ok: out.status < 400, status: out.status, json: async () => out.body };
}

/** Same boot as boot(), but with a swappable fetch so tests can inject failures for one request. */
async function bootTracked(customFetch = demoFetch) {
  const features = await Promise.all(loaders.map(async (l) => (await l()).default));
  const timers = createFakeTimers(T0);
  const doc = createFakeDocument();
  const requests = [];
  const env = {
    document: doc, window: null, timers, now: timers.now, features, storage: null, logs: [],
    log: (m, e) => env.logs.push([m, e && e.message]),
    fetch: async (url) => {
      requests.push(url);
      return customFetch(url, timers.now());
    },
  };
  const app = createApp(env);
  app.start();
  await timers.advance(0); await settle();
  return { app, env, timers, doc, requests };
}

async function boot() {
  return bootTracked(demoFetch);
}

/** Listens to 'selection' and returns a snapshot getter so tests can assert fire counts cheaply. */
function spy(app) {
  const fires = [];
  app.bus.on('selection', () => fires.push(app.state.tracked.size));
  return fires;
}

const P = (guid, name = guid) => ({ guid, name });

test('addPlayer: tracks and focuses; state.player derives the most recently added', async () => {
  const { app } = await boot();
  const fires = spy(app);
  app.ctx.addPlayer(P('a'));
  assert.equal(app.state.tracked.size, 1);
  assert.deepEqual(app.state.player, { guid: 'a', name: 'a' });
  app.ctx.addPlayer(P('b'));
  assert.equal(app.state.tracked.size, 2);
  assert.deepEqual(app.state.player, { guid: 'b', name: 'b' });
  assert.deepEqual([...app.state.tracked.keys()], ['a', 'b']);
  assert.equal(fires.length, 2);
  app.stop();
});

test('addPlayer: re-adding an already-tracked guid re-focuses it instead of duplicating', async () => {
  const { app } = await boot();
  app.ctx.addPlayer(P('a'));
  app.ctx.addPlayer(P('b'));
  app.ctx.addPlayer(P('a', 'A renamed'));
  assert.equal(app.state.tracked.size, 2);
  assert.deepEqual([...app.state.tracked.keys()], ['b', 'a']);
  assert.deepEqual(app.state.player, { guid: 'a', name: 'A renamed' });
  app.stop();
});

test('addPlayer: invalid input (no guid) is ignored, no event', async () => {
  const { app } = await boot();
  const fires = spy(app);
  app.ctx.addPlayer(null);
  app.ctx.addPlayer({});
  assert.equal(app.state.tracked.size, 0);
  assert.equal(fires.length, 0);
  app.stop();
});

test('TRACK_MAX cap: adding past the cap drops the oldest, not the newest', async () => {
  const { app } = await boot();
  assert.equal(CONFIG.TRACK_MAX, 8);
  for (let i = 0; i < CONFIG.TRACK_MAX; i++) app.ctx.addPlayer(P(`p${i}`));
  assert.equal(app.state.tracked.size, CONFIG.TRACK_MAX);
  app.ctx.addPlayer(P('newcomer'));
  assert.equal(app.state.tracked.size, CONFIG.TRACK_MAX, 'still capped');
  assert.equal(app.state.tracked.has('p0'), false, 'oldest evicted');
  assert.equal(app.state.tracked.has('newcomer'), true);
  assert.deepEqual(app.state.player, { guid: 'newcomer', name: 'newcomer' });
  app.stop();
});

test('removePlayer: drops one guid; focus falls back to the next most-recent, or null when empty', async () => {
  const { app } = await boot();
  const fires = spy(app);
  app.ctx.addPlayer(P('a'));
  app.ctx.addPlayer(P('b'));
  app.ctx.addPlayer(P('c'));
  app.ctx.removePlayer('c');
  assert.equal(app.state.tracked.size, 2);
  assert.deepEqual(app.state.player, { guid: 'b', name: 'b' }, 'falls back to next most-recent');
  app.ctx.removePlayer('a');
  assert.equal(app.state.tracked.size, 1);
  assert.deepEqual(app.state.player, { guid: 'b', name: 'b' }, 'unaffected removal of a non-focused guid');
  app.ctx.removePlayer('b');
  assert.equal(app.state.tracked.size, 0);
  assert.equal(app.state.player, null);
  assert.equal(fires.length, 6, '3 adds + 3 removes');
  app.stop();
});

test('removePlayer: no-op (no event) for an untracked guid', async () => {
  const { app } = await boot();
  app.ctx.addPlayer(P('a'));
  const fires = spy(app);
  app.ctx.removePlayer('nope');
  assert.equal(fires.length, 0);
  assert.equal(app.state.tracked.size, 1);
  app.stop();
});

test('focusPlayer: moves an already-tracked guid to focused without adding/removing anyone', async () => {
  const { app } = await boot();
  app.ctx.addPlayer(P('a'));
  app.ctx.addPlayer(P('b'));
  app.ctx.addPlayer(P('c'));
  const fires = spy(app);
  app.ctx.focusPlayer('a');
  assert.equal(app.state.tracked.size, 3);
  assert.deepEqual(app.state.player, { guid: 'a', name: 'a' });
  assert.deepEqual([...app.state.tracked.keys()], ['b', 'c', 'a']);
  assert.equal(fires.length, 1);
  app.stop();
});

test('focusPlayer: no-op (no event) for a guid that is not tracked', async () => {
  const { app } = await boot();
  app.ctx.addPlayer(P('a'));
  const fires = spy(app);
  app.ctx.focusPlayer('nope');
  assert.equal(fires.length, 0);
  assert.deepEqual(app.state.player, { guid: 'a', name: 'a' });
  app.stop();
});

test('clearPlayers: empties the set and always emits selection, even if already empty', async () => {
  const { app } = await boot();
  app.ctx.addPlayer(P('a'));
  app.ctx.addPlayer(P('b'));
  const fires = spy(app);
  app.ctx.clearPlayers();
  assert.equal(app.state.tracked.size, 0);
  assert.equal(app.state.player, null);
  assert.equal(fires.length, 1);
  app.ctx.clearPlayers();
  assert.equal(fires.length, 2, 'emits again even though nothing was tracked');
  app.stop();
});

test('selection fires exactly once per addPlayer/removePlayer/focusPlayer/clearPlayers/setPlayer call', async () => {
  const { app } = await boot();
  const fires = spy(app);
  app.ctx.addPlayer(P('a'));
  app.ctx.addPlayer(P('b'));
  app.ctx.focusPlayer('a');
  app.ctx.removePlayer('b');
  app.ctx.clearPlayers();
  app.ctx.setPlayer(P('c'));
  app.ctx.setPlayer(null);
  assert.equal(fires.length, 7);
  app.stop();
});

test('setPlayer: backward-compatible single-select - a player clears everyone else and tracks just that one', async () => {
  const { app } = await boot();
  app.ctx.addPlayer(P('a'));
  app.ctx.addPlayer(P('b'));
  const fires = spy(app);
  app.ctx.setPlayer({ guid: 'c', name: 'C' });
  assert.equal(app.state.tracked.size, 1);
  assert.deepEqual(app.state.player, { guid: 'c', name: 'C' });
  assert.equal(fires.length, 1, 'one selection event, not clear+add');
  app.stop();
});

test('setPlayer(null): clears everyone and emits, matching pre-U1 single-select semantics', async () => {
  const { app } = await boot();
  const seen = [];
  app.bus.on('selection', () => seen.push(app.state.player));
  const c = [...app.state.contacts.values()][0];
  app.ctx.setPlayer({ guid: c.guid, name: c.name });
  app.ctx.setPlayer(null);
  assert.equal(seen.length, 2);
  assert.equal(seen[1], null);
  assert.equal(app.state.tracked.size, 0);
  app.stop();
});

test('deselect: clears everyone tracked (same as clearPlayers), no-op when nothing is tracked', async () => {
  const { app } = await boot();
  const fires = spy(app);
  app.ctx.deselect();
  assert.equal(fires.length, 0, 'no-op: nothing tracked');
  app.ctx.addPlayer(P('a'));
  app.ctx.addPlayer(P('b'));
  app.ctx.deselect();
  assert.equal(app.state.tracked.size, 0);
  assert.equal(app.state.player, null);
  assert.equal(fires.length, 3, 'addPlayer x2 + deselect x1');
  app.stop();
});

test('state.player is read-only: assigning to it throws (strict ES module), tracked stays the source of truth', async () => {
  const { app } = await boot();
  app.ctx.addPlayer(P('a'));
  assert.throws(() => { app.state.player = null; });
  assert.deepEqual(app.state.player, { guid: 'a', name: 'a' });
  app.stop();
});

test('sectorOf still derives scope=player from a single tracked player (U1 must not change single-select sweep behaviour)', async () => {
  const { app, timers, requests } = await boot();
  const guid = [...app.state.contacts.keys()][0];
  app.ctx.addPlayer({ guid, name: 'x' });
  await timers.advance(300); await settle();
  const last = requests.filter((u) => u.startsWith('/api/v1/sweep')).map((u) => new URL(u, 'http://x').searchParams).at(-1);
  assert.equal(last.get('scope'), 'player');
  assert.equal(last.get('guid'), guid);
  app.stop();
});

// Stage 11 U2: roster sweep (scope=zone|all) vs tracked sweeps (scope=player per guid), run in the same
// cycle from core.js's runSweep. See docs/plans/stage-11-radar-ux.md section 3.2.

test('roster sweep is unaffected by tracked-player changes - the core U2 regression this task fixes', async () => {
  const { app, timers } = await boot();
  const before = new Set(app.state.contacts.keys());
  assert.ok(before.size > 1, 'the demo roster has more than one contact to begin with');
  const guid = [...before][0];
  app.ctx.addPlayer({ guid, name: 'x' });
  await timers.advance(300); await settle();
  for (const g of before) assert.equal(app.state.contacts.has(g), true, `roster still has ${g} after tracking one player`);
  app.stop();
});

test('tracked stream populates state.trackedData with {contact, trail} per guid', async () => {
  const { app, timers } = await boot();
  const [a, b] = [...app.state.contacts.keys()];
  app.ctx.addPlayer({ guid: a, name: a });
  app.ctx.addPlayer({ guid: b, name: b });
  await timers.advance(300); await settle();
  for (const g of [a, b]) {
    const d = app.state.trackedData.get(g);
    assert.ok(d, `trackedData has an entry for ${g}`);
    assert.equal(d.contact.guid, g);
    assert.ok(Array.isArray(d.trail));
  }
  app.stop();
});

test('a tracked player outside the roster zone still lands in state.contacts via the per-guid merge', async () => {
  const { app, timers } = await boot();
  // CHARTS[0] = Dun Morogh (mapId 27), CHARTS[1] = Elwynn Forest (mapId 37); demo contacts alternate.
  const outside = [...app.state.contacts.values()].find((c) => c.mapId === 37);
  assert.ok(outside, 'demo data has a contact outside mapId 27');
  app.ctx.setZone({ mapId: 27, name: 'Dun Morogh' });
  await timers.advance(300); await settle();
  assert.equal(app.state.contacts.has(outside.guid), false, 'the zone-scoped roster sweep excludes it');
  app.ctx.addPlayer({ guid: outside.guid, name: outside.name });
  await timers.advance(300); await settle();
  assert.equal(app.state.contacts.has(outside.guid), true, 'the tracked stream merges its own contact back in');
  assert.ok(app.state.trackedData.has(outside.guid));
  app.stop();
});

test('W6: setWorldView(true) widens the roster sweep back to all zones, even with a zone still selected', async () => {
  const { app, timers } = await boot();
  const outside = [...app.state.contacts.values()].find((c) => c.mapId === 37);
  assert.ok(outside, 'demo data has a contact outside mapId 27');
  app.ctx.setZone({ mapId: 27, name: 'Dun Morogh' });
  await timers.advance(300); await settle();
  assert.equal(app.state.contacts.has(outside.guid), false, 'zone view: roster sweep is zone-scoped, as before');
  app.ctx.setWorldView(true);
  await timers.advance(300); await settle();
  assert.equal(app.state.contacts.has(outside.guid), true, 'world view: roster sweep covers every zone');
  app.ctx.setWorldView(false);
  await timers.advance(300); await settle();
  assert.equal(app.state.zone.mapId, 27, 'toggling world view off leaves the selected zone untouched');
  app.stop();
});

test('removePlayer prunes trackedData immediately, without waiting for the next sweep', async () => {
  const { app, timers } = await boot();
  const guid = [...app.state.contacts.keys()][0];
  app.ctx.addPlayer({ guid, name: guid });
  await timers.advance(300); await settle();
  assert.ok(app.state.trackedData.has(guid));
  app.ctx.removePlayer(guid);
  assert.equal(app.state.trackedData.has(guid), false);
  app.stop();
});

test('clearPlayers prunes every trackedData entry immediately', async () => {
  const { app, timers } = await boot();
  const [a, b] = [...app.state.contacts.keys()];
  app.ctx.addPlayer({ guid: a, name: a });
  app.ctx.addPlayer({ guid: b, name: b });
  await timers.advance(300); await settle();
  assert.equal(app.state.trackedData.size, 2);
  app.ctx.clearPlayers();
  assert.equal(app.state.trackedData.size, 0);
  app.stop();
});

test('one failing tracked request is logged and skipped, without dropping the roster or the other tracked guids', async () => {
  const bad = 'Player-does-not-exist';
  const { app, timers, env } = await bootTracked((url, now) => {
    const u = new URL(url, 'http://x');
    if (u.pathname === '/api/v1/sweep' && u.searchParams.get('scope') === 'player' && u.searchParams.get('guid') === bad) {
      throw new Error('network blip');
    }
    return demoFetch(url, now);
  });
  const rosterGuid = [...app.state.contacts.keys()][0];
  const otherGuid = [...app.state.contacts.keys()][1];
  app.ctx.addPlayer({ guid: bad, name: 'Ghost' });
  app.ctx.addPlayer({ guid: otherGuid, name: otherGuid });
  await timers.advance(300); await settle();
  assert.equal(app.state.contacts.has(rosterGuid), true, 'roster intact despite one tracked failure');
  assert.equal(app.state.trackedData.has(otherGuid), true, 'the other tracked guid still got its data');
  assert.equal(app.state.trackedData.has(bad), false, 'the failing guid has no stale/partial data');
  assert.ok(env.logs.some(([m]) => typeof m === 'string' && m.includes('tracked sweep failed')), 'the failure was logged');
  app.stop();
});

test('all TRACK_MAX tracked guids are requested within one scheduler cycle', async () => {
  const { app, timers, requests } = await boot();
  const guids = [...app.state.contacts.keys()].slice(0, CONFIG.TRACK_MAX);
  assert.equal(guids.length, CONFIG.TRACK_MAX, 'the demo roster has enough contacts to fill TRACK_MAX');
  for (const g of guids) app.ctx.addPlayer({ guid: g, name: g });
  const before = requests.length;
  await timers.advance(300); await settle();
  const playerGuids = requests.slice(before)
    .filter((u) => u.startsWith('/api/v1/sweep'))
    .map((u) => new URL(u, 'http://x').searchParams)
    .filter((p) => p.get('scope') === 'player')
    .map((p) => p.get('guid'));
  assert.equal(new Set(playerGuids).size, CONFIG.TRACK_MAX);
  for (const g of guids) assert.ok(playerGuids.includes(g), `guid ${g} was requested`);
  app.stop();
});
