// Demo backend for the page: answers the PLAN.md section 4 read API with synthetic, moving players.
// Used by dev-server.mjs (so the page can be viewed without the Go servers) and by the boot tests.
// Pure: demoApi(url, nowMs) -> {status, body}.

const NAMES = ['Aldric', 'Brenna', 'Cassian', 'Dara', 'Eryn', 'Fenwick', 'Gwyn', 'Hale', 'Isolde', 'Joren',
  'Kestrel', 'Lysa', 'Marek', 'Nyla', 'Orrin', 'Petra', 'Quill', 'Rowan', 'Sable', 'Tamsin', 'Ulric', 'Vesna', 'Wren', 'Yara'];
const CHARTS = [
  { mapId: 27, name: 'Dun Morogh', x0: -2500, x1: -500, y0: -1000, y1: 2000 },
  { mapId: 37, name: 'Elwynn Forest', x0: -1000, x1: 1000, y0: -2000, y1: 1000 },
];
const MIN_MS = 60_000;
const ACTIVE_WINDOW_MS = 15 * MIN_MS;
const REACTIONS = ['friendly', 'hostile', 'neutral'];
const PER_ZONE = NAMES.length / CHARTS.length;

function contactAt(i, now) {
  const chart = CHARTS[i % CHARTS.length];
  const slot = Math.floor(i / CHARTS.length);
  const t = now / 1000;
  const cx = (chart.x0 + chart.x1) / 2;
  const cy = (chart.y0 + chart.y1) / 2;
  const ang = t / (90 + slot * 11) + slot * 0.8;
  const reach = 0.2 + slot * 0.06; // fraction of the half-extent, so every contact stays inside its chart
  const ageMin = i % 7 === 0 ? 6 + (i % 3) : i % 11 === 0 ? 20 : 0;
  const lastSeen = now - ageMin * MIN_MS - (i % 9) * 20_000;
  const hits = 40 + i * 13;
  return {
    guid: `Player-4372-${(0xa000 + i).toString(16).toUpperCase().padStart(8, '0')}`,
    name: NAMES[i],
    raw: `${NAMES[i]}-Demo`,
    pastNames: i % 5 === 0 ? [`${NAMES[i]}x`] : [],
    reaction: REACTIONS[i % 3],
    level: 20 + ((i * 7) % 41),
    maxHp: 1800 + i * 90,
    x: Math.round((cx + Math.sin(ang) * reach * ((chart.x1 - chart.x0) / 2)) * 10) / 10,
    y: Math.round((cy + Math.cos(ang) * reach * ((chart.y1 - chart.y0) / 2)) * 10) / 10,
    mapId: chart.mapId,
    zone: chart.name,
    posTime: lastSeen,
    firstSeen: now - 3 * 3600_000,
    lastSeen,
    events: 500 + i * 31,
    srcEvents: 300 + i * 17,
    dmgTotal: hits * (55 + i * 4),
    dmgHits: hits,
    dmgEvents: hits + 25,
    hpSum: (1500 + i * 70) * 20,
    hpSamples: 20,
    kb: i % 4 === 0 ? 12 - (i % 12) : 0,
    assists: i % 3 === 0 ? 5 + (i % 6) : 0,
  };
}

const allContacts = (now) => NAMES.map((_, i) => contactAt(i, now));

function trailFor(guid, now, sinceMs) {
  const i = NAMES.findIndex((_, k) => contactAt(k, now).guid === guid);
  if (i < 0) return [];
  const fixes = [];
  const step = 20_000;
  for (let t = Math.floor(now / step) * step - 60 * step; t <= now; t += step) {
    const c = contactAt(i, t);
    if (t > sinceMs) fixes.push({ t, mapId: c.mapId, x: c.x, y: c.y, kind: t % 100_000 === 0 ? 'approx' : 'exact' });
  }
  return fixes;
}

function matches(c, p, now) {
  const grpOk = !p.grp || p.grp === 'any' || p.grp === (c.reaction === 'friendly' ? 'friendly' : c.reaction === 'hostile' ? 'hostile' : 'other');
  if (!grpOk || c.lastSeen <= Number(p.since || 0)) return false;
  if (p.scope === 'player') return c.guid === p.guid;
  if (now - c.lastSeen > ACTIVE_WINDOW_MS) return false;
  return p.scope !== 'zone' || c.mapId === Number(p.mapId);
}

function sweep(p, now) {
  const contacts = allContacts(now).filter((c) => matches(c, p, now));
  const body = { now, changed: Number(p.since || 0) === 0 || contacts.length > 0, sector: { ...p }, contacts };
  if (p.trail === '1' && p.scope === 'player') body.trail = trailFor(p.guid, now, Number(p.since || 0));
  return body;
}

function search(p, now) {
  const q = String(p.q || '').toLowerCase();
  const limit = Math.min(Number(p.limit) || 50, 500);
  const hits = allContacts(now).filter((c) => !q || [c.name, c.raw, ...c.pastNames].some((n) => n.toLowerCase().includes(q)));
  return { total: hits.length, contacts: hits.slice(0, limit) };
}

function bounty(p, now) {
  const rows = allContacts(now).filter((c) => c.kb > 0 || c.assists > 0).sort((a, b) => b.kb - a.kb || b.assists - a.assists);
  return rows.slice(0, Number(p.limit) || 50).map((c, i) => ({
    rank: i + 1, guid: c.guid, name: c.name, kb: c.kb, assists: c.assists, score: c.kb * 10,
  }));
}

/** Answers one GET request path+query. Returns {status, body}. */
export function demoApi(url, now = Date.now()) {
  const u = new URL(url, 'http://demo.local');
  const p = Object.fromEntries(u.searchParams);
  switch (u.pathname) {
    case '/api/v1/sweep': return { status: 200, body: sweep(p, now) };
    case '/api/v1/contacts': return { status: 200, body: search(p, now) };
    case '/api/v1/trail': return { status: 200, body: trailFor(p.guid, now, 0) };
    case '/api/v1/bounty': return { status: 200, body: bounty(p, now) };
    case '/api/v1/charts': return { status: 200, body: CHARTS };
    case '/api/v1/status':
      return { status: 200, body: { version: 'demo', rulesets: [{ id: 'pvp', contacts: NAMES.length, lastWrite: now }], uptimeSec: 1 } };
    default:
      return { status: 404, body: { error: 'not_found', message: 'unknown API path' } };
  }
}

export const DEMO_CONTACTS_PER_ZONE = PER_ZONE;
