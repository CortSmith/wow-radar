#!/usr/bin/env python3
"""
Nearby Tracker - companion app for WoW Forever.

Tails the newest WoWCombatLog*.txt (turn it on in game with /combatlog), keeps a
list of every player the log has mentioned, remembers where each one was last
seen, and serves a local-only web page (http://127.0.0.1:8777) that updates live.

Python 3.9+ standard library only. Read-only: it never touches the game, sends
no input to it, and listens on 127.0.0.1 only.
"""
import argparse
import calendar
import collections
import csv
import glob
import json
import math
import os
import re
import sys
import threading
import time
import webbrowser
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

DEFAULT_LOGS = r"C:\Program Files (x86)\World of Warcraft\_forever_\Logs"

TS_RE = re.compile(r"^(\d+)/(\d+)/(\d+) (\d+):(\d+):(\d+)\.(\d+)([+-]\d+)?")
FLOAT_RE = re.compile(r"^-?\d+\.\d+$")
INT_RE = re.compile(r"^\d+$")
UNIT_PREFIXES = ("Player-", "Pet-", "Creature-", "Vehicle-")

# Position-estimate tuning (yards / seconds). See estimate().
EST_LIFE = 300.0         # a player's heat blob lasts this long after their last located event (5 minutes)
EST_APPROX_WEIGHT = 0.3  # an approximate fix says much less than an exact one
EST_RANK_DECAY = 0.9     # older events inside the window count a bit less
EST_MAX_SPEED = 30.0     # yd/s cap on extrapolated movement
EST_PROJECT_SECS = 10.0  # never extrapolate movement further than this


def estimate(evs, now, n, gap, life=EST_LIFE):
    """Best guess of where a player is, from their recent located events.

    evs: chronological (t, x, y, kind, map) tuples, kind 'e' exact / 'a' approximate.
    Window: starting from the newest located event, walk back while the gap between
    consecutive events stays <= gap seconds (one 'encounter': from first seen to last
    seen), keeping at most n events. The guess is the weighted mean of those events
    (exact ones count more, newer ones count more). If the exact fixes show movement,
    one more point is projected forward along it.
    Returns None once the newest event is older than `life` seconds."""
    if not evs:
        return None
    mp = evs[-1][4]
    win, prev = [], None
    for e in reversed(evs):
        if e[4] != mp or (prev is not None and prev - e[0] > gap):
            break
        win.append(e)
        prev = e[0]
        if len(win) >= n:
            break
    t_last = win[0][0]
    if now - t_last > life:
        return None
    pts = []
    for rank, (t, x, y, kind, _m) in enumerate(win):
        w = (1.0 if kind == "e" else EST_APPROX_WEIGHT) * EST_RANK_DECAY ** rank
        pts.append([x, y, w])
    exact = [e for e in win if e[3] == "e"]
    if len(exact) >= 2:
        a, b = exact[0], exact[min(4, len(exact) - 1)]
        dt = a[0] - b[0]
        if dt >= 1.5:
            vx, vy = (a[1] - b[1]) / dt, (a[2] - b[2]) / dt
            sp = math.hypot(vx, vy)
            if sp > EST_MAX_SPEED:
                vx, vy, sp = vx * EST_MAX_SPEED / sp, vy * EST_MAX_SPEED / sp, EST_MAX_SPEED
            if sp >= 0.5:
                tp = min(max(0.0, now - a[0]), EST_PROJECT_SECS)
                pts.append([a[1] + vx * tp, a[2] + vy * tp, 1.5])
    wsum = sum(p[2] for p in pts)
    mx = sum(p[0] * p[2] for p in pts) / wsum
    my = sum(p[1] * p[2] for p in pts) / wsum
    spread = math.sqrt(sum(p[2] * ((p[0] - mx) ** 2 + (p[1] - my) ** 2) for p in pts) / wsum / 2)
    return {"x": round(mx, 1), "y": round(my, 1), "s": round(max(8.0, spread), 1),
            "map": mp, "t": t_last, "n": len(win), "ex": len(exact)}


def parse_ts(s):
    """'11/10/2026 15:11:56.040-6' -> epoch seconds (UTC)."""
    m = TS_RE.match(s)
    if not m:
        return None
    mo, d, y, hh, mm, ss, ms, off = m.groups()
    try:
        t = calendar.timegm((int(y), int(mo), int(d), int(hh), int(mm), int(ss)))
    except (ValueError, OverflowError):
        return None
    return t + int(ms[:3].ljust(3, "0")) / 1000.0 - (int(off) if off else 0) * 3600


def common_token_suffix(names):
    """Longest run of '-' separated trailing tokens shared by every name."""
    token_lists = [n.split("-") for n in names]
    best = []
    shortest = min(len(t) for t in token_lists)
    for k in range(1, shortest):  # always leave at least one token for the name
        tail = token_lists[0][-k:]
        if all(t[-k:] == tail for t in token_lists):
            best = tail
        else:
            break
    return "-" + "-".join(best) if best else ""


