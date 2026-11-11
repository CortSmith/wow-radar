#!/usr/bin/env python3
"""
extract_maps.py - pull the zone map art out of your own WoW Forever install and turn it into PNGs
for Nearby Tracker's background map. Personal use: the images stay on your computer.

What it does
  1. Reads the game's local CASC archive (Data\\data) like the game does.
  2. Finds the 12 map tiles of each zone (Interface\\WorldMap\\<Zone>\\<Zone>1..12.blp).
  3. Tiles that are stored locally are read from disk. Tiles the client has not downloaded yet
     (it streams art on demand) are fetched from Blizzard's CDN, using the archive indexes that are
     already in Data\\indices. Only the few KB of each tile are requested.
  4. Reveals the zone: the base art is the "unexplored" parchment, so the game's exploration overlays
     (WorldMapOverlay / WorldMapOverlayTile tables, read from the same install) are painted on top.
  5. Stitches each zone into one image: maps\\<Zone>.png, plus maps\\index.json for the web page.

Needs Python 3.9+ and Pillow (py -m pip install pillow). Safe to re-run: finished zones are skipped.
Read-only on the game files.
"""
import argparse
import bisect
import glob
import io
import json
import mmap
import os
import struct
import sys
import time
import urllib.request
import zlib
from concurrent.futures import ThreadPoolExecutor

DEFAULT_WOW = r"C:\Program Files (x86)\World of Warcraft"
PRODUCT = "wowf"          # WoW Forever
MAP_W, MAP_H = 1002, 668  # visible part of a 4x3 grid of 256 px tiles

# (name shown in the log's MAP_CHANGE line, art folder under Interface\WorldMap)
ZONES = [
    # Eastern Kingdoms
    ("Alterac Mountains", "Alterac"), ("Arathi Highlands", "Arathi"), ("Badlands", "Badlands"),
    ("Blasted Lands", "BlastedLands"), ("Burning Steppes", "BurningSteppes"), ("Deadwind Pass", "DeadwindPass"),
    ("Dun Morogh", "DunMorogh"), ("Duskwood", "Duskwood"), ("Eastern Plaguelands", "EasternPlaguelands"),
    ("Elwynn Forest", "Elwynn"), ("Hillsbrad Foothills", "Hilsbrad"), ("The Hinterlands", "Hinterlands"),
    ("Loch Modan", "LochModan"), ("Redridge Mountains", "Redridge"), ("Searing Gorge", "SearingGorge"),
    ("Silverpine Forest", "Silverpine"), ("Stranglethorn Vale", "StranglethornVale"),
    ("Swamp of Sorrows", "SwampOfSorrows"), ("Tirisfal Glades", "Tirisfal"),
    ("Western Plaguelands", "WesternPlaguelands"), ("Westfall", "Westfall"), ("Wetlands", "Wetlands"),
    ("Ghostlands", "Ghostlands"), ("Eversong Woods", "EversongWoods"), ("Isle of Quel'Danas", "Sunwell"),
    # Kalimdor
    ("Ashenvale", "Ashenvale"), ("Azshara", "Aszhara"), ("Darkshore", "Darkshore"), ("Desolace", "Desolace"),
    ("Durotar", "Durotar"), ("Dustwallow Marsh", "Dustwallow"), ("Felwood", "Felwood"), ("Feralas", "Feralas"),
    ("Moonglade", "Moonglade"), ("Mulgore", "Mulgore"), ("Silithus", "Silithus"),
    ("Stonetalon Mountains", "StonetalonMountains"), ("Tanaris", "Tanaris"), ("Teldrassil", "Teldrassil"),
    ("The Barrens", "Barrens"), ("Thousand Needles", "ThousandNeedles"), ("Un'Goro Crater", "UngoroCrater"),
    ("Winterspring", "Winterspring"), ("Azuremyst Isle", "AzuremystIsle"), ("Bloodmyst Isle", "BloodmystIsle"),
    # Cities
    ("Stormwind City", "Stormwind"), ("Ironforge", "Ironforge"), ("Darnassus", "Darnassis"),
    ("Orgrimmar", "Orgrimmar"), ("Thunder Bluff", "ThunderBluff"), ("Undercity", "Undercity"),
    ("Silvermoon City", "SilvermoonCity"), ("The Exodar", "TheExodar"), ("Shattrath City", "ShattrathCity"),
    ("Dalaran", "Dalaran"),
    # Outland
    ("Hellfire Peninsula", "Hellfire"), ("Zangarmarsh", "Zangarmarsh"), ("Terokkar Forest", "TerokkarForest"),
    ("Nagrand", "Nagrand"), ("Blade's Edge Mountains", "BladesEdgeMountains"), ("Netherstorm", "Netherstorm"),
    ("Shadowmoon Valley", "ShadowmoonValley"),
    # Northrend
    ("Borean Tundra", "BoreanTundra"), ("Dragonblight", "Dragonblight"), ("Grizzly Hills", "GrizzlyHills"),
    ("Howling Fjord", "HowlingFjord"), ("Icecrown", "IcecrownGlacier"), ("Sholazar Basin", "SholazarBasin"),
    ("The Storm Peaks", "TheStormPeaks"), ("Zul'Drak", "ZulDrak"), ("Crystalsong Forest", "CrystalsongForest"),
    ("Wintergrasp", "LakeWintergrasp"),
    # Continents
    ("Azeroth", "Azeroth"), ("Kalimdor", "Kalimdor"), ("Eastern Kingdoms", "EasternKingdoms"),
    ("Outland", "Expansion01"), ("Northrend", "Northrend"),
]

