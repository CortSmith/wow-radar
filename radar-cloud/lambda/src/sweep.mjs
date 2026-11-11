/**
 * GET /api/poll: a sweep, i.e. one sweep cycle from the page, cheapest-first change detection.
 *
 * A sweep looks at one sector (see config SECTORS):
 *  - player: one GetItem on the contact, gated by its `ut`;
 *  - zone:   one tripwire read, then GSI queries (only the requested reaction groups) if that map changed;
 *  - all:    one tripwire read, then one partition query if the global counter moved.
 * Unchanged sweeps therefore cost exactly one read. All reads are eventually consistent (half price).
 * Clients re-send `since` with a small overlap, which absorbs GSI replication lag.
 */
import { readItem, readEdge, queryZoneIndex } from './db.mjs';
import {
  contactPartition,
  contactSk,
  guidFromSk,
  trailPartition,
  trailPrefix,
  zoneIndexKey,
  tripwireZoneField,
} from './keys.mjs';
import { resolveRuleset } from './console-settings.mjs';
import { listContacts } from './contacts.mjs';
import { toContactRow, toSectorRow } from './contact-rows.mjs';
import { toFixRow } from './trails.mjs';
import { readTripwire } from './tripwire.mjs';
import { HttpError } from './http-error.mjs';
import { MESSAGES } from './messages.mjs';
import { isBlank, parseClampedNumber, requireGuid } from './validate.mjs';
import { SECTORS, REACTION_GROUPS, ANY_GROUP, TIMESTAMP_MAX_MS, MAP_ID_MAX, CLIENT_TRIPWIRE_MAX } from './config.mjs';

/** Fix tail of a contact newer than `trailSince`; reads only the newest trail chunk (one item). */
async function readTrailTail(user, ruleset, contact, trailSince) {
  const newest = await readEdge(trailPartition(user, ruleset), trailPrefix(guidFromSk(contact.SK)), false);
  return (newest?.f || [])
    .filter(fix => fix[0] > trailSince)
    .map(fix => toFixRow(fix, contact.mapId === fix[1] ? contact.zone || '' : ''));
}

async function sweepPlayer({ user, ruleset, query, now, since, clientTripwire }) {
  const guid = requireGuid(query.guid);
  const contact = await readItem({ PK: contactPartition(user, ruleset), SK: contactSk(guid) });
  const tripwire = clientTripwire ?? 0;
  if (!contact || (since > 0 && !(contact.ut > since))) return { changed: false, v: tripwire, now };
  const result = { changed: true, v: tripwire, now, player: toContactRow(contact) };
  if (query.track === '1') {
    const trailSince = parseClampedNumber(query.tsince, 0, TIMESTAMP_MAX_MS, 'tsince');
    result.fixes = await readTrailTail(user, ruleset, contact, trailSince);
  }
  return result;
}

async function sweepAll({ user, ruleset, now, since, clientTripwire }) {
  const { v = 0 } = await readTripwire(user, ruleset);
  if (since > 0 && clientTripwire !== null && clientTripwire === v) return { changed: false, v, now };
  return { changed: true, v, now, ...(await listContacts(user, ruleset)) };
}

/** Validates the zone-only parameters; done before any read so bad input costs nothing. */
function parseZoneParams(query) {
  const mapId = Number(query.mapId);
  if (isBlank(query.mapId) || !Number.isInteger(mapId) || mapId < 0 || mapId > MAP_ID_MAX)
    throw new HttpError(400, MESSAGES.mapIdRequired);
  const group = query.grp;
  if (!isBlank(group) && ![...REACTION_GROUPS, ANY_GROUP].includes(group))
    throw new HttpError(400, MESSAGES.groupInvalid);
  const groups = !group || group === ANY_GROUP ? REACTION_GROUPS : [group];
  return { mapId, groups };
}

async function sweepZone({ user, ruleset, query, now, since }) {
  const { mapId, groups } = parseZoneParams(query);
  const zoneField = tripwireZoneField(mapId);
  const tripwire = await readTripwire(user, ruleset, zoneField);
  const v = tripwire.v || 0;
  const lastWrite = tripwire[zoneField];
  if (since > 0 && !(lastWrite > since)) return { changed: false, v, now };
  const rows = [];
  // A map that was never written has no index entries: skip the queries entirely.
  if (lastWrite !== undefined) {
    for (const group of groups) {
      const items = await queryZoneIndex(zoneIndexKey(user, ruleset, mapId, group), since);
      rows.push(...items.map(toSectorRow));
    }
  }
  return { changed: true, v, now, players: rows };
}

const SECTOR_SWEEPS = { player: sweepPlayer, zone: sweepZone, all: sweepAll };

/**
 * GET /api/poll (frozen wire name): answers "anything new since `since`?" for one sector. `v` is the tripwire
 * counter the client last saw; `since` and the returned `now` are server milliseconds. Costs one eventually
 * consistent read when nothing changed, otherwise the sector query.
 * @param {import('./types.mjs').RequestContext} context
 * @throws {HttpError} 400 for a bad ruleset, sector, numeric parameter, guid or map.
 */
export async function handleSweep({ user, query }) {
  const now = Date.now();
  const ruleset = await resolveRuleset(user, query.rs);
  if (!SECTORS.includes(query.scope)) throw new HttpError(400, MESSAGES.sectorInvalid);
  const since = parseClampedNumber(query.since, 0, now, 'since');
  const clientTripwire = isBlank(query.v) ? null : parseClampedNumber(query.v, 0, CLIENT_TRIPWIRE_MAX, 'v');
  return SECTOR_SWEEPS[query.scope]({ user, ruleset, query, now, since, clientTripwire });
}