class Tracker:
    def __init__(self, realm_suffix="", clock="wall", dump=False):
        self.lock = threading.Lock()
        self.players = {}          # guid -> dict
        self.me_guid = None
        self.me = {"x": None, "y": None, "map": None, "t": 0, "facing": 0}
        self.maps = {}             # uiMapID -> {name, bounds}
        self.cur_map = None
        self.zone_name = ""
        self.advanced = None
        self.realm_suffix = realm_suffix
        self.realm_locked = bool(realm_suffix)
        self.clock = clock
        self.dump = dump
        self.last_t = 0.0
        self.lines = 0
        self.ver = 0               # bumped whenever a line was parsed (lets the web server push only on change)
        self.bad = 0
        self.file = ""
        self.file_size = 0
        self.started = time.time()

    # ---------------------------------------------------------------- parsing
    def feed(self, line):
        line = line.rstrip("\r\n")
        ts_s, sep, rest = line.partition("  ")
        if not sep:
            return
        t = parse_ts(ts_s)
        if t is None:
            return
        try:
            f = next(csv.reader([rest]))
        except Exception:
            self.bad += 1
            return
        if not f:
            return
        try:
            self._handle(t, f)
        except Exception as exc:  # never let one odd line kill the tailer
            self.bad += 1
            if self.dump:
                print("parse error:", exc, "|", line[:200])

    def _handle(self, t, f):
        ev = f[0]
        with self.lock:
            self.lines += 1
            self.ver += 1
            if t > self.last_t:
                self.last_t = t
            if ev == "COMBAT_LOG_VERSION":
                if "ADVANCED_LOG_ENABLED" in f:
                    i = f.index("ADVANCED_LOG_ENABLED")
                    self.advanced = f[i + 1] == "1" if i + 1 < len(f) else None
                return
            if ev == "ZONE_CHANGE":
                if len(f) > 2:
                    self.zone_name = f[2]
                return
            if ev == "MAP_CHANGE":
                if len(f) >= 7:
                    mid = int(f[1])
                    a, b, c, d = (float(v) for v in f[3:7])
                    # (yMin, xMin, yMax, xMax) in the order the client writes them
                    self.maps[mid] = {
                        "name": f[2],
                        "x0": min(b, d), "x1": max(b, d),
                        "y0": min(a, c), "y1": max(a, c),
                    }
                    self.cur_map = mid
                return
            if len(f) < 9:
                return

            involved = []
            for g, n, fl in ((f[1], f[2], f[3]), (f[5], f[6], f[7])):
                if g.startswith("Player-"):
                    self._touch(g, n, fl, t)
                    involved.append(g)

            pos = self._find_pos(f)
            exact_guid = None
            if pos and pos["guid"].startswith("Player-") and pos["guid"] in self.players:
                exact_guid = pos["guid"]
                p = self.players[exact_guid]
                p["exact"] = {"x": pos["x"], "y": pos["y"], "map": pos["map"], "t": t,
                              "hp": pos["hp"], "max": pos["max"]}
                if exact_guid == self.me_guid:
                    self.me.update(x=pos["x"], y=pos["y"], map=pos["map"], t=t,
                                   facing=pos["facing"])
                    self.cur_map = pos["map"]
                else:
                    self._rec(p, t, pos["x"], pos["y"], "e", pos["map"])

            # Players named without a position of their own: they were within log
            # range of me, so remember where I was as an approximate fix.
            if self.me["x"] is not None and abs(t - self.me["t"]) < 120:
                for g in involved:
                    if g == self.me_guid or g == exact_guid:
                        continue
                    p = self.players[g]
                    p["approx"] = {"x": self.me["x"], "y": self.me["y"],
                                   "map": self.me["map"], "t": t}
                    self._rec(p, t, self.me["x"], self.me["y"], "a", self.me["map"])
            if self.dump:
                names = [self.players[g]["raw"] for g in involved]
                print("%-24s %-40s %s" % (ev, ", ".join(names)[:40],
                                          ("pos %s %.1f,%.1f map %s" % (
                                              self.players[exact_guid]["raw"] if exact_guid else pos["guid"],
                                              pos["x"], pos["y"], pos["map"])) if pos else ""))

    @staticmethod
    def _rec(p, t, x, y, kind, mp):
        """Remember one located event for the position estimate (skip same-instant duplicates)."""
        ev = p["ev"]
        if ev:
            l = ev[-1]
            if l[3] == kind and l[1] == x and l[2] == y and t - l[0] < 0.5:
                return
        ev.append((t, x, y, kind, mp))

    def _touch(self, g, raw, flags, t):
        p = self.players.get(g)
        if p is None:
            p = {"guid": g, "raw": raw, "flags": flags, "first": t, "last": t, "n": 0,
                 "exact": None, "approx": None, "ev": collections.deque(maxlen=60)}
            self.players[g] = p
            self._detect_realm()
        p["raw"] = raw
        p["n"] += 1
        if t >= p["last"]:
            p["last"] = t
        try:
            fv = int(flags, 16)
        except ValueError:
            return
        p["flags"] = flags
        if fv & 0x1:                      # affiliation "mine": that's me
            self.me_guid = g

    @staticmethod
    def _find_pos(f):
        """Advanced-logging block: ... info, owner, hp, maxhp, ... X, Y, map, facing, level.
        X sits 14 fields after the info GUID."""
        n = len(f)
        for i in range(23, n - 4):
            if (FLOAT_RE.match(f[i]) and FLOAT_RE.match(f[i + 1])
                    and INT_RE.match(f[i + 2]) and FLOAT_RE.match(f[i + 3])):
                guid = f[i - 14]
                if guid.startswith(UNIT_PREFIXES):
                    try:
                        return {"guid": guid, "x": float(f[i]), "y": float(f[i + 1]),
                                "map": int(f[i + 2]), "facing": float(f[i + 3]),
                                "hp": int(f[i - 12]), "max": int(f[i - 11])}
                    except ValueError:
                        return None
                return None
        return None

    def _detect_realm(self):
        """Log names look like 'First-Last-PvP-US'. The trailing realm part is the
        same for everyone, so find it once enough names have been seen."""
        if self.realm_locked or len(self.players) < 6:
            return
        suffix = common_token_suffix([p["raw"] for p in self.players.values()])
        if suffix:
            self.realm_suffix = suffix

    def display_name(self, raw):
        if self.realm_suffix and raw.endswith(self.realm_suffix):
            raw = raw[: -len(self.realm_suffix)]
        return raw.replace("-", " ")

    # ---------------------------------------------------------------- output
    def snapshot(self, keep_minutes=60, n=15, gap=30.0, log_range=200.0, now=None, limit=600):
        with self.lock:
            if now is None:
                now = self.last_t if (self.clock == "log" and self.last_t) else time.time()
            cutoff = now - keep_minutes * 60
            out = []
            cur = self.me.get("map") if self.me.get("map") is not None else self.cur_map
            cand = [p for p in self.players.values() if p["last"] >= cutoff]
            cand.sort(key=lambda p: -p["last"])
            for p in cand:
                if len(out) >= limit and p["guid"] != self.me_guid:
                    break
                # only players that have a fix on the map you are looking at (the page ignores the rest)
                if cur is not None and p["guid"] != self.me_guid and not (
                        (p["exact"] and p["exact"].get("map") == cur) or (p["approx"] and p["approx"].get("map") == cur)):
                    continue
                try:
                    fv = int(p["flags"], 16)
                except ValueError:
                    fv = 0
                if fv & 0x1 or fv & 0x10:
                    rel = "F"      # friendly
                elif fv & 0x40:
                    rel = "H"      # hostile
                elif fv & 0x20:
                    rel = "N"      # neutral
                else:
                    rel = "?"
                is_me = p["guid"] == self.me_guid
                est = None if is_me else estimate(list(p["ev"]), now, n, gap)
                out.append({
                    "guid": p["guid"], "name": self.display_name(p["raw"]), "raw": p["raw"],
                    "rel": rel, "flags": p["flags"], "last": p["last"], "n": p["n"],
                    "me": is_me,
                    "exact": p["exact"], "approx": p["approx"],
                    "est": est,
                })
            return {
                "now": now,
                "players": out,
                "me": dict(self.me, guid=self.me_guid,
                           name=self.display_name(self.players[self.me_guid]["raw"])
                           if self.me_guid in self.players else None),
                "map": self.cur_map,
                "zone": self.zone_name,
                "maps": {str(k): v for k, v in self.maps.items()},
                "advanced": self.advanced,
                "realmSuffix": self.realm_suffix,
                "stats": {"file": self.file, "size": self.file_size, "lines": self.lines,
                          "bad": self.bad, "lastEvent": self.last_t},
            }


# ------------------------------------------------------------------- tailing
def newest_log(folder):
    files = glob.glob(os.path.join(folder, "WoWCombatLog*.txt"))
    return max(files, key=os.path.getmtime) if files else None


