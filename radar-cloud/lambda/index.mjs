import { DynamoDBClient } from '@aws-sdk/client-dynamodb';
import { DynamoDBDocumentClient, GetCommand, PutCommand, QueryCommand, BatchGetCommand, BatchWriteCommand, TransactWriteCommand, UpdateCommand } from '@aws-sdk/lib-dynamodb';
import { scrypt, randomBytes, createHash, createHmac, timingSafeEqual } from 'node:crypto';
import { promisify } from 'node:util';
import { gzipSync, gunzipSync } from 'node:zlib';

const scryptP = promisify(scrypt);
const T = process.env.TABLE;
const MAXACC = +(process.env.MAX_ACCOUNTS_PER_KEY || 5);
const db = DynamoDBDocumentClient.from(new DynamoDBClient({}), { marshallOptions: { removeUndefinedValues: true } });
const sha = s => createHash('sha256').update(s).digest('hex');
const TOKEN_TTL = 7 * 864e5;
let secret;

const res = (code, obj, ev) => {
  const body = JSON.stringify(obj);
  const gz = ev?.headers?.['accept-encoding']?.includes('gzip') && body.length > 2048;
  return {
    statusCode: code,
    headers: { 'content-type': 'application/json', 'cache-control': 'no-store', ...(gz && { 'content-encoding': 'gzip' }) },
    ...(gz ? { body: gzipSync(body).toString('base64'), isBase64Encoded: true } : { body }),
  };
};

async function getSecret() {
  if (secret) return secret;
  const k = { PK: 'CFG', SK: 'SECRET' };
  let r = (await db.send(new GetCommand({ TableName: T, Key: k }))).Item;
  if (!r) {
    try { await db.send(new PutCommand({ TableName: T, Item: { ...k, v: randomBytes(32).toString('hex') }, ConditionExpression: 'attribute_not_exists(PK)' })); } catch {}
    r = (await db.send(new GetCommand({ TableName: T, Key: k, ConsistentRead: true }))).Item;
  }
  return (secret = r.v);
}
async function sign(username) {
  const p = Buffer.from(JSON.stringify({ u: username, e: Date.now() + TOKEN_TTL })).toString('base64url');
  return p + '.' + createHmac('sha256', await getSecret()).update(p).digest('base64url');
}
async function verify(ev) {
  const h = ev.headers?.authorization || '';
  const [p, s] = h.replace(/^Bearer /i, '').split('.');
  if (!p || !s) return null;
  const want = createHmac('sha256', await getSecret()).update(p).digest('base64url');
  if (s.length !== want.length || !timingSafeEqual(Buffer.from(s), Buffer.from(want))) return null;
  try { const o = JSON.parse(Buffer.from(p, 'base64url').toString()); return o.e > Date.now() ? o.u : null; } catch { return null; }
}

const validCreds = b => b && typeof b.username === 'string' && /^[A-Za-z0-9_.-]{3,32}$/.test(b.username)
  && typeof b.password === 'string' && b.password.length >= 8 && b.password.length <= 128
  && typeof b.apiKey === 'string' && b.apiKey.length >= 8 && b.apiKey.length <= 128;

async function signup(b, ev) {
  if (!validCreds(b)) return res(400, { error: 'Username 3-32 chars (A-Z a-z 0-9 _ . -), password 8+ chars, API key required.' });
  const username = b.username.toLowerCase(), kh = sha(b.apiKey);
  const salt = randomBytes(16);
  const hash = (await scryptP(b.password, salt, 32)).toString('hex');
  try {
    await db.send(new TransactWriteCommand({ TransactItems: [
      { Put: { TableName: T, Item: { PK: 'U#' + username, SK: 'PROFILE', salt: salt.toString('hex'), hash, kh, created: Date.now() }, ConditionExpression: 'attribute_not_exists(PK)' } },
      { Update: { TableName: T, Key: { PK: 'K#' + kh, SK: 'KEY' }, UpdateExpression: 'ADD cnt :one',
          ConditionExpression: 'active = :t AND (attribute_not_exists(cnt) OR cnt < :max)',
          ExpressionAttributeValues: { ':one': 1, ':t': true, ':max': MAXACC } } },
    ] }));
  } catch (e) {
    if (e.name !== 'TransactionCanceledException') throw e;
    const [uRes, kRes] = e.CancellationReasons || [];
    if (uRes?.Code === 'ConditionalCheckFailed') return res(409, { error: 'Username taken.' });
    const k = (await db.send(new GetCommand({ TableName: T, Key: { PK: 'K#' + kh, SK: 'KEY' } }))).Item;
    if (!k || !k.active) return res(403, { error: 'Invalid or inactive API key.' });
    return res(403, { error: `This API key already has ${MAXACC} accounts.` });
  }
  return res(200, { token: await sign(username), username }, ev);
}

