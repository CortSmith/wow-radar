'use strict';
/**
 * feedback.js - single responsibility: every place where the UI tells the operator how things are going
 * (footer, upload status, bounty status, login error, progress bar, connection dot).
 *
 * Public API (Radar.ui.feedback): footer(text), uploadStatus(text), bountyStatus(text), authError(text),
 * progress(fraction|null) (null hides the bar), connection("ok"|"sweep"|"off").
 * Callers pass text from messages.js (errorText(err) for failures) so wording stays in one place.
 */
(function (g) {
  const radar = g.Radar;
  const { $ } = radar.dom;

  const footer = text => {
    $('status-footer').textContent = text;
  };
  const uploadStatus = text => {
    $('ping-status').textContent = text;
  };
  const bountyStatus = text => {
    $('bounty-status').textContent = text;
  };
  const authError = text => {
    $('auth-error').textContent = text;
  };
  const connection = kind => {
    $('connection-dot').className = 'dot ' + (kind === 'off' ? '' : kind);
  };
  function progress(fraction) {
    $('ping-progress').hidden = fraction === null;
    const percent = Math.round((fraction || 0) * radar.config.units.PCT_FULL);
    $('ping-progress-fill').style.width = percent + '%';
    $('ping-progress').setAttribute('aria-valuenow', String(percent));
  }

  const ns = g.Radar.ui || (g.Radar.ui = {});
  ns.feedback = { footer, uploadStatus, bountyStatus, authError, progress, connection };
})(typeof self !== 'undefined' ? self : globalThis);
