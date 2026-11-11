/**
 * GET /api/players: lists a ruleset's contacts and charts with optional filters (map, reaction, name search).
 */
import { readAll } from './db.mjs';
import { contactPartition, CONTACT_PREFIX, CHART_PREFIX } from './keys.mjs';
import { resolveRuleset } from './console-settings.mjs';
import { toContactRow } from './contact-rows.mjs';
import { toChartRow } from './charts.mjs';
import { HttpError } from './http-error.mjs';
import { MESSAGES } from './messages.mjs';
import { parseOptionalInteger } from './validate.mjs';
import { REACTIONS, NAME_MAX_CHARS } from './config.mjs';

function parseFilters(query) {
  const mapId = parseOptionalInteger(query.mapId, 'mapId');
  if (query.reaction && !REACTIONS.includes(query.reaction)) throw new HttpError(400, MESSAGES.badReaction);
  const needle = typeof query.q === 'string' ? query.q.trim().slice(0, NAME_MAX_CHARS).toLowerCase() : '';
  return { mapId, reaction: query.reaction, needle };
}

function matchesName(item, needle) {
  return (
    (item.name || '').toLowerCase().includes(needle) ||
    (item.pastNames || []).some(name => name.toLowerCase().includes(needle))
  );
}

function matchesFilters(item, { mapId, reaction, needle }) {
  if (mapId !== undefined && item.mapId !== mapId) return false;
  if (reaction && item.reaction !== reaction) return false;
  return !needle || matchesName(item, needle);
}

/**
 * Reads all contacts and charts of a user's ruleset (one partition query).
 * @param {string} user
 * @param {string} ruleset
 * @param {{mapId?:number, reaction?:string, needle?:string}} [filters]
 * @returns {Promise<{players:Object[], maps:Object[]}>}
 */
export async function listContacts(user, ruleset, filters = {}) {
  const result = { players: [], maps: [] };
  for (const item of await readAll(contactPartition(user, ruleset))) {
    if (item.SK.startsWith(CHART_PREFIX)) result.maps.push(toChartRow(item));
    else if (item.SK.startsWith(CONTACT_PREFIX) && matchesFilters(item, filters))
      result.players.push(toContactRow(item));
  }
  return result;
}

/**
 * GET /api/players: filters run in memory after one partition query, so cost does not depend on the filters.
 * @param {import('./types.mjs').RequestContext} context
 * @throws {HttpError} 400 for a bad ruleset, mapId or reaction.
 */
export async function handleContactList({ user, query }) {
  const ruleset = await resolveRuleset(user, query.rs);
  return listContacts(user, ruleset, parseFilters(query));
}