async function login(b) {
  if (!b || typeof b.username !== 'string' || typeof b.password !== 'string' || typeof b.apiKey !== 'string')
    return res(400, { error: 'Missing fields.' });
  const username = b.username.toLowerCase().slice(0, 32);
  const u = (await db.send(new GetCommand({ TableName: T, Key: { PK: 'U#' + username, SK: 'PROFILE' } }))).Item;
  const salt = Buffer.from(u?.salt || '00'.repeat(16), 'hex');
  const hash = await scryptP(b.password.slice(0, 128), salt, 32); // always hash: uniform timing
  const kh = sha(b.apiKey);
  const ok = u && timingSafeEqual(hash, Buffer.from(u.hash, 'hex')) && u.kh === kh;
  if (!ok) return res(401, { error: 'Invalid username, password or API key.' });
  const k = (await db.send(new GetCommand({ TableName: T, Key: { PK: 'K#' + kh, SK: 'KEY' } }))).Item;
  if (!k?.active) return res(403, { error: 'API key is not active.' });
  return res(200, { token: await sign(username), username });
}

// ---------------------------------------------------------------- v2 helpers
const RULESETS = ['normal', 'pvp', 'rp', 'rppvp'];
const REACT = new Set(['friendly', 'hostile', 'neutral', 'unknown']);
const GUID_RE = /^Player-[\w-]{1,40}$/;
const FP_RE = /^[\w.-]{1,128}$/;
const MAX_UPLOAD_PLAYERS = 500, MAX_FIXES_PER_PLAYER_REQ = 2000, MAX_FIXES_PER_REQ = 20000;
const CHUNK_FIXES = 300, MAX_TRACK = 5000, MAX_SETTINGS_BYTES = 8192, MAX_PAST = 10;
const POINTS_PER_KB = 10, POINTS_PER_ASSIST = 0;
const MIN_FIX_DIST = 2, MIN_FIX_MS = 5000;
const TS_MAX = 4e12, MAX_BODY = 5.5 * 1024 * 1024;

class Bad extends Error { constructor(m, code = 400) { super(m); this.code = code; } }
const cl = (v, lo, hi, d = 0) => (typeof v === 'number' && Number.isFinite(v) ? Math.min(hi, Math.max(lo, v)) : d);
const int = (v, lo, hi, d = 0) => Math.floor(cl(v, lo, hi, d));
const r1 = v => Math.round(v * 10) / 10;
const str = (v, n) => (typeof v === 'string' ? v.slice(0, n) : '');
const chunk = (a, n) => Array.from({ length: Math.ceil(a.length / n) }, (_, i) => a.slice(i * n, i * n + n));
const sleep = ms => new Promise(r => setTimeout(r, ms));
const pk = (c, user, rs) => `${c}#${user}#${rs}`;

async function getSettings(user) {
  const it = (await db.send(new GetCommand({ TableName: T, Key: { PK: 'U#' + user, SK: 'SETTINGS' } }))).Item;
  return it?.v && typeof it.v === 'object' ? it.v : {};
}
// rs: explicit value is validated; absent -> settings.ruleset -> 'normal'
async function getRs(user, v) {
  if (v === undefined || v === null || v === '') {
    const r = (await getSettings(user)).ruleset;
    return RULESETS.includes(r) ? r : 'normal';
  }
  if (!RULESETS.includes(v)) throw new Bad('rs must be one of ' + RULESETS.join('|'));
  return v;
}

