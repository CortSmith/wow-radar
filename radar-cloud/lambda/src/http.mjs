/**
 * HTTP plumbing for Lambda Function URL events: reading the JSON request body and building JSON responses.
 * Knows nothing about routes or the domain.
 */
import { gzipSync, gunzipSync } from 'node:zlib';
import { HttpError } from './http-error.mjs';
import { MESSAGES } from './messages.mjs';
import { BODY_LIMIT_BYTES, INFLATED_BODY_LIMIT_BYTES, GZIP_RESPONSE_MIN_BYTES } from './config.mjs';

/**
 * Builds a JSON response. Large bodies are gzipped when the client accepts it
 * (the Function URL response cap is 6 MB and bandwidth is billed).
 * @param {number} status
 * @param {*} payload JSON-serialisable.
 * @param {Object} [event] Request event, used to read Accept-Encoding.
 * @returns {{statusCode:number, headers:Object, body:string, isBase64Encoded?:boolean}}
 */
export function jsonResponse(status, payload, event) {
  const body = JSON.stringify(payload);
  const shouldGzip = event?.headers?.['accept-encoding']?.includes('gzip') && body.length > GZIP_RESPONSE_MIN_BYTES;
  return {
    statusCode: status,
    headers: {
      'content-type': 'application/json',
      'cache-control': 'no-store',
      ...(shouldGzip && { 'content-encoding': 'gzip' }),
    },
    ...(shouldGzip ? { body: gzipSync(body).toString('base64'), isBase64Encoded: true } : { body }),
  };
}

/**
 * Converts any thrown value into the uniform error response `{ error }`.
 * Unexpected errors are logged (message and stack only, never request data) and masked as 500.
 * @param {unknown} error
 * @returns {ReturnType<typeof jsonResponse>}
 */
export function errorResponse(error) {
  if (error instanceof HttpError) return jsonResponse(error.status, { error: error.message });
  console.error(error);
  return jsonResponse(500, { error: MESSAGES.serverError });
}

/**
 * Reads and parses the JSON body, transparently inflating `content-encoding: gzip`.
 * @param {Object} event
 * @returns {*} Parsed JSON value.
 * @throws {HttpError} 413 when too large, 400 for bad gzip or bad JSON.
 */
export function readJsonBody(event) {
  let buffer = event.isBase64Encoded ? Buffer.from(event.body || '', 'base64') : Buffer.from(event.body || '');
  if (buffer.length > BODY_LIMIT_BYTES) throw new HttpError(413, MESSAGES.bodyTooLarge);
  if (/gzip/i.test(event.headers?.['content-encoding'] || '')) buffer = inflate(buffer);
  try {
    return JSON.parse(buffer.toString());
  } catch {
    throw new HttpError(400, MESSAGES.badJson);
  }
}

function inflate(buffer) {
  try {
    return gunzipSync(buffer, { maxOutputLength: INFLATED_BODY_LIMIT_BYTES });
  } catch {
    throw new HttpError(400, MESSAGES.badGzip);
  }
}
