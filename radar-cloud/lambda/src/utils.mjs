/** Tiny generic helpers with no domain knowledge. */

/**
 * Splits an array into consecutive slices.
 * @template T
 * @param {T[]} items
 * @param {number} size Slice length (> 0).
 * @returns {T[][]}
 */
export function splitIntoChunks(items, size) {
  return Array.from({ length: Math.ceil(items.length / size) }, (_, index) =>
    items.slice(index * size, index * size + size),
  );
}

/**
 * Resolves after a delay (used for retry backoff).
 * @param {number} ms
 * @returns {Promise<void>}
 */
export const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
