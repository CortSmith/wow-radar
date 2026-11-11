'use strict';
/**
 * messages.js - single responsibility: every user-visible text and the one rule for turning an error into text.
 *
 * Keeping the wording here means error handling is consistent: logic modules throw Error(messages.xxx) and UI modules
 * show errorText(err) through feedback.js; nothing else invents user-facing strings for failures.
 * Public API (Radar.messages): errorText(err), plus the text constants and builders below. No DOM, no dependencies.
 */
(function (g) {
  /** @param {unknown} err @returns {string} the message of an Error, or the stringified value. */
  const errorText = err => (err && err.message ? err.message : String(err));

  const messages = {
    errorText,

    // ---- failures thrown by logic modules ----
    unreachable: 'Could not reach the server.',
    loginFailed: 'Login failed',
    sessionExpired: 'Session expired, please log in again.',
    workerFailed: 'Worker failed',
    serverError: status => 'Server error (' + status + ')',

    // ---- empty states ----
    waitingForEvents: 'Waiting for combat log events...',
    noContactsYet: 'No players yet. Start live or upload a combat log...',
    notLoaded: 'Not loaded',
    noMatchingContacts: 'No matching players',
    noMatchingCharts: 'No matching maps',
    noBounties:
      'No bounties yet. Kills of friendly players are recorded from live logs; use Analyze logs for older fights.',
    noRecordedPositions: name => name + ': no recorded positions',

    // ---- auth overlay ----
    authTitleLogin: 'Log in',
    authTitleSignup: 'Create account',
    authToggleToLogin: 'I already have an account',

    // ---- upload / analyze / live tail statuses ----
    checkingLedger: name => 'Checking ledger for ' + name + '...',
    parsing: (name, doneMb, totalMb, pending) =>
      'Parsing ' + name + ': ' + doneMb + '/' + totalMb + ' MB, ' + pending + ' players pending',
    uploading: (rs, n, total) => 'Uploading ' + rs + ': request ' + n + '/' + total,
    uploadDone: (sent, changed) => 'Done: ' + sent + ' players sent, ' + changed + ' new or updated.',
    uploadError: (err, hasPending) =>
      'Error: ' + errorText(err) + (hasPending ? ' (will retry on the next upload)' : ''),
    uploaded: (received, changed) => 'Uploaded ' + received + ' players (' + changed + ' new or updated)',
    uploadRetry: err => 'Upload failed, will retry: ' + errorText(err),
    waitingForHead: 'Waiting for the log to reach 64 KB before uploading',
    noLogsYet: 'No WoWCombatLog*.txt in the chosen folder yet',
    liveFile: name => 'Live: ' + name,
    liveError: err => 'Live error: ' + errorText(err),
    liveReading: 'Live: reading logs...',
    liveStopping: 'Stopping: final upload...',
    liveNeedsChromium: 'Live tailing needs a Chromium-based browser (Chrome, Edge). Use Choose files instead.',
    analyzeConfirm: (rulesetName, fileCount) =>
      'This clears ALL stored data of the ' +
      rulesetName +
      ' rule-set (players, tracks, bounty, upload ledger) and rebuilds it from the ' +
      fileCount +
      ' log file(s) you chose. Continue?',
    analyzeResetting: rulesetName => 'Resetting ' + rulesetName + '...',
    analyzeProgress: (label, text) => label + ' ' + text,
    analyzeLabel: 'Analyzing:',
    analyzeDone: (fileCount, sent) => 'Done: ' + fileCount + ' file(s), ' + sent + ' players sent.',
    analyzeFailed: err => 'Analyze failed: ' + errorText(err),
    sweepFailed: err => 'Sweep: ' + errorText(err),

    // ---- times (shown in the operator's zone; ISO UTC detail with the +00:00 offset) ----
    logSpan: (first, last, firstIso, lastIso) =>
      'Log spans ' + first + ' to ' + last + ' (UTC ' + firstIso + ' to ' + lastIso + ')',
    newestEvent: (text, iso) => 'Newest event ' + text + ' (UTC ' + iso + ')',
    fixSpan: (first, last) => 'Fixes span ' + first + ' to ' + last,
    timezoneBrowserDefault: 'Browser default',
  };

  const ns = g.Radar || (g.Radar = {});
  ns.messages = messages;
  if (typeof module !== 'undefined' && module.exports) module.exports = messages;
})(typeof self !== 'undefined' ? self : globalThis);
