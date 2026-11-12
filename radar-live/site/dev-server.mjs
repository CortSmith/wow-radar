// Tiny dev server: serves this folder, the repo's maps/ folder (images are not copied), and a demo API.
//   node site/dev-server.mjs [port]      then open http://localhost:8080/
// Env: PORT, MAPS_DIR (default ../../maps), VIEW_KEY (if set, /api/* needs the X-View-Key header: tests the key prompt).
import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import { extname, resolve, sep } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { demoApi } from './dev-demo.mjs';

const HERE = fileURLToPath(new URL('.', import.meta.url));
const TYPES = {
  '.html': 'text/html; charset=utf-8', '.css': 'text/css; charset=utf-8', '.js': 'text/javascript; charset=utf-8',
  '.mjs': 'text/javascript; charset=utf-8', '.json': 'application/json; charset=utf-8', '.png': 'image/png', '.svg': 'image/svg+xml',
};
const DEFAULT_PORT = 8080;

/** Resolves urlPath under root, or null if it would escape. */
export function safeJoin(root, urlPath) {
  const full = resolve(root, decodeURIComponent(urlPath).replace(/^[/\\]+/, ''));
  return full === resolve(root) || full.startsWith(resolve(root) + sep) ? full : null;
}

async function sendFile(res, file) {
  try {
    const data = await readFile(file);
    res.writeHead(200, { 'Content-Type': TYPES[extname(file)] || 'application/octet-stream', 'Cache-Control': 'no-cache' });
    res.end(data);
  } catch {
    res.writeHead(404, { 'Content-Type': 'text/plain' });
    res.end('not found');
  }
}

/** Builds the request handler (exported for tests). */
export function createHandler({ siteDir = HERE, mapsDir = resolve(HERE, '..', '..', 'maps'), viewKey = '', now = Date.now } = {}) {
  return async (req, res) => {
    const url = new URL(req.url, 'http://localhost');
    if (url.pathname === '/healthz') {
      res.writeHead(200, { 'Content-Type': 'text/plain' });
      res.end('ok');
      return;
    }
    if (url.pathname.startsWith('/api/')) {
      const denied = viewKey && req.headers['x-view-key'] !== viewKey;
      const out = denied ? { status: 401, body: { error: 'unauthorized', message: 'view key required' } } : demoApi(req.url, now());
      res.writeHead(out.status, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify(out.body));
      return;
    }
    const isMap = url.pathname.startsWith('/maps/');
    const file = isMap ? safeJoin(mapsDir, url.pathname.slice('/maps/'.length)) : safeJoin(siteDir, url.pathname === '/' ? 'index.html' : url.pathname);
    if (!file || file.endsWith('.go')) {
      res.writeHead(404);
      res.end('not found');
      return;
    }
    await sendFile(res, file);
  };
}

if (import.meta.url === pathToFileURL(process.argv[1] || '').href) {
  const port = Number(process.argv[2] || process.env.PORT || DEFAULT_PORT);
  createServer(createHandler({ viewKey: process.env.VIEW_KEY || '', mapsDir: process.env.MAPS_DIR || undefined }))
    .listen(port, () => console.log(`radar-live dev server: http://localhost:${port}/ (demo data${process.env.VIEW_KEY ? ', view key required' : ''})`));
}
