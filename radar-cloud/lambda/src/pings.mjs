/**
 * POST /api/upload: applies a ping (one uploaded batch of contacts, trail fixes and charts).
 *
 * Idempotency is the central design goal because the client retries freely:
 *  - a contact already stamped with this ping's id (`lb`) is skipped, so counters never double count;
 *  - fixes are watermarked by the stored last fix (see trails.mjs);
 *  - unchanged contacts are not rewritten (cost: no write, and no tripwire trip, so sweeps stay quiet);
 *  - the ledger moves last, only after every write succeeded.
 */
import { readItems, writeItems } from './db.mjs';
import { contactPartition, trailPartition, contactSk } from './keys.mjs';
import { resolveRuleset } from './console-settings.mjs';
import { parseContact, mergeContact, isSameContact } from './contact-merge.mjs';
import { parseCharts, isChartChanged } from './charts.mjs';
import { planTrailAppend, trimTrail } from './trails.mjs';
import { stampContact, tripTripwire } from './tripwire.mjs';
import { advanceLedger } from './ledger.mjs';
import { HttpError } from './http-error.mjs';
import { MESSAGES } from './messages.mjs';
import { splitIntoChunks } from './utils.mjs';
import { isFiniteNumber, isPlainObject } from './validate.mjs';
import {
  PING_MAX_CONTACTS,
  PING_MAX_CHARTS,
  PING_MAX_FIXES,
  PING_BATCH_ID_PATTERN,
  FINGERPRINT_PATTERN,
  TRAIL_PARALLEL_COUNT,
} from './config.mjs';

function assertValidPing(body) {
  if (!isPlainObject(body) || !Array.isArray(body.players)) throw new HttpError(400, MESSAGES.pingNeedsContacts);
  if (body.players.length > PING_MAX_CONTACTS) throw new HttpError(400, MESSAGES.pingTooManyContacts);
  if (body.maps !== undefined && (!Array.isArray(body.maps) || body.maps.length > PING_MAX_CHARTS))
    throw new HttpError(400, MESSAGES.pingTooManyCharts);
  if (typeof body.batchId !== 'string' || !PING_BATCH_ID_PATTERN.test(body.batchId))
    throw new HttpError(400, MESSAGES.pingBatchIdInvalid);
  const hasValidLedger = typeof body.fp === 'string' && FINGERPRINT_PATTERN.test(body.fp) && isFiniteNumber(body.to);
  if (body.final && !hasValidLedger) throw new HttpError(400, MESSAGES.pingFinalNeedsLedger);
}

/** Validates the contacts; malformed ones are skipped, a repeated guid keeps the last entry. */
function parseContacts(rawContacts) {
  const contactsByGuid = new Map();
  let fixCount = 0;
  for (const raw of rawContacts) {
    const contact = parseContact(raw);
    if (!contact) continue;
    fixCount += contact.fixes.length;
    contactsByGuid.set(contact.guid, contact);
  }
  if (fixCount > PING_MAX_FIXES) throw new HttpError(400, MESSAGES.pingTooManyFixes);
  return contactsByGuid;
}

/** Pairs each incoming contact with its merged result, skipping contacts this ping already applied. */
function planMerges(contactsByGuid, storedBySk, pingId) {
  const plans = [];
  for (const [guid, incoming] of contactsByGuid) {
    const stored = storedBySk.get(contactSk(guid));
    if (stored && stored.lb === pingId) continue; // replayed ping: already applied to this contact
    plans.push({ guid, stored, incoming, merged: mergeContact(stored, incoming, pingId) });
  }
  return plans;
}

/**
 * Appends fixes first and the contact item last: a crash between the two is retried safely because the
 * stored last fix acts as a watermark, whereas the reverse order could lose fixes forever.
 * Mutates `plan.merged.fixes` with the new trail length.
 */
async function writeTrails(partition, plans) {
  const withFixes = plans.filter(plan => plan.incoming.fixes.length);
  const chunks = [];
  for (const group of splitIntoChunks(withFixes, TRAIL_PARALLEL_COUNT)) {
    await Promise.all(
      group.map(async plan => {
        const { chunks: planned, added } = await planTrailAppend(partition, plan.guid, plan.incoming.fixes);
        chunks.push(...planned);
        plan.merged.fixes = (plan.stored?.fixes || 0) + added;
      }),
    );
  }
  if (chunks.length) await writeItems(chunks);
  for (const plan of withFixes) plan.merged.fixes = await trimTrail(partition, plan.guid, plan.merged.fixes);
}

/**
 * POST /api/upload: applies one ping. Retrying the same ping is safe and cheap (see the module header).
 * @param {import('./types.mjs').RequestContext} context
 * @returns {Promise<{received:number, changed:number}>}
 * @throws {HttpError} 400 for any invalid field or exceeded limit.
 */
export async function handlePing({ user, body }) {
  assertValidPing(body);
  const ruleset = await resolveRuleset(user, body.rs);
  const partition = contactPartition(user, ruleset);
  const trails = trailPartition(user, ruleset);

  const contactsByGuid = parseContacts(body.players);
  const charts = parseCharts(body.maps || [], partition);
  const storedBySk = await readItems(partition, [
    ...[...contactsByGuid.keys()].map(contactSk),
    ...charts.map(chart => chart.SK),
  ]);

  const plans = planMerges(contactsByGuid, storedBySk, body.batchId);
  await writeTrails(trails, plans);

  // Server clock on purpose: `ut`/`ls` and the tripwire must share the clock that sweep `since` values use.
  const now = Date.now();
  const contactWrites = plans
    .filter(plan => !isSameContact(plan.stored, plan.merged))
    .map(plan => stampContact({ PK: partition, SK: contactSk(plan.guid), ...plan.merged }, user, ruleset, now));
  const chartWrites = charts.filter(chart => isChartChanged(storedBySk.get(chart.SK), chart));
  await writeItems([...contactWrites, ...chartWrites]);

  const changed = contactWrites.length + chartWrites.length;
  if (changed)
    await tripTripwire(
      user,
      ruleset,
      now,
      new Set(contactWrites.filter(item => item.mapId != null).map(item => item.mapId)),
    );
  if (body.final) await advanceLedger(user, ruleset, body.fp, body.to);
  return { received: contactsByGuid.size, changed };
}
