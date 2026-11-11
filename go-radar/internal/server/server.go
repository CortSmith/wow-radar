// Package server serves the embedded UI and the JSON/SSE API on 127.0.0.1.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"nearbyradar/internal/dataset"
	"nearbyradar/internal/history"
	"nearbyradar/internal/maps"
	"nearbyradar/internal/tracker"
	"nearbyradar/web"
)

// StateSource is what the server needs from the tracker.
type StateSource interface {
	Snapshot() tracker.Snapshot
	Version() uint64
}

// HistorySource is what the server needs from the history store.
type HistorySource interface {
	Snapshot() []history.Entry
	Search(q string, limit int) []history.Match
	Track(guid string, mapID int) (history.TrackResult, bool)
}

// Status describes the log being followed.
type Status struct {
	LogFile string `json:"logFile"`
	LogsDir string `json:"logsDir"`
}

// Options configure a Server.
type Options struct {
	State       StateSource
	History     HistorySource
	Maps        *maps.Index // optional
	Orientation string      // "std" (X north/up, Y west/left) or "alt"
	Status      func() Status
	Interval    time.Duration // SSE tick, default 500ms
	Bounty      BountySource  // optional: enables /api/bounty and /api/bounty/player
	Analyzer    BountyJobs    // optional: enables /api/bounty/analyze
	Rulesets    RulesetSource // optional: enables /api/ruleset and view.ruleset
}

// Server is the HTTP front end.
type Server struct {
	opt     Options
	clients atomic.Int32
	ln      net.Listener
	addr    string
}

// New creates a Server.
func New(o Options) *Server {
	if o.Interval <= 0 {
		o.Interval = 500 * time.Millisecond
	}
	if o.Orientation == "" {
		o.Orientation = "std"
	}
	return &Server{opt: o}
}

// ZoneView describes the background image of the current zone.
type ZoneView struct {
	URL      string `json:"url"`
	W        int    `json:"w"`
	H        int    `json:"h"`
	Revealed bool   `json:"revealed"`
}

// View is UI-only metadata added to the tracker snapshot.
type View struct {
	Orientation string    `json:"orientation"`
	Zone        *ZoneView `json:"zone"`
	LogFile     string    `json:"logFile"`
	LogsDir     string    `json:"logsDir"`
	// Available lists every map with a known world rectangle and an image, sorted by name.
	Available []AvailMap `json:"available"`
	// NoImage lists maps with a known rectangle but no image in the maps index.
	NoImage []NoImageMap `json:"noImage"`
	// Ruleset is the active server rule-set (nil without a RulesetSource).
	Ruleset *dataset.Brief `json:"ruleset,omitempty"`
}

// AvailMap is a selectable map.
type AvailMap struct {
	MapID    int    `json:"mapId"`
	Name     string `json:"name"`
	URL      string `json:"url"`
	W        int    `json:"w"`
	H        int    `json:"h"`
	Revealed bool   `json:"revealed"`
}

// NoImageMap is a map the radar can place pips on but has no picture for.
type NoImageMap struct {
	MapID int    `json:"mapId"`
	Name  string `json:"name"`
}

type stateResp struct {
	tracker.Snapshot
	View View `json:"view"`
}

func (s *Server) state() stateResp {
	snap := s.opt.State.Snapshot()
	v := View{Orientation: s.opt.Orientation, Available: []AvailMap{}, NoImage: []NoImageMap{}}
	if s.opt.Status != nil {
		st := s.opt.Status()
		v.LogFile, v.LogsDir = st.LogFile, st.LogsDir
	}
	if s.opt.Maps != nil {
		// Prefer the current map's MAP_CHANGE name (always present once the map is known);
		// ZONE_CHANGE may be missing when the app starts mid-log, so it is only the fallback.
		names := []string{snap.Zone}
		if mi, ok := snap.Maps[strconv.Itoa(snap.MapID)]; ok && mi.Name != "" {
			names = []string{mi.Name, snap.Zone}
		}
		for _, n := range names {
			if z, ok := s.opt.Maps.Lookup(n); ok {
				v.Zone = &ZoneView{URL: "/api/maps/" + z.File, W: z.W, H: z.H, Revealed: z.Revealed}
				break
			}
		}
	}
	s.fillAvailable(&v, snap)
	if s.opt.Rulesets != nil {
		b := s.opt.Rulesets.Brief()
		v.Ruleset = &b
	}
	return stateResp{Snapshot: snap, View: v}
}

