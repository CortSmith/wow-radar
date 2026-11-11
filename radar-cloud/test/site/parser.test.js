"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const P = require("../../site/parser.js");
const H = require("./helpers.js");

test("timestamps: offsets, fractional ms, bad input", () => {
  assert.equal(P.parseTime("11/11/2026 14:00:00.000-5"), Date.UTC(2026, 10, 11, 19, 0, 0));
  assert.equal(P.parseTime("11/11/2026 14:00:00.250+2"), Date.UTC(2026, 10, 11, 12, 0, 0, 250));
  assert.equal(P.parseTime("11/11/2026 14:00:00.5+5:30"), Date.UTC(2026, 10, 11, 8, 30, 0, 500));
  assert.equal(P.parseTime("1/2/2026 03:04:05"), new Date(2026, 0, 2, 3, 4, 5).getTime()); // no offset -> local
  for (const bad of ["", "garbage", "13/40/2026 10:00:00.000", "1/1/2026 25:00:00.000", "1/1/2026 10:61:00", "1/1 10:00:00", "1/1/2026 10:00"]) {
    assert.ok(Number.isNaN(P.parseTime(bad)), bad);
  }
});

test("splitCSV handles quotes, escaped quotes, commas, malformed", () => {
  assert.deepEqual(P.splitCSV("a,b,c"), ["a", "b", "c"]);
  assert.deepEqual(P.splitCSV('a,"b,c",d'), ["a", "b,c", "d"]);
  assert.deepEqual(P.splitCSV('"say ""hi""",x'), ['say "hi"', "x"]);
  assert.equal(P.splitCSV('a,"unterminated'), null);
  assert.equal(P.splitCSV('"a"b,c'), null);
  assert.deepEqual(P.splitCSV("a,,c"), ["a", "", "c"]);
});

test("COMBAT_LOG_VERSION, ZONE_CHANGE, MAP_CHANGE", () => {
  let ev = P.parseLine(`${H.ts(0)}  COMBAT_LOG_VERSION,20,ADVANCED_LOG_ENABLED,1,BUILD_VERSION,3.4.3`);
  assert.equal(ev.type, "COMBAT_LOG_VERSION"); assert.equal(ev.advanced, true);
  ev = P.parseLine(`${H.ts(0)}  COMBAT_LOG_VERSION,20,ADVANCED_LOG_ENABLED,0`);
  assert.equal(ev.advanced, false);
  ev = P.parseLine(H.zoneChange(1, "Elwynn Forest"));
  assert.equal(ev.zone, "Elwynn Forest");
  ev = P.parseLine(H.mapChange(1, 1429, "Elwynn Forest", 500, -500, 300, -900));
  assert.deepEqual(ev.map, { id: 1429, name: "Elwynn Forest", x0: -500, x1: 500, y0: -900, y1: 300 });
  assert.equal(P.parseLine(`${H.ts(0)}  MAP_CHANGE,1,"X",a,b,c,d`).map, null);
});

test("damage events: advanced block, amount, unit flags", () => {
  const ev = P.parseLine(H.dmg(1, H.FOE, H.ALLY, { x: 12.5, y: -3, hp: 900, max: 2000, level: 70, amount: 777 }));
  assert.equal(ev.type, "SPELL_DAMAGE"); assert.ok(ev.isDamage); assert.ok(ev.hasAmount);
  assert.equal(ev.amount, 777);
  assert.equal(ev.src.guid, H.FOE.g); assert.equal(ev.src.flags, H.HOSTILE); assert.equal(ev.dst.name, "Ally-PvP-US");
  assert.equal(ev.info.guid, H.ALLY.g); assert.equal(ev.info.x, 12.5); assert.equal(ev.info.y, -3);
  assert.equal(ev.info.mapId, 1429); assert.equal(ev.info.level, 70); assert.equal(ev.info.hp, 900); assert.equal(ev.info.maxHp, 2000);
  assert.ok(ev.info.hasPos && ev.info.hasHp);
  const sw = P.parseLine(H.swing(2, H.FOE, H.ALLY, { amount: 42 }));
  assert.equal(sw.amount, 42); assert.ok(sw.isDamage);
});

test("heal / non-damage / landed / periodic classification", () => {
  const ev = P.parseLine(H.dmg(1, H.ALLY, H.ME).replace("SPELL_DAMAGE", "SPELL_HEAL"));
  assert.ok(ev.isHeal); assert.ok(!ev.isDamage); assert.ok(ev.hasAmount);
  assert.ok(!P.parseLine(H.dmg(1, H.ALLY, H.ME).replace("SPELL_DAMAGE", "SPELL_DAMAGE_LANDED")).isDamage);
  assert.ok(P.parseLine(H.dmg(1, H.ALLY, H.ME).replace("SPELL_DAMAGE", "SPELL_PERIODIC_DAMAGE")).isDamage);
  const aura = P.parseLine(H.dmg(1, H.ALLY, H.ME).replace("SPELL_DAMAGE", "SPELL_AURA_APPLIED"));
  assert.ok(!aura.isDamage && !aura.hasAmount);
  const env = P.parseLine(`${H.ts(1)}  ENVIRONMENTAL_DAMAGE,0000000000000000,nil,0x80000000,0x80000000,${H.ME.g},"Hero",0x511,0x0,Falling,${H.adv(H.ME.g)},50,50,0,1,0,0,0`);
  assert.ok(env.isDamage); assert.equal(env.amount, 50); assert.equal(env.info.guid, H.ME.g);
});

