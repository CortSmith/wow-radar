/**
 * Bounty: ranking of contacts by killing blows (same rules as the Go app), plus the single-contact lookup.
 */
import { readAll } from './db.mjs';
import { contactPartition, CONTACT_PREFIX, guidFromSk } from './keys.mjs';
import { resolveRuleset } from './console-settings.mjs';
import { HttpError } from './http-error.mjs';
import { MESSAGES } from './messages.mjs';
import { requireGuid } from './validate.mjs';
import { BOUNTY_POINTS_PER_KILLING_BLOW, BOUNTY_POINTS_PER_ASSIST } from './config.mjs';

const compareText = (a, b) => (a < b ? -1 : a > b ? 1 : 0);

/**
 * Ranks contacts with at least one killing blow or assist.
 * Order: killing blows, assists, most recent activity, then guid (a total order, so ranks are stable).
 * @param {import('./types.mjs').Contact[]} items
 * @returns {Object[]} Entries with `rank` starting at 1.
 */
export function buildBountyBoard(items) {
  const entries = items
    .filter(item => item.kb > 0 || item.assists > 0)
    .map(item => ({
      guid: guidFromSk(item.SK),
      name: item.name || '',
      kb: item.kb || 0,
      assists: item.assists || 0,
      score: (item.kb || 0) * BOUNTY_POINTS_PER_KILLING_BLOW + (item.assists || 0) * BOUNTY_POINTS_PER_ASSIST,
      lastSeen: item.lastSeen || 0,
    }));
  entries.sort(
    (a, b) => b.kb - a.kb || b.assists - a.assists || b.lastSeen - a.lastSeen || compareText(a.guid, b.guid),
  );
  return entries.map(({ lastSeen, ...entry }, index) => ({ ...entry, rank: index + 1 }));
}

/** GET /api/bounty: ranks every contact of the ruleset (one partition query, cost grows with contact count). */
export async function handleBountyBoard({ user, query }) {
  const ruleset = await resolveRuleset(user, query.rs);
  return { entries: buildBountyBoard(await readAll(contactPartition(user, ruleset), CONTACT_PREFIX)) };
}

/**
 * GET /api/bounty/player: one contact's rank. It still ranks the whole ruleset, because rank depends on everyone.
 * @returns {Promise<Object>} The board entry with its rank.
 * @throws {HttpError} 400 for a bad guid/ruleset, 404 when the contact has no kills or assists.
 */
export async function handleBountyEntry({ user, query }) {
  const ruleset = await resolveRuleset(user, query.rs);
  const guid = requireGuid(query.guid);
  const entry = buildBountyBoard(await readAll(contactPartition(user, ruleset), CONTACT_PREFIX)).find(
    candidate => candidate.guid === guid,
  );
  if (!entry) throw new HttpError(404, MESSAGES.notOnBountyBoard);
  return entry;
}
