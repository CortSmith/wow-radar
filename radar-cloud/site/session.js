'use strict';
/**
 * session.js - single responsibility: the operator's pass (token + username): persistence and the authenticated api().
 *
 * Public API (Radar.session):
 *  - isSignedIn(), username(), restore() (from localStorage), start(token, username), end()
 *  - api(path, options) -> Promise<json>   Bearer-authenticated; a 401 ends the session and calls the expiry handler
 *  - onExpired(fn)                        registers what to do on 401 (app.js wires it to the logout flow)
 *  - login({signup, username, password, apiKey}) -> Promise<{token, username}>   (no state change; call start() on
 * success)
 */
(function (g) {
  const radar = g.Radar;
  const TOKEN_KEY = 'nrc.token',
    USER_KEY = 'nrc.user';
  let token = null,
    name = '',
    expiredHandler = null;

  const api = radar.http.makeApi(
    radar.adapters.httpFetch,
    () => token,
    () => {
      if (expiredHandler) expiredHandler();
    },
  );

  function restore() {
    token = radar.adapters.storage.read(TOKEN_KEY, null);
    name = radar.adapters.storage.read(USER_KEY, '');
  }
  function start(newToken, newName) {
    token = newToken;
    name = newName;
    radar.adapters.storage.write(TOKEN_KEY, token);
    radar.adapters.storage.write(USER_KEY, name);
  }
  function end() {
    token = null;
    name = '';
    radar.adapters.storage.remove(TOKEN_KEY);
    radar.adapters.storage.remove(USER_KEY);
  }

  radar.session = {
    isSignedIn: () => !!token,
    username: () => name,
    restore,
    start,
    end,
    api,
    onExpired(fn) {
      expiredHandler = fn;
    },
    login: credentials => radar.http.authenticate(radar.adapters.httpFetch, credentials),
  };
})(typeof self !== 'undefined' ? self : globalThis);
