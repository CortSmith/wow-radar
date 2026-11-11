/**
 * DynamoDB access layer: one document client plus small read/write/query/batch helpers with retry.
 * No domain knowledge and no key formats; callers pass keys built by keys.mjs.
 * `@aws-sdk/lib-dynamodb` is imported by that exact specifier because tests stub it by name.
 * Cost notes use on-demand units: 1 RRU = one strongly consistent read of up to 4 KB (eventually
 * consistent reads cost half), 1 WRU = one write of up to 1 KB.
 */
import { DynamoDBClient } from '@aws-sdk/client-dynamodb';
import {
  DynamoDBDocumentClient,
  GetCommand,
  PutCommand,
  QueryCommand,
  BatchGetCommand,
  BatchWriteCommand,
  TransactWriteCommand,
  UpdateCommand,
} from '@aws-sdk/lib-dynamodb';
import {
  TABLE_NAME,
  BATCH_GET_MAX_KEYS,
  BATCH_WRITE_MAX_REQUESTS,
  RETRY_ATTEMPTS_MAX,
  RETRY_BASE_MS,
  ZONE_INDEX,
  SECTOR_PAGE_ROWS_MAX,
} from './config.mjs';
import { splitIntoChunks, sleep } from './utils.mjs';

const client = DynamoDBDocumentClient.from(new DynamoDBClient({}), {
  marshallOptions: { removeUndefinedValues: true },
});

const PREFIX_CONDITION = 'PK = :p AND begins_with(SK, :s)';

/**
 * Tells an expected conditional-write rejection apart from a real failure.
 * @param {unknown} error Anything thrown by a conditional write.
 * @returns {boolean} True when a ConditionExpression failed (the idempotent "already done" outcome).
 */
export const isConditionFailure = error => error?.name === 'ConditionalCheckFailedException';

/**
 * GetItem by primary key. Eventually consistent by default (0.5 RRU per 4 KB); `consistent` doubles the price and
 * is only worth it for read-after-write, such as the signing secret. A missing item is not an error.
 * @param {{PK:string, SK:string}} key
 * @param {{consistent?:boolean, projection?:string}} [options] `projection` is a ProjectionExpression that trims
 *   the response (billing still counts the whole item).
 * @returns {Promise<Object|undefined>} The item, or undefined when absent.
 */
export async function readItem(key, { consistent = false, projection } = {}) {
  const result = await client.send(
    new GetCommand({
      TableName: TABLE_NAME,
      Key: key,
      ...(consistent && { ConsistentRead: true }),
      ...(projection && { ProjectionExpression: projection }),
    }),
  );
  return result.Item;
}

/**
 * PutItem: replaces any existing item (1 WRU per KB). A failed condition rejects, so idempotent callers
 * can use `attribute_not_exists(PK)` to create-once.
 * @param {Object} item Full item including PK and SK.
 * @param {string} [condition] ConditionExpression guarding the write.
 * @throws {Error} Rejects when the condition fails (test with isConditionFailure).
 */
export async function writeItem(item, condition) {
  await client.send(
    new PutCommand({
      TableName: TABLE_NAME,
      Item: item,
      ...(condition && { ConditionExpression: condition }),
    }),
  );
}

/**
 * UpdateItem: changes attributes in place without reading the item first (1 WRU per KB of the item).
 * @param {{PK:string, SK:string}} key
 * @param {string} expression UpdateExpression, for example `ADD v :one`.
 * @param {Object} values ExpressionAttributeValues referenced by the expressions.
 * @param {string} [condition] ConditionExpression; a failed condition rejects.
 * @throws {Error} Rejects when the condition fails (test with isConditionFailure).
 */
export async function updateItem(key, expression, values, condition) {
  await client.send(
    new UpdateCommand({
      TableName: TABLE_NAME,
      Key: key,
      UpdateExpression: expression,
      ExpressionAttributeValues: values,
      ...(condition && { ConditionExpression: condition }),
    }),
  );
}

/**
 * TransactWriteItems: all items succeed or none do (2 WRU per KB, double a plain write).
 * @param {Object[]} items Transaction items; each carries its own TableName.
 * @throws {Error} TransactionCanceledException with per-item CancellationReasons when any condition fails.
 */
export async function runTransaction(items) {
  await client.send(new TransactWriteCommand({ TransactItems: items }));
}

/**
 * Queries a whole partition (optionally only SKs with a prefix), following pagination. Eventually
 * consistent; the cost grows with the total size of everything returned, so use prefixes.
 * @param {string} partitionKey
 * @param {string} [skPrefix] Restricts to sort keys beginning with this.
 * @returns {Promise<Object[]>} Items in sort-key order; empty when none.
 */
export async function readAll(partitionKey, skPrefix) {
  const items = [];
  let startKey;
  do {
    const page = await client.send(
      new QueryCommand({
        TableName: TABLE_NAME,
        ExclusiveStartKey: startKey,
        KeyConditionExpression: skPrefix ? PREFIX_CONDITION : 'PK = :p',
        ExpressionAttributeValues: skPrefix ? { ':p': partitionKey, ':s': skPrefix } : { ':p': partitionKey },
      }),
    );
    items.push(...page.Items);
    startKey = page.LastEvaluatedKey;
  } while (startKey);
  return items;
}