test("UNIT_DIED and unconscious (feign death) flag; PARTY_KILL", () => {
  assert.ok(!P.parseLine(H.died(1, H.ALLY)).unconscious);
  assert.ok(P.parseLine(H.died(1, H.ALLY, "1")).unconscious);
  const pk = P.parseLine(H.partyKill(1, H.FOE, H.ALLY));
  assert.equal(pk.type, "PARTY_KILL"); assert.equal(pk.src.guid, H.FOE.g); assert.equal(pk.dst.guid, H.ALLY.g);
  assert.equal(P.parseLine(H.died(1, H.ALLY)).src.guid, ""); // 0000000000000000 -> empty
});

test("unknown prefix: scans for an advanced block with a position", () => {
  const l = `${H.ts(1)}  WEIRD_EVENT,${H.FOE.g},"Foe-PvP-US",0x548,0x0,${H.ALLY.g},"Ally-PvP-US",0x514,0x0,7,8,${H.adv(H.ALLY.g, { x: 5, y: 6 })}`;
  const ev = P.parseLine(l);
  assert.equal(ev.info.x, 5); assert.equal(ev.info.guid, H.ALLY.g);
});

test("malformed lines return null or a bare event", () => {
  assert.equal(P.parseLine("no separator at all"), null);
  assert.equal(P.parseLine("bad time  SPELL_DAMAGE,a"), null);
  assert.equal(P.parseLine(`${H.ts(1)}  `), null);
  assert.equal(P.parseLine(`${H.ts(1)}  X,"unterminated`), null);
  assert.equal(P.parseLine(`${H.ts(1)}  SPELL_DAMAGE,a,b`).src, null);
  assert.ok(P.parseLine(H.dmg(1, H.FOE, H.ALLY) + "\r"));
});

test("non-numeric position fields give no position", () => {
  const ev = P.parseLine(H.dmg(1, H.FOE, H.ALLY).replace(",100,200,1429,", ",nan,200,1429,"));
  assert.ok(!ev.info.hasPos);
});

test("kindOf / reactionOf / isMine", () => {
  assert.equal(P.kindOf("Player-1-A"), P.KIND_PLAYER);
  assert.equal(P.kindOf("Creature-0-1"), P.KIND_NPC);
  assert.equal(P.kindOf("Vehicle-0-1"), P.KIND_NPC);
  assert.equal(P.kindOf("Pet-0-1"), P.KIND_PET);
  assert.equal(P.kindOf("Item-0"), P.KIND_OTHER);
  assert.equal(P.reactionOf(0x511), "friendly"); assert.equal(P.reactionOf(0x548), "hostile");
  assert.equal(P.reactionOf(0x568), "hostile"); assert.equal(P.reactionOf(0x228), "neutral"); assert.equal(P.reactionOf(0x8), "unknown");
  assert.ok(P.isMine(0x511)); assert.ok(!P.isMine(0x548));
});

test("Namer: realm suffix detection needs 6 distinct names; display strips it", () => {
  const n = new P.Namer("");
  for (let i = 0; i < 5; i++) n.observe(`Pl${i}-PvP-US`);
  assert.equal(n.suffix, "");
  assert.equal(n.display("Pl0-PvP-US"), "Pl0 PvP US");
  n.observe("Pl5-PvP-US");
  assert.equal(n.suffix, "-PvP-US");
  assert.equal(n.display("Pl0-PvP-US"), "Pl0");
  assert.equal(n.display("First-Last-PvP-US"), "First Last");
  n.observe("Other-EU"); // locked
  assert.equal(n.suffix, "-PvP-US");
  const m = new P.Namer("RP-EU");
  assert.equal(m.suffix, "-RP-EU"); assert.equal(m.display("A-RP-EU"), "A");
  const d = new P.Namer("");
  for (const x of ["A-X-PvP-US", "B-PvP-US", "C-PvP-US", "D-PvP-US", "E-PvP-US", "F-PvP-US"]) d.observe(x);
  assert.equal(d.suffix, "-PvP-US"); // longest COMMON run
  const e = new P.Namer("");
  for (const x of ["A-US", "B-EU", "C-US", "D-US", "E-US", "F-US"]) e.observe(x);
  assert.equal(e.suffix, "");
});

test("rule-set parse and detection from raw name", () => {
  for (const [s, id] of [["normal", "normal"], ["PvP", "pvp"], ["rp", "rp"], ["RP-PvP", "rppvp"], ["rp_pvp", "rppvp"], ["rp pvp", "rppvp"], ["rppvp", "rppvp"], ["x", ""]]) assert.equal(P.parseRuleset(s), id, s);
  assert.equal(P.detectFromRawName("Hero-PvP-US"), "pvp");
  assert.equal(P.detectFromRawName("Hero-Normal-EU"), "normal");
  assert.equal(P.detectFromRawName("Hero-RP-US"), "rp");
  assert.equal(P.detectFromRawName("Hero-RP-PvP-US"), "rppvp");
  assert.equal(P.detectFromRawName("Rp-Realm-US"), ""); // first part is the name, never a realm token
  assert.equal(P.detectFromRawName("Normal"), "");
  assert.equal(P.detectFromRawName("Hero-Realm-US"), "");
});