def tail_loop(tracker, folder, from_start, tail_mb, stop):
    cur = None
    fh = None
    buf = b""
    first = True
    warned = False
    while not stop.is_set():
        try:
            path = newest_log(folder)
            if path is None:
                if not warned:
                    print("Waiting for a WoWCombatLog*.txt in %s  (type /combatlog in game)" % folder)
                    warned = True
                stop.wait(1.0)
                continue
            if path != cur:
                if fh:
                    fh.close()
                fh = open(path, "rb")
                cur = path
                buf = b""
                size = os.path.getsize(path)
                skip_partial = False
                if first and not from_start and size > tail_mb * 1048576:
                    fh.seek(size - int(tail_mb * 1048576))
                    skip_partial = True
                first = False
                tracker.file = os.path.basename(path)
                print("Reading %s" % path)
                if skip_partial:
                    fh.readline()
            size = os.path.getsize(cur)
            tracker.file_size = size
            if size < fh.tell():          # file was truncated / restarted
                fh.seek(0)
                buf = b""
            chunk = fh.read(4 * 1048576)
            if chunk:
                buf += chunk
                *complete, buf = buf.split(b"\n")
                for raw in complete:
                    tracker.feed(raw.decode("utf-8", "replace"))
                continue                   # more may be waiting
        except OSError as exc:
            print("log read problem (will retry):", exc)
            cur = None
            if fh:
                fh.close()
                fh = None
        stop.wait(0.15)


