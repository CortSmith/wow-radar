// usage: TABLE=<name> node scripts/create-key.mjs [--deactivate <key>]   (needs devDeps @aws-sdk/client-dynamodb + @aws-sdk/lib-dynamodb: npm i)
import { DynamoDBClient } from '@aws-sdk/client-dynamodb';
import { DynamoDBDocumentClient, PutCommand, UpdateCommand } from '@aws-sdk/lib-dynamodb';
import { randomBytes, createHash } from 'node:crypto';
const t = process.env.TABLE; if (!t) throw new Error('set TABLE');
const db = DynamoDBDocumentClient.from(new DynamoDBClient({}));
const h = k => createHash('sha256').update(k).digest('hex');
if (process.argv[2] === '--deactivate') {
  await db.send(new UpdateCommand({ TableName: t, Key: { PK: 'K#' + h(process.argv[3]), SK: 'KEY' }, UpdateExpression: 'SET active=:f', ExpressionAttributeValues: { ':f': false } }));
  console.log('deactivated');
} else {
  const key = randomBytes(18).toString('base64url');
  await db.send(new PutCommand({ TableName: t, Item: { PK: 'K#' + h(key), SK: 'KEY', active: true, cnt: 0, created: Date.now() } }));
  console.log('API key:', key);
}