/**
 * Queries one item from either end of an SK prefix: costs a single item's read, not the partition's.
 * Eventually consistent.
 * @param {string} partitionKey
 * @param {string} skPrefix
 * @param {boolean} ascending True for the oldest item, false for the newest.
 * @returns {Promise<Object|undefined>} The edge item, or undefined when the prefix is empty.
 */
export async function readEdge(partitionKey, skPrefix, ascending) {
  const page = await client.send(
    new QueryCommand({
      TableName: TABLE_NAME,
      Limit: 1,
      ScanIndexForward: ascending,
      KeyConditionExpression: PREFIX_CONDITION,
      ExpressionAttributeValues: { ':p': partitionKey, ':s': skPrefix },
    }),
  );
  return page.Items[0];
}

/**
 * Queries the zone-idx GSI for rows of one partition written after `since`. GSI reads are always eventually
 * consistent, so a fresh write can take a moment to appear; callers overlap their `since` windows to tolerate that
 * lag. Paging stops after a page of SECTOR_PAGE_ROWS_MAX rows to bound the cost of one sweep.
 * @param {string} zoneKey zk value (see keys.zoneIndexKey).
 * @param {number} since Exclusive lower bound on `ls`, in server milliseconds.
 * @returns {Promise<Object[]>} Projected index items.
 */
export async function queryZoneIndex(zoneKey, since) {
  const rows = [];
  let startKey;
  do {
    const page = await client.send(
      new QueryCommand({
        TableName: TABLE_NAME,
        IndexName: ZONE_INDEX,
        ConsistentRead: false,
        ExclusiveStartKey: startKey,
        KeyConditionExpression: 'zk = :k AND ls > :s',
        ExpressionAttributeValues: { ':k': zoneKey, ':s': since },
      }),
    );
    rows.push(...page.Items);
    startKey = page.Items.length < SECTOR_PAGE_ROWS_MAX ? page.LastEvaluatedKey : undefined;
  } while (startKey);
  return rows;
}

/**
 * Re-sends the unprocessed part of a batch call until it is empty, backing off exponentially
 * (RETRY_BASE_MS doubling), because DynamoDB returns throttled items instead of failing the call.
 * @param {(request:Object)=>Promise<Object>} send Performs one attempt for the given RequestItems.
 * @param {(response:Object)=>Object|undefined} unprocessedOf Extracts the leftover RequestItems.
 * @param {Object} firstRequest RequestItems of the first attempt.
 * @param {(response:Object)=>void} [onResponse] Called with every response, to collect results.
 * @param {string} throttledMessage Error text when RETRY_ATTEMPTS_MAX attempts leave work undone.
 * @throws {Error} With `throttledMessage` when the table stays throttled.
 */
async function drainBatch(send, unprocessedOf, firstRequest, onResponse, throttledMessage) {
  let request = firstRequest;
  for (let attempt = 0; request && attempt < RETRY_ATTEMPTS_MAX; attempt++) {
    const response = await send(request);
    onResponse?.(response);
    const leftover = unprocessedOf(response);
    request = leftover && Object.keys(leftover).length ? leftover : null;
    if (request) await sleep(RETRY_BASE_MS * 2 ** attempt);
  }
  if (request) throw new Error(throttledMessage);
}

/**
 * BatchGetItem for several sort keys of one partition, split into BATCH_GET_MAX_KEYS requests and retried when
 * throttled. Eventually consistent, one read unit per item (0.5 RRU per 4 KB).
 * @param {string} partitionKey
 * @param {string[]} sortKeys
 * @returns {Promise<Map<string, Object>>} Items by SK; missing items are simply absent.
 * @throws {Error} 'read throttled' when retries are exhausted.
 */
export async function readItems(partitionKey, sortKeys) {
  const found = new Map();
  for (const part of splitIntoChunks(sortKeys, BATCH_GET_MAX_KEYS)) {
    await drainBatch(
      request => client.send(new BatchGetCommand({ RequestItems: request })),
      response => response.UnprocessedKeys,
      { [TABLE_NAME]: { Keys: part.map(SK => ({ PK: partitionKey, SK })) } },
      response => {
        for (const item of response.Responses?.[TABLE_NAME] || []) found.set(item.SK, item);
      },
      'read throttled',
    );
  }
  return found;
}

async function writeBatches(requests) {
  for (const part of splitIntoChunks(requests, BATCH_WRITE_MAX_REQUESTS)) {
    await drainBatch(
      request => client.send(new BatchWriteCommand({ RequestItems: request })),
      response => response.UnprocessedItems,
      { [TABLE_NAME]: part },
      undefined,
      'write throttled',
    );
  }
}

/**
 * BatchWriteItem puts, split into BATCH_WRITE_MAX_REQUESTS per call. Not atomic: a crash can leave part written,
 * so callers rely on idempotent retries. Costs 1 WRU per KB per item.
 * @param {Object[]} items Whole items to store.
 * @throws {Error} 'write throttled' when retries are exhausted.
 */
export const writeItems = items => writeBatches(items.map(Item => ({ PutRequest: { Item } })));

/**
 * BatchWriteItem deletes (1 WRU per KB of each deleted item); deleting a missing key succeeds silently.
 * @param {{PK:string, SK:string}[]} keys Objects whose PK and SK identify the items; other attributes are ignored.
 * @throws {Error} 'write throttled' when retries are exhausted.
 */
export const dropItems = keys => writeBatches(keys.map(({ PK, SK }) => ({ DeleteRequest: { Key: { PK, SK } } })));
