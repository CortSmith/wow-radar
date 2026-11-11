// Local imitation of production: CloudFront (static S3 site + /api/* -> Lambda Function URL) in one process.
// Real site/ files, REAL lambda/index.mjs, in-memory DynamoDB stub (test/stub-lib.mjs) with optional persistence.
//   node dev/server.mjs [--port 8787] [--latency 30] [--demo] [--sim] [--fresh]
import http from 'node:http';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { register } from 'node:module';
import { gzipSync, gunzipSync } from 'node:zlib';
import { createHash } from 'node:crypto';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const flag = n => process.argv.includes('--' + n);
const val = (n, d) => { const i = process.argv.indexOf('--' + n); return i < 0 ? d : process.argv[i + 1]; };
const PORT = +val('port', 8787), LATENCY = +val('latency', 30);
const DATA = path.join(root, 'dev', '.data.json');

process.env.TABLE = 'LocalTable';
process.env.MAX_ACCOUNTS_PER_KEY = '5';
register(pathToFileURL(path.join(root, 'test', 'hooks.mjs')).href, import.meta.url);
const stub = await import(new URL('../test/stub-lib.mjs', import.meta.url).href);
const { handler } = await import(new URL('../lambda/index.mjs', import.meta.url).href);
const sha = s => createHash('sha256').update(s).digest('hex');

// ---- persistence of the in-memory table ----
if (!flag('fresh') && fs.existsSync(DATA)) {
  const j = JSON.parse(fs.readFileSync(DATA, 'utf8'));
  for (const [pk, rows] of Object.entries(j)) stub.tables.set(pk, new Map(Object.entries(rows)));
  console.log('loaded', DATA);
}
const save = () => fs.writeFileSync(DATA, JSON.stringify(Object.fromEntries([...stub.tables].map(([pk, m]) => [pk, Object.fromEntries(m)]))));
setInterval(save, 10000).unref();
for (const s of ['SIGINT', 'SIGTERM']) process.on(s, () => { save(); process.exit(0); });

const KEY = 'dev-key-0001', DEAD = 'dev-inactive-key';
if (!stub.tables.get('K#' + sha(KEY))) {
  stub.seedKey(sha(KEY), { active: true, cnt: 0 });
  stub.seedKey(sha(DEAD), { active: false, cnt: 0 });
}

// ---- Function-URL-style (payload v2) invocation ----
async function invoke(req, body) {
  const u = new URL(req.url, 'http://x');
  const headers = Object.fromEntries(Object.entries(req.headers).map(([k, v]) => [k.toLowerCase(), String(v)]));
  delete headers.host; // CloudFront ALL_VIEWER_EXCEPT_HOST_HEADER
  return handler({
    version: '2.0', rawPath: u.pathname, rawQueryString: u.search.slice(1), headers,
    queryStringParameters: u.searchParams.size ? Object.fromEntries(u.searchParams) : undefined,
    requestContext: { http: { method: req.method, path: u.pathname, sourceIp: '127.0.0.1' } },
    body: body.length ? body.toString('utf8') : undefined, isBase64Encoded: false,
  });
}

// ---- static (private S3 + OAC behaviour) ----
const MIME = { '.html': 'text/html; charset=utf-8', '.js': 'text/javascript', '.css': 'text/css', '.json': 'application/json', '.svg': 'image/svg+xml', '.webp': 'image/webp', '.png': 'image/png', '.ico': 'image/x-icon' };
const siteDir = path.join(root, 'site');
function serveStatic(req, res) {
  let p = decodeURIComponent(new URL(req.url, 'http://x').pathname);
  if (p === '/') p = '/index.html';
  const f = path.normalize(path.join(siteDir, p));
  if (!f.startsWith(siteDir) || !fs.existsSync(f) || fs.statSync(f).isDirectory()) {
    res.writeHead(403, { 'content-type': 'application/xml' }); // S3 + OAC answers 403 for missing keys
    return res.end('<Error><Code>AccessDenied</Code></Error>');
  }
  const ext = path.extname(f), buf = fs.readFileSync(f);
  const h = { 'content-type': MIME[ext] || 'application/octet-stream', etag: '"' + sha(buf.toString('latin1')).slice(0, 16) + '"', 'cache-control': ext === '.webp' ? 'public, max-age=31536000, immutable' : 'public, max-age=0, must-revalidate' };
  if (req.headers['if-none-match'] === h.etag) { res.writeHead(304, h); return res.end(); }
  const gz = /text|javascript|json|svg/.test(h['content-type']) && /gzip/.test(req.headers['accept-encoding'] || '');
  if (gz) h['content-encoding'] = 'gzip';
  res.writeHead(200, h);
  res.end(gz ? gzipSync(buf) : buf);
}

