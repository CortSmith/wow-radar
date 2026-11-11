// In-memory stand-in for @aws-sdk/lib-dynamodb (only the operations lambda/index.mjs uses).
export const tables = new Map(); // PK -> Map(SK -> item)
export const stats = { writes: 0, reads: 0 };
// read-operation log (one entry per Get/Query/BatchGet call); tests reset it per request
export const log = [];
export const GSI = { 'zone-idx': { pk: 'zk', sk: 'ls', include: ['name', 'reaction', 'level', 'maxHp', 'x', 'y', 'mapId', 'zone', 'firstSeen', 'lastSeen', 'posTime'] } };
const proj = (it, expr) => {
  if (!it || !expr) return it;
  const out = {}; for (const a of expr.split(',').map(x => x.trim())) if (a in it) out[a] = it[a];
  return out;
};
const cp = x => (x === undefined ? x : structuredClone(x));
const part = pk => tables.get(pk) || tables.set(pk, new Map()).get(pk);
const mk = n => class { constructor(input) { this.input = input; } static get n() { return n; } };
export const GetCommand = mk('Get'), PutCommand = mk('Put'), QueryCommand = mk('Query'), BatchGetCommand = mk('BatchGet'),
  BatchWriteCommand = mk('BatchWrite'), TransactWriteCommand = mk('TransactWrite'), UpdateCommand = mk('Update');
class CCF extends Error { constructor() { super('cond'); this.name = 'ConditionalCheckFailedException'; } }
class TCE extends Error { constructor(r) { super('tx'); this.name = 'TransactionCanceledException'; this.CancellationReasons = r; } }
const put = (it) => { stats.writes++; part(it.PK).set(it.SK, cp(it)); };
const get = (pk, sk) => cp(tables.get(pk)?.get(sk));

const ops = {
  Get: i => { log.push({ op: 'Get', pk: i.Key.PK, sk: i.Key.SK, cr: !!i.ConsistentRead }); stats.reads++; return { Item: proj(get(i.Key.PK, i.Key.SK), i.ProjectionExpression) }; },
  Put: i => {
    if (i.ConditionExpression === 'attribute_not_exists(PK)' && get(i.Item.PK, i.Item.SK)) throw new CCF();
    put(i.Item); return {};
  },
  Query: i => {
    stats.reads++;
    if (i.IndexName) {
      const g = GSI[i.IndexName]; if (!g) throw new Error('no such index');
      if (i.ConsistentRead) throw new Error('ConsistentRead not supported on GSI');
      const k = i.ExpressionAttributeValues[':k'], lo = i.ExpressionAttributeValues[':s'];
      log.push({ op: 'Query', index: i.IndexName, pk: k, cr: false });
      const rows = [];
      for (const part of tables.values()) for (const it of part.values()) if (it[g.pk] === k && it[g.sk] > lo) rows.push(it);
      rows.sort((a, b) => a[g.sk] - b[g.sk]);
      const keep = ['PK', 'SK', g.pk, g.sk, ...g.include]; // projection actually applied
      return { Items: rows.map(it => Object.fromEntries(keep.filter(a => a in it).map(a => [a, cp(it[a])]))) };
    }
    log.push({ op: 'Query', pk: i.ExpressionAttributeValues[':p'], prefix: i.ExpressionAttributeValues[':s'], cr: !!i.ConsistentRead });
    const pk = i.ExpressionAttributeValues[':p'], s = i.ExpressionAttributeValues[':s'];
    let items = [...(tables.get(pk)?.values() || [])].filter(x => !s || x.SK.startsWith(s)).sort((a, b) => (a.SK < b.SK ? -1 : 1));
    if (i.ScanIndexForward === false) items.reverse();
    if (i.Limit) items = items.slice(0, i.Limit);
    return { Items: items.map(cp) };
  },
  BatchGet: i => {
    const Keys = i.RequestItems[Object.keys(i.RequestItems)[0]].Keys;
    stats.reads += Keys.length; log.push({ op: 'BatchGet', n: Keys.length });
    return { Responses: { [process.env.TABLE]: Keys.map(k => get(k.PK, k.SK)).filter(Boolean) } };
  },
  BatchWrite: i => {
    const reqs = i.RequestItems[Object.keys(i.RequestItems)[0]];
    if (reqs.length > 25) throw new Error('batch >25');
    for (const r of reqs) { if (r.PutRequest) put(r.PutRequest.Item); else { stats.writes++; tables.get(r.DeleteRequest.Key.PK)?.delete(r.DeleteRequest.Key.SK); } }
    return {};
  },
  TransactWrite: i => {
    const [a, b] = i.TransactItems;
    const reasons = [{}, {}];
    if (get(a.Put.Item.PK, a.Put.Item.SK)) reasons[0] = { Code: 'ConditionalCheckFailed' };
    const k = get(b.Update.Key.PK, b.Update.Key.SK), v = b.Update.ExpressionAttributeValues;
    if (!k || k.active !== true || (k.cnt !== undefined && !(k.cnt < v[':max']))) reasons[1] = { Code: 'ConditionalCheckFailed' };
    if (reasons.some(r => r.Code)) throw new TCE(reasons);
    put(a.Put.Item); k.cnt = (k.cnt || 0) + 1; put(k); return {};
  },
  Update: i => {
    const it = get(i.Key.PK, i.Key.SK) || { ...i.Key };
    if (i.UpdateExpression === 'SET lastTs = :to') {
      if (it.lastTs !== undefined && !(it.lastTs < i.ExpressionAttributeValues[':to'])) throw new CCF();
      it.lastTs = i.ExpressionAttributeValues[':to'];
    } else if (/^ADD v :one( SET .+)?$/.test(i.UpdateExpression)) {
      it.v = (it.v || 0) + 1;
      const m = i.UpdateExpression.match(/SET (.+)$/);
      for (const a of m ? m[1].split(',') : []) { const [n, val] = a.split('=').map(x => x.trim()); it[n] = i.ExpressionAttributeValues[val]; }
    } else throw new Error('unsupported update');
    put(it); return {};
  },
};
export class DynamoDBDocumentClient {
  static from() { return new DynamoDBDocumentClient(); }
  async send(cmd) { return ops[cmd.constructor.n](cmd.input); }
}
export const seedKey = (hashKey, o) => put({ PK: 'K#' + hashKey, SK: 'KEY', ...o });
