'use strict';
/**
 * types.js - single responsibility: the one place where the frontend's data shapes are documented (JSDoc typedefs).
 *
 * Documentation only: no code runs here and index.html does not load it. Editors that understand JSDoc pick the
 * typedefs up for completion; other modules refer to them by name (`@param {Fix[]} fixes`).
 * Wire names (pips, lastSeen, mapId, guid, rs, ...) are frozen by the backend and the Go app and are kept as they are;
 * the radar vocabulary of NAMING.md names the concepts (Contact = a "player" row, Blip = a "pip", Ping = an upload
 * batch,
 * Sector = a sweep scope).
 */

/**
 * A tracked player as stored in the cloud (a row of GET /api/players or a sweep delta). Alias: Player.
 * @typedef {Object} Contact
 * @property {string} guid          WoW guid, "Player-<realm>-<id>"
 * @property {string} name          display name (never contains markup assumptions: always render as text)
 * @property {"friendly"|"hostile"|"neutral"|"unknown"} reaction
 * @property {number} level
 * @property {number} lastSeen      unix ms of the newest event
 * @property {number} [lastX]       last exact position (world yards); with lastY, lastMap, lastZone
 * @property {number} [lastY]
 * @property {number} [lastMap]     uiMapID of the chart the last fix is on
 * @property {string} [lastZone]
 * @property {number} [maxHp]
 * @property {number} [events]      event count (history table "Samples")
 */

/** @typedef {Contact} Player  Alias kept for readers who know the user-facing word. */

/**
 * A contact as the radar draws it: one entry of Snapshot.pips (Go /api/state shape). Alias: Pip.
 * @typedef {Object} Blip
 * @property {string} guid
 * @property {string} name
 * @property {"player"|"pet"|"npc"} kind
 * @property {"friendly"|"hostile"|"neutral"|"unknown"} reaction
 * @property {"active"|"stale"} state    stale after config.engine.STALE_MS without an event
 * @property {boolean} me                the operator's own character
 * @property {number} x
 * @property {number} y
 * @property {number} mapId
 * @property {"exact"|"approx"|"none"} posKind
 * @property {number} hp
 * @property {number} maxHp
 * @property {boolean} hasHp
 * @property {number} level
 * @property {number} ageSec             seconds since the last event
 * @property {{x:number,y:number,spread:number,mapId:number}|null} est   position estimate with its spread in yards
 */

/** @typedef {Blip} Pip  Alias: the wire name of a Blip. */

/**
 * What the radar shows: from the live engine (snap message) or built from cloud rows (contacts.cloudSnapshot).
 * @typedef {Object} Snapshot
 * @property {number} now
 * @property {string} zone               zone name the game reports ("" for cloud snapshots)
 * @property {number} mapId              chart of the operator (live) or the busiest chart (cloud)
 * @property {Blip|null} me
 * @property {Blip[]} pips
 * @property {Object<string,{name:string,x0:number,x1:number,y0:number,y1:number}>} maps   charts by uiMapID
 * @property {boolean|null} advanced     false when advanced combat logging is off (no positions)
 * @property {boolean} [cloud]           true for snapshots built from cloud rows
 * @property {{events:number,units:number,sources:number}} stats
 * @property {{detected:string|null,active:string,mode:string}} [ruleset]   live only
 * @property {{orientation:string,zone:Object|null,available:Object[],noImage:Object[]}} [view]   added by
 * view-controller.js
 */

/**
 * One position of a contact. Wire form in uploads is a tuple [t, mapId, x, y, kind]; the API returns objects.
 * @typedef {Object} Fix
 * @property {number} t                  unix ms
 * @property {number} mapId
 * @property {number} x
 * @property {number} y
 * @property {"exact"|"approx"} kind
 * @property {string} [zone]
 */

/**
 * One flushed engine batch for one rule-set, uploaded as several POST /api/upload requests. Alias: UploadBatch.
 * Resumable: a failed upload keeps batchId and `next`, so the retry reuses the same batchId (the server skips
 * contacts it has).
 * @typedef {Object} Ping
 * @property {string} rs                 rule-set id: normal | pvp | rp | rppvp
 * @property {number} from               ledger watermark before this batch (unix ms)
 * @property {number} to                 newest event time in this batch; becomes the new watermark
 * @property {Object[]} players          contact deltas {guid,name,reaction,level,firstSeen,lastSeen,d,fixes,...}
 * @property {Object[]} maps             chart rectangles seen in the log
 * @property {string} [batchId]          assigned on the first upload attempt
 * @property {Object[][]} [chunks]       players split into request-sized slices
 * @property {number} [next]             index of the next unsent chunk
 * @property {number} [received]         contacts the server received so far
 * @property {number} [changed]          contacts that were new or updated
 */

/** @typedef {Ping} UploadBatch  Alias: the plain-language name of a Ping. */

/**
 * What one sweep (GET /api/poll) looks at. Alias: PollScope (the wire name of the scope).
 * @typedef {Object} Sector
 * @property {"player"|"zone"|"all"} scope   a contact's trail < a chart (+group) < everything
 * @property {string} rs
 * @property {string} [guid]             scope "player"
 * @property {number} [track]            scope "player": 1 when trail fixes are wanted
 * @property {number} [mapId]            scope "zone"
 * @property {"any"|"friendly"|"hostile"} [grp]   scope "zone"
 */

/** @typedef {Sector} PollScope  Alias: the wire name of a Sector. */

/**
 * Per-operator console settings (GET/PUT /api/settings). Calibration is per chart: world offset and scale.
 * @typedef {Object} Settings
 * @property {"auto"|"manual"} mode
 * @property {string} ruleset            rule-set used when mode is "manual"
 * @property {Object<string,{dx:number,dy:number,sx:number,sy:number}>} cal   by uiMapID
 * @property {Object<string,boolean|number>} prefs   follow, stale, bg, fit, showBlue, showRed, range
 * @property {number} uploadSec          live auto-upload interval
 * @property {string} [timezone]         IANA zone (or 'UTC') every time is displayed in; absent = the browser's zone.
 *                                       Display only: stored times are UTC epoch milliseconds.
 */