const sleep = ms => new Promise(r => setTimeout(r, ms));
const LIMIT = 6 * 1024 * 1024;
http.createServer(async (req, res) => {
  const t0 = Date.now();
  if (!req.url.startsWith('/api/')) return serveStatic(req, res);
  const chunks = []; let size = 0;
  for await (const c of req) { size += c.length; if (size <= LIMIT) chunks.push(c); }
  if (size > LIMIT) { res.writeHead(413, { 'content-type': 'application/json' }); return res.end('{"message":"Request Entity Too Large"}'); } // Function URL 6MB cap
  await sleep(LATENCY);
  const r0 = stub.stats.reads, w0 = stub.stats.writes;
  const out = await invoke(req, Buffer.concat(chunks));
  const body = out.isBase64Encoded ? Buffer.from(out.body, 'base64') : Buffer.from(out.body || '');
  res.writeHead(out.statusCode, { ...out.headers, 'content-length': body.length });
  res.end(body);
  const u = new URL(req.url, 'http://x');
  const q = [...u.searchParams].filter(([k]) => ['scope', 'mapId', 'grp', 'guid', 'since'].includes(k)).map(([k, v]) => k + '=' + v).join(' ');
  console.log(`${req.method.padEnd(4)} ${u.pathname.padEnd(18)} ${out.statusCode} ${String(Date.now() - t0).padStart(4)}ms  reads=${stub.stats.reads - r0} writes=${stub.stats.writes - w0}  ${q}`);
}).listen(PORT, '127.0.0.1', async () => {
  console.log(`\nradar-cloud LOCAL  http://127.0.0.1:${PORT}   (simulated latency ${LATENCY}ms)`);
  console.log(`  active API key : ${KEY}   (inactive test key: ${DEAD})`);
  if (flag('demo') || flag('sim')) {
    await demo();
    console.log('  demo account   : demo / demo-password-1   key ' + KEY);
  }
});

// ---- demo data + live simulator (stands in for another session uploading) ----
async function call(method, p, { q, body, token } = {}) {
  const r = await handler({
    requestContext: { http: { method } }, rawPath: p, queryStringParameters: q,
    headers: token ? { authorization: 'Bearer ' + token } : {}, body: body ? JSON.stringify(body) : undefined,
  });
  const text = r.isBase64Encoded ? gunzipSync(Buffer.from(r.body, 'base64')).toString() : r.body;
  return { code: r.statusCode, json: JSON.parse(text) };
}
const MAPS = [{ id: 1413, name: 'Barrens', x0: -5000, x1: 5000, y0: -5000, y1: 5000 }, { id: 1429, name: 'Elwynn', x0: -5000, x1: 5000, y0: -5000, y1: 5000 }];
async function demo() {
  let r = await call('POST', '/api/login', { body: { username: 'demo', password: 'demo-password-1', apiKey: KEY } });
  if (r.code !== 200) r = await call('POST', '/api/signup', { body: { username: 'demo', password: 'demo-password-1', apiKey: KEY } });
  const token = r.json.token;
  if (!token) return console.log('demo login failed', r.json);
  const names = ['Arthas', 'Jaina', 'Thrall', 'Sylvanas', 'Illidan', 'Tyrande', 'Malfurion', 'Garrosh', 'Varian', 'Anduin', 'Voljin', 'Baine', 'Cairne', 'Rexxar', 'Uther', 'Falstad'];
  const now0 = Date.now(), players = []; let seq = 0;
  for (let i = 0; i < 48; i++) {
    const m = MAPS[i % 2];
    players.push({
      guid: 'Player-1-' + String(i + 1).padStart(8, '0'), name: names[i % names.length] + (i >= names.length ? i : ''),
      reaction: i % 3 === 0 ? 'friendly' : 'hostile', level: 40 + (i % 21), maxHp: 2000 + i * 40,
      x: (Math.random() - 0.5) * 8000, y: (Math.random() - 0.5) * 8000, mapId: m.id, zone: m.name,
      posTime: now0, firstSeen: now0 - 36e5, lastSeen: now0,
      d: { events: 20, srcEvents: 8, dmgTotal: 4000, dmgHits: 8, dmgEvents: 10, hpSum: 90000, hpSamples: 45, maxHp: 2000 + i * 40, kb: i % 5 === 0 ? 2 : 0, assists: i % 7 === 0 ? 1 : 0 },
    });
  }
  const up = (ps, from, to, id) => call('POST', '/api/upload', { token, body: { rs: 'normal', fp: 'demo-fp', from, to, batchId: id, final: true, players: ps, maps: MAPS } });
  const first = await up(players, 0, now0, 'demo-seed-' + now0);
  console.log('  demo seed      :', JSON.stringify(first.json));
  if (flag('sim')) {
    console.log('  simulator      : moves 8 random demo players every 5s (like a live session uploading)');
    setInterval(async () => {
      const now = Date.now(), moved = [];
      for (let k = 0; k < 8; k++) {
        const p = players[Math.floor(Math.random() * players.length)];
        p.x = Math.max(-4900, Math.min(4900, p.x + (Math.random() - 0.5) * 600));
        p.y = Math.max(-4900, Math.min(4900, p.y + (Math.random() - 0.5) * 600));
        p.posTime = p.lastSeen = now;
        moved.push({ ...p, d: { events: 1, srcEvents: 1, dmgTotal: 100, dmgHits: 1, dmgEvents: 1, hpSum: 2000, hpSamples: 1, maxHp: p.maxHp, kb: 0, assists: 0 }, fixes: [[now, p.mapId, Math.round(p.x * 10) / 10, Math.round(p.y * 10) / 10, 'exact']] });
      }
      await up(moved, now - 5000, now, 'sim-' + (++seq) + '-' + now);
    }, 5000).unref();
  }
}