# Classic-style uiMapIDs (what the combat log's MAP_CHANGE line carries) - used to find each zone's overlays
UIMAP = {
    "Durotar": 1411, "Mulgore": 1412, "The Barrens": 1413, "Alterac Mountains": 1416, "Arathi Highlands": 1417,
    "Badlands": 1418, "Blasted Lands": 1419, "Tirisfal Glades": 1420, "Silverpine Forest": 1421,
    "Western Plaguelands": 1422, "Eastern Plaguelands": 1423, "Hillsbrad Foothills": 1424, "The Hinterlands": 1425,
    "Dun Morogh": 1426, "Searing Gorge": 1427, "Burning Steppes": 1428, "Elwynn Forest": 1429,
    "Deadwind Pass": 1430, "Duskwood": 1431, "Loch Modan": 1432, "Redridge Mountains": 1433,
    "Stranglethorn Vale": 1434, "Swamp of Sorrows": 1435, "Westfall": 1436, "Wetlands": 1437,
    "Teldrassil": 1438, "Darkshore": 1439, "Ashenvale": 1440, "Thousand Needles": 1441,
    "Stonetalon Mountains": 1442, "Desolace": 1443, "Feralas": 1444, "Dustwallow Marsh": 1445, "Tanaris": 1446,
    "Azshara": 1447, "Felwood": 1448, "Un'Goro Crater": 1449, "Moonglade": 1450, "Silithus": 1451,
    "Winterspring": 1452, "Stormwind City": 1453, "Orgrimmar": 1454, "Ironforge": 1455, "Thunder Bluff": 1456,
    "Darnassus": 1457, "Undercity": 1458,
}
# file data ids of the game tables that describe the exploration overlays
FDID_UIMAPXMAPART, FDID_WORLDMAPOVERLAY, FDID_WORLDMAPOVERLAYTILE = 1957217, 1134579, 1957212


def norm(name):
    return "".join(ch for ch in name.lower() if ch.isalnum())


