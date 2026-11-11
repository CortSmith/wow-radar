/**
 * Pass (session token) issuing and verification, plus the constant-time compare helper.
 * A pass is `<base64url payload>.<base64url HMAC-SHA256>`; the HMAC secret is generated once per
 * deployment and kept in the table, so no secret ever lives in code or environment variables.
 */
import { randomBytes, createHmac, timingSafeEqual } from 'node:crypto';
import { readItem, writeItem, isConditionFailure } from './db.mjs';
import { secretKey } from './keys.mjs';
import { PASS_TTL_MS, SIGNING_SECRET_BYTES } from './config.mjs';

let cachedSecret;

/**
 * Compares two strings/buffers in constant time (length mismatch is an early, non-secret "false").
 * @param {string|Buffer} left
 * @param {string|Buffer} right
 * @returns {boolean}
 */
export function safeEqual(left, right) {
  const a = Buffer.from(left);
  const b = Buffer.from(right);
  return a.length === b.length && timingSafeEqual(a, b);
}

async function readSigningSecret() {
  if (cachedSecret) return cachedSecret;
  let item = await readItem(secretKey());
  if (!item) {
    // Concurrent cold starts may all try to create it: the conditional put lets exactly one win,
    // and everyone then re-reads (strongly consistent) so all instances agree on the winner's secret.
    try {
      await writeItem(
        { ...secretKey(), v: randomBytes(SIGNING_SECRET_BYTES).toString('hex') },
        'attribute_not_exists(PK)',
      );
    } catch (error) {
      if (!isConditionFailure(error)) throw error;
    }
    item = await readItem(secretKey(), { consistent: true });
  }
  cachedSecret = item.v;
  return cachedSecret;
}

const signature = async payload =>
  createHmac('sha256', await readSigningSecret())
    .update(payload)
    .digest('base64url');

/**
 * Issues a pass valid for PASS_TTL_MS.
 * @param {string} username Lower-cased operator name.
 * @returns {Promise<string>}
 */
export async function issuePass(username) {
  const payload = Buffer.from(JSON.stringify({ u: username, e: Date.now() + PASS_TTL_MS })).toString('base64url');
  return `${payload}.${await signature(payload)}`;
}

/**
 * Verifies the Bearer pass of a request.
 * @param {Object} event Lambda event (reads the Authorization header).
 * @returns {Promise<string|null>} The username, or null when missing, forged or expired.
 */
export async function verifyPass(event) {
  const header = event.headers?.authorization || '';
  const [payload, given] = header.replace(/^Bearer /i, '').split('.');
  if (!payload || !given) return null;
  if (!safeEqual(given, await signature(payload))) return null;
  try {
    const claims = JSON.parse(Buffer.from(payload, 'base64url').toString());
    return claims.e > Date.now() ? claims.u : null;
  } catch {
    return null;
  }
}
