/**
 * Local imitation of production: CloudFront (static site + /api/* -> Lambda Function URL) in one process.
 * Serves the real site/ files and the REAL lambda/index.mjs on an in-memory DynamoDB stub
 * (test/stub-lib.mjs), optionally persisted to dev/.data.json.
 *
 * Usage: node dev/server.mjs [--port 8787] [--latency 30] [--demo] [--sim] [--fresh]
 *   --demo    seed a demo operator with 48 contacts (login demo / demo-password-1)
 *   --sim     --demo plus a simulator that moves 8 contacts every 5 s (like a live uploader)
 *   --fresh   ignore and overwrite the saved data
 */
import http from 'node:http';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { register } from 'node:module';
import { gzipSync, gunzipSync } from 'node:zlib';
import {
  DEFAULT_PORT,
  DEFAULT_LATENCY_MS,
  ACTIVE_BEACON_KEY,
  INACTIVE_BEACON_KEY,
  DEMO_USERNAME,
  DEMO_PASSWORD,
  LOG_PREFIX,
  sha256Hex,
} from './harbor.mjs';

const ROOT_DIR = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const SITE_DIR = path.join(ROOT_DIR, 'site');
const DATA_FILE = path.join(ROOT_DIR, 'dev', '.data.json');
const SAVE_INTERVAL_MS = 10000;
/** Function URLs reject request payloads above 6 MB. */
const REQUEST_LIMIT_BYTES = 6 * 1024 * 1024;
const SIM_INTERVAL_MS = 5000;
const SIM_MOVERS_PER_TICK = 8;
const DEMO_CONTACT_COUNT = 48;
const ONE_HOUR_MS = 60 * 60 * 1000;

const hasFlag = name => process.argv.includes('--' + name);
const readOption = (name, fallback) => {
  const index = process.argv.indexOf('--' + name);
  return index < 0 ? fallback : process.argv[index + 1];
};
const PORT = +readOption('port', DEFAULT_PORT);
const LATENCY_MS = +readOption('latency', DEFAULT_LATENCY_MS);
const log = (...parts) => console.log(LOG_PREFIX, ...parts);

// The lambda reads TABLE at import time, and its AWS SDK imports are redirected to the stubs by the hooks.
process.env.TABLE = 'LocalTable';
process.env.MAX_ACCOUNTS_PER_KEY = '5';
register(pathToFileURL(path.join(ROOT_DIR, 'test', 'hooks.mjs')).href, import.meta.url);
const stub = await import(new URL('../test/stub-lib.mjs', import.meta.url).href);
const { handler } = await import(new URL('../lambda/index.mjs', import.meta.url).href);

// ---------------------------------------------------------------- persistence of the in-memory table
/**
 * Restores the saved table. A corrupt file (for example the window was closed mid-save) must never stop the server
 * from starting: it is set aside as `.data.json.corrupt` and the server starts empty.
 */
function loadSavedData() {
  if (hasFlag('fresh') || !fs.existsSync(DATA_FILE)) return;
  try {
    const saved = JSON.parse(fs.readFileSync(DATA_FILE, 'utf8'));
    for (const [partition, rows] of Object.entries(saved)) stub.tables.set(partition, new Map(Object.entries(rows)));
    log('loaded', DATA_FILE);
  } catch (error) {
    stub.tables.clear();
    const aside = `${DATA_FILE}.corrupt`;
    fs.renameSync(DATA_FILE, aside);
    log(`saved data was unreadable (${error.message}); moved to ${aside} and starting empty`);
  }
}

/** Writes to a temp file then renames, so a crash or closed window can never leave a half-written data file. */
function saveData() {
  const plain = Object.fromEntries([...stub.tables].map(([partition, rows]) => [partition, Object.fromEntries(rows)]));
  const temp = `${DATA_FILE}.tmp`;
  fs.writeFileSync(temp, JSON.stringify(plain));
  fs.renameSync(temp, DATA_FILE);
}

