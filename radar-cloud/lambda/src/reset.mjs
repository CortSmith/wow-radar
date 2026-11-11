/**
 * POST /api/reset: wipes a ruleset's bounty counters or all of its tracked data.
 * Charts (map calibration) are always kept; the ledger is always cleared so the client re-uploads cleanly.
 */
import { readAll, writeItems, dropItems } from './db.mjs';
import { contactPartition, trailPartition, ledgerPartition, tripwireKey, CONTACT_PREFIX } from './keys.mjs';
import { resolveRuleset } from './console-settings.mjs';
import { tripTripwire } from './tripwire.mjs';
import { HttpError } from './http-error.mjs';
import { MESSAGES } from './messages.mjs';

const RESET_SCOPES = ['bounty', 'all'];

async function resetBounty(user, ruleset, contacts) {
  await writeItems(contacts.filter(item => item.kb || item.assists).map(item => ({ ...item, kb: 0, assists: 0 })));
  // Sweeps must notice the change, so trip the wire (no map was touched: counter only).
  await tripTripwire(user, ruleset, Date.now(), []);
}

async function resetAll(user, ruleset, contacts) {
  await dropItems(contacts);
  await dropItems(await readAll(trailPartition(user, ruleset)));
  await dropItems([tripwireKey(user, ruleset)]);
}

/**
 * POST /api/reset: `what` is "bounty" (zero kills and assists) or "all" (delete contacts, trails and tripwire).
 * Deletes are batched writes, so the cost scales with the amount of data removed.
 * @param {import('./types.mjs').RequestContext} context
 * @returns {Promise<{ok:true, what:string, rs:string}>}
 * @throws {HttpError} 400 for an unknown `what` or ruleset.
 */
export async function handleReset({ user, body }) {
  if (!body || !RESET_SCOPES.includes(body.what)) throw new HttpError(400, MESSAGES.resetScopeInvalid);
  const ruleset = await resolveRuleset(user, body.rs);
  const contacts = await readAll(contactPartition(user, ruleset), CONTACT_PREFIX);
  if (body.what === 'all') await resetAll(user, ruleset, contacts);
  else await resetBounty(user, ruleset, contacts);
  await dropItems(await readAll(ledgerPartition(user, ruleset)));
  return { ok: true, what: body.what, rs: ruleset };
}