# ----------------------------------------------------------------- web server
def make_handler(tracker, keep_minutes, port, maps_dir):
    allowed_hosts = {"127.0.0.1:%d" % port, "localhost:%d" % port}

    class Handler(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def log_message(self, *a):
            pass

        def _send(self, code, body, ctype):
            self.send_response(code)
            self.send_header("Content-Type", ctype)
            self.send_header("Content-Length", str(len(body)))
            self.send_header("Cache-Control", "no-store")
            self.end_headers()
            self.wfile.write(body)

        def do_GET(self):
            # Only answer requests addressed to localhost (blocks DNS-rebinding tricks).
            if self.headers.get("Host", "") not in allowed_hosts:
                self._send(403, b"forbidden", "text/plain")
                return
            url = urlparse(self.path)
            path = url.path
            q = parse_qs(url.query)

            def num(key, default, lo, hi):
                try:
                    return min(hi, max(lo, float(q[key][0])))
                except (KeyError, ValueError, IndexError):
                    return default

            kw = {"n": int(num("n", 15, 1, 60)), "gap": num("gap", 30.0, 3, 900),
                  "log_range": num("range", 200.0, 5, 500)}
            keep = num("keep", keep_minutes, 1, 600)
            if path == "/":
                self._send(200, INDEX_HTML.encode("utf-8"), "text/html; charset=utf-8")
            elif path.startswith("/maps/"):
                name = path[len("/maps/"):]
                ext = os.path.splitext(name)[1].lower()
                if not re.fullmatch(r"[A-Za-z0-9_.\-]+", name) or ext not in (".png", ".json"):
                    self._send(404, b"not found", "text/plain")
                    return
                try:
                    with open(os.path.join(maps_dir, name), "rb") as fh:
                        body = fh.read()
                except OSError:
                    self._send(404, b"not found", "text/plain")
                    return
                self._send(200, body, "image/png" if ext == ".png" else "application/json")
            elif path == "/api/state":
                body = json.dumps(tracker.snapshot(keep, **kw), separators=(',', ':')).encode()
                self._send(200, body, "application/json")
            elif path == "/events":
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.send_header("Cache-Control", "no-store")
                self.send_header("Connection", "close")
                self.end_headers()
                try:
                    last_ver, last_sent = -1, 0.0
                    while True:
                        now = time.time()
                        # push as soon as new log lines were parsed (at most 4 per second); heartbeat every 3 s
                        if (tracker.ver != last_ver and now - last_sent >= 0.25) or now - last_sent >= 3.0:
                            last_ver = tracker.ver
                            data = json.dumps(tracker.snapshot(keep, **kw), separators=(",", ":"))
                            self.wfile.write(b"data: " + data.encode() + b"\n\n")
                            self.wfile.flush()
                            last_sent = now
                        time.sleep(0.05)
                except (BrokenPipeError, ConnectionError, OSError):
                    pass
            else:
                self._send(404, b"not found", "text/plain")

    return Handler


def main():
    ap = argparse.ArgumentParser(description="Nearby Tracker for WoW Forever")
    ap.add_argument("--logs", default=os.environ.get("WOW_LOGS", DEFAULT_LOGS),
                    help="folder that holds WoWCombatLog*.txt (default: %(default)s)")
    ap.add_argument("--port", type=int, default=8777)
    ap.add_argument("--keep", type=float, default=60, help="forget players unseen for this many minutes")
    ap.add_argument("--realm-suffix", default="",
                    help="realm part of log names to strip, e.g. -PvP-US (auto-detected if omitted)")
    ap.add_argument("--from-start", action="store_true", help="read the whole current log, not just the last part")
    ap.add_argument("--tail-mb", type=float, default=8, help="at start, read only the last N MB of the log")
    ap.add_argument("--clock", choices=("wall", "log"), default="wall",
                    help="'log' measures ages from the newest log line (for replaying old logs)")
    ap.add_argument("--dump", action="store_true", help="print every parsed event (to check the log format)")
    ap.add_argument("--maps", default=os.path.join(os.path.dirname(os.path.abspath(__file__)), "maps"),
                    help="folder with zone map images made by extract_maps.py (default: %(default)s)")
    ap.add_argument("--no-browser", action="store_true")
    args = ap.parse_args()

    tracker = Tracker(args.realm_suffix, args.clock, args.dump)
    stop = threading.Event()
    threading.Thread(target=tail_loop, daemon=True,
                     args=(tracker, args.logs, args.from_start, args.tail_mb, stop)).start()

    server = ThreadingHTTPServer(("127.0.0.1", args.port), make_handler(tracker, args.keep, args.port, args.maps))
    server.daemon_threads = True
    url = "http://127.0.0.1:%d/" % args.port
    print("Nearby Tracker running at %s   (Ctrl+C to stop)" % url)
    print("Log folder: %s" % args.logs)
    if not args.no_browser:
        threading.Timer(0.8, lambda: webbrowser.open(url)).start()
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        stop.set()
        server.server_close()


INDEX_HTML = r"""<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Nearby Tracker</title>
<style>
:root{--bg:#10131a;--panel:#171b24;--line:#2a3140;--text:#dfe5f0;--dim:#8590a5;
--ally:#4fa3ff;--other:#ff6b5e;--gold:#e8c35a;color-scheme:dark}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--text);font:14px/1.4 system-ui,Segoe UI,Roboto,sans-serif;
height:100vh;display:flex;flex-direction:column}
header{display:flex;flex-wrap:wrap;gap:6px 18px;align-items:center;padding:8px 14px;background:var(--panel);
border-bottom:1px solid var(--line)}
header h1{font-size:15px;margin:0;font-weight:600}
header .sp{flex:1}
label{color:var(--dim);display:inline-flex;align-items:center;gap:5px;white-space:nowrap}
input[type=number]{width:58px;background:#0c0f15;color:var(--text);border:1px solid var(--line);border-radius:4px;padding:2px 4px}
select,button{background:#0c0f15;color:var(--text);border:1px solid var(--line);border-radius:4px;padding:3px 8px;font:inherit;cursor:pointer}
button:hover,select:hover{border-color:var(--dim)}
#warn{display:none;background:#4a2b12;color:#ffd9a8;padding:6px 14px;border-bottom:1px solid var(--line)}
main{flex:1;display:flex;min-height:0}
#mapwrap{flex:1;position:relative;min-width:0}
canvas{position:absolute;inset:0;width:100%;height:100%;display:block;cursor:grab}
canvas.drag{cursor:grabbing}
#tip{position:absolute;pointer-events:none;background:#000c;border:1px solid var(--line);border-radius:4px;
padding:3px 7px;font-size:12px;display:none;white-space:nowrap}
aside{width:340px;display:flex;flex-direction:column;border-left:1px solid var(--line);min-height:0}
.col{flex:1;min-height:0;display:flex;flex-direction:column;border-bottom:1px solid var(--line)}
.col h2{margin:0;padding:6px 12px;font-size:13px;background:var(--panel);display:flex;gap:8px;align-items:center}
.col h2 .n{color:var(--dim);font-weight:400}
.list{overflow:auto;flex:1}
.row{display:flex;gap:8px;align-items:center;padding:4px 12px;cursor:pointer;border-bottom:1px solid #1b2030}
.row:hover,.row.hl{background:#212838}
.dot{width:9px;height:9px;border-radius:50%;flex:none}
.nm{flex:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.meta{color:var(--dim);font-size:12px;white-space:nowrap;font-variant-numeric:tabular-nums}
.empty{color:var(--dim);padding:10px 12px}
#status{color:var(--dim);font-size:12px;padding:5px 14px;border-top:1px solid var(--line);background:var(--panel)}
#curpos{position:absolute;right:10px;bottom:8px;font-size:12px;color:#cfd6e4;background:#000a;padding:2px 7px;border-radius:4px;pointer-events:none;display:none}
#cal{position:absolute;left:10px;top:10px;background:#000d;border:1px solid var(--line);border-radius:6px;padding:8px 10px;display:none;font-size:12px;z-index:5;max-width:330px;line-height:1.7}
#cal input{width:64px}
#toast{position:fixed;bottom:34px;left:50%;transform:translateX(-50%);background:#243;border:1px solid #4a8;
padding:6px 12px;border-radius:6px;display:none}
@media (max-width:800px){main{flex-direction:column}aside{width:auto;height:45%;border-left:0}}
</style></head><body>
<header>
 <h1>Nearby Tracker</h1>
 <span id="zone" class="meta"></span>
 <span class="sp"></span>
 <label><input type="checkbox" id="follow" checked> Follow me</label>
 <label><input type="checkbox" id="rings" checked> Approx. rings</label>
 <label><input type="checkbox" id="labels" checked> Names</label>
 <label title="How far the game's combat log reaches. Players seen without a position are drawn as a ring this big around where you stood.">Log range <input type="number" id="range" value="200" min="5" max="500"> yd</label>
 <label>Forget after <input type="number" id="keep" value="30" min="1" max="600"> min</label>
 <label title="Show the zone's map image behind everything (made by extract_maps.py)"><input type="checkbox" id="mapimg" checked> Map</label>
 <label title="Estimate where players probably are now from their recent events"><input type="checkbox" id="heat" checked> Heatmap</label>
 <select id="heatwho" title="Whose position estimate the heatmap shows"><option value="O">Neutral / hostile</option><option value="F">Friendly</option><option value="A">Everyone</option></select>
 <label title="Use this many of each player's latest located events">Events <input type="number" id="evn" value="15" min="1" max="60"></label>
 <label title="Events further apart than this start a new encounter; older ones are ignored">Encounter gap <input type="number" id="gap" value="30" min="3" max="900"> s</label>
 <label>Orientation <select id="ori"><option value="std">North up (X north, Y west)</option>
   <option value="alt">Alt (Y north, X east)</option></select></label>
 <button id="fitp" title="Zoom to show you and everyone tracked">Fit players</button>
 <button id="fit">Fit zone</button>
 <button id="calbtn" title="Line the map image up with the game's coordinates">Align map</button>
</header>
<div id="warn"></div>
<main>
 <div id="mapwrap"><canvas id="cv"></canvas><div id="tip"></div><div id="curpos"></div>
  <div id="cal"><b>Align map</b> <span id="calinfo" class="meta"></span><br>
   Stand still, do something (cast, bandage) so the log records you, then type the coordinates the in-game map shows for you:<br>
   X <input id="cx" type="number" step="0.1" min="0" max="100"> Y <input id="cy" type="number" step="0.1" min="0" max="100">
   <select id="corg" title="Where Y = 0 is on the in-game map"><option value="bottom">Y=0 at bottom</option><option value="top">Y=0 at top</option></select>
   <button id="caladd">Add point</button> <button id="calclr">Reset zone</button><br><span id="calpts" class="meta"></span></div></div>
 <aside>
  <div class="col"><h2><span style="color:var(--ally)">&#9679;</span> Friendly <span class="n" id="nF"></span>
    <span class="sp" style="flex:1"></span>
    <select id="sort"><option value="seen">Last seen</option><option value="dist">Distance</option><option value="name">Name</option></select></h2>
    <div class="list" id="lF"></div></div>
  <div class="col"><h2><span style="color:var(--other)">&#9679;</span> Neutral / hostile <span class="n" id="nO"></span></h2>
    <div class="list" id="lO"></div></div>
 </aside>
</main>
<div id="status">connecting&hellip;</div>
<div id="toast"></div>
<script>
const $=id=>document.getElementById(id);
const sv=(k,v)=>{try{localStorage.setItem('nt_'+k,v)}catch(e){}};
const ld=(k,d)=>{try{const v=localStorage.getItem('nt_'+k);return v===null?d:v}catch(e){return d}};
for(const id of ['follow','rings','labels','heat','mapimg']){const el=$(id);el.checked=ld(id,el.checked?'1':'0')==='1';el.onchange=()=>{sv(id,el.checked?'1':'0');draw()}}
for(const id of ['range','keep','ori','sort','heatwho','evn','gap']){const el=$(id);el.value=ld(id,el.value);el.onchange=()=>{sv(id,el.value);renderLists();draw()}}

let S=null,recv=0,cam={x:0,y:0},scale=2,hl=null,fitted=false,pins=[];
const cv=$('cv'),ctx=cv.getContext('2d');
let W=0,H=0,dpr=1;
function resize(){const r=cv.getBoundingClientRect();dpr=window.devicePixelRatio||1;W=r.width;H=r.height;
  cv.width=Math.round(W*dpr);cv.height=Math.round(H*dpr);draw()}
new ResizeObserver(resize).observe($('mapwrap'));

const alt=()=>$('ori').value==='alt';
function w2s(x,y){const cx=W/2,cy=H/2;
  return alt()?[cx+(x-cam.x)*scale,cy-(y-cam.y)*scale]:[cx-(y-cam.y)*scale,cy-(x-cam.x)*scale]}
function s2w(sx,sy){const cx=W/2,cy=H/2;
  return alt()?[cam.x+(sx-cx)/scale,cam.y+(cy-sy)/scale]:[cam.x+(cy-sy)/scale,cam.y-(sx-cx)/scale]}
const nowT=()=>S?S.now+(performance.now()-recv)/1000:0;
const keepS=()=>Math.max(1,+$('keep').value||30)*60;
const curMap=()=>S?(S.me&&S.me.map!=null?S.me.map:S.map):null;

function fitZone(){
  if(!S)return;const m=S.maps[curMap()];if(!m)return;
  const dx=m.x1-m.x0,dy=m.y1-m.y0;
  const wx=alt()?dx:dy, wy=alt()?dy:dx;     // extent along screen x / screen y
  scale=Math.max(0.01,Math.min(W/wx,H/wy)*0.92);
  cam.x=(m.x0+m.x1)/2;cam.y=(m.y0+m.y1)/2;$('follow').checked=false;sv('follow','0');draw();
}
$('fit').onclick=fitZone;
function fitPlayers(){
  if(!S)return;const m=curMap(),pts=[];
  if(S.me&&S.me.x!=null&&S.me.map===m)pts.push([S.me.x,S.me.y]);
  for(const p of S.players){if(p.me)continue;const e=p.est;
    if(e&&e.map===m)pts.push([e.x,e.y]);else if(p.exact&&p.exact.map===m)pts.push([p.exact.x,p.exact.y])}
  if(!pts.length)return;
  let x0=1e9,x1=-1e9,y0=1e9,y1=-1e9;
  for(const q of pts){x0=Math.min(x0,q[0]);x1=Math.max(x1,q[0]);y0=Math.min(y0,q[1]);y1=Math.max(y1,q[1])}
  const dx=Math.max(80,x1-x0),dy=Math.max(80,y1-y0),wx=alt()?dx:dy,wy=alt()?dy:dx;
  scale=Math.max(0.05,Math.min(W/wx,H/wy)*0.75);cam.x=(x0+x1)/2;cam.y=(y0+y1)/2;
  $('follow').checked=false;sv('follow','0');draw();
}
$('fitp').onclick=fitPlayers;

function niceStep(){const t=[10,25,50,100,250,500,1000,2500];for(const s of t)if(s*scale>=70)return s;return 5000}

function ageStr(a){a=Math.max(0,a);if(a<60)return Math.round(a)+'s';if(a<3600)return Math.floor(a/60)+'m '+Math.round(a%60)+'s';return Math.floor(a/3600)+'h '+Math.floor(a%3600/60)+'m'}
function visiblePins(){
  const out=[];if(!S)return out;const m=curMap(),t=nowT(),ks=keepS();
  for(const p of S.players){
    if(p.me)continue;const age=t-p.last;if(age>ks)continue;
    const fade=Math.max(0.3,1-age/ks);
    const e=p.exact&&p.exact.map===m?p.exact:null;
    const a=p.approx&&p.approx.map===m?p.approx:null;
    out.push({p,e,a,age,fade,showApprox:!!a&&(!e||a.t>e.t+15)});
  }return out;
}
function col(p){return p.rel==='F'?'#4fa3ff':'#ff6b5e'}

// ---- heat layer: one small blob per player, red (just seen) -> blue (long ago), gone after 5 minutes ----
const HEAT_LIFE=300;
function heatWanted(p){const w=$('heatwho').value;return w==='A'||(w==='F'?p.rel==='F':p.rel!=='F')}
function ageColor(age){const t=Math.min(1,Math.max(0,age/HEAT_LIFE));
  return [Math.round(255+(40-255)*t),Math.round(40+(90-40)*t),Math.round(30+(255-30)*t)]}
function drawHeat(){
  if(!$('heat').checked||!S)return;
  const now=nowT(),m=curMap(),list=[];
  for(const p of S.players){
    const e=p.est;
    if(p.me||!e||e.map!==m||!e.ex||!heatWanted(p))continue;   // needs at least one exact position
    const age=now-e.t;if(age>=HEAT_LIFE)continue;
    list.push({e,age:Math.max(0,age)});
  }
  list.sort((a,b)=>b.age-a.age);                               // oldest first so the freshest (red) sits on top
  const R=Math.min(70,Math.max(12,25*scale));                  // about 25 yd, kept small on screen
  for(const h of list){
    const q=w2s(h.e.x,h.e.y),c=ageColor(h.age);
    const a=0.85*Math.min(1,(HEAT_LIFE-h.age)/60);             // full strength, then fades out over the last minute
    if(a<=0.01)continue;
    const rgb=c[0]+','+c[1]+','+c[2];
    const g=ctx.createRadialGradient(q[0],q[1],0,q[0],q[1],R);
    g.addColorStop(0,'rgba('+rgb+','+a+')');g.addColorStop(.45,'rgba('+rgb+','+(a*.55)+')');g.addColorStop(1,'rgba('+rgb+',0)');
    ctx.fillStyle=g;ctx.fillRect(q[0]-R,q[1]-R,2*R,2*R);
  }
}

// ---- zone map image (made by extract_maps.py), chosen by the zone you are in ----
const normZ=s=>String(s||'').toLowerCase().replace(/[^a-z0-9]/g,'');
let mapIdx=null,mapIdxT=-1e9;const mapImgs={};
function loadMapIndex(){
  const t=performance.now();if(mapIdx||t-mapIdxT<20000)return;mapIdxT=t;
  fetch('/maps/index.json',{cache:'no-store'}).then(r=>r.ok?r.json():null).then(j=>{if(j&&j.zones){mapIdx=j.zones;draw()}}).catch(()=>{});
}
function zoneImage(){
  if(!S)return null;loadMapIndex();if(!mapIdx)return null;
  const m=S.maps[curMap()];
  const ent=(m&&mapIdx[normZ(m.name)])||mapIdx[normZ(S.zone)];
  if(!ent)return null;
  let im=mapImgs[ent.file];
  if(!im){im=mapImgs[ent.file]=new Image();im.onload=draw;im.onerror=()=>{im.bad=true};im.src='/maps/'+encodeURIComponent(ent.file)}
  return im.complete&&im.naturalWidth&&!im.bad?{im,w:ent.w||im.naturalWidth,h:ent.h||im.naturalHeight}:null;
}
let calib={};try{calib=JSON.parse(ld('calib','{}'))||{}}catch(e){calib={}}
function fitAxis(pairs,lo,hi){            // pairs [[world, fraction 0..1]]  ->  [edge at fraction 0, span] ; default lo..hi
  if(!pairs.length)return null;
  const span=hi-lo;
  if(pairs.length===1)return {a:pairs[0][0],s:span,start:true};
  const n=pairs.length,su=pairs.reduce((t,p)=>t+p[1],0),sw=pairs.reduce((t,p)=>t+p[0],0);
  const suu=pairs.reduce((t,p)=>t+p[1]*p[1],0),suw=pairs.reduce((t,p)=>t+p[1]*p[0],0),den=n*suu-su*su;
  if(Math.abs(den)<1e-6)return {a:pairs[0][0],s:span,start:true};
  const B=(n*suw-su*sw)/den,A=(sw-B*su)/n;     // world = A + B*fraction
  return B<0?{a:A,s:-B}:{a:pairs[0][0],s:span};
}
function effRect(m){
  const c=calib[curMap()]||[];
  if(!c.length)return m;
  // c items: {x,y (world), u,v (fractions: u west->east, v north->south)}
  const fy=fitAxis(c.map(p=>[p.y,p.u]),m.y0,m.y1),fx=fitAxis(c.map(p=>[p.x,p.v]),m.x0,m.x1);
  const y1=fy.start?fy.a+c[0].u*(m.y1-m.y0)*1:fy.a,sy=fy.s,x1=fx.start?fx.a+c[0].v*(m.x1-m.x0):fx.a,sx=fx.s;
  return {x0:x1-sx,x1:x1,y0:y1-sy,y1:y1};
}
const yFlip=()=>$('corg').value==='bottom';
function mapPct(m,x,y){const r=effRect(m),v=(r.x1-x)/(r.x1-r.x0)*100;return [(r.y1-y)/(r.y1-r.y0)*100,yFlip()?100-v:v]}
function calUi(){
  const m=S&&S.maps[curMap()],c=calib[curMap()]||[];
  $('calpts').textContent=c.length?c.length+' point'+(c.length>1?'s':'')+' saved for this zone'+(c.length<2?' (add a second one somewhere else to fix the scale)':''):'No points yet - using the zone rectangle from the log.';
  if(m&&S.me&&S.me.x!=null&&S.me.map===curMap()){const p=mapPct(m,S.me.x,S.me.y);
    $('calinfo').textContent='  You are at '+p[0].toFixed(1)+', '+p[1].toFixed(1)+' on this map ('+ageStr(nowT()-S.me.t)+' old fix)'}
  else $('calinfo').textContent='';
}
function toast(t){const e=$('toast');e.textContent=t;e.style.display='block';setTimeout(()=>e.style.display='none',2500)}
$('corg').value=ld('corg','bottom');$('corg').onchange=()=>{sv('corg',$('corg').value);calUi()};
$('calbtn').onclick=()=>{const c=$('cal');c.style.display=c.style.display==='block'?'none':'block';calUi()};
$('caladd').onclick=()=>{
  const m=S&&S.maps[curMap()];if(!m||!S.me||S.me.x==null||S.me.map!==curMap()){toast('No position for you in this zone yet');return}
  const X=parseFloat($('cx').value),Y=parseFloat($('cy').value);if(!(X>=0&&X<=100&&Y>=0&&Y<=100)){toast('Type both coordinates (0-100)');return}
  const k=curMap();(calib[k]=calib[k]||[]).push({x:S.me.x,y:S.me.y,u:X/100,v:yFlip()?1-Y/100:Y/100});
  sv('calib',JSON.stringify(calib));calUi();draw();
};
$('calclr').onclick=()=>{delete calib[curMap()];sv('calib',JSON.stringify(calib));calUi();draw()};
function ptTxt(pt){const m=S&&S.maps[curMap()];if(!m||!pt)return '';const p=mapPct(m,pt.x,pt.y);return p[0].toFixed(1)+', '+p[1].toFixed(1)}
// 0-100 map coordinates, X from the left and Y from the BOTTOM (0,0 = bottom-left corner of the map)
function drawMapGrid(m){
  const r=effRect(m),flip=yFlip();
  ctx.save();ctx.lineWidth=1;ctx.font='10px system-ui';
  for(let k=0;k<=10;k++){
    const f=k/10,yy=r.y1-f*(r.y1-r.y0),xx=r.x1-f*(r.x1-r.x0);
    ctx.strokeStyle=k%5?'rgba(255,255,255,.10)':'rgba(255,255,255,.22)';ctx.beginPath();
    let a=w2s(r.x0,yy),b=w2s(r.x1,yy);ctx.moveTo(a[0],a[1]);ctx.lineTo(b[0],b[1]);
    a=w2s(xx,r.y1);b=w2s(xx,r.y0);ctx.moveTo(a[0],a[1]);ctx.lineTo(b[0],b[1]);ctx.stroke();
    ctx.fillStyle='rgba(255,255,255,.75)';ctx.strokeStyle='rgba(0,0,0,.8)';ctx.lineWidth=3;
    const lx=w2s(r.x0,yy),ly=w2s(xx,r.y1),ty=flip?100-k*10:k*10,tx=k*10;
    ctx.textAlign='center';ctx.strokeText(tx,lx[0],lx[1]+12);ctx.fillText(tx,lx[0],lx[1]+12);
    ctx.textAlign='left';ctx.strokeText(ty,ly[0]+4,ly[1]-3);ctx.fillText(ty,ly[0]+4,ly[1]-3);
  }
  ctx.restore();
}
function drawMapImage(m){
  if(!$('mapimg').checked||!m)return;
  const z=zoneImage();if(!z)return;
  m=effRect(m);
  // image: left = west (y1), right = east (y0), top = north (x1), bottom = south (x0); stretched to the zone rect
  const P0=w2s(m.x1,m.y1),Pu=w2s(m.x1,m.y0),Pv=w2s(m.x0,m.y1);
  ctx.save();
  ctx.transform((Pu[0]-P0[0])/z.w,(Pu[1]-P0[1])/z.w,(Pv[0]-P0[0])/z.h,(Pv[1]-P0[1])/z.h,P0[0],P0[1]);
  ctx.imageSmoothingQuality='high';ctx.globalAlpha=0.9;ctx.drawImage(z.im,0,0,z.w,z.h);
  ctx.restore();
}

let raf=0;
function draw(){if(raf)return;raf=requestAnimationFrame(()=>{raf=0;drawNow()})}
function drawNow(){
  if(!W)return;ctx.setTransform(dpr,0,0,dpr,0,0);ctx.clearRect(0,0,W,H);
  ctx.fillStyle='#0c1017';ctx.fillRect(0,0,W,H);
  if(!S)return;
  const m=S.maps[curMap()];
  // ---- bottom layers: zone outline, grid, compass, heat ----
  if(m){const c=[[m.x0,m.y0],[m.x1,m.y0],[m.x1,m.y1],[m.x0,m.y1]].map(q=>w2s(q[0],q[1]));
    ctx.beginPath();c.forEach((q,i)=>i?ctx.lineTo(q[0],q[1]):ctx.moveTo(q[0],q[1]));ctx.closePath();
    ctx.fillStyle='#141b27';ctx.fill();drawMapImage(m);if($('mapimg').checked)drawMapGrid(m);ctx.strokeStyle='#3a4660';ctx.lineWidth=1.5;ctx.stroke();}
  const step=niceStep();
  const corners=[s2w(0,0),s2w(W,0),s2w(0,H),s2w(W,H)];
  const xs=corners.map(c=>c[0]),ys=corners.map(c=>c[1]);
  const x0=Math.floor(Math.min(...xs)/step)*step,x1=Math.max(...xs),y0=Math.floor(Math.min(...ys)/step)*step,y1=Math.max(...ys);
  ctx.strokeStyle='#1d2535';ctx.lineWidth=1;ctx.beginPath();
  for(let x=x0;x<=x1;x+=step){const a=w2s(x,y0),b=w2s(x,y1);ctx.moveTo(a[0],a[1]);ctx.lineTo(b[0],b[1])}
  for(let y=y0;y<=y1;y+=step){const a=w2s(x0,y),b=w2s(x1,y);ctx.moveTo(a[0],a[1]);ctx.lineTo(b[0],b[1])}
  ctx.stroke();
  ctx.fillStyle='#55617a';ctx.font='11px system-ui';ctx.textAlign='left';ctx.fillText('grid '+step+' yd',8,H-8);
  ctx.fillStyle='#8590a5';ctx.font='12px system-ui';ctx.textAlign='center';
  ctx.fillText(alt()?'E':'N',alt()?W-14:W/2,alt()?H/2+4:16);
  const me=S.me&&S.me.x!=null&&S.me.map===curMap()?S.me:null;
  const R=Math.max(5,+$('range').value||200);
  if(me){const q=w2s(me.x,me.y);
    ctx.strokeStyle='#33405a';ctx.setLineDash([4,4]);ctx.lineWidth=1;
    ctx.beginPath();ctx.arc(q[0],q[1],R*scale,0,6.2832);ctx.stroke();ctx.setLineDash([]);}
  drawHeat();

  // ---- middle layers: approximate rings and the estimate marker of the highlighted player ----
  const vs=visiblePins(),labels=[];pins=[];
  for(const v of vs){
    if(!(v.showApprox&&$('rings').checked))continue;
    const c=col(v.p),q=w2s(v.a.x,v.a.y);
    ctx.globalAlpha=v.fade*0.6;ctx.strokeStyle=c;ctx.setLineDash([3,4]);ctx.lineWidth=1;
    ctx.beginPath();ctx.arc(q[0],q[1],R*scale,0,6.2832);ctx.stroke();ctx.setLineDash([]);
    ctx.globalAlpha=v.fade*0.07;ctx.fillStyle=c;ctx.fill();ctx.globalAlpha=1;
    labels.push({t:'?',x:q[0],y:q[1]+4,c,a:v.fade,b:true});
    if($('labels').checked)labels.push({t:v.p.name,x:q[0],y:q[1]-R*scale-4,c,a:v.fade});
  }
  for(const v of vs){
    if(hl!==v.p.guid||!v.p.est||v.p.est.map!==curMap())continue;
    const e=v.p.est,q=w2s(e.x,e.y),c=col(v.p);
    ctx.strokeStyle=c;ctx.lineWidth=1.5;ctx.setLineDash([6,4]);
    ctx.beginPath();ctx.arc(q[0],q[1],Math.max(6,e.s*scale),0,6.2832);ctx.stroke();ctx.setLineDash([]);
    ctx.beginPath();ctx.moveTo(q[0]-8,q[1]);ctx.lineTo(q[0]+8,q[1]);ctx.moveTo(q[0],q[1]-8);ctx.lineTo(q[0],q[1]+8);ctx.stroke();
    labels.push({t:'est. ±'+Math.round(e.s)+' yd',x:q[0],y:q[1]+Math.max(6,e.s*scale)+14,c:'#ffffff',a:1,b:true});
  }

  // ---- top layers: dots, then your arrow, then every name ----
  for(const v of vs){
    if(!v.e)continue;
    const c=col(v.p),q=w2s(v.e.x,v.e.y),on=hl===v.p.guid;
    ctx.globalAlpha=Math.max(v.fade,0.55);ctx.fillStyle=c;ctx.beginPath();ctx.arc(q[0],q[1],on?7:5,0,6.2832);ctx.fill();
    ctx.strokeStyle=on?'#fff':'#000a';ctx.lineWidth=on?2:1.2;ctx.stroke();ctx.globalAlpha=1;
    if($('labels').checked||on)labels.push({t:v.p.name,x:q[0],y:q[1]-10,c:'#e8edf7',a:Math.max(v.fade,0.7),b:on});
    pins.push({x:q[0],y:q[1],v});
  }
  if(me){const q=w2s(me.x,me.y),f=me.facing||0;
    const d=alt()?[Math.cos(f),-Math.sin(f)]:[-Math.sin(f),-Math.cos(f)];
    ctx.save();ctx.translate(q[0],q[1]);ctx.rotate(Math.atan2(d[1],d[0])+Math.PI/2);
    ctx.fillStyle='#fff';ctx.strokeStyle='#000';ctx.lineWidth=1.2;ctx.beginPath();
    ctx.moveTo(0,-10);ctx.lineTo(7,8);ctx.lineTo(0,4);ctx.lineTo(-7,8);ctx.closePath();ctx.fill();ctx.stroke();ctx.restore();
    labels.push({t:'You',x:q[0],y:q[1]+22,c:'#ffffff',a:1,b:true});
  }
  ctx.textAlign='center';ctx.lineJoin='round';
  for(const l of labels){
    ctx.globalAlpha=l.a;ctx.font=(l.b?'bold ':'')+'11px system-ui';
    ctx.lineWidth=3;ctx.strokeStyle='#0c1017';ctx.strokeText(l.t,l.x,l.y);   // dark outline keeps names readable over the heat
    ctx.fillStyle=l.c;ctx.fillText(l.t,l.x,l.y);
  }
  ctx.globalAlpha=1;
}

// interaction
let drag=null;
cv.addEventListener('mousedown',e=>{drag={x:e.clientX,y:e.clientY};cv.classList.add('drag')});
window.addEventListener('mouseup',()=>{drag=null;cv.classList.remove('drag')});
window.addEventListener('mousemove',e=>{
  if(drag){const dx=e.clientX-drag.x,dy=e.clientY-drag.y;drag={x:e.clientX,y:e.clientY};
    if(alt()){cam.x-=dx/scale;cam.y+=dy/scale}else{cam.y+=dx/scale;cam.x+=dy/scale}
    $('follow').checked=false;sv('follow','0');draw();return}
});
cv.addEventListener('wheel',e=>{e.preventDefault();
  const r=cv.getBoundingClientRect(),sx=e.clientX-r.left,sy=e.clientY-r.top;
  const before=s2w(sx,sy);scale=Math.min(40,Math.max(0.01,scale*(e.deltaY<0?1.2:1/1.2)));
  if(!$('follow').checked){const after=s2w(sx,sy);cam.x+=before[0]-after[0];cam.y+=before[1]-after[1]}
  draw()},{passive:false});
function pinAt(ev){const r=cv.getBoundingClientRect(),sx=ev.clientX-r.left,sy=ev.clientY-r.top;
  let best=null,bd=100;for(const q of pins){const d=(q.x-sx)**2+(q.y-sy)**2;if(d<bd){bd=d;best=q}}return best}
cv.addEventListener('mouseleave',()=>{$('curpos').style.display='none'});
cv.addEventListener('mousemove',e=>{const r0=cv.getBoundingClientRect(),m0=S&&S.maps[curMap()],cp=$('curpos');
  if(m0){const w=s2w(e.clientX-r0.left,e.clientY-r0.top);cp.textContent='cursor '+ptTxt({x:w[0],y:w[1]});cp.style.display='block'}
  if(drag)return;const q=pinAt(e),t=$('tip');
  const g=q?q.v.p.guid:null;
  if(q){const p=q.v.p;t.style.display='block';t.style.left=(e.clientX-cv.getBoundingClientRect().left+12)+'px';
    t.style.top=(e.clientY-cv.getBoundingClientRect().top+12)+'px';
    t.textContent=p.name+' \u2022 ('+ptTxt(q.v.e||q.v.a)+') \u2022 seen '+ageStr(q.v.age)+' ago'+distText(q.v)}else t.style.display='none';
  if(g!==hl){hl=g;draw();renderLists()}});
cv.addEventListener('click',e=>{const q=pinAt(e);if(q)copyTarget(q.v.p.name)});

function copyTarget(name){
  const txt='/target '+name;
  const done=()=>{const t=$('toast');t.textContent='Copied: '+txt+'  \u2014 paste into WoW chat';t.style.display='block';
    clearTimeout(copyTarget.t);copyTarget.t=setTimeout(()=>t.style.display='none',2500)};
  if(navigator.clipboard&&window.isSecureContext){navigator.clipboard.writeText(txt).then(done,()=>fb())}else fb();
  function fb(){const ta=document.createElement('textarea');ta.value=txt;document.body.appendChild(ta);ta.select();
    try{document.execCommand('copy')}catch(e){}ta.remove();done()}
}

function distOf(v){
  const me=S&&S.me&&S.me.x!=null&&S.me.map===curMap()?S.me:null;if(!me)return null;
  const pt=v.e||v.a;if(!pt)return null;
  const d=Math.hypot(pt.x-me.x,pt.y-me.y);
  // approximate fix = where I stood when they were seen, so they are at most (distance + log range) away
  return {d:v.e?d:d+Math.max(5,+$('range').value||200),approx:!v.e}}
function dTxt(d){return d.approx?'\u2264'+Math.round(d.d)+' yd':Math.round(d.d)+' yd'}
function distText(v){const d=distOf(v);return d?' \u2022 '+dTxt(d):''}

function renderLists(){
  if(!S)return;const t=nowT(),ks=keepS(),sort=$('sort').value;
  const vs=[];const m=curMap();
  for(const p of S.players){if(p.me)continue;const age=t-p.last;if(age>ks)continue;
    const e=p.exact&&p.exact.map===m?p.exact:null,a=p.approx&&p.approx.map===m?p.approx:null;
    vs.push({p,e,a,age})}
  const cmp={seen:(a,b)=>a.age-b.age,name:(a,b)=>a.p.name.localeCompare(b.p.name),
    dist:(a,b)=>{const x=distOf(a),y=distOf(b);return (x?x.d:1e9)-(y?y.d:1e9)}}[sort];
  vs.sort(cmp);
  for(const [id,nid,rels] of [['lF','nF','F'],['lO','nO','NH?']]){
    const el=$(id),list=vs.filter(v=>rels.includes(v.p.rel));
    $(nid).textContent='('+list.length+')';el.textContent='';
    if(!list.length){const d=document.createElement('div');d.className='empty';d.textContent='Nobody yet';el.appendChild(d);continue}
    for(const v of list.slice(0,300)){
      const r=document.createElement('div');r.className='row'+(hl===v.p.guid?' hl':'');
      const dot=document.createElement('span');dot.className='dot';dot.style.background=col(v.p);
      dot.style.opacity=v.e?1:0.45;
      const n=document.createElement('span');n.className='nm';n.textContent=v.p.name;
      const mt=document.createElement('span');mt.className='meta';
      const d=distOf(v),pt=v.e||v.a;mt.textContent=(pt?(v.e?'':'~')+ptTxt(pt)+' \u00b7 ':'')+(d?dTxt(d)+' \u00b7 ':'')+ageStr(v.age);
      r.append(dot,n,mt);r.title=v.p.raw+'  flags '+v.p.flags+(v.e?'':'  (no position yet)');
      r.onclick=()=>copyTarget(v.p.name);
      r.onmouseenter=()=>{hl=v.p.guid;draw()};r.onmouseleave=()=>{hl=null;draw()};
      el.appendChild(r);
    }
  }
}

function renderStatus(){
  const st=S.stats,ev=st.lastEvent?ageStr(nowT()-st.lastEvent)+' ago':'none';
  const me=S.me&&S.me.name?S.me.name:'(not detected yet)';
  $('status').textContent=(st.file||'no log file yet')+'  \u00b7  '+st.lines+' lines  \u00b7  last event '+ev+'  \u00b7  you: '+me+
    '  \u00b7  '+S.players.length+' players tracked'+(S.realmSuffix?'  \u00b7  realm suffix '+S.realmSuffix:'');
  $('zone').textContent=(S.zone||'')+(curMap()!=null?'  (map '+curMap()+')':'')+(S.me&&S.me.x!=null&&S.me.map===curMap()?'  \u2022 you '+ptTxt(S.me):'');
  const w=$('warn');
  if(S.advanced===false){w.style.display='block';w.textContent='Advanced combat logging is OFF in this log, so there are no positions. Turn on Options > Network > Advanced Combat Logging, then /combatlog again.'}
  else if(!st.file){w.style.display='block';w.textContent='No combat log found yet. Type /combatlog in game (or check the --logs folder).'}
  else w.style.display='none';
}

function onState(){
  if(!S)return;
  if(!fitted&&S.maps[curMap()]&&!(S.me&&S.me.x!=null)){fitted=true;fitZone()}
  if($('follow').checked&&S.me&&S.me.x!=null){cam.x=S.me.x;cam.y=S.me.y;if(!fitted){fitted=true;scale=2}}
  const t=performance.now();if(t-lastLists>800){lastLists=t;renderLists()}
  renderStatus();if($('cal').style.display==='block')calUi();draw();
}
let lastLists=0;
let es=null;
function connect(){
  if(es)es.close();
  const q='keep='+encodeURIComponent($('keep').value||30)+'&n='+encodeURIComponent($('evn').value||15)+'&gap='+encodeURIComponent($('gap').value||30)+'&range='+encodeURIComponent($('range').value||200);
  es=new EventSource('/events?'+q);
  es.onmessage=e=>{try{S=JSON.parse(e.data);recv=performance.now();onState()}catch(x){console.error(x)}};
  es.onerror=()=>{$('status').textContent='connection lost \u2014 retrying\u2026'};
}
for(const id of ['evn','gap','range','keep'])$(id).addEventListener('change',connect);
connect();resize();
setInterval(()=>{if(S){renderLists();draw()}},5000);
</script></body></html>
"""

if __name__ == "__main__":
    main()