async function queryAll(PK, prefix) {
  const items = [];
  let ExclusiveStartKey;
  do {
    const r = await db.send(new QueryCommand({
      TableName: T, ExclusiveStartKey,
      KeyConditionExpression: prefix ? 'PK = :p AND begins_with(SK, :s)' : 'PK = :p',
      ExpressionAttributeValues: prefix ? { ':p': PK, ':s': prefix } : { ':p': PK },
    }));
    items.push(...r.Items); ExclusiveStartKey = r.LastEvaluatedKey;
  } while (ExclusiveStartKey);
  return items;
}
async function queryEdge(PK, prefix, asc) {
  const r = await db.send(new QueryCommand({
    TableName: T, Limit: 1, ScanIndexForward: asc,
    KeyConditionExpression: 'PK = :p AND begins_with(SK, :s)', ExpressionAttributeValues: { ':p': PK, ':s': prefix },
  }));
  return r.Items[0];
}
async function batchGet(PK, sks) {
  const out = new Map();
  for (const part of chunk(sks, 100)) {
    let req = { [T]: { Keys: part.map(SK => ({ PK, SK })) } };
    for (let a = 0; req && a < 6; a++) {
      const r = await db.send(new BatchGetCommand({ RequestItems: req }));
      for (const i of r.Responses?.[T] || []) out.set(i.SK, i);
      req = r.UnprocessedKeys && Object.keys(r.UnprocessedKeys).length ? r.UnprocessedKeys : null;
      if (req) await sleep(50 * 2 ** a);
    }
    if (req) throw new Error('read throttled');
  }
  return out;
}
async function batchWrite(reqs) {
  for (const part of chunk(reqs, 25)) {
    let req = { [T]: part };
    for (let a = 0; req && a < 6; a++) {
      const r = await db.send(new BatchWriteCommand({ RequestItems: req }));
      req = r.UnprocessedItems && Object.keys(r.UnprocessedItems).length ? r.UnprocessedItems : null;
      if (req) await sleep(50 * 2 ** a);
    }
    if (req) throw new Error('write throttled');
  }
}
const batchPut = items => batchWrite(items.map(Item => ({ PutRequest: { Item } })));
const batchDel = keys => batchWrite(keys.map(k => ({ DeleteRequest: { Key: { PK: k.PK, SK: k.SK } } })));

// ---------------------------------------------------------------- players / merge
const ADD = ['events', 'srcEvents', 'dmgTotal', 'dmgHits', 'dmgEvents', 'hpSum', 'hpSamples', 'kb', 'assists'];
const CMP = ['name', 'reaction', 'level', 'maxHp', 'x', 'y', 'mapId', 'zone', 'posTime', 'firstSeen', 'lastSeen', 'fixes', 'lb', ...ADD];

function parseDelta(d) {
  d = d && typeof d === 'object' ? d : {};
  const D = {
    events: int(d.events, 0, 1e7), srcEvents: int(d.srcEvents, 0, 1e7), dmgHits: int(d.dmgHits, 0, 1e7), dmgEvents: int(d.dmgEvents, 0, 1e7),
    hpSamples: int(d.hpSamples, 0, 1e7), kb: int(d.kb, 0, 1e6), assists: int(d.assists, 0, 1e6),
    dmgTotal: r1(cl(d.dmgTotal, 0, 1e12)), hpSum: r1(cl(d.hpSum, 0, 1e12)), maxHp: r1(cl(d.maxHp, 0, 1e9)),
  };
  D.has = ADD.some(k => D[k] > 0);
  return D;
}

// Idempotent for identical input: merging the same data again yields an equal item (-> no write).
function merge(old, p, D, batchId) {
  const n = { ...(old || {}) };
  for (const k of ADD) n[k] = old?.[k] || 0;
  n.firstSeen = Math.min(old?.firstSeen ?? Infinity, p.firstSeen);
  n.lastSeen = Math.max(old?.lastSeen ?? 0, p.lastSeen);
  n.maxHp = Math.max(old?.maxHp ?? 0, D.maxHp);
  n.level = Math.max(old?.level ?? 0, p.level);
  n.fixes = old?.fixes || 0;
  let past = [...(old?.pastNames || [])];
  const addPast = nm => { if (nm && !past.includes(nm)) past.push(nm); };
  if (!old || p.lastSeen >= old.lastSeen) {
    if (old?.name && p.name && old.name !== p.name) addPast(old.name);
    n.name = p.name || old?.name || '';
    n.reaction = p.reaction === 'unknown' && old?.reaction ? old.reaction : p.reaction;
  } else {
    n.name = old.name || p.name; n.reaction = old.reaction;
    if (p.name && p.name !== n.name) addPast(p.name);
  }
  for (const nm of p.pastNames) addPast(nm);
  past = past.filter(x => x !== n.name).slice(-MAX_PAST);
  if (past.length) n.pastNames = past; else delete n.pastNames;
  if (p.hasPos && (old?.posTime == null || p.posTime >= old.posTime)) {
    n.x = p.x; n.y = p.y; n.mapId = p.mapId; n.zone = p.zone || old?.zone || ''; n.posTime = p.posTime;
  }
  if (D.has) {
    for (const k of ADD) n[k] = k === 'dmgTotal' || k === 'hpSum' ? r1(n[k] + D[k]) : n[k] + D[k];
    n.lb = batchId;
  }
  return n;
}
const same = (a, b) => a && CMP.every(k => a[k] === b[k]) && JSON.stringify(a.pastNames || []) === JSON.stringify(b.pastNames || []);

