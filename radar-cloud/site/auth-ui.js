'use strict';
/**
 * auth-ui.js - single responsibility: the login / create-account overlay.
 *
 * Public API (Radar.ui.auth):
 *  - init({onSignedIn})  bind the form; onSignedIn() runs after the pass was stored
 *  - show()              show the overlay (and reset the header user area)
 *  - hide()
 * Calls POST /api/login or /api/signup through session.login; failures are shown in the form with messages.errorText.
 */
(function (g) {
  const radar = g.Radar,
    { $ } = radar.dom,
    M = radar.messages,
    feedback = radar.ui.feedback;
  let isSignup = false;

  function setMode(signup) {
    isSignup = signup;
    $('auth-title').textContent = $('auth-submit').textContent = signup ? M.authTitleSignup : M.authTitleLogin;
    $('auth-toggle').textContent = signup ? M.authToggleToLogin : M.authTitleSignup;
    $('auth-pass').autocomplete = signup ? 'new-password' : 'current-password';
    feedback.authError('');
  }

  function show() {
    $('auth-overlay').hidden = false;
    $('operator-name').textContent = '';
    $('logout').hidden = true;
    feedback.connection('off');
  }
  const hide = () => {
    $('auth-overlay').hidden = true;
  };

  /**
   * Log in or sign up; on success store the pass and hand over to onSignedIn (not awaited, like the original flow),
   * on failure show the server message in the form.
   * @param {Object} e the DOM event
   * @param {Object} onSignedIn the on signed in
   * @returns {Promise<void>} resolves when done
   */
  async function onSubmit(e, onSignedIn) {
    e.preventDefault();
    feedback.authError('');
    $('auth-submit').disabled = true;
    try {
      const pass = await radar.session.login({
        signup: isSignup,
        username: $('auth-user').value,
        password: $('auth-pass').value,
        apiKey: $('auth-key').value,
      });
      radar.session.start(pass.token, pass.username);
      $('auth-pass').value = '';
      $('auth-key').value = '';
      onSignedIn();
    } catch (err) {
      feedback.authError(M.errorText(err));
    }
    $('auth-submit').disabled = false;
  }

  /**
   * Bind the module to the shared state (and bus) and attach its DOM listeners.
   * @param {Object} opts options
   */
  function init(opts) {
    $('auth-toggle').addEventListener('click', () => setMode(!isSignup));
    $('auth-form').addEventListener('submit', e => onSubmit(e, opts.onSignedIn));
  }

  radar.ui.auth = { init, show, hide };
})(typeof self !== 'undefined' ? self : globalThis);
