/**
 * Ledger: per log-file ingest watermark, so the client can resume a partly uploaded file instead of re-sending it.
 */
import { readItem, updateItem, isConditionFailure } from './db.mjs';
import { ledgerKey } from './keys.mjs';
import { resolveRuleset } from './console-settings.mjs';
import { requireFingerprint, int } from './validate.mjs';
import { TIMESTAMP_MAX_MS } from './config.mjs';

/**
 * Moves the watermark forward. It never moves backward (a stale or replayed final ping is harmless) and
 * must only be called after every contact write of the ping succeeded; otherwise a crash would make the
 * client skip data that was never stored.
 * @param {string} user
 * @param {string} ruleset
 * @param {string} fingerprint
 * @param {number} lastTs
 */
export async function advanceLedger(user, ruleset, fingerprint, lastTs) {
  try {
    await updateItem(
      ledgerKey(user, ruleset, fingerprint),
      'SET lastTs = :to',
      { ':to': int(lastTs, 0, TIMESTAMP_MAX_MS) },
      'attribute_not_exists(lastTs) OR lastTs < :to',
    );
  } catch (error) {
    if (!isConditionFailure(error)) throw error;
  }
}

/**
 * GET /api/ledger: where a log file was last ingested up to (milliseconds of game-log time, 0 when never),
 * so the client resumes instead of re-sending. One eventually consistent read.
 * @param {import('./types.mjs').RequestContext} context
 * @returns {Promise<{lastTs:number}>}
 * @throws {HttpError} 400 for a bad ruleset or fingerprint.
 */
export async function handleLedgerRead({ user, query }) {
  const ruleset = await resolveRuleset(user, query.rs);
  const fingerprint = requireFingerprint(query.fp);
  const item = await readItem(ledgerKey(user, ruleset, fingerprint));
  return { lastTs: item?.lastTs || 0 };
}