function parsePlayer(p) {
  if (!p || typeof p.guid !== 'string' || !GUID_RE.test(p.guid)) return null;
  const lastSeen = int(p.lastSeen, 0, TS_MAX);
  const hasPos = [p.x, p.y, p.mapId].every(v => typeof v === 'number' && Number.isFinite(v));
  return {
    guid: p.guid, name: str(p.name, 48), reaction: REACT.has(p.reaction) ? p.reaction : 'unknown', level: int(p.level, 0, 200),
    pastNames: Array.isArray(p.pastNames) ? p.pastNames.slice(0, 20).map(s => str(s, 48)).filter(Boolean) : [],
    hasPos, x: hasPos ? r1(cl(p.x, -1e7, 1e7)) : undefined, y: hasPos ? r1(cl(p.y, -1e7, 1e7)) : undefined,
    mapId: hasPos ? int(p.mapId, 0, 1e6) : undefined, zone: str(p.zone, 64),
    posTime: int(p.posTime, 0, TS_MAX, lastSeen), firstSeen: int(p.firstSeen, 0, TS_MAX, lastSeen), lastSeen,
    D: parseDelta(p.d), fixes: cleanFixes(Array.isArray(p.fixes) ? p.fixes : []),
  };
}

// ---------------------------------------------------------------- tracks
const seqOf = it => +it.SK.slice(it.SK.lastIndexOf('#') + 1);
const sk = (guid, seq) => `${guid}#${String(seq).padStart(6, '0')}`;

// fix = [t, mapId, x, y, kind(0 exact | 1 approx)], chronological, coordinates rounded to 0.1
function cleanFixes(raw) {
  const out = [];
  for (const f of raw.slice(0, MAX_FIXES_PER_PLAYER_REQ)) {
    if (!Array.isArray(f) || f.length < 4) continue;
    const [t, mapId, x, y, kind] = f;
    if (![t, mapId, x, y].every(v => typeof v === 'number' && Number.isFinite(v))) continue;
    out.push([int(t, 0, TS_MAX), int(mapId, 0, 1e6), r1(cl(x, -1e7, 1e7)), r1(cl(y, -1e7, 1e7)), kind === 'approx' || kind === 1 ? 1 : 0]);
  }
  return out.sort((a, b) => a[0] - b[0]);
}
const skipFix = (prev, f) => prev[1] === f[1] && Math.abs(f[0] - prev[0]) < MIN_FIX_MS && Math.hypot(prev[2] - f[2], prev[3] - f[3]) < MIN_FIX_DIST;

// Appends only fixes newer than the stored last fix, deduped (2yd/5s) against the previous fix.
// Only the last chunk is read; returns the chunk items to put.
async function appendTrack(TK, guid, fixes) {
  const last = await queryEdge(TK, guid + '#', false);
  const lastFix = last?.f?.[last.f.length - 1];
  let prev = lastFix || null;
  const acc = [];
  for (const f of fixes) {
    if (lastFix && f[0] <= lastFix[0]) continue;
    if (prev && skipFix(prev, f)) continue;
    acc.push(f); prev = f;
  }
  if (!acc.length) return { puts: [], added: 0 };
  const puts = [];
  let seq = last ? seqOf(last) : 0;
  let cur = last && last.f.length < CHUNK_FIXES ? last.f.slice() : null;
  for (const f of acc) {
    if (!cur) { cur = []; seq++; }
    cur.push(f);
    if (cur.length >= CHUNK_FIXES) { puts.push({ PK: TK, SK: sk(guid, seq), f: cur, n: cur.length }); cur = null; }
  }
  if (cur) puts.push({ PK: TK, SK: sk(guid, seq), f: cur, n: cur.length });
  return { puts, added: acc.length };
}

// ---------------------------------------------------------------- v3: poll support
const grpOf = r => (r === 'friendly' || r === 'hostile' ? r : 'other');
const zkOf = (user, rs, mapId, grp) => `${user}#${rs}#${mapId}#${grp}`;
// ut = server time of last write (poll gate); zk/ls = GSI keys, only while a position is known
function stamp(item, user, rs, now) {
  item.ut = now;
  if (item.mapId != null) { item.zk = zkOf(user, rs, item.mapId, grpOf(item.reaction)); item.ls = now; } else { delete item.zk; delete item.ls; }
  return item;
}
async function bumpVersion(user, rs, now, mapIds) {
  const names = [...mapIds].map(m => 'z' + int(m, 0, 1e6));
  await db.send(new UpdateCommand({
    TableName: T, Key: { PK: pk('V', user, rs), SK: 'VER' },
    UpdateExpression: 'ADD v :one' + (names.length ? ' SET ' + names.map(n => `${n} = :now`).join(', ') : ''),
    ExpressionAttributeValues: { ':one': 1, ...(names.length && { ':now': now }) },
  }));
}