function seedBeacons() {
  if (stub.tables.get('K#' + sha256Hex(ACTIVE_BEACON_KEY))) return;
  stub.seedKey(sha256Hex(ACTIVE_BEACON_KEY), { active: true, cnt: 0 });
  stub.seedKey(sha256Hex(INACTIVE_BEACON_KEY), { active: false, cnt: 0 });
}

// ---------------------------------------------------------------- Function-URL-style (payload v2) invocation
async function invokeLambda(request, body) {
  const url = new URL(request.url, 'http://x');
  const headers = Object.fromEntries(
    Object.entries(request.headers).map(([name, value]) => [name.toLowerCase(), String(value)]),
  );
  delete headers.host; // CloudFront ALL_VIEWER_EXCEPT_HOST_HEADER
  return handler({
    version: '2.0',
    rawPath: url.pathname,
    rawQueryString: url.search.slice(1),
    headers,
    queryStringParameters: url.searchParams.size ? Object.fromEntries(url.searchParams) : undefined,
    requestContext: { http: { method: request.method, path: url.pathname, sourceIp: '127.0.0.1' } },
    body: body.length ? body.toString('utf8') : undefined,
    isBase64Encoded: false,
  });
}

// ---------------------------------------------------------------- static site (private S3 + OAC behaviour)
const MIME_TYPES = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript',
  '.css': 'text/css',
  '.json': 'application/json',
  '.svg': 'image/svg+xml',
  '.webp': 'image/webp',
  '.png': 'image/png',
  '.ico': 'image/x-icon',
};
const COMPRESSIBLE = /text|javascript|json|svg/;
const IMMUTABLE_CACHE = 'public, max-age=31536000, immutable';
const REVALIDATE_CACHE = 'public, max-age=0, must-revalidate';

function resolveSiteFile(urlPath) {
  const file = path.normalize(path.join(SITE_DIR, urlPath === '/' ? '/index.html' : urlPath));
  const isServable = file.startsWith(SITE_DIR) && fs.existsSync(file) && !fs.statSync(file).isDirectory();
  return isServable ? file : null;
}

function serveStatic(request, response) {
  const file = resolveSiteFile(decodeURIComponent(new URL(request.url, 'http://x').pathname));
  if (!file) {
    response.writeHead(403, { 'content-type': 'application/xml' }); // S3 + OAC answers 403 for missing keys
    return response.end('<Error><Code>AccessDenied</Code></Error>');
  }
  const extension = path.extname(file);
  const content = fs.readFileSync(file);
  const headers = {
    'content-type': MIME_TYPES[extension] || 'application/octet-stream',
    etag: '"' + sha256Hex(content.toString('latin1')).slice(0, 16) + '"',
    'cache-control': extension === '.webp' ? IMMUTABLE_CACHE : REVALIDATE_CACHE,
  };
  if (request.headers['if-none-match'] === headers.etag) {
    response.writeHead(304, headers);
    return response.end();
  }
  const shouldGzip =
    COMPRESSIBLE.test(headers['content-type']) && /gzip/.test(request.headers['accept-encoding'] || '');
  if (shouldGzip) headers['content-encoding'] = 'gzip';
  response.writeHead(200, headers);
  response.end(shouldGzip ? gzipSync(content) : content);
}

// ---------------------------------------------------------------- API requests
const LOGGED_QUERY_PARAMS = ['scope', 'mapId', 'grp', 'guid', 'since'];

async function readRequestBody(request) {
  const parts = [];
  let size = 0;
  for await (const part of request) {
    size += part.length;
    if (size <= REQUEST_LIMIT_BYTES) parts.push(part);
  }
  return size > REQUEST_LIMIT_BYTES ? null : Buffer.concat(parts);
}

