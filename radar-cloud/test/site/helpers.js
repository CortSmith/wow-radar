"use strict";
// Synthetic combat log builders.
const MINE = 0x511, FRIEND = 0x514, HOSTILE = 0x548;
function ts(sec, off) { // base 2026-11-11 14:00:00 at UTC-5 ; sec may be fractional
  const ms = Math.round((sec % 1) * 1000);
  const s = Math.floor(sec);
  const mm = String(Math.floor(s / 60) % 60).padStart(2, "0"), ss = String(s % 60).padStart(2, "0");
  const hh = String(14 + Math.floor(s / 3600)).padStart(2, "0");
  return `11/11/2026 ${hh}:${mm}:${ss}.${String(ms).padStart(3, "0")}${off === undefined ? "-5" : off}`;
}
const T0 = Date.UTC(2026, 10, 11, 19, 0, 0); // 14:00 at -5
function adv(guid, o) {
  o = Object.assign({ owner: "0000000000000000", hp: 1000, max: 2000, x: 100, y: 200, map: 1429, level: 60 }, o || {});
  return [guid, o.owner, o.hp, o.max, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, o.x, o.y, o.map, 0, o.level].join(",");
}
const q = (n) => `"${n}"`;
// damage event with advanced block describing `info` unit (default: destination)
function dmg(sec, src, dst, o) {
  o = o || {};
  const infoG = o.infoOf === "src" ? src.g : dst.g;
  return `${ts(sec)}  SPELL_DAMAGE,${src.g},${q(src.n)},${hex(src.f)},0x0,${dst.g},${q(dst.n)},${hex(dst.f)},0x0,133,${q("Fireball")},0x4,${adv(infoG, o)},${o.amount === undefined ? 500 : o.amount},500,0,4,0,0,0,nil,nil,nil`;
}
function swing(sec, src, dst, o) {
  o = o || {};
  const infoG = o.infoOf === "src" ? src.g : dst.g;
  return `${ts(sec)}  SWING_DAMAGE,${src.g},${q(src.n)},${hex(src.f)},0x0,${dst.g},${q(dst.n)},${hex(dst.f)},0x0,${adv(infoG, o)},${o.amount === undefined ? 300 : o.amount},300,0,1,0,0,0,nil,nil,nil`;
}
function died(sec, dst, flag) {
  return `${ts(sec)}  UNIT_DIED,0000000000000000,nil,0x80000000,0x80000000,${dst.g},${q(dst.n)},${hex(dst.f)},0x0${flag ? "," + flag : ""}`;
}
function partyKill(sec, src, dst) {
  return `${ts(sec)}  PARTY_KILL,${src.g},${q(src.n)},${hex(src.f)},0x0,${dst.g},${q(dst.n)},${hex(dst.f)},0x0`;
}
const hex = (n) => "0x" + n.toString(16);
const player = (id, name, f) => ({ g: `Player-1-${id}`, n: name, f });
const ME = player("0000000A", "Hero-PvP-US", MINE);
const ALLY = player("0000000B", "Ally-PvP-US", FRIEND);
const FOE = player("0000000C", "Foe-PvP-US", HOSTILE);
const FOE2 = player("0000000D", "Foe2-PvP-US", HOSTILE);
const mapChange = (sec, id, name, a, b, c, d) => `${ts(sec)}  MAP_CHANGE,${id},${q(name)},${a},${b},${c},${d}`;
const zoneChange = (sec, name) => `${ts(sec)}  ZONE_CHANGE,1,${q(name)},0`;
module.exports = { MINE, FRIEND, HOSTILE, ts, T0, adv, dmg, swing, died, partyKill, player, ME, ALLY, FOE, FOE2, mapChange, zoneChange, hex };
