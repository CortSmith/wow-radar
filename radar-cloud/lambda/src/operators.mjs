/**
 * Operator (account) sign-up and log-in, gated by a beacon (API key).
 * Passwords are scrypt-hashed; beacons are stored only as SHA-256 hashes.
 */
import { scrypt, randomBytes, createHash } from 'node:crypto';
import { promisify } from 'node:util';
import { readItem, runTransaction } from './db.mjs';
import { beaconKey, operatorKey } from './keys.mjs';
import { issuePass, safeEqual } from './pass.mjs';
import { HttpError } from './http-error.mjs';
import { MESSAGES } from './messages.mjs';
import {
  TABLE_NAME,
  BEACON_MAX_OPERATORS,
  USERNAME_PATTERN,
  USERNAME_MAX_CHARS,
  PASSWORD_MIN_CHARS,
  PASSWORD_MAX_CHARS,
  BEACON_KEY_MIN_CHARS,
  BEACON_KEY_MAX_CHARS,
  SCRYPT_SALT_BYTES,
  SCRYPT_KEY_BYTES,
} from './config.mjs';

const scryptAsync = promisify(scrypt);
const sha256Hex = value => createHash('sha256').update(value).digest('hex');
const isStringWithin = (value, min, max) => typeof value === 'string' && value.length >= min && value.length <= max;

/** Hashing against this when the user does not exist keeps response time uniform (no user enumeration). */
const DUMMY_SALT_HEX = '00'.repeat(SCRYPT_SALT_BYTES);

function assertValidSignUp(body) {
  const valid =
    body &&
    USERNAME_PATTERN.test(typeof body.username === 'string' ? body.username : '') &&
    isStringWithin(body.password, PASSWORD_MIN_CHARS, PASSWORD_MAX_CHARS) &&
    isStringWithin(body.apiKey, BEACON_KEY_MIN_CHARS, BEACON_KEY_MAX_CHARS);
  if (!valid) throw new HttpError(400, MESSAGES.invalidSignUp);
}

/** Atomically creates the operator and claims one slot on the beacon: both succeed or neither does. */
function createOperatorTransaction(username, saltHex, hashHex, beaconHash) {
  return [
    {
      Put: {
        TableName: TABLE_NAME,
        Item: { ...operatorKey(username), salt: saltHex, hash: hashHex, kh: beaconHash, created: Date.now() },
        ConditionExpression: 'attribute_not_exists(PK)',
      },
    },
    {
      Update: {
        TableName: TABLE_NAME,
        Key: beaconKey(beaconHash),
        UpdateExpression: 'ADD cnt :one',
        ConditionExpression: 'active = :t AND (attribute_not_exists(cnt) OR cnt < :max)',
        ExpressionAttributeValues: { ':one': 1, ':t': true, ':max': BEACON_MAX_OPERATORS },
      },
    },
  ];
}

/** Explains which half of a cancelled sign-up transaction failed. */
async function explainSignUpFailure(error, beaconHash) {
  if (error.name !== 'TransactionCanceledException') throw error;
  const [operatorReason] = error.CancellationReasons || [];
  if (operatorReason?.Code === 'ConditionalCheckFailed') return new HttpError(409, MESSAGES.usernameTaken);
  const beacon = await readItem(beaconKey(beaconHash));
  if (!beacon || !beacon.active) return new HttpError(403, MESSAGES.beaconInvalid);
  return new HttpError(403, MESSAGES.beaconFull);
}

/**
 * POST /api/signup
 * @param {import('./types.mjs').RequestContext} context
 * @returns {Promise<{token:string, username:string}>}
 * @throws {HttpError} 400 invalid fields, 409 name taken, 403 bad/inactive/full beacon.
 */
export async function signUpOperator({ body }) {
  assertValidSignUp(body);
  const username = body.username.toLowerCase();
  const beaconHash = sha256Hex(body.apiKey);
  const salt = randomBytes(SCRYPT_SALT_BYTES);
  const hashHex = (await scryptAsync(body.password, salt, SCRYPT_KEY_BYTES)).toString('hex');
  try {
    await runTransaction(createOperatorTransaction(username, salt.toString('hex'), hashHex, beaconHash));
  } catch (error) {
    throw await explainSignUpFailure(error, beaconHash);
  }
  return { token: await issuePass(username), username };
}

/**
 * POST /api/login
 * @param {import('./types.mjs').RequestContext} context
 * @returns {Promise<{token:string, username:string}>}
 * @throws {HttpError} 400 missing fields, 401 wrong credentials, 403 deactivated beacon.
 */
export async function logInOperator({ body }) {
  if (
    !body ||
    typeof body.username !== 'string' ||
    typeof body.password !== 'string' ||
    typeof body.apiKey !== 'string'
  ) {
    throw new HttpError(400, MESSAGES.missingFields);
  }
  const username = body.username.toLowerCase().slice(0, USERNAME_MAX_CHARS);
  const operator = await readItem(operatorKey(username));
  const salt = Buffer.from(operator?.salt || DUMMY_SALT_HEX, 'hex');
  // Always hash, even for unknown users: uniform timing hides which usernames exist.
  const hash = await scryptAsync(body.password.slice(0, PASSWORD_MAX_CHARS), salt, SCRYPT_KEY_BYTES);
  const beaconHash = sha256Hex(body.apiKey);
  const credentialsMatch =
    operator && safeEqual(hash, Buffer.from(operator.hash, 'hex')) && safeEqual(operator.kh, beaconHash);
  if (!credentialsMatch) throw new HttpError(401, MESSAGES.badCredentials);
  const beacon = await readItem(beaconKey(beaconHash));
  if (!beacon?.active) throw new HttpError(403, MESSAGES.beaconInactive);
  return { token: await issuePass(username), username };
}
