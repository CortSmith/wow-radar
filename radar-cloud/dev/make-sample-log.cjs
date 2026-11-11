/**
 * Generates a synthetic WoWCombatLog for manual upload / live-tail testing in the browser.
 * Usage: node dev/make-sample-log.cjs [out.txt] [--append] [--no-offset]
 *   (default)  writes 10 minutes of log lines to the file and exits
 *   --append   writes the header, then keeps adding one second of lines every second, simulating a live
 *              session (point the page's live mode at that folder); stop with Ctrl+C
 *   --no-offset  writes timestamps without the UTC offset suffix (14:00:00.000 instead of 14:00:00.000-5), so the
 *              parser reads them in the operator's chosen time zone (the fallback path of site/time-zone.js)
 * Line builders come from the frontend test helpers (test/site/helpers.js) so the format cannot drift.
 */
const fs = require('fs');
const path = require('path');
const helpers = require('../test/site/helpers.js');

const outputFile =
  process.argv.slice(2).find(arg => !arg.startsWith('--')) || path.join(__dirname, 'WoWCombatLog-sample.txt');
const shouldAppend = process.argv.includes('--append');
const shouldDropOffset = process.argv.includes('--no-offset');
/** Timestamp, then the UTC offset glued to the clock (e.g. "-5" or "+5:30"), then the two spaces before the event. */
const OFFSET_SUFFIX_PATTERN = /^(\d+\/\d+\/\d+ \d+:\d+:\d+\.\d+)[+-]\d+(?::\d+)?( {2})/;
const outputLines = lines =>
  shouldDropOffset ? lines.map(line => line.replace(OFFSET_SUFFIX_PATTERN, '$1$2')) : lines;

const LOG_SECONDS = 600;
const APPEND_INTERVAL_MS = 1000;
const ORBIT_RADIUS_YD = 3000;
const BARRENS_MAP_ID = 1413;
const KILL_EVERY_SEC = 30;

const castMembers = [
  helpers.ALLY,
  helpers.FOE,
  helpers.FOE2,
  helpers.player('0000000E', 'Mage-PvP-US', helpers.HOSTILE),
  helpers.player('0000000F', 'Priest-PvP-US', helpers.FRIEND),
];

const headerLines = [
  '11/11/2026 14:00:00.000-5  COMBAT_LOG_VERSION,20,ADVANCED_LOG_ENABLED,1,BUILD_VERSION,1.0,PROJECT_ID,1',
  helpers.mapChange(0.5, BARRENS_MAP_ID, 'Barrens', -5000, 5000, -5000, 5000),
  helpers.zoneChange(0.6, 'The Barrens'),
];

/** Lines for one second: every cast member orbits the origin, trading damage with the local player. */
function linesForSecond(second) {
  const lines = [];
  castMembers.forEach((member, index) => {
    const x = Math.round(Math.sin(second / 7 + index) * ORBIT_RADIUS_YD);
    const y = Math.round(Math.cos(second / 9 + index) * ORBIT_RADIUS_YD);
    lines.push(
      helpers.dmg(second + 0.01 * index, member, helpers.ME, {
        x,
        y,
        map: BARRENS_MAP_ID,
        level: 55 + index,
        amount: 200 + index * 50,
        infoOf: 'src',
      }),
    );
    lines.push(
      helpers.swing(second + 0.02 * index, helpers.ME, member, {
        x: x + 40,
        y: y + 40,
        map: BARRENS_MAP_ID,
        amount: 300,
      }),
    );
  });
  if (second % KILL_EVERY_SEC === 0) {
    lines.push(helpers.partyKill(second + 0.5, helpers.ME, helpers.FOE));
    lines.push(helpers.died(second + 0.6, helpers.FOE));
  }
  return lines;
}

if (shouldAppend) {
  fs.writeFileSync(outputFile, outputLines(headerLines).join('\n') + '\n');
  console.log('appending to', outputFile, '(Ctrl+C to stop)');
  let second = 1;
  setInterval(
    () => fs.appendFileSync(outputFile, outputLines(linesForSecond(second++)).join('\n') + '\n'),
    APPEND_INTERVAL_MS,
  );
} else {
  const lines = [...headerLines];
  for (let second = 1; second < LOG_SECONDS; second++) lines.push(...linesForSecond(second));
  fs.writeFileSync(outputFile, outputLines(lines).join('\n') + '\n');
  console.log('wrote', outputFile, lines.length, 'lines');
}
