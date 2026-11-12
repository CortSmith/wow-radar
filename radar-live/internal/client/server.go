package client

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	tokenHeader     = "X-Radar-Token"
	apiPrefix       = "/api/"
	tokenBytes      = 32
	maxBodyBytes    = 64 << 10
	loopbackAddress = "127.0.0.1"
	localhostName   = "localhost"
	readHeaderLimit = 5 * time.Second
	writeLimit      = 30 * time.Second
	idleLimit       = 60 * time.Second
	shutdownLimit   = 10 * time.Second
)

// UIOptions configure a UIServer.
type UIOptions struct {
	Settings     Settings // the settings shown first
	SettingsWarn string   // why the saved settings could not be loaded, if so
	SettingsPath string   // where Start saves the non-secret settings ("" = do not save)
	Keys         *KeyStore
	Session      *Session
	Picker       FilePicker
	CanBrowse    bool   // the picker is a real dialog (see CanBrowse)
	Token        string // required on every API call; see NewToken
	Source       string // ingest source id used for uploads
	Version      string
	Quit         func() // lets the page end the program (browser mode); nil hides the button
}

// UIServer is the local web UI: a page, a JSON API and nothing else. It is meant to listen on
// 127.0.0.1 only (see ListenLocal) and refuses requests with a foreign Host or Origin or without
// the per-run token.
type UIServer struct {
	opts UIOptions

	mu      sync.Mutex // guards current
	current Settings
}

// NewUIServer returns a server for opts.
func NewUIServer(opts UIOptions) *UIServer {
	return &UIServer{opts: opts, current: opts.Settings}
}

// NewToken returns a fresh random token for one run.
func NewToken() string {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("system random source failed: %v", err)) // there is no safe way on without one
	}
	return hex.EncodeToString(b)
}

// ListenLocal listens on 127.0.0.1:port (0 = any free port). Nothing else is ever bound.
func ListenLocal(port int) (net.Listener, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort(loopbackAddress, strconv.Itoa(port)))
	if err != nil {
		return nil, fmt.Errorf("listen on %s port %d: %w", loopbackAddress, port, err)
	}
	return ln, nil
}

// PageURL is the address that opens the UI, including the token.
func PageURL(ln net.Listener, token string) string {
	return "http://" + ln.Addr().String() + "/?t=" + url.QueryEscape(token)
}

// Serve serves the UI on ln until ctx ends, then shuts down gracefully.
func (u *UIServer) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler: u.Handler(), ReadHeaderTimeout: readHeaderLimit, WriteTimeout: writeLimit, IdleTimeout: idleLimit,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownLimit)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		_ = srv.Close()
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Handler returns the full handler: security headers, Host and Origin checks, the token check for
// /api/ and the routes.
func (u *UIServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", u.handlePage)
	mux.HandleFunc("GET /app.js", serveAsset)
	mux.HandleFunc("GET /logic.js", serveAsset)
	mux.HandleFunc("GET /style.css", serveAsset)
	u.routeAPI(mux)
	return secure(u.requireToken(mux))
}

// secure adds the response headers and refuses foreign Host and Origin values. This is the defence
// against DNS rebinding and cross-site requests from a web page the user happens to have open.
func secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-store")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		if !hostAllowed(r) || !originAllowed(r) {
			writeError(w, http.StatusForbidden, "forbidden", "This address is not allowed.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// hostAllowed accepts only 127.0.0.1 or localhost together with the port this connection arrived on.
func hostAllowed(r *http.Request) bool {
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil || (host != loopbackAddress && host != localhostName) {
		return false
	}
	local, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok {
		return false
	}
	_, localPort, err := net.SplitHostPort(local.String())
	return err == nil && port == localPort
}

// originAllowed accepts requests without an Origin header, or whose Origin is this very server.
func originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	return origin == "" || origin == "http://"+r.Host
}

// requireToken demands the per-run token (in a header) for every /api/ call.
func (u *UIServer) requireToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, apiPrefix) && !u.tokenOK(r.Header.Get(tokenHeader)) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Missing or wrong token. Reopen radar-client.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (u *UIServer) tokenOK(got string) bool {
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(u.opts.Token)) == 1
}

// handlePage serves index.html, but only to someone who has the token (the link radar-client opens).
func (u *UIServer) handlePage(w http.ResponseWriter, r *http.Request) {
	if !u.tokenOK(r.URL.Query().Get("t")) {
		writeError(w, http.StatusForbidden, "forbidden", "Open the link that radar-client printed or opened.")
		return
	}
	servePage(w, u.opts.Token)
}
