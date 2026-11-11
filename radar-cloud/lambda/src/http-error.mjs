/**
 * HttpError: the one way a handler reports an expected failure.
 * The router turns it into the uniform body `{ "error": "<message>" }`; anything else becomes a 500.
 */
export class HttpError extends Error {
  /**
   * @param {number} status HTTP status code (4xx for caller mistakes).
   * @param {string} message Safe, user-facing text (never include secrets or internals).
   */
  constructor(status, message) {
    super(message);
    this.name = 'HttpError';
    this.status = status;
  }
}
