// Generates a synthetic WoWCombatLog for manual upload / live-tail testing in the browser.
//   node dev/make-sample-log.cjs [out.txt] [--append]
// --append keeps adding lines every second, simulating a live session (point the page's live mode at that folder).
const fs = require('fs'), path = require('path');
const h = require('../test/site/helpers.js');
const out = process.argv.slice(2).find(a => !a.startsWith('--')) || path.join(__dirname, 'WoWCombatLog-sample.txt');
const append = process.argv.includes('--append');
const me = h.ME;
const cast = [h.ALLY, h.FOE, h.FOE2, h.player('0000000E', 'Mage-PvP-US', h.HOSTILE), h.player('0000000F', 'Priest-PvP-US', h.FRIEND)];
const lines = [
  '11/11/2026 14:00:00.000-5  COMBAT_LOG_VERSION,20,ADVANCED_LOG_ENABLED,1,BUILD_VERSION,1.0,PROJECT_ID,1',
  h.mapChange(0.5, 1413, 'Barrens', -5000, 5000, -5000, 5000),
  h.zoneChange(0.6, 'The Barrens'),
];
let t = 1;
function tick(sec) {
  const o = [];
  cast.forEach((p, i) => {
    const x = Math.round(Math.sin(sec / 7 + i) * 3000), y = Math.round(Math.cos(sec / 9 + i) * 3000);
    o.push(h.dmg(sec + 0.01 * i, p, me, { x, y, map: 1413, level: 55 + i, amount: 200 + i * 50, infoOf: 'src' }));
    o.push(h.swing(sec + 0.02 * i, me, p, { x: x + 40, y: y + 40, map: 1413, amount: 300 }));
  });
  if (sec % 30 === 0) { o.push(h.partyKill(sec + 0.5, me, h.FOE)); o.push(h.died(sec + 0.6, h.FOE)); }
  return o;
}
if (!append) {
  for (; t < 600; t++) lines.push(...tick(t));
  fs.writeFileSync(out, lines.join('\n') + '\n');
  console.log('wrote', out, lines.length, 'lines');
} else {
  fs.writeFileSync(out, lines.join('\n') + '\n');
  console.log('appending to', out, '(Ctrl+C to stop)');
  setInterval(() => fs.appendFileSync(out, tick(t++).join('\n') + '\n'), 1000);
}