// ---------------------------------------------------------------- endpoints
async function upload(user, b, ev) {
  if (!b || typeof b !== 'object' || !Array.isArray(b.players)) throw new Bad('players[] required.');
  if (b.players.length > MAX_UPLOAD_PLAYERS) throw new Bad(`max ${MAX_UPLOAD_PLAYERS} players per request.`);
  if (b.maps !== undefined && (!Array.isArray(b.maps) || b.maps.length > 300)) throw new Bad('maps[] max 300.');
  if (typeof b.batchId !== 'string' || !/^[\w.:-]{1,64}$/.test(b.batchId)) throw new Bad('batchId required (1-64 chars).');
  if (b.final && (typeof b.fp !== 'string' || !FP_RE.test(b.fp) || typeof b.to !== 'number' || !Number.isFinite(b.to))) throw new Bad('final batch needs fp and to.');
  const rs = await getRs(user, b.rs);
  const PK = pk('P', user, rs), TK = pk('T', user, rs), batchId = b.batchId;

  const players = new Map();
  let totalFixes = 0;
  for (const raw of b.players) {
    const p = parsePlayer(raw);
    if (!p) continue;
    totalFixes += p.fixes.length;
    players.set(p.guid, p); // duplicate guid in one request: last wins
  }
  if (totalFixes > MAX_FIXES_PER_REQ) throw new Bad(`max ${MAX_FIXES_PER_REQ} fixes per request.`);
  const maps = [];
  for (const m of b.maps || []) {
    if ([m?.id, m?.x0, m?.x1, m?.y0, m?.y1].every(v => typeof v === 'number' && Number.isFinite(v)))
      maps.push({ PK, SK: 'M#' + int(m.id, 0, 1e6), name: str(m.name, 64), x0: m.x0, x1: m.x1, y0: m.y0, y1: m.y1 });
  }
  const existing = await batchGet(PK, [...[...players.keys()].map(g => 'G#' + g), ...maps.map(m => m.SK)]);

  const todo = [];
  for (const [guid, p] of players) {
    const old = existing.get('G#' + guid);
    if (old && old.lb === batchId) continue; // replayed batch: already applied to this player
    todo.push({ guid, old, p, n: merge(old, p, p.D, batchId) });
  }
  // tracks: chunks first, player item last (a crashed attempt is retried safely: fixes are watermarked by the stored last fix)
  const withFix = todo.filter(x => x.p.fixes.length);
  const chunkPuts = [];
  for (const grp of chunk(withFix, 10)) {
    await Promise.all(grp.map(async x => {
      const { puts, added } = await appendTrack(TK, x.guid, x.p.fixes);
      chunkPuts.push(...puts); x.n.fixes = (x.old?.fixes || 0) + added;
    }));
  }
  if (chunkPuts.length) await batchPut(chunkPuts);
  for (const x of withFix) { // cap: drop oldest chunks while over the limit
    for (let g = 0; x.n.fixes > MAX_TRACK && g < 50; g++) {
      const oldest = await queryEdge(TK, x.guid + '#', true);
      if (!oldest) break;
      await batchDel([oldest]);
      x.n.fixes = Math.max(0, x.n.fixes - (oldest.n ?? oldest.f?.length ?? 0));
    }
  }
  const now = Date.now(); // server clock: ut/ls and the version item use it (poll `since` is also server time)
  const writes = todo.filter(x => !same(x.old, x.n)).map(x => stamp({ PK, SK: 'G#' + x.guid, ...x.n }, user, rs, now));
  const mapWrites = maps.filter(m => { const o = existing.get(m.SK); return !o || ['name', 'x0', 'x1', 'y0', 'y1'].some(k => o[k] !== m[k]); });
  await batchPut([...writes, ...mapWrites]);
  if (writes.length || mapWrites.length) await bumpVersion(user, rs, now, new Set(writes.filter(w => w.mapId != null).map(w => w.mapId)));

  if (b.final) { // ledger watermark only moves forward, and only after all player writes succeeded
    try {
      await db.send(new UpdateCommand({
        TableName: T, Key: { PK: pk('L', user, rs), SK: b.fp }, UpdateExpression: 'SET lastTs = :to',
        ConditionExpression: 'attribute_not_exists(lastTs) OR lastTs < :to', ExpressionAttributeValues: { ':to': int(b.to, 0, TS_MAX) },
      }));
    } catch (e) { if (e.name !== 'ConditionalCheckFailedException') throw e; }
  }
  return res(200, { received: players.size, changed: writes.length + mapWrites.length }, ev);
}

