// Package headers is the guard module that adds the browser-side security headers to every
// response of every listener: a strict Content-Security-Policy, nosniff, no-referrer, a
// Permissions-Policy that denies every feature, cross-origin isolation, clickjacking denial,
// optional HSTS and no-store caching of /api/*. It only uses core's Server socket, so deleting its
// line from a binary's modules.go removes all of it and the host still works.
package headers

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"radarlive/internal/core"
)

// Module name and settings (environment: RADAR_HEADERS_HSTS, RADAR_HEADERS_API_ORIGINS).
const (
	ModuleName        = "headers"
	SettingHSTS       = "headers-hsts"
	SettingAPIOrigins = "headers-api-origins"

	hstsValue         = "max-age=31536000; includeSubDomains"
	apiPrefix         = "/api/"
	permissionsPolicy = "accelerometer=(), autoplay=(), camera=(), display-capture=(), encrypted-media=(), " +
		"fullscreen=(), geolocation=(), gyroscope=(), magnetometer=(), microphone=(), midi=(), payment=(), " +
		"picture-in-picture=(), publickey-credentials-get=(), usb=(), xr-spatial-tracking=()"
)

// Module sets the headers.
type Module struct {
	host *core.Host
	csp  string
	hsts bool
}

// New returns the headers module.
func New() *Module { return &Module{} }

// Name implements core.Module.
func (*Module) Name() string { return ModuleName }

// Init declares the settings and registers the handler wrapper. Registered before the other guard
// modules it is the outermost wrapper, so even their 429 and 503 answers carry the headers.
func (m *Module) Init(host *core.Host) error {
	m.host = host
	for _, s := range []core.Setting{
		{Key: SettingHSTS, Kind: core.KindBool, Default: "false", Usage: "send Strict-Transport-Security (only when the site is served over HTTPS, for example through a tunnel)"},
		{Key: SettingAPIOrigins, Kind: core.KindString, Usage: "comma-separated API origins the page may call besides itself (CSP connect-src)"},
	} {
		if err := host.Config.Declare(s); err != nil {
			return err
		}
	}
	host.Server.WrapHandler(m.wrap)
	return nil
}

// Start validates the configuration; the host does not listen when it is unusable.
func (m *Module) Start(context.Context) error {
	origins, err := parseOrigins(m.host.Config.String(SettingAPIOrigins))
	if err != nil {
		return err
	}
	m.csp = policy(origins)
	m.hsts = m.host.Config.Bool(SettingHSTS)
	return nil
}

// policy builds the Content-Security-Policy value.
func policy(apiOrigins []string) string {
	connect := strings.Join(append([]string{"'self'"}, apiOrigins...), " ")
	return "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src " + connect +
		"; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
}

// parseOrigins accepts only plain scheme://host[:port] origins, so a setting can never inject
// another CSP directive or widen the policy with a wildcard.
func parseOrigins(raw string) ([]string, error) {
	var out []string
	for _, o := range strings.Split(raw, ",") {
		if o = strings.TrimSpace(o); o == "" {
			continue
		}
		u, err := url.Parse(o)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Path != "" ||
			u.RawQuery != "" || u.Fragment != "" || u.User != nil || strings.ContainsAny(o, "*;,' \t\"") {
			return nil, fmt.Errorf("headers: %s entry %q must look like https://host[:port]", SettingAPIOrigins, o)
		}
		out = append(out, o)
	}
	return out, nil
}

func (m *Module) wrap(_ string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", m.csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", permissionsPolicy)
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		if m.hsts {
			h.Set("Strict-Transport-Security", hstsValue)
		}
		if strings.HasPrefix(r.URL.Path, apiPrefix) {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}
