/**
 * Catalogue of every user-facing error message the API can return, so wording and casing stay uniform
 * (full sentences, capitalised, ending in a period) and limits are built from config constants.
 * Paired with HttpError: `throw new HttpError(400, MESSAGES.badJson)`.
 */
import {
  RULESETS,
  SECTORS,
  REACTION_GROUPS,
  BEACON_MAX_OPERATORS,
  TIMEZONE_MAX_CHARS,
  USERNAME_MIN_CHARS,
  USERNAME_MAX_CHARS,
  PASSWORD_MIN_CHARS,
  CONSOLE_SETTINGS_MAX_BYTES,
  PING_MAX_CONTACTS,
  PING_MAX_CHARTS,
  PING_MAX_FIXES,
  PING_BATCH_ID_MAX_CHARS,
  BYTES_PER_KB,
} from './config.mjs';

const pipeList = values => values.join('|');

/** Error texts by name; entries that need a value are functions. */
export const MESSAGES = {
  // request plumbing
  notFound: 'Not found.',
  notLoggedIn: 'Not logged in.',
  bodyTooLarge: 'Body too large.',
  badJson: 'Bad JSON.',
  badGzip: 'Bad gzip.',
  serverError: 'Server error.',

  // operators and beacons
  invalidSignUp:
    `Username must be ${USERNAME_MIN_CHARS}-${USERNAME_MAX_CHARS} characters (A-Z a-z 0-9 _ . -), ` +
    `password at least ${PASSWORD_MIN_CHARS} characters, and an API key is required.`,
  missingFields: 'Missing fields.',
  usernameTaken: 'Username taken.',
  beaconInvalid: 'Invalid or inactive API key.',
  beaconFull: `This API key already has ${BEACON_MAX_OPERATORS} accounts.`,
  beaconInactive: 'API key is not active.',
  badCredentials: 'Invalid username, password or API key.',

  // console settings and rulesets
  rulesetInvalid: `The rs parameter must be one of ${pipeList(RULESETS)}.`,
  settingsRulesetInvalid: `The ruleset setting must be one of ${pipeList(RULESETS)}.`,
  settingsTimezoneInvalid: `The timezone setting must be an IANA time zone name such as UTC or America/New_York (at most ${TIMEZONE_MAX_CHARS} characters).`,
  settingsNotObject: 'Settings must be a JSON object.',
  settingsTooLarge: `Settings exceed ${CONSOLE_SETTINGS_MAX_BYTES / BYTES_PER_KB} KB.`,

  // pings (uploads)
  pingNeedsContacts: 'The players array is required.',
  pingTooManyContacts: `At most ${PING_MAX_CONTACTS} players per request.`,
  pingTooManyCharts: `At most ${PING_MAX_CHARTS} maps per request.`,
  pingTooManyFixes: `At most ${PING_MAX_FIXES} fixes per request.`,
  pingBatchIdInvalid: `A batchId of 1-${PING_BATCH_ID_MAX_CHARS} characters is required.`,
  pingFinalNeedsLedger: 'A final batch needs fp and to.',

  // queries
  badReaction: 'The reaction parameter is not a known reaction.',
  guidRequired: 'A valid guid is required.',
  fingerprintRequired: 'A valid fp is required.',
  mapIdRequired: 'A valid mapId is required.',
  sectorInvalid: `The scope parameter must be one of ${pipeList(SECTORS)}.`,
  groupInvalid: `The grp parameter must be one of ${pipeList([...REACTION_GROUPS, 'any'])}.`,
  resetScopeInvalid: "The what parameter must be 'bounty' or 'all'.",
  notOnBountyBoard: 'Player is not on the bounty board.',
  mustBeInteger: name => `The ${name} parameter must be an integer.`,
  mustBeNumber: name => `The ${name} parameter must be a number.`,
};
