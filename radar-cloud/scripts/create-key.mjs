/**
 * Mints or deactivates a beacon (API key) in the deployed table.
 * Usage: TABLE=<table name> node scripts/create-key.mjs                     print a new active key
 *        TABLE=<table name> node scripts/create-key.mjs --deactivate <key>  stop the key logging in / signing up
 * Needs AWS credentials and the dev dependencies (`npm install`). The key is shown once; only its SHA-256 is stored.
 */
import { DynamoDBClient } from '@aws-sdk/client-dynamodb';
import { DynamoDBDocumentClient, PutCommand, UpdateCommand } from '@aws-sdk/lib-dynamodb';
import { randomBytes, createHash } from 'node:crypto';

const KEY_BYTES = 18;
const tableName = process.env.TABLE;
if (!tableName) throw new Error('Set the TABLE environment variable to the table name (stack output TableName).');

const client = DynamoDBDocumentClient.from(new DynamoDBClient({}));
const sha256Hex = value => createHash('sha256').update(value).digest('hex');
const beaconKey = key => ({ PK: 'K#' + sha256Hex(key), SK: 'KEY' });

async function createBeacon() {
  const key = randomBytes(KEY_BYTES).toString('base64url');
  await client.send(
    new PutCommand({ TableName: tableName, Item: { ...beaconKey(key), active: true, cnt: 0, created: Date.now() } }),
  );
  console.log('API key:', key);
}

async function deactivateBeacon(key) {
  if (!key) throw new Error('Usage: --deactivate <key>');
  await client.send(
    new UpdateCommand({
      TableName: tableName,
      Key: beaconKey(key),
      UpdateExpression: 'SET active = :off',
      ExpressionAttributeValues: { ':off': false },
    }),
  );
  console.log('deactivated');
}

if (process.argv[2] === '--deactivate') await deactivateBeacon(process.argv[3]);
else await createBeacon();