async function ledger(user, q, ev) {
  const rs = await getRs(user, q.rs);
  if (typeof q.fp !== 'string' || !FP_RE.test(q.fp)) throw new Bad('fp required.');
  const it = (await db.send(new GetCommand({ TableName: T, Key: { PK: pk('L', user, rs), SK: q.fp } }))).Item;
  return res(200, { lastTs: it?.lastTs || 0 }, ev);
}

async function settings(user, method, body, ev) {
  if (method === 'GET') return res(200, await getSettings(user), ev);
  if (!body || typeof body !== 'object' || Array.isArray(body)) throw new Bad('Settings must be a JSON object.');
  if (Buffer.byteLength(JSON.stringify(body)) > MAX_SETTINGS_BYTES) throw new Bad('Settings exceed 8 KB.', 413);
  if (body.ruleset !== undefined && !RULESETS.includes(body.ruleset)) throw new Bad('ruleset must be one of ' + RULESETS.join('|'));
  await db.send(new PutCommand({ TableName: T, Item: { PK: 'U#' + user, SK: 'SETTINGS', v: body, ts: Date.now() } }));
  return res(200, { ok: true }, ev);
}

async function me(user, ev) {
  return res(200, { username: user, settings: await getSettings(user), rulesets: RULESETS }, ev);
}

const row = i => ({
  guid: i.SK.slice(2), name: i.name || '', pastNames: i.pastNames || [], reaction: i.reaction, level: i.level || 0,
  events: i.events || 0, srcEvents: i.srcEvents || 0, dmgTotal: i.dmgTotal || 0, dmgHits: i.dmgHits || 0, dmgEvents: i.dmgEvents || 0,
  hpSum: i.hpSum || 0, hpSamples: i.hpSamples || 0, maxHp: i.maxHp || 0, kb: i.kb || 0, assists: i.assists || 0,
  avgDamagePerHit: i.dmgHits ? r1((i.dmgTotal || 0) / i.dmgHits) : 0,
  avgDamagePerEvent: i.srcEvents ? r1((i.dmgTotal || 0) / i.srcEvents) : 0,
  avgHp: i.hpSamples ? r1((i.hpSum || 0) / i.hpSamples) : 0,
  firstSeen: i.firstSeen, lastSeen: i.lastSeen, lastX: i.x, lastY: i.y, lastMap: i.mapId, lastZone: i.zone, posTime: i.posTime,
  fixes: i.fixes || 0,
});

async function players(user, q, ev, raw) {
  const rs = await getRs(user, q.rs);
  let mapId;
  if (q.mapId !== undefined && q.mapId !== '') { mapId = Number(q.mapId); if (!Number.isInteger(mapId)) throw new Bad('mapId must be an integer.'); }
  if (q.reaction && !REACT.has(q.reaction)) throw new Bad('bad reaction.');
  const needle = typeof q.q === 'string' ? q.q.trim().slice(0, 48).toLowerCase() : '';
  const out = { players: [], maps: [] };
  for (const i of await queryAll(pk('P', user, rs))) {
    if (i.SK.startsWith('M#')) { out.maps.push({ id: +i.SK.slice(2), name: i.name, x0: i.x0, x1: i.x1, y0: i.y0, y1: i.y1 }); continue; }
    if (!i.SK.startsWith('G#')) continue;
    if (mapId !== undefined && i.mapId !== mapId) continue;
    if (q.reaction && i.reaction !== q.reaction) continue;
    if (needle && !(i.name || '').toLowerCase().includes(needle) && !(i.pastNames || []).some(n => n.toLowerCase().includes(needle))) continue;
    out.players.push(row(i));
  }
  return raw ? out : res(200, out, ev);
}

const GRPS = ['friendly', 'hostile', 'other'];
const zoneRow = i => ({
  guid: i.SK.slice(2), name: i.name || '', reaction: i.reaction, level: i.level || 0, maxHp: i.maxHp || 0,
  firstSeen: i.firstSeen, lastSeen: i.lastSeen, lastX: i.x, lastY: i.y, lastMap: i.mapId, lastZone: i.zone, posTime: i.posTime, ls: i.ls,
});

