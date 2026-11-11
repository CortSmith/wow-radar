/**
 * Input sanitising for untrusted data: numeric clamping, string trimming and typed query-parameter parsing.
 * Every function here is pure; failures are reported as HttpError(400).
 */
import { HttpError } from './http-error.mjs';
import { MESSAGES } from './messages.mjs';
import { GUID_PATTERN, FINGERPRINT_PATTERN, ROUND1_SCALE, TIMEZONE_MAX_CHARS, TIMEZONE_PATTERN } from './config.mjs';

/**
 * Whether a value names an IANA time zone this runtime knows. The runtime (Intl) is the authority, so zones added by
 * newer tz databases are accepted as soon as Node ships them.
 * @param {unknown} value
 * @returns {boolean} True for 'UTC' and names such as 'Asia/Kolkata'; false for non-strings, offsets and unknown names.
 */
export function isTimeZoneName(value) {
  if (typeof value !== 'string' || value.length > TIMEZONE_MAX_CHARS || !TIMEZONE_PATTERN.test(value)) return false;
  try {
    new Intl.DateTimeFormat('en-US', { timeZone: value });
    return true;
  } catch {
    return false;
  }
}

/**
 * @param {unknown} value
 * @returns {boolean} True for finite numbers only (rejects NaN, Infinity and numeric strings).
 */
export const isFiniteNumber = value => typeof value === 'number' && Number.isFinite(value);

/**
 * Clamps a number into [low, high]; anything that is not a finite number becomes the fallback.
 * @param {unknown} value
 * @param {number} low
 * @param {number} high
 * @param {number} [fallback=0]
 * @returns {number}
 */
export const clamp = (value, low, high, fallback = 0) =>
  isFiniteNumber(value) ? Math.min(high, Math.max(low, value)) : fallback;

/** Like clamp, but floors the result to an integer. */
export const int = (value, low, high, fallback = 0) => Math.floor(clamp(value, low, high, fallback));

/**
 * Rounds to one decimal place: keeps stored coordinates and sums small and makes repeated merges idempotent.
 * The static site has its own copy (site/tracker.js round1) because a Lambda asset and the browser bundle cannot share
 * code without a build step; keep both rounding the same way.
 */
export const round1 = value => Math.round(value * ROUND1_SCALE) / ROUND1_SCALE;

/**
 * @param {unknown} value
 * @param {number} maxChars
 * @returns {string} The value cut to maxChars, or '' when it is not a string.
 */
export const text = (value, maxChars) => (typeof value === 'string' ? value.slice(0, maxChars) : '');

/**
 * @param {unknown} value
 * @returns {boolean} True for a non-null, non-array object (a JSON object).
 */
export const isPlainObject = value => Boolean(value) && typeof value === 'object' && !Array.isArray(value);

/** @returns {boolean} True when a query/body value was left out ('' counts as absent). */
export const isBlank = value => value === undefined || value === null || value === '';

/**
 * Parses an optional integer query parameter.
 * @param {unknown} value
 * @param {string} name Parameter name for the error message.
 * @returns {number|undefined} undefined when blank.
 * @throws {HttpError} 400 when present but not an integer.
 */
export function parseOptionalInteger(value, name) {
  if (value === undefined || value === '') return undefined;
  const parsed = Number(value);
  if (!Number.isInteger(parsed)) throw new HttpError(400, MESSAGES.mustBeInteger(name));
  return parsed;
}

/**
 * Parses a numeric query parameter, clamped and floored; absent means 0.
 * @param {unknown} value
 * @param {number} low
 * @param {number} high
 * @param {string} name
 * @returns {number}
 * @throws {HttpError} 400 when present but not numeric.
 */
export function parseClampedNumber(value, low, high, name) {
  if (value === undefined || value === '') return 0;
  const parsed = Number(value);
  if (!Number.isFinite(parsed)) throw new HttpError(400, MESSAGES.mustBeNumber(name));
  return Math.floor(Math.min(high, Math.max(low, parsed)));
}

/**
 * @param {unknown} value
 * @returns {string} The validated player GUID.
 * @throws {HttpError} 400 when missing or malformed.
 */
export function requireGuid(value) {
  if (typeof value !== 'string' || !GUID_PATTERN.test(value)) throw new HttpError(400, MESSAGES.guidRequired);
  return value;
}

/**
 * @param {unknown} value
 * @returns {string} The validated log-file fingerprint.
 * @throws {HttpError} 400 when missing or malformed.
 */
export function requireFingerprint(value) {
  if (typeof value !== 'string' || !FINGERPRINT_PATTERN.test(value))
    throw new HttpError(400, MESSAGES.fingerprintRequired);
  return value;
}
