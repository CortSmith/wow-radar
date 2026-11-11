/**
 * Lambda entry point: a thin router. Request flow (see ARCHITECTURE.md):
 *   parse JSON body -> look up route -> authenticate (unless public) -> run handler -> JSON response.
 * Handlers return a payload or throw HttpError; this file is the only place that turns either into HTTP.
 */
import { readJsonBody, jsonResponse, errorResponse } from './src/http.mjs';
import { HttpError } from './src/http-error.mjs';
import { MESSAGES } from './src/messages.mjs';
import { verifyPass } from './src/pass.mjs';
import { signUpOperator, logInOperator } from './src/operators.mjs';
import { handleMe, handleConsoleRead, handleConsoleWrite } from './src/console-settings.mjs';
import { handleLedgerRead } from './src/ledger.mjs';
import { handleContactList } from './src/contacts.mjs';
import { handleSweep } from './src/sweep.mjs';
import { handleTrack } from './src/trails.mjs';
import { handleBountyBoard, handleBountyEntry } from './src/bounty.mjs';
import { handlePing } from './src/pings.mjs';
import { handleReset } from './src/reset.mjs';

/** Methods whose body is parsed (before routing, so malformed JSON is a 400 even on unknown paths). */
const BODY_METHODS = new Set(['POST', 'PUT']);

/** "METHOD /path" -> handler. Public routes skip authentication. These paths are the frozen wire contract. */
const ROUTES = new Map([
  ['POST /api/signup', { handle: signUpOperator, isPublic: true }],
  ['POST /api/login', { handle: logInOperator, isPublic: true }],
  ['GET /api/me', { handle: handleMe }],
  ['GET /api/settings', { handle: handleConsoleRead }],
  ['PUT /api/settings', { handle: handleConsoleWrite }],
  ['GET /api/ledger', { handle: handleLedgerRead }],
  ['GET /api/players', { handle: handleContactList }],
  ['GET /api/poll', { handle: handleSweep }],
  ['GET /api/track', { handle: handleTrack }],
  ['GET /api/bounty', { handle: handleBountyBoard }],
  ['GET /api/bounty/player', { handle: handleBountyEntry }],
  ['POST /api/upload', { handle: handlePing }],
  ['POST /api/reset', { handle: handleReset }],
]);

async function dispatch(event) {
  const method = event.requestContext?.http?.method;
  const body = BODY_METHODS.has(method) ? readJsonBody(event) : undefined;
  const route = ROUTES.get(`${method} ${event.rawPath}`);
  const context = { query: event.queryStringParameters || {}, body, event };
  if (route?.isPublic) return route.handle(context);
  // Authenticate before revealing whether a route exists, so anonymous callers learn nothing about the API.
  context.user = await verifyPass(event);
  if (!context.user) throw new HttpError(401, MESSAGES.notLoggedIn);
  if (!route) throw new HttpError(404, MESSAGES.notFound);
  return route.handle(context);
}

/**
 * Lambda handler (Function URL payload v2).
 * @param {Object} event
 * @returns {Promise<{statusCode:number, headers:Object, body:string, isBase64Encoded?:boolean}>}
 */
export const handler = async event => {
  try {
    return jsonResponse(200, await dispatch(event), event);
  } catch (error) {
    return errorResponse(error);
  }
};