// GET /api/poll: cheapest-first change detection (player: 1 GetItem; zone/all: 1 version GetItem, then queries only if changed)
async function poll(user, q, ev) {
  const now = Date.now();
  const rs = await getRs(user, q.rs);
  if (!['player', 'zone', 'all'].includes(q.scope)) throw new Bad('scope must be player|zone|all.');
  const num = (v, lo, hi, name) => {
    if (v === undefined || v === '') return 0;
    const x = Number(v); if (!Number.isFinite(x)) throw new Bad(name + ' must be a number.');
    return Math.floor(Math.min(hi, Math.max(lo, x)));
  };
  const since = num(q.since, 0, now, 'since'), cv = q.v === undefined || q.v === '' ? null : num(q.v, 0, 1e15, 'v');
  const PK = pk('P', user, rs);
  if (q.scope === 'player') {
    if (typeof q.guid !== 'string' || !GUID_RE.test(q.guid)) throw new Bad('guid required.');
    const it = (await db.send(new GetCommand({ TableName: T, Key: { PK, SK: 'G#' + q.guid }, ConsistentRead: false }))).Item;
    if (!it || (since > 0 && !(it.ut > since))) return res(200, { changed: false, v: cv ?? 0, now }, ev);
    const out = { changed: true, v: cv ?? 0, now, player: row(it) };
    if (q.track === '1') {
      const tsince = num(q.tsince, 0, TS_MAX, 'tsince');
      const last = await db.send(new QueryCommand({
        TableName: T, Limit: 1, ScanIndexForward: false, ConsistentRead: false,
        KeyConditionExpression: 'PK = :p AND begins_with(SK, :s)', ExpressionAttributeValues: { ':p': pk('T', user, rs), ':s': q.guid + '#' },
      }));
      out.fixes = (last.Items[0]?.f || []).filter(f => f[0] > tsince).map(([t, mapId, x, y, k]) => ({ t, mapId, zone: it.mapId === mapId ? it.zone || '' : '', x, y, kind: k ? 'approx' : 'exact' }));
    }
    return res(200, out, ev);
  }
  let mapId;
  if (q.scope === 'zone') {
    mapId = Number(q.mapId);
    if (q.mapId === undefined || q.mapId === '' || !Number.isInteger(mapId) || mapId < 0 || mapId > 1e6) throw new Bad('mapId required.');
    if (q.grp !== undefined && q.grp !== '' && ![...GRPS, 'any'].includes(q.grp)) throw new Bad('grp must be friendly|hostile|other|any.');
  }
  const zf = mapId === undefined ? null : 'z' + mapId;
  const ver = (await db.send(new GetCommand({
    TableName: T, Key: { PK: pk('V', user, rs), SK: 'VER' }, ConsistentRead: false, ProjectionExpression: zf ? `v, ${zf}` : 'v',
  }))).Item || {};
  const v = ver.v || 0;
  if (q.scope === 'all') {
    if (since > 0 && cv !== null && cv === v) return res(200, { changed: false, v, now }, ev);
    const out = await players(user, { rs }, null, true);
    return res(200, { changed: true, v, now, ...out }, ev);
  }
  const z = ver[zf];
  if (since > 0 && !(z > since)) return res(200, { changed: false, v, now }, ev);
  const grps = !q.grp || q.grp === 'any' ? GRPS : [q.grp];
  const rows = [];
  if (z !== undefined) for (const g of grps) {
    let ExclusiveStartKey;
    do {
      const r = await db.send(new QueryCommand({
        TableName: T, IndexName: 'zone-idx', ConsistentRead: false, ExclusiveStartKey,
        KeyConditionExpression: 'zk = :k AND ls > :s', ExpressionAttributeValues: { ':k': zkOf(user, rs, mapId, g), ':s': since },
      }));
      rows.push(...r.Items.map(zoneRow)); ExclusiveStartKey = r.Items.length < 5000 ? r.LastEvaluatedKey : undefined;
    } while (ExclusiveStartKey);
  }
  return res(200, { changed: true, v, now, players: rows }, ev);
}

async function track(user, q, ev) {
  const rs = await getRs(user, q.rs);
  if (typeof q.guid !== 'string' || !GUID_RE.test(q.guid)) throw new Bad('guid required.');
  let only;
  if (q.map !== undefined && q.map !== '') { only = Number(q.map); if (!Number.isInteger(only)) throw new Bad('map must be an integer.'); }
  const PK = pk('P', user, rs);
  const [pl, chunks, mapItems] = await Promise.all([
    db.send(new GetCommand({ TableName: T, Key: { PK, SK: 'G#' + q.guid } })).then(r => r.Item),
    queryAll(pk('T', user, rs), q.guid + '#'),
    queryAll(PK, 'M#'),
  ]);
  const names = new Map(mapItems.map(m => [+m.SK.slice(2), m.name]));
  const counts = new Map(), fixes = [];
  let total = 0;
  for (const c of chunks) for (const [t, mapId, x, y, k] of c.f) {
    total++; counts.set(mapId, (counts.get(mapId) || 0) + 1);
    if (only === undefined || mapId === only) fixes.push({ t, mapId, zone: names.get(mapId) || (pl?.mapId === mapId ? pl.zone : '') || '', x, y, kind: k ? 'approx' : 'exact' });
  }
  const maps = [...counts].sort((a, b) => a[0] - b[0]).map(([mapId, count]) => ({ mapId, zone: names.get(mapId) || '', count }));
  return res(200, { guid: q.guid, name: pl?.name || '', reaction: pl?.reaction || 'unknown', total, maps, fixes }, ev);
}