function logApiCall(request, statusCode, startedAt, readsBefore, writesBefore) {
  const url = new URL(request.url, 'http://x');
  const params = [...url.searchParams]
    .filter(([name]) => LOGGED_QUERY_PARAMS.includes(name))
    .map(([name, value]) => `${name}=${value}`)
    .join(' ');
  const elapsed = String(Date.now() - startedAt).padStart(4);
  log(
    `${request.method.padEnd(4)} ${url.pathname.padEnd(18)} ${statusCode} ${elapsed}ms`,
    `reads=${stub.stats.reads - readsBefore} writes=${stub.stats.writes - writesBefore}`,
    params,
  );
}

async function serveApi(request, response) {
  const startedAt = Date.now();
  const body = await readRequestBody(request);
  if (!body) {
    response.writeHead(413, { 'content-type': 'application/json' });
    return response.end('{"message":"Request Entity Too Large"}');
  }
  await new Promise(resolve => setTimeout(resolve, LATENCY_MS));
  const readsBefore = stub.stats.reads;
  const writesBefore = stub.stats.writes;
  const result = await invokeLambda(request, body);
  const payload = result.isBase64Encoded ? Buffer.from(result.body, 'base64') : Buffer.from(result.body || '');
  response.writeHead(result.statusCode, { ...result.headers, 'content-length': payload.length });
  response.end(payload);
  logApiCall(request, result.statusCode, startedAt, readsBefore, writesBefore);
}

// ---------------------------------------------------------------- demo data and live simulator
async function callApi(method, apiPath, { query, body, token } = {}) {
  const result = await handler({
    requestContext: { http: { method } },
    rawPath: apiPath,
    queryStringParameters: query,
    headers: token ? { authorization: 'Bearer ' + token } : {},
    body: body ? JSON.stringify(body) : undefined,
  });
  const text = result.isBase64Encoded ? gunzipSync(Buffer.from(result.body, 'base64')).toString() : result.body;
  return { code: result.statusCode, json: JSON.parse(text) };
}

const DEMO_CHARTS = [
  { id: 1413, name: 'Barrens', x0: -5000, x1: 5000, y0: -5000, y1: 5000 },
  { id: 1429, name: 'Elwynn', x0: -5000, x1: 5000, y0: -5000, y1: 5000 },
];
const DEMO_NAMES = [
  'Arthas',
  'Jaina',
  'Thrall',
  'Sylvanas',
  'Illidan',
  'Tyrande',
  'Malfurion',
  'Garrosh',
  'Varian',
  'Anduin',
  'Voljin',
  'Baine',
  'Cairne',
  'Rexxar',
  'Uther',
  'Falstad',
];
const DEMO_FINGERPRINT = 'demo-fp';
const MAP_EDGE = 4900;

const randomCoord = spread => (Math.random() - 0.5) * spread;
const clampToMap = value => Math.max(-MAP_EDGE, Math.min(MAP_EDGE, value));

function makeDemoContact(index, now) {
  const chart = DEMO_CHARTS[index % 2];
  const maxHp = 2000 + index * 40;
  return {
    guid: 'Player-1-' + String(index + 1).padStart(8, '0'),
    name: DEMO_NAMES[index % DEMO_NAMES.length] + (index >= DEMO_NAMES.length ? index : ''),
    reaction: index % 3 === 0 ? 'friendly' : 'hostile',
    level: 40 + (index % 21),
    maxHp,
    x: randomCoord(8000),
    y: randomCoord(8000),
    mapId: chart.id,
    zone: chart.name,
    posTime: now,
    firstSeen: now - ONE_HOUR_MS,
    lastSeen: now,
    d: {
      events: 20,
      srcEvents: 8,
      dmgTotal: 4000,
      dmgHits: 8,
      dmgEvents: 10,
      hpSum: 90000,
      hpSamples: 45,
      maxHp,
      kb: index % 5 === 0 ? 2 : 0,
      assists: index % 7 === 0 ? 1 : 0,
    },
  };
}

