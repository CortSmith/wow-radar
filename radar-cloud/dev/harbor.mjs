/**
 * Shared constants and helpers for the local dev stack ("harbor"): used by server.mjs and smoke.mjs.
 * Keep dev-only credentials here so the server that seeds them and the smoke test that uses them cannot drift.
 */
import { createHash } from 'node:crypto';

/** Port the local server listens on unless --port is given. */
export const DEFAULT_PORT = 8787;
/** Simulated network latency added to every API call unless --latency is given. */
export const DEFAULT_LATENCY_MS = 30;
/** Base URL the smoke test targets by default. */
export const LOCAL_URL = `http://127.0.0.1:${DEFAULT_PORT}`;

/** Seeded active beacon (API key). */
export const ACTIVE_BEACON_KEY = 'dev-key-0001';
/** Seeded deactivated beacon, for negative tests. */
export const INACTIVE_BEACON_KEY = 'dev-inactive-key';
/** Demo operator created by `--demo`. */
export const DEMO_USERNAME = 'demo';
/** Password of the demo operator. */
export const DEMO_PASSWORD = 'demo-password-1';

/** Console prefix for harbor output (NAMING.md). */
export const LOG_PREFIX = '[harbor]';

/**
 * @param {string} value
 * @returns {string} Lowercase hex SHA-256 (how beacons are stored).
 */
export const sha256Hex = value => createHash('sha256').update(value).digest('hex');