// ranking as Go bounty store: killing blows, assists, most recent activity, guid
function bountyBoard(items) {
  const list = items.filter(i => i.SK.startsWith('G#') && (i.kb > 0 || i.assists > 0)).map(i => ({
    guid: i.SK.slice(2), name: i.name || '', kb: i.kb || 0, assists: i.assists || 0,
    score: (i.kb || 0) * POINTS_PER_KB + (i.assists || 0) * POINTS_PER_ASSIST, last: i.lastSeen || 0,
  }));
  list.sort((a, b) => b.kb - a.kb || b.assists - a.assists || b.last - a.last || (a.guid < b.guid ? -1 : a.guid > b.guid ? 1 : 0));
  return list.map(({ last, ...e }, idx) => ({ ...e, rank: idx + 1 }));
}
async function bounty(user, q, ev) {
  const rs = await getRs(user, q.rs);
  return res(200, { entries: bountyBoard(await queryAll(pk('P', user, rs), 'G#')) }, ev);
}
async function bountyPlayer(user, q, ev) {
  const rs = await getRs(user, q.rs);
  if (typeof q.guid !== 'string' || !GUID_RE.test(q.guid)) throw new Bad('guid required.');
  const e = bountyBoard(await queryAll(pk('P', user, rs), 'G#')).find(x => x.guid === q.guid);
  if (!e) return res(404, { error: 'Player is not on the bounty board.' }, ev);
  return res(200, e, ev);
}

async function reset(user, b, ev) {
  if (!b || !['bounty', 'all'].includes(b.what)) throw new Bad("what must be 'bounty' or 'all'.");
  const rs = await getRs(user, b.rs);
  const players = await queryAll(pk('P', user, rs), 'G#');
  if (b.what === 'all') {
    await batchDel(players);
    await batchDel(await queryAll(pk('T', user, rs)));
  } else {
    await batchPut(players.filter(i => i.kb || i.assists).map(i => ({ ...i, kb: 0, assists: 0 })));
    await bumpVersion(user, rs, Date.now(), new Set());
  }
  if (b.what === 'all') await batchDel([{ PK: pk('V', user, rs), SK: 'VER' }]);
  await batchDel(await queryAll(pk('L', user, rs)));
  return res(200, { ok: true, what: b.what, rs }, ev);
}

export const handler = async ev => {
  try {
    const method = ev.requestContext?.http?.method, path = ev.rawPath;
    const q = ev.queryStringParameters || {};
    let body;
    if (method === 'POST' || method === 'PUT') {
      let buf = ev.isBase64Encoded ? Buffer.from(ev.body || '', 'base64') : Buffer.from(ev.body || '');
      if (buf.length > MAX_BODY) return res(413, { error: 'Body too large.' });
      if (/gzip/i.test(ev.headers?.['content-encoding'] || '')) { try { buf = gunzipSync(buf, { maxOutputLength: 32 * 1024 * 1024 }); } catch { return res(400, { error: 'Bad gzip.' }); } }
      try { body = JSON.parse(buf.toString()); } catch { return res(400, { error: 'Bad JSON.' }); }
    }
    if (method === 'POST' && path === '/api/signup') return await signup(body, ev);
    if (method === 'POST' && path === '/api/login') return await login(body);
    const user = await verify(ev);
    if (!user) return res(401, { error: 'Not logged in.' });
    if (method === 'GET') {
      if (path === '/api/me') return await me(user, ev);
      if (path === '/api/settings') return await settings(user, 'GET', null, ev);
      if (path === '/api/ledger') return await ledger(user, q, ev);
      if (path === '/api/players') return await players(user, q, ev);
      if (path === '/api/poll') return await poll(user, q, ev);
      if (path === '/api/track') return await track(user, q, ev);
      if (path === '/api/bounty') return await bounty(user, q, ev);
      if (path === '/api/bounty/player') return await bountyPlayer(user, q, ev);
    } else if (method === 'PUT' && path === '/api/settings') return await settings(user, 'PUT', body, ev);
    else if (method === 'POST' && path === '/api/upload') return await upload(user, body, ev);
    else if (method === 'POST' && path === '/api/reset') return await reset(user, body, ev);
    return res(404, { error: 'Not found.' });
  } catch (e) {
    if (e instanceof Bad) return res(e.code, { error: e.message });
    console.error(e);
    return res(500, { error: 'Server error.' });
  }
};