async function signInDemoOperator() {
  const credentials = { username: DEMO_USERNAME, password: DEMO_PASSWORD, apiKey: ACTIVE_BEACON_KEY };
  let result = await callApi('POST', '/api/login', { body: credentials });
  if (result.code !== 200) result = await callApi('POST', '/api/signup', { body: credentials });
  return result.json.token;
}

function simulatePing(contacts, now) {
  const moved = [];
  for (let step = 0; step < SIM_MOVERS_PER_TICK; step++) {
    const contact = contacts[Math.floor(Math.random() * contacts.length)];
    contact.x = clampToMap(contact.x + randomCoord(600));
    contact.y = clampToMap(contact.y + randomCoord(600));
    contact.posTime = contact.lastSeen = now;
    const fix = [now, contact.mapId, Math.round(contact.x * 10) / 10, Math.round(contact.y * 10) / 10, 'exact'];
    moved.push({
      ...contact,
      d: {
        events: 1,
        srcEvents: 1,
        dmgTotal: 100,
        dmgHits: 1,
        dmgEvents: 1,
        hpSum: 2000,
        hpSamples: 1,
        maxHp: contact.maxHp,
        kb: 0,
        assists: 0,
      },
      fixes: [fix],
    });
  }
  return moved;
}

async function seedDemo() {
  const token = await signInDemoOperator();
  if (!token) return log('demo login failed');
  const now = Date.now();
  const contacts = Array.from({ length: DEMO_CONTACT_COUNT }, (_, index) => makeDemoContact(index, now));
  const sendPing = (players, from, to, batchId) =>
    callApi('POST', '/api/upload', {
      token,
      body: { rs: 'normal', fp: DEMO_FINGERPRINT, from, to, batchId, final: true, players, maps: DEMO_CHARTS },
    });
  log('  demo seed      :', JSON.stringify((await sendPing(contacts, 0, now, 'demo-seed-' + now)).json));
  if (!hasFlag('sim')) return;
  log(
    `  simulator      : moves ${SIM_MOVERS_PER_TICK} random demo players every ${SIM_INTERVAL_MS / 1000}s`,
    '(like a live session uploading)',
  );
  let pingNumber = 0;
  setInterval(() => {
    const tick = Date.now();
    sendPing(simulatePing(contacts, tick), tick - SIM_INTERVAL_MS, tick, `sim-${++pingNumber}-${tick}`).catch(error =>
      log('simulator ping failed:', error.message),
    );
  }, SIM_INTERVAL_MS).unref();
}

// ---------------------------------------------------------------- start
loadSavedData();
setInterval(saveData, SAVE_INTERVAL_MS).unref();
for (const signal of ['SIGINT', 'SIGTERM'])
  process.on(signal, () => {
    saveData();
    process.exit(0);
  });
seedBeacons();

// A bug in one request must not take the whole dev server down: report it and keep serving.
for (const event of ['uncaughtException', 'unhandledRejection'])
  process.on(event, error => log(`${event}:`, error?.stack || error));

http
  .createServer((request, response) => {
    if (request.url.startsWith('/api/')) serveApi(request, response);
    else serveStatic(request, response);
  })
  .on('error', error => {
    const hint =
      error.code === 'EADDRINUSE' ? ` Port ${PORT} is already in use: close the other copy or use --port 8788.` : '';
    log(`cannot start: ${error.message}.${hint}`);
    process.exit(1);
  })
  .listen(PORT, '127.0.0.1', async () => {
    log(`radar-cloud LOCAL  http://127.0.0.1:${PORT}   (simulated latency ${LATENCY_MS}ms)`);
    log(`  active API key : ${ACTIVE_BEACON_KEY}   (inactive test key: ${INACTIVE_BEACON_KEY})`);
    if (hasFlag('demo') || hasFlag('sim')) {
      await seedDemo();
      log(`  demo account   : ${DEMO_USERNAME} / ${DEMO_PASSWORD}   key ${ACTIVE_BEACON_KEY}`);
    }
  });
