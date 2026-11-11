/**
 * Console settings: the per-operator free-form UI settings object, the /api/me summary, and ruleset
 * resolution (an explicit `rs` wins; otherwise the operator's saved ruleset; otherwise 'normal').
 */
import { readItem, writeItem } from './db.mjs';
import { consoleKey } from './keys.mjs';
import { HttpError } from './http-error.mjs';
import { MESSAGES } from './messages.mjs';
import { RULESETS, DEFAULT_RULESET, CONSOLE_SETTINGS_MAX_BYTES } from './config.mjs';
import { isBlank, isPlainObject, isTimeZoneName } from './validate.mjs';

/**
 * @param {string} user
 * @returns {Promise<Object>} The saved settings object, or {} when none/invalid.
 */
export async function readConsole(user) {
  const item = await readItem(consoleKey(user));
  return item?.v && typeof item.v === 'object' ? item.v : {};
}

/**
 * Resolves the ruleset for a request.
 * @param {string} user
 * @param {unknown} explicit The `rs` query/body value; blank means "use the default".
 * @returns {Promise<string>} One of RULESETS.
 * @throws {HttpError} 400 when an explicit value is not a known ruleset.
 */
export async function resolveRuleset(user, explicit) {
  if (isBlank(explicit)) {
    const saved = (await readConsole(user)).ruleset;
    return RULESETS.includes(saved) ? saved : DEFAULT_RULESET;
  }
  if (!RULESETS.includes(explicit)) throw new HttpError(400, MESSAGES.rulesetInvalid);
  return explicit;
}

/** GET /api/me: the caller's name, saved settings and the valid rulesets, in one read. */
export async function handleMe({ user }) {
  return { username: user, settings: await readConsole(user), rulesets: RULESETS };
}

/** GET /api/settings: the saved settings object, or {} when none was ever saved. */
export const handleConsoleRead = ({ user }) => readConsole(user);

/**
 * PUT /api/settings: replaces the whole settings document (last write wins; it is not merged).
 * @returns {Promise<{ok:true}>}
 * An optional `timezone` (IANA name, 'UTC' allowed) is display-only: stored times stay UTC epoch milliseconds.
 * @throws {HttpError} 400 for a non-object body, unknown ruleset or unknown time zone, 413 when over the size cap.
 */
export async function handleConsoleWrite({ user, body }) {
  if (!isPlainObject(body)) throw new HttpError(400, MESSAGES.settingsNotObject);
  if (Buffer.byteLength(JSON.stringify(body)) > CONSOLE_SETTINGS_MAX_BYTES)
    throw new HttpError(413, MESSAGES.settingsTooLarge);
  if (body.ruleset !== undefined && !RULESETS.includes(body.ruleset))
    throw new HttpError(400, MESSAGES.settingsRulesetInvalid);
  if (body.timezone !== undefined && !isTimeZoneName(body.timezone))
    throw new HttpError(400, MESSAGES.settingsTimezoneInvalid);
  await writeItem({ ...consoleKey(user), v: body, ts: Date.now() });
  return { ok: true };
}
