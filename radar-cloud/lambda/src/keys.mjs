/**
 * The only place that builds DynamoDB keys. Key formats are FROZEN (stored data depends on them).
 *
 * | Item            | PK                   | SK                  | Notes                                   |
 * |-----------------|----------------------|---------------------|-----------------------------------------|
 * | signing secret  | CFG                  | SECRET              | one per deployment                      |
 * | beacon (API key)| K#<sha256(key)>      | KEY                 | active, cnt (operator count)            |
 * | operator        | U#<username>         | PROFILE             | salt, hash, kh                          |
 * | console settings| U#<username>         | SETTINGS            | v = free-form object                    |
 * | contact         | P#<user>#<ruleset>   | G#<guid>            | zk/ls = GSI keys (only when positioned) |
 * | chart (map)     | P#<user>#<ruleset>   | M#<mapId>           | calibration rectangle                   |
 * | trail chunk     | T#<user>#<ruleset>   | <guid>#<seq:6>      | f = fixes, n = count                    |
 * | tripwire        | V#<user>#<ruleset>   | VER                 | v counter + z<mapId> = last write time  |
 * | ledger          | L#<user>#<ruleset>   | <fingerprint>       | lastTs watermark                        |
 * | zone-idx GSI    | zk = <user>#<ruleset>#<mapId>#<group>        | ls = last-seen write time (number)      |
 */
import { int } from './validate.mjs';
import { MAP_ID_MAX, TRAIL_SEQ_DIGITS } from './config.mjs';

/** SK prefix of contact items. */
export const CONTACT_PREFIX = 'G#';
/** SK prefix of chart items. */
export const CHART_PREFIX = 'M#';

const scoped = (kind, user, ruleset) => `${kind}#${user}#${ruleset}`;

/** @returns {{PK:string, SK:string}} Key of the HMAC signing secret item. */
export const secretKey = () => ({ PK: 'CFG', SK: 'SECRET' });
/** @param {string} keyHash SHA-256 hex of the API key. @returns {{PK:string, SK:string}} Beacon key. */
export const beaconKey = keyHash => ({ PK: `K#${keyHash}`, SK: 'KEY' });
/** @param {string} username Lower-cased name. @returns {{PK:string, SK:string}} Operator profile key. */
export const operatorKey = username => ({ PK: `U#${username}`, SK: 'PROFILE' });
/** @param {string} username Lower-cased name. @returns {{PK:string, SK:string}} Console settings key. */
export const consoleKey = username => ({ PK: `U#${username}`, SK: 'SETTINGS' });

/** @returns {string} Partition holding a ruleset's contacts and charts. */
export const contactPartition = (user, ruleset) => scoped('P', user, ruleset);
/** @returns {string} Partition holding a ruleset's trail chunks. */
export const trailPartition = (user, ruleset) => scoped('T', user, ruleset);
/** @returns {{PK:string, SK:string}} Key of the tripwire (version) item. */
export const tripwireKey = (user, ruleset) => ({ PK: scoped('V', user, ruleset), SK: 'VER' });
/** @returns {string} Partition holding a ruleset's ledger entries. */
export const ledgerPartition = (user, ruleset) => scoped('L', user, ruleset);
/** @returns {{PK:string, SK:string}} Key of one log file's ledger entry. */
export const ledgerKey = (user, ruleset, fingerprint) => ({ PK: ledgerPartition(user, ruleset), SK: fingerprint });

/** @param {string} guid @returns {string} Sort key of a contact. */
export const contactSk = guid => CONTACT_PREFIX + guid;
/** @param {string} sk Contact sort key. @returns {string} The guid. */
export const guidFromSk = sk => sk.slice(CONTACT_PREFIX.length);
/** @param {number} mapId @returns {string} Sort key of a chart. */
export const chartSk = mapId => CHART_PREFIX + mapId;
/** @param {string} sk Chart sort key. @returns {number} The map id. */
export const mapIdFromSk = sk => +sk.slice(CHART_PREFIX.length);

/** @returns {string} SK prefix shared by all trail chunks of a contact (chunks sort by padded sequence). */
export const trailPrefix = guid => `${guid}#`;
/** @returns {string} Sort key of trail chunk number `seq` of a contact. */
export const trailSk = (guid, seq) => `${guid}#${String(seq).padStart(TRAIL_SEQ_DIGITS, '0')}`;
/** @param {{SK:string}} item Trail chunk. @returns {number} Its sequence number. */
export const trailSeq = item => +item.SK.slice(item.SK.lastIndexOf('#') + 1);

/** @returns {string} Partition key of the zone-idx GSI: one per (user, ruleset, map, reaction group). */
export const zoneIndexKey = (user, ruleset, mapId, group) => `${user}#${ruleset}#${mapId}#${group}`;
/** @returns {string} Attribute on the tripwire item holding the last write time of one map. */
export const tripwireZoneField = mapId => `z${int(mapId, 0, MAP_ID_MAX)}`;
