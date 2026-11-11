# Nearby Tracker (companion app for WoW Forever)

Reads the game's combat log file while you play and shows a radar-style web page with:

- every player the log has mentioned, split into **Friendly** and **Neutral / hostile**,
- a radar-style map with a pin at the last place each player was detected, which moves when they are detected again,
- click a name or a pin to copy `/target First Last`, then paste it into WoW chat yourself.

It only reads a text file. It never sends anything to the game.

## Three versions in this folder

| Version | Folder | What it is | Docs |
|---|---|---|---|
| **Python** | `nearby_tracker.py`, `start.bat` | The original. One script, nothing to install but Python. Local-only page on `127.0.0.1:8777`. Includes the Heatmap. | This file, below |
| **Go** | `go-radar/` | Desktop app (single `radar.exe`, WebView2 window). Adds persistent history and tracks, player search, bounty ranking and per-server rule-sets (normal, PvP, RP, RP-PvP). | [go-radar/README.md](go-radar/README.md) |
| **Cloud** | `radar-cloud/` | The Go app's features as a hosted AWS (CDK) site: log in with username, password and API key, store history, tracks and bounty in your own account, view from any browser. Logs are parsed in the browser and only small per-player summaries are uploaded. Built to cost well under $1/month. | [radar-cloud/README.md](radar-cloud/README.md) |

Shared pieces: the `maps/` folder (zone images, made with `extract_maps.py`) is used by all three. The cloud
version ships a compressed WebP copy of the maps in `radar-cloud/site/maps/`.

Not every feature exists in every version:

| Feature | Python | Go | Cloud |
|---|---|---|---|
| Live radar from the log file | yes | yes | yes (Chrome/Edge folder watching) |
| Zone map images | yes | yes | yes |
| Heatmap | yes | no | no |
| History, tracks, search | no | yes | yes |
| Bounty ranking, rule-sets | no | yes | yes |
| Accounts / API keys / stored in the cloud | no | no | yes |
| Static ads column | no | no | yes |

### Try the cloud version without AWS
Double-click `radar-cloud\run-local.bat` (add `--sim` for simulated live data), then open
`http://127.0.0.1:8787` and log in with `demo` / `demo-password-1` and API key `dev-key-0001`. It needs no npm,
only a `node` executable (it falls back to the one bundled with VS Code). Deploying to AWS is covered in
[radar-cloud/README.md](radar-cloud/README.md); nothing has been deployed yet.

## Python version

### Run
1. In game: `/combatlog` (also make sure Advanced Combat Logging is on, it carries the positions).
2. Install Python 3.9+ if you don't have it. Nothing else to install.
3. Double-click `start.bat` (or `python nearby_tracker.py`). Your browser opens `http://127.0.0.1:8777/`.

The log folder defaults to `C:\Program Files (x86)\World of Warcraft\_forever_\Logs`.
Use `--logs "D:\path\to\Logs"` if yours is elsewhere. The newest `WoWCombatLog*.txt` is followed, and a new file
(after `/combatlog` off/on or a relog) is picked up automatically.

Options: `--port 8777`, `--keep 60` (forget players unseen for N minutes), `--from-start` (read the whole
current log), `--realm-suffix -PvP-US` (normally auto-detected), `--dump` (print every parsed event, for
checking the format), `--no-browser`.

### Reading the page
- **Names:** the log writes `Mc-Cheeki-PvP-US`. The realm part is detected and removed and `-` becomes a space,
  so you get the in-game name `Mc Cheeki`.
- **Solid dot = exact position.** The log gives a unit's coordinates only on events where that unit is the
  main subject (they cast, they swing, they get hit). Idle players don't produce positions.
- **Dashed ring with `?` = approximate.** The player was named by the log without a position, so the ring is
  drawn where *you* were at that moment, sized by "Log range". They were somewhere inside it. The list shows
  `≤ N yd` for these (distance to the ring plus its radius).
- Pins fade with age and disappear after "Forget after" minutes. A newer sighting always replaces the old pin.
- Your own position updates whenever the log has an event about you. If you stand still and idle it can go stale.
- Mouse wheel zooms, drag pans, "Follow me" keeps you centred, "Fit zone" shows the whole zone outline.
- The default orientation is X = north, Y = west (the usual world-coordinate layout). If pins move the
  wrong way when you run north, switch "Orientation".
- Friendly vs neutral/hostile comes from the log's reaction flags (`0x10` friendly, `0x20` neutral, `0x40` hostile).
  A player's flag can change between events, and the newest one is used. Check one name in game to confirm
  which group is which faction for you.

### Zone maps (personal use)
The page shows the map image of the zone you are in and switches when you change zone (the log's `MAP_CHANGE`
line gives the zone name, and the image is stretched to the zone's coordinate rectangle). Untick **Map** to hide it.

Make the images once with `extract_maps.py`, which reads the game's own `Data` folder (CASC) and writes
`maps\<Zone>.png` plus `maps\index.json` next to the script. The base art is the blank "unexplored" parchment, so the
script also paints the game's exploration overlays on top (read from the same install), giving a fully revealed map.
Use `--no-overlays` to skip that. Zones already done are skipped; zones saved without overlays are redone.
1. `py -m pip install pillow`
2. `py extract_maps.py` (add `--only "Dun Morogh,Elwynn Forest"` to do a few zones, `--force` to redo).
   Defaults: `--wow "C:\Program Files (x86)\World of Warcraft\_forever_"`, `--out maps`.
- Tiles the client has not stored locally are downloaded from Blizzard's CDN (internet needed). If you only
  want local files, add `--no-cdn`. Opening the world map in game for a zone may make the client cache it.
- Zones missing from `maps` simply show no image. Dots, names and heat are always drawn above the map.
- For your own use only; don't share the extracted images.

### Heatmap (where players probably are now)
Turn it on with **Heatmap** and pick whose blobs to show (Neutral / hostile by default).
- Each player with a known position gets one small blob (about 25 yd across the map, kept small on screen).
- **Red = just seen, shifting to blue as time passes, gone after 5 minutes.** The last minute fades out.
- The blob sits on the best guess for where they are now: the weighted average of their **last N located events**
  (default 15, the *Events* box) inside their current **encounter**. An encounter runs from when you first saw
  events from them until they went quiet. If two events are further apart than *Encounter gap* (default 30 s),
  everything before the gap is ignored.
- Exact positions count fully, approximate ones count much less, newer events count more. If the exact fixes show
  them moving, the guess leans a little ahead along that path (at most 10 s, at most 30 yd/s).
- Players with only approximate sightings (no exact position at all) get no blob, just their dashed ring.
- Hover a name in the list to see the single best guess (`est. +/- N yd` is how spread out their recent events were).
  "Fit players" zooms to show everyone tracked.
- It is a heuristic, not a real tracker: it can't know about turns, mounts or stealth between events.
- Dots and names are always drawn above the heat.

### Limits
- Only players who do something (cast, hit, buff, die) ever show up. Silent bystanders are invisible to the log.
- Map artwork only appears after you run `extract_maps.py` (see "Zone maps" above). Without it you get the zone outline and a grid.
- Positions are only valid within one zone, so the page shows pins for your current zone.
