'use strict';
/**
 * http.js - single responsibility: talk to the backend API (login/signup and authenticated JSON calls).
 *
 * Public API (Radar.http):
 *  - authenticate(fetchFn, {signup, username, password, apiKey}) -> Promise<{token, username}>   the "pass" of an
 * operator
 *  - makeApi(fetchFn, getToken, onUnauthorized) -> api(path, opt) -> Promise<json>   401 calls onUnauthorized and
 * throws
 * Errors are Error objects whose message comes from messages.js or the server's {error} field. fetch is injected so
 * the module stays DOM-free and testable. HTTP paths and JSON fields are frozen wire names.
 */
(function (g) {
  const dep = (name, file) =>
    typeof require !== 'undefined' && typeof module !== 'undefined' ? require(file) : g.Radar[name];
  const M = dep('messages', './messages.js');
  const JSON_HEADERS = { 'Content-Type': 'application/json' };
  const HTTP_UNAUTHORIZED = 401;

  /**
   * Log in or create an account (the response carries the operator's pass).
   * @param {function(string, Object): Promise<Response>} fetchFn  injected fetch
   * @param {{signup: boolean, username: string, password: string, apiKey: string}} o
   * @returns {Promise<{token: string, username: string}>}
   * @throws {Error} unreachable server, or the server's {error} message
   */
  async function authenticate(fetchFn, o) {
    let r;
    try {
      r = await fetchFn(o.signup ? '/api/signup' : '/api/login', {
        method: 'POST',
        headers: JSON_HEADERS,
        body: JSON.stringify({ username: o.username.trim(), password: o.password, apiKey: o.apiKey.trim() }),
      });
    } catch (e) {
      throw new Error(M.unreachable);
    }
    let d;
    try {
      d = await r.json();
    } catch (e) {
      d = { error: M.serverError(r.status) };
    }
    if (!d.token) throw new Error(d.error || M.loginFailed);
    return { token: d.token, username: d.username || o.username.trim() };
  }

  /**
   * Build the authenticated JSON client. A 401 calls onUnauthorized once and throws sessionExpired.
   * @param {function(string, Object): Promise<Response>} fetchFn
   * @param {function(): string} getToken  read at call time so a new login is picked up
   * @param {function(): void} [onUnauthorized]
   * @returns {function(string, Object=): Promise<Object>} api(path, options)
   */
  function makeApi(fetchFn, getToken, onUnauthorized) {
    return async function api(path, opt) {
      opt = Object.assign({}, opt || {});
      opt.headers = Object.assign({ Authorization: 'Bearer ' + getToken() }, opt.headers || {});
      const r = await fetchFn(path, opt);
      if (r.status === HTTP_UNAUTHORIZED) {
        if (onUnauthorized) onUnauthorized();
        throw new Error(M.sessionExpired);
      }
      const d = await r.json().catch(() => ({}));
      if (!r.ok) throw new Error(d.error || M.serverError(r.status));
      return d;
    };
  }

  const api = { authenticate, makeApi, JSON_HEADERS };
  (g.Radar || (g.Radar = {})).http = api;
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
})(typeof self !== 'undefined' ? self : globalThis);
