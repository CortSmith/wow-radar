/**
 * Tripwire: the cheap "did anything change" signal for sweeps (the VER item), plus the zone-idx GSI keys
 * stamped on contacts. Writers trip the wire after changing data; readers compare against it before
 * paying for a real query.
 */
import { readItem, updateItem } from './db.mjs';
import { tripwireKey, zoneIndexKey, tripwireZoneField } from './keys.mjs';

/** Maps a reaction to its zone-idx group: friendly and hostile stay separate, everything else is "other". */
export const reactionGroup = reaction => (reaction === 'friendly' || reaction === 'hostile' ? reaction : 'other');

/**
 * Stamps a contact item before writing it.
 * `ut` (server time of the last write) is the per-contact sweep gate. `zk`/`ls` are the GSI keys and exist only
 * while a position is known, so contacts without one stay out of the index (sparse index = cheaper).
 * Server time is used on purpose: sweep `since` values are server time too, so client clocks never matter.
 * @param {Object} item Contact item (mutated and returned).
 * @param {string} user
 * @param {string} ruleset
 * @param {number} now Server time in ms.
 */
export function stampContact(item, user, ruleset, now) {
  item.ut = now;
  if (item.mapId != null) {
    item.zk = zoneIndexKey(user, ruleset, item.mapId, reactionGroup(item.reaction));
    item.ls = now;
  } else {
    delete item.zk;
    delete item.ls;
  }
  return item;
}

/**
 * Trips the wire: bumps the global counter and records the write time of each touched map,
 * so a sweep of an untouched map can stop after one read.
 * @param {string} user
 * @param {string} ruleset
 * @param {number} now Server time in ms.
 * @param {Iterable<number>} mapIds Maps whose contacts changed (may be empty).
 */
export async function tripTripwire(user, ruleset, now, mapIds) {
  const fields = [...mapIds].map(tripwireZoneField);
  const setClause = fields.length ? ' SET ' + fields.map(field => `${field} = :now`).join(', ') : '';
  await updateItem(tripwireKey(user, ruleset), 'ADD v :one' + setClause, {
    ':one': 1,
    ...(fields.length && { ':now': now }),
  });
}

/**
 * Reads the tripwire with an eventually consistent read (half price; a one-sweep delay is acceptable).
 * @param {string} user
 * @param {string} ruleset
 * @param {string} [zoneField] Also project this map's write-time attribute.
 * @returns {Promise<import('./types.mjs').Tripwire>} {} when nothing was ever written.
 */
export async function readTripwire(user, ruleset, zoneField) {
  const item = await readItem(tripwireKey(user, ruleset), { projection: zoneField ? `v, ${zoneField}` : 'v' });
  return item || {};
}
