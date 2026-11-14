package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	tokenHeader     = "X-Radar-Token"
	apiPrefix       = "/api/"
	maxBodyBytes    = 64 << 10
	loopbackAddress = "127.0.0.1"
	localhostName   = "localhost"

	contentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; " +
		"img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
)

// Only these four files are embedded, so the web/test folder can never be served.
//
//go:embed web/index.html web/app.js web/logic.js web/style.css
var webFiles embed.FS

var assetTypes = map[string]string{
	"app.js":    "text/javascript; charset=utf-8",
	"logic.js":  "text/javascript; charset=utf-8",
	"style.css": "text/css; charset=utf-8",
}

var pageTemplate = template.Must(template.ParseFS(webFiles, "web/index.html"))

func newToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("system random source failed: %v", err))
	}
	return hex.EncodeToString(b)
}

func pageURL(ln net.Listener, token string) string {
	return "http://" + ln.Addr().String() + "/?t=" + url.QueryEscape(token)
}

// listenLocal listens on addr, which must be a loopback IP (or localhost) with a port.
func listenLocal(addr string) (net.Listener, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("listen address %q: %w", addr, err)
	}
	if host == localhostName {
		host = loopbackAddress
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("listen address %q: only loopback addresses are allowed", addr)
	}
	return net.Listen("tcp", net.JoinHostPort(host, port))
}

// handler returns the full handler: security headers, Host/Origin checks, token check and routes.
func (a *app) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", a.handlePage)
	for name := range assetTypes {
		mux.HandleFunc("GET /"+name, serveAsset)
	}
	a.routeAPI(mux)
	return secure(a.requireToken(mux))
}

func (a *app) serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{Handler: a.handler(), ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		_ = srv.Close()
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

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

func originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	return origin == "" || origin == "http://"+r.Host
}

func (a *app) requireToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, apiPrefix) && !a.tokenOK(r.Header.Get(tokenHeader)) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Missing or wrong token. Reopen radar-keys.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *app) tokenOK(got string) bool {
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(a.token)) == 1
}

func (a *app) handlePage(w http.ResponseWriter, r *http.Request) {
	if !a.tokenOK(r.URL.Query().Get("t")) {
		writeError(w, http.StatusForbidden, "forbidden", "Open the link that radar-keys printed or opened.")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pageTemplate.Execute(w, struct{ Token string }{a.token}); err != nil {
		http.Error(w, "page error", http.StatusInternalServerError)
	}
}

func serveAsset(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")
	ct, ok := assetTypes[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(webFiles, "web/"+name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ct)
	_, _ = w.Write(data)
}