# ------------------------------------------------------------------ CASC basics
class Casc:
    """Local CASC storage: Data\\data\\*.idx (version 7) + data.NNN archives."""

    def __init__(self, data_dir):
        self.dd = os.path.join(data_dir, "data")
        best = {}
        for fn in os.listdir(self.dd):
            if fn.endswith(".idx") and len(fn) == 14:
                b, v = int(fn[:2], 16), int(fn[2:10], 16)
                if b not in best or v > best[b][0]:
                    best[b] = (v, fn)
        if len(best) != 16:
            raise SystemExit("Could not find the 16 index files in %s" % self.dd)
        self.idx = {}
        for b, (_v, fn) in best.items():
            f = open(os.path.join(self.dd, fn), "rb")
            mm = mmap.mmap(f.fileno(), 0, access=mmap.ACCESS_READ)
            ver, = struct.unpack_from("<H", mm, 8)
            sizes = tuple(mm[0x0C:0x10])
            if ver != 7 or sizes != (4, 5, 9, 30):
                raise SystemExit("Unsupported index format in %s (version %s, %s)" % (fn, ver, sizes))
            n, = struct.unpack_from("<I", mm, 0x20)
            self.idx[b] = (mm, 0x28, n // 18)
        self._arch = {}

    def lookup(self, ekey):
        k = bytes(ekey[:9])
        x = 0
        for c in k:
            x ^= c
        mm, base, n = self.idx[(x & 0xF) ^ (x >> 4)]
        lo, hi = 0, n
        while lo < hi:
            mid = (lo + hi) // 2
            if mm[base + mid * 18: base + mid * 18 + 9] < k:
                lo = mid + 1
            else:
                hi = mid
        if lo < n:
            o = base + lo * 18
            if mm[o:o + 9] == k:
                off5 = int.from_bytes(mm[o + 9:o + 14], "big")
                size, = struct.unpack_from("<I", mm, o + 14)
                return off5 >> 30, off5 & 0x3FFFFFFF, size
        return None

    def read(self, ekey):
        hit = self.lookup(ekey)
        if not hit:
            return None
        arch, off, size = hit
        f = self._arch.get(arch)
        if f is None:
            f = self._arch[arch] = open(os.path.join(self.dd, "data.%03d" % arch), "rb")
        f.seek(off)
        blob = f.read(size)
        return blte_decode(blob[0x1E:])


def blte_decode(b):
    if b[:4] != b"BLTE":
        raise ValueError("not BLTE data")
    hsz, = struct.unpack(">I", b[4:8])
    if hsz == 0:
        sizes, pos = [(len(b) - 8, 0)], 8
    else:
        cnt = int.from_bytes(b[9:12], "big")
        sizes = [struct.unpack(">II", b[12 + 24 * i: 20 + 24 * i]) for i in range(cnt)]
        pos = hsz
    out = []
    for cs, _ds in sizes:
        chunk = b[pos:pos + cs]
        pos += cs
        mode = chunk[:1]
        if mode == b"N":
            out.append(chunk[1:])
        elif mode == b"Z":
            out.append(zlib.decompress(chunk[1:]))
        else:
            raise ValueError("unsupported BLTE block mode %r (encrypted?)" % mode)
    return b"".join(out)


class Encoding:
    def __init__(self, data):
        (magic, _v, cks, eks, cps, _eps, ccount, _ec, _u, esz) = struct.unpack(">2sBBBHHIIBI", data[:22])
        if magic != b"EN" or cks != 16 or eks != 16:
            raise SystemExit("Unexpected encoding file format")
        self.d, self.page, self.pages = data, cps * 1024, 22 + esz + ccount * 32
        idx = 22 + esz
        self.first = [data[idx + i * 32: idx + i * 32 + 16] for i in range(ccount)]

    def ekeys(self, ckey):
        i = bisect.bisect_right(self.first, ckey) - 1
        if i < 0:
            return []
        o = self.pages + i * self.page
        end, d = o + self.page, self.d
        while o < end:
            n = d[o]
            if n == 0:
                break
            if d[o + 6:o + 22] == ckey:
                return [d[o + 22 + 16 * j: o + 38 + 16 * j] for j in range(n)]
            o += 22 + 16 * n
        return []


# ------------------------------------------------------------------ root (path hash -> file id)
M32 = 0xFFFFFFFF


def _rot(x, k):
    return ((x << k) | (x >> (32 - k))) & M32


def hashlittle2(data):
    """Bob Jenkins' lookup3 hashlittle2 with zero seeds; returns (c, b)."""
    n = len(data)
    a = b = c = (0xDEADBEEF + n) & M32
    i = 0
    while n - i > 12:
        a = (a + int.from_bytes(data[i:i + 4], "little")) & M32
        b = (b + int.from_bytes(data[i + 4:i + 8], "little")) & M32
        c = (c + int.from_bytes(data[i + 8:i + 12], "little")) & M32
        a = (a - c) & M32; a ^= _rot(c, 4); c = (c + b) & M32
        b = (b - a) & M32; b ^= _rot(a, 6); a = (a + c) & M32
        c = (c - b) & M32; c ^= _rot(b, 8); b = (b + a) & M32
        a = (a - c) & M32; a ^= _rot(c, 16); c = (c + b) & M32
        b = (b - a) & M32; b ^= _rot(a, 19); a = (a + c) & M32
        c = (c - b) & M32; c ^= _rot(b, 4); b = (b + a) & M32
        i += 12
    rem = data[i:]
    if not rem:
        return c, b
    pad = rem + b"\0" * (12 - len(rem))
    a = (a + int.from_bytes(pad[0:4], "little")) & M32
    b = (b + int.from_bytes(pad[4:8], "little")) & M32
    c = (c + int.from_bytes(pad[8:12], "little")) & M32
    c ^= b; c = (c - _rot(b, 14)) & M32
    a ^= c; a = (a - _rot(c, 11)) & M32
    b ^= a; b = (b - _rot(a, 25)) & M32
    c ^= b; c = (c - _rot(b, 16)) & M32
    a ^= c; a = (a - _rot(c, 4)) & M32
    b ^= a; b = (b - _rot(a, 14)) & M32
    c ^= b; c = (c - _rot(b, 24)) & M32
    return c, b


def name_hash(path):
    c, b = hashlittle2(path.upper().replace("/", "\\").encode("ascii"))
    return (c << 32) | b


def find_files(root, wanted, fdids=None):
    """TSFM root (v2). wanted: {name_hash: key}, fdids: {file_data_id: key}.
    Returns {key: content_key}."""
    if root[:4] != b"TSFM":
        raise SystemExit("Unexpected root file format")
    hdr, _ver, total, _named = struct.unpack_from("<IIII", root, 4)
    pos, found, seen, good = hdr, {}, 0, set()
    fdids = fdids or {}
    while pos < len(root):
        cnt, _loc, _u1, flags = struct.unpack_from("<IIII", root, pos)
        pos += 17
        deltas = struct.unpack_from("<%di" % cnt, root, pos)
        pos += 4 * cnt
        named = not (flags & 0x10000000)
        keys, hashes = pos, pos + cnt * 16
        en = bool(_loc & 0x2)          # block holds the enUS version (other locales carry other-language art)
        if fdids:
            fid = 0
            for i in range(cnt):
                fid += deltas[i]
                k = fdids.get(fid)
                if k is not None and (k not in found or (en and k not in good)):
                    found[k] = root[keys + i * 16: keys + i * 16 + 16]
                    if en:
                        good.add(k)
                fid += 1
        if named and wanted:
            for i in range(cnt):
                h = int.from_bytes(root[hashes + i * 8: hashes + i * 8 + 8], "little")
                p = wanted.get(h)
                if p is not None and (p not in found or (en and p not in good)):
                    found[p] = root[keys + i * 16: keys + i * 16 + 16]
                    if en:
                        good.add(p)
        pos += cnt * (24 if named else 16)
        seen += cnt
    if seen != total:
        raise SystemExit("Root file did not parse cleanly (%d of %d entries)" % (seen, total))
    return found


# ------------------------------------------------------------------ build info / CDN
def parse_kv(path):
    kv = {}
    with open(path, "r", encoding="utf-8", errors="replace") as f:
        for line in f:
            if "=" in line and not line.startswith("#"):
                k, v = line.split("=", 1)
                kv[k.strip()] = v.strip()
    return kv


def build_info(wow_dir):
    """Row of .build.info for our product: dict of column -> value (empty if not readable)."""
    path = os.path.join(wow_dir, ".build.info")
    if not os.path.exists(path):
        return {}
    with open(path, "r", encoding="utf-8", errors="replace") as f:
        lines = [l.rstrip("\n") for l in f if l.strip()]
    cols = [c.split("!")[0] for c in lines[0].split("|")]
    for row in lines[1:]:
        d = dict(zip(cols, row.split("|")))
        if d.get("Product", "").lower() == PRODUCT:
            return d
    return {}


def find_build_config(data_dir, info):
    cfg_root = os.path.join(data_dir, "config")
    key = info.get("Build Key", "")
    if key:
        p = os.path.join(cfg_root, key[:2], key[2:4], key)
        if os.path.exists(p):
            return parse_kv(p)
    best = None
    for p in glob.glob(os.path.join(cfg_root, "*", "*", "*")):
        try:
            kv = parse_kv(p)
        except OSError:
            continue
        if kv.get("build-uid") == PRODUCT and "root" in kv and "encoding" in kv:
            num = "".join(ch for ch in kv.get("build-name", "").split("patch")[0] if ch.isdigit())
            score = int(num or 0)
            if best is None or score > best[0]:
                best = (score, kv)
    if not best:
        raise SystemExit("No build config for product '%s' found under %s" % (PRODUCT, cfg_root))
    return best[1]


def find_in_indices(folder, ekeys):
    """Search the local copies of the CDN archive indexes (Data\\indices\\*.index).
    ekeys: iterable of 16-byte keys -> {ekey: (archive_name, offset, size)}.
    Each index file is opened only while it is searched."""
    todo, out = set(ekeys), {}
    for p in glob.glob(os.path.join(folder, "*.index")):
        if not todo:
            break
        with open(p, "rb") as f:
            mm = mmap.mmap(f.fileno(), 0, access=mmap.ACCESS_READ)
            try:
                n, = struct.unpack_from("<I", mm, len(mm) - 12)
                blocks = (n + 169) // 170
                last = [mm[blocks * 4096 + 16 * i: blocks * 4096 + 16 * i + 16] for i in range(blocks)]
                for ek in list(todo):
                    i = bisect.bisect_left(last, ek)
                    if i >= len(last):
                        continue
                    for j in range(170):
                        o = i * 4096 + j * 24
                        cur = mm[o:o + 16]
                        if cur == ek:
                            size, off = struct.unpack(">II", mm[o + 16:o + 24])
                            out[ek] = (os.path.basename(p)[:-6], off, size)
                            todo.discard(ek)
                            break
                        if cur > ek or cur == b"\0" * 16:
                            break
            finally:
                mm.close()
    return out


def cdn_get(hosts, path, rng=None, timeout=60):
    last = None
    for h in hosts:
        base = h if h.startswith("http") else "https://" + h
        req = urllib.request.Request("%s/%s" % (base.rstrip("/"), path), headers={"User-Agent": "Mozilla/5.0"})
        if rng:
            req.add_header("Range", "bytes=%d-%d" % rng)
        try:
            with urllib.request.urlopen(req, timeout=timeout) as r:
                return r.read()
        except Exception as e:  # try the next host
            last = e
    raise RuntimeError("CDN request failed for %s: %s" % (path, last))


# ------------------------------------------------------------------ images
def decode_blp(raw):
    from PIL import Image
    return Image.open(io.BytesIO(raw)).convert("RGBA")


def stitch(tiles):
    from PIL import Image
    w, h = tiles[0].size
    sheet = Image.new("RGBA", (w * 4, h * 3))
    for i, im in enumerate(tiles):
        sheet.paste(im, ((i % 4) * w, (i // 4) * h))
    return sheet.crop((0, 0, min(MAP_W, sheet.width), min(MAP_H, sheet.height)))


# ------------------------------------------------------------------ game tables (WDC5 .db2)
class DB2:
    """Minimal WDC5 reader: returns each row's field values (ints / int lists), id and relationship value."""

    def __init__(self, d):
        if d[:4] != b"WDC5":
            raise ValueError("unsupported table format %r" % d[:4])
        self.d = d
        o = 8 + 128
        (self.rc, self.fc, self.rs, _ss, _th, _lh, _mn, _mx, _loc) = struct.unpack_from("<9I", d, o)
        o += 36
        o += 4
        (_tfc, _bo, _lk, fsisz, commonsz, palsz, nsec) = struct.unpack_from("<7I", d, o)
        o += 28
        secs = [struct.unpack_from("<QIIIIIIII", d, o + 40 * i) for i in range(nsec)]
        o += 40 * nsec + 4 * self.fc
        self.fsi = [struct.unpack_from("<HHIIIII", d, o + 24 * i) for i in range(fsisz // 24)]
        o += fsisz
        self.pal = d[o:o + palsz]
        o += palsz
        self.common = d[o:o + commonsz]
        self.rows = []
        commons, pals, co, po = {}, {}, 0, 0
        for i, (_fo, _fs, add, st, v0, _v1, _v2) in enumerate(self.fsi):
            if st == 2:
                c = {struct.unpack_from("<I", self.common, co + 8 * j)[0]: struct.unpack_from("<I", self.common, co + 8 * j + 4)[0]
                     for j in range(add // 8)}
                c["_def"] = v0
                commons[i] = c
                co += add
            elif st in (3, 4):
                pals[i] = po
                po += add
        for (_th2, foff, rc, strsz, _oe, idsz, relsz, _omc, cpc) in secs:
            p = foff + rc * self.rs + strsz
            ids = list(struct.unpack_from("<%dI" % rc, d, p)) if idsz else None
            p += idsz + 8 * cpc
            rel = {}
            if relsz:
                n = struct.unpack_from("<I", d, p)[0]
                for j in range(n):
                    fid, ridx = struct.unpack_from("<II", d, p + 12 + 8 * j)
                    rel[ridx] = fid
            for r in range(rc):
                rec = d[foff + r * self.rs: foff + (r + 1) * self.rs]
                vals = []
                for i, (fo, fs, _add, st, _v0, v1, v2) in enumerate(self.fsi):
                    if st == 0:
                        vals.append(self._bits(rec, fo, fs))
                    elif st == 1:
                        vals.append(self._bits(rec, fo, v1))
                    elif st == 5:
                        v = self._bits(rec, fo, v1)
                        if v1 and v >> (v1 - 1):
                            v -= 1 << v1
                        vals.append(v)
                    elif st == 2:
                        c = commons[i]
                        vals.append(c.get(ids[r] if ids else None, c["_def"]))
                    elif st == 3:
                        vals.append(struct.unpack_from("<I", self.pal, pals[i] + 4 * self._bits(rec, fo, v1))[0])
                    elif st == 4:
                        vals.append(list(struct.unpack_from("<%dI" % v2, self.pal, pals[i] + 4 * v2 * self._bits(rec, fo, v1))))
                    else:
                        vals.append(None)
                self.rows.append({"vals": vals, "id": ids[r] if ids else None, "rel": rel.get(r)})

    @staticmethod
    def _bits(buf, bitpos, n):
        if n == 0:
            return 0
        b0 = bitpos >> 3
        nb = ((bitpos & 7) + n + 7) >> 3
        return (int.from_bytes(buf[b0:b0 + nb], "little") >> (bitpos & 7)) & ((1 << n) - 1)


def overlay_plan(xmap_raw, ov_raw, tile_raw):
    """-> (art_of {uiMapID: art id}, overlays {art id: [dict(w,h,ox,oy,tiles={(row,col): file id})]})"""
    art_of = {r["rel"]: r["vals"][1] for r in DB2(xmap_raw).rows if r["rel"] is not None}
    tiles = {}
    for r in DB2(tile_raw).rows:
        row, col, layer, fdid = r["vals"][:4]
        if layer == 0 and r["rel"] is not None:
            tiles.setdefault(r["rel"], {})[(row, col)] = fdid
    overlays = {}
    for r in DB2(ov_raw).rows:
        v = r["vals"]    # ID, UiMapArtID, width, height, offsetX, offsetY, ...
        if v[0] in tiles:
            overlays.setdefault(v[1], []).append({"w": v[2], "h": v[3], "ox": v[4], "oy": v[5], "tiles": tiles[v[0]]})
    return art_of, overlays


def reveal(base, overlays, raw_of):
    """Paint the exploration overlays onto the base map. raw_of: {file id: BLP bytes}."""
    from PIL import Image
    img = base.copy()
    n = 0
    for ov in overlays:
        for (row, col), fdid in ov["tiles"].items():
            raw = raw_of.get(fdid)
            if raw is None:
                continue
            try:
                t = decode_blp(raw)
            except Exception:
                continue
            w, h = min(t.width, ov["w"] - col * 256), min(t.height, ov["h"] - row * 256)
            x, y = ov["ox"] + col * 256, ov["oy"] + row * 256
            if w <= 0 or h <= 0:
                continue
            sx, sy = max(0, -x), max(0, -y)
            w, h = min(w, img.width - x), min(h, img.height - y)
            if w - sx <= 0 or h - sy <= 0:
                continue
            piece = t.crop((sx, sy, w, h))
            layer = Image.new("RGBA", img.size, (0, 0, 0, 0))
            layer.paste(piece, (x + sx, y + sy))
            img = Image.alpha_composite(img, layer)
            n += 1
    return img, n


# ------------------------------------------------------------------ fetching files (local first, then CDN)
class Fetcher:
    def __init__(self, args, data_dir, info, casc, enc):
        self.args, self.data_dir, self.info, self.casc, self.enc = args, data_dir, info, casc, enc
        hosts = list(args.cdn_host or [])
        hosts += [h for h in info.get("CDN Hosts", "").split() if h]
        hosts += ["us.cdn.blizzard.com", "level3.blizzard.com", "eu.cdn.blizzard.com", "cdn.blizzard.com"]
        self.hosts = list(dict.fromkeys(hosts))
        self.cdn_path = info.get("CDN Path", "tpr/wow").strip("/") or "tpr/wow"

    def get(self, ckeys, label):
        """ckeys: {key: content key} -> {key: decoded bytes} (whatever could be had)."""
        out, missing = {}, {}
        for k, ck in ckeys.items():
            eks = self.enc.ekeys(ck)
            data = None
            for cand in eks:
                data = self.casc.read(cand)
                if data:
                    break
            if data:
                out[k] = data
            elif eks:
                missing[k] = eks[0]
        print("%s: %d stored locally, %d not stored locally" % (label, len(out), len(missing)))
        if missing and not self.args.no_cdn:
            print("  locating them in the CDN archive indexes...")
            hits = find_in_indices(os.path.join(self.data_dir, "indices"), set(missing.values()))
            print("  found %d of %d in the indexes" % (len(hits), len(set(missing.values()))))

            def job(item):
                k, ek = item
                hit = hits.get(ek)
                if hit:
                    arch, off, size = hit
                    path, rng = "%s/data/%s/%s/%s" % (self.cdn_path, arch[:2], arch[2:4], arch), (off, off + size - 1)
                else:   # not inside an archive: stored as its own file
                    h = ek.hex()
                    path, rng = "%s/data/%s/%s/%s" % (self.cdn_path, h[:2], h[2:4], h), None
                last = None
                for _try in range(3):
                    try:
                        return k, blte_decode(cdn_get(self.hosts, path, rng))
                    except Exception as e:
                        last = e
                        time.sleep(1)
                raise last

            done = 0
            with ThreadPoolExecutor(max_workers=6) as ex:
                for fut in [ex.submit(job, it) for it in missing.items()]:
                    try:
                        k, data = fut.result()
                        out[k] = data
                    except Exception as e:
                        print("  download failed: %s" % e)
                        continue
                    done += 1
                    if done % 50 == 0:
                        print("  downloaded %d/%d" % (done, len(missing)))
            print("  downloaded %d/%d" % (done, len(missing)))
        return out


def main():
    ap = argparse.ArgumentParser(description="Extract zone maps from WoW Forever for Nearby Tracker")
    ap.add_argument("--wow", default=DEFAULT_WOW, help="World of Warcraft folder (default: %(default)s)")
    ap.add_argument("--data-dir", help="the Data folder, if it is not <wow>\\Data")
    ap.add_argument("--out", default=os.path.join(os.path.dirname(os.path.abspath(__file__)), "maps"))
    ap.add_argument("--only", help="comma separated zone names, e.g. 'Dun Morogh,Elwynn Forest'")
    ap.add_argument("--no-cdn", action="store_true", help="only use tiles already stored locally")
    ap.add_argument("--no-overlays", action="store_true", help="base parchment only (zones look unexplored)")
    ap.add_argument("--cdn-host", action="append", help="extra CDN host to try first")
    ap.add_argument("--force", action="store_true", help="redo zones that already have a finished image")
    args = ap.parse_args()

    data_dir = args.data_dir or os.path.join(args.wow, "Data")
    os.makedirs(args.out, exist_ok=True)
    zones = ZONES
    if args.only:
        want = {norm(z) for z in args.only.split(",")}
        zones = [z for z in ZONES if norm(z[0]) in want or norm(z[1]) in want]
        if not zones:
            raise SystemExit("No known zone matches --only")

    index_path = os.path.join(args.out, "index.json")
    index = {"zones": {}}
    if os.path.exists(index_path):
        try:
            index = json.load(open(index_path, encoding="utf-8"))
        except (OSError, ValueError):
            pass

    def finished(z):
        if not os.path.exists(os.path.join(args.out, z[1] + ".png")):
            return False
        e = index["zones"].get(norm(z[0]), {})
        return args.no_overlays or e.get("revealed", False) or z[0] not in UIMAP

    zones = [z for z in zones if args.force or not finished(z)]
    if not zones:
        print("Nothing to do: every zone already has a finished image (use --force to redo).")
        return

    t0 = time.time()
    info = build_info(args.wow)
    cfg = find_build_config(data_dir, info)
    print("Build: %s  (%s)" % (cfg.get("build-name", "?"), PRODUCT))
    casc = Casc(data_dir)
    enc_ckey, enc_ekey = [bytes.fromhex(x) for x in cfg["encoding"].split()[:2]]
    enc = Encoding(casc.read(enc_ekey))
    root_ckey = bytes.fromhex(cfg["root"])
    root = None
    for ek in enc.ekeys(root_ckey):
        root = casc.read(ek)
        if root:
            break
    if root is None:
        raise SystemExit("Could not read the root file from local storage")
    fetcher = Fetcher(args, data_dir, info, casc, enc)

    # base art (by path) and the three overlay tables (by file id)
    wanted = {}
    for _disp, folder in zones:
        for i in range(1, 13):
            p = "Interface\\WorldMap\\%s\\%s%d.blp" % (folder, folder, i)
            wanted[name_hash(p)] = p
    do_overlays = not args.no_overlays and any(z[0] in UIMAP for z in zones)
    tables = {FDID_UIMAPXMAPART: "xmap", FDID_WORLDMAPOVERLAY: "ov", FDID_WORLDMAPOVERLAYTILE: "tile"} if do_overlays else {}
    found = find_files(root, wanted, tables)
    print("Looked up %d zone(s) in %.0fs" % (len(zones), time.time() - t0))
    base_found = {k: v for k, v in found.items() if k not in tables.values()}
    tiles_raw = fetcher.get(base_found, "Base map tiles")

    overlays_of, art_of, ov_raw = {}, {}, {}
    if do_overlays:
        try:
            tbl = fetcher.get({k: found[k] for k in ("xmap", "ov", "tile") if k in found}, "Overlay tables")
            if len(tbl) < 3:
                raise ValueError("overlay tables not available (%d of 3)" % len(tbl))
            art_of, overlays_of = overlay_plan(tbl["xmap"], tbl["ov"], tbl["tile"])
            need = {}
            for disp, _f in zones:
                for ov in overlays_of.get(art_of.get(UIMAP.get(disp)), []):
                    for fd in ov["tiles"].values():
                        need[fd] = "ovt%d" % fd
            if need:
                f2 = find_files(root, {}, {fd: k for fd, k in need.items()})
                got = fetcher.get(f2, "Overlay tiles")
                ov_raw = {int(k[3:]): v for k, v in got.items()}
        except Exception as e:
            print("  overlays unavailable, using the plain parchment: %s" % e)
            do_overlays = False
    del root

    ok = 0
    for disp, folder in zones:
        paths = ["Interface\\WorldMap\\%s\\%s%d.blp" % (folder, folder, i) for i in range(1, 13)]
        if not all(p in tiles_raw for p in paths):
            have = sum(1 for p in paths if p in tiles_raw)
            if have == 0 and not any(p in found for p in paths):
                print("  skip %-24s (no such art in this build)" % disp)
            else:
                print("  skip %-24s (only %d/12 tiles available)" % (disp, have))
            continue
        try:
            img = stitch([decode_blp(tiles_raw[p]) for p in paths])
            revealed, note = False, ""
            ovs = overlays_of.get(art_of.get(UIMAP.get(disp)), []) if do_overlays else []
            if ovs:
                img, n = reveal(img, ovs, ov_raw)
                total = sum(len(o["tiles"]) for o in ovs)
                revealed = n == total
                note = "  (%d/%d overlay tiles painted)" % (n, total)
                if not revealed:
                    note += " - some overlay tiles are missing, re-run to retry"
        except Exception as e:
            print("  skip %-24s (image error: %s)" % (disp, e))
            continue
        fn = folder + ".png"
        img.save(os.path.join(args.out, fn), optimize=True)
        index["zones"][norm(disp)] = {"name": disp, "file": fn, "w": img.width, "h": img.height, "revealed": revealed}
        ok += 1
        print("  wrote %-24s -> maps\\%s%s" % (disp, fn, note))
    json.dump(index, open(index_path, "w", encoding="utf-8"), indent=1)
    print("Done: %d zone image(s) written in %.0fs. Reload the Nearby Tracker page." % (ok, time.time() - t0))


if __name__ == "__main__":
    main()