// fillAvailable joins the tracker's known map rectangles with the maps index by MAP_CHANGE name.
func (s *Server) fillAvailable(v *View, snap tracker.Snapshot) {
	for k, mi := range snap.Maps {
		id, err := strconv.Atoi(k)
		if err != nil || mi.Name == "" || mi.X0 == mi.X1 || mi.Y0 == mi.Y1 {
			continue
		}
		if s.opt.Maps != nil {
			if z, ok := s.opt.Maps.Lookup(mi.Name); ok {
				v.Available = append(v.Available, AvailMap{MapID: id, Name: mi.Name, URL: "/api/maps/" + z.File, W: z.W, H: z.H, Revealed: z.Revealed})
				continue
			}
		}
		v.NoImage = append(v.NoImage, NoImageMap{MapID: id, Name: mi.Name})
	}
	sort.Slice(v.Available, func(i, j int) bool {
		return lessName(v.Available[i].Name, v.Available[i].MapID, v.Available[j].Name, v.Available[j].MapID)
	})
	sort.Slice(v.NoImage, func(i, j int) bool {
		return lessName(v.NoImage[i].Name, v.NoImage[i].MapID, v.NoImage[j].Name, v.NoImage[j].MapID)
	})
}

func lessName(a string, ai int, b string, bi int) bool {
	if la, lb := strings.ToLower(a), strings.ToLower(b); la != lb {
		return la < lb
	}
	return ai < bi
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/state", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, s.state()) })
	mux.HandleFunc("/api/history", func(w http.ResponseWriter, r *http.Request) {
		rows := s.opt.History.Snapshot()
		if rows == nil {
			rows = []history.Entry{}
		}
		writeJSON(w, rows)
	})
	mux.HandleFunc("/api/players", s.players)
	mux.HandleFunc("/api/track", s.track)
	mux.HandleFunc("/api/events", s.events)
	s.registerBounty(mux)
	s.registerRuleset(mux)
	mux.HandleFunc("/api/maps/", s.mapImage)
	ui, _ := fs.Sub(web.FS(), ".")
	mux.Handle("/", http.FileServer(http.FS(ui)))
	return hostGuard(mux)
}

// hostGuard rejects requests whose Host is not loopback (DNS-rebinding protection).
func hostGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Host
		if i := strings.LastIndexByte(h, ':'); i >= 0 && !strings.HasSuffix(h, "]") {
			h = h[:i]
		}
		switch h {
		case "127.0.0.1", "localhost", "[::1]":
			next.ServeHTTP(w, r)
		default:
			http.Error(w, "forbidden host", http.StatusForbidden)
		}
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

func (s *Server) mapImage(w http.ResponseWriter, r *http.Request) {
	if s.opt.Maps == nil {
		http.NotFound(w, r)
		return
	}
	p, ok := s.opt.Maps.FilePath(strings.TrimPrefix(r.URL.Path, "/api/maps/"))
	if !ok || !strings.HasSuffix(strings.ToLower(p), ".png") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "max-age=3600")
	http.ServeFile(w, r, p)
}

// events streams the live state as Server-Sent Events. A frame is sent when the tracker
// changed, and at least every 2s so that stale/expiry transitions (which depend on the
// wall clock only) reach the UI.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	s.clients.Add(1)
	defer s.clients.Add(-1)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	send := func() bool {
		b, err := json.Marshal(s.state())
		if err != nil {
			return false
		}
		_, err = fmt.Fprintf(w, "event: state\ndata: %s\n\n", b)
		fl.Flush()
		return err == nil
	}
	if !send() {
		return
	}
	last, lastSend := s.opt.State.Version(), time.Now()
	tk := time.NewTicker(s.opt.Interval)
	defer tk.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tk.C:
			if v := s.opt.State.Version(); v != last || time.Since(lastSend) >= 2*time.Second {
				last, lastSend = v, time.Now()
				if !send() {
					return
				}
			}
		}
	}
}

// Start listens on a random 127.0.0.1 port and serves until ctx is cancelled.
func (s *Server) Start(ctx context.Context) (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	s.ln, s.addr = ln, ln.Addr().String()
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	go func() {
		<-ctx.Done()
		sc, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if srv.Shutdown(sc) != nil {
			_ = srv.Close()
		}
	}()
	return s.addr, nil
}

// URL is the base URL after Start.
func (s *Server) URL() string { return "http://" + s.addr + "/" }

// Clients is the number of currently connected SSE clients.
func (s *Server) Clients() int { return int(s.clients.Load()) }

const (
	playersLimit = 50  // max rows returned by /api/players
	maxQueryLen  = 100 // search text is truncated to this many bytes
)

// players serves GET /api/players?q=<text>: history players matching q, newest first.
func (s *Server) players(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query().Get("q")
	if len(q) > maxQueryLen {
		q = q[:maxQueryLen]
	}
	writeJSON(w, s.opt.History.Search(q, playersLimit))
}

// track serves GET /api/track?guid=<guid>[&map=<id>]: the chronological fixes of one player.
func (s *Server) track(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	guid := q.Get("guid")
	if guid == "" {
		http.Error(w, "guid is required", http.StatusBadRequest)
		return
	}
	mapID := 0
	if m := q.Get("map"); m != "" {
		n, err := strconv.Atoi(m)
		if err != nil || n < 0 {
			http.Error(w, "map must be a non-negative integer", http.StatusBadRequest)
			return
		}
		mapID = n
	}
	res, ok := s.opt.History.Track(guid, mapID)
	if !ok {
		http.Error(w, "unknown guid", http.StatusNotFound)
		return
	}
	writeJSON(w, res)
}
