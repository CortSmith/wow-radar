// Package armoryclient is a small HTTPS client for the Battle.net WoW Classic Profile API. It has no host
// and no database: it reads one character at a time (summary plus equipment, specializations, statistics
// and pvp-summary sub-resources, merged into domain.ArmoryCharacter) and expands a guild into its roster.
//
// Authentication is OAuth2 client credentials: the client id and secret (both Secret values) are exchanged
// at the token endpoint for a short-lived bearer token that lives in memory only, is refreshed before it
// expires (and once on a 401) and is never logged.
//
// Everything this package knows about the API's shape (endpoint paths, namespace, JSON field names) comes
// from community sources and is UNVERIFIED: the official documentation page could not be read. All paths
// are configurable and the field mapping is isolated in mapping.go, marked PROVISIONAL. The transport
// (limiter, retries with Retry-After, circuit breaker, size cap, no redirects) is shape-agnostic.
//
// Security: base and token URLs must be https without userinfo, redirects are never followed, TLS is
// verified, responses are size capped, realm, character and guild names are validated and slugified
// before they enter a URL, and the client id, secret and tokens never appear in an error.
package armoryclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"radarlive/internal/domain"
)

// Source is what the sync needs from the armory; armorysync depends on this shape and nothing else.
type Source interface {
	// GuildRoster lists the members of a guild (realm in slug form). A guild without a roster (404 or 403)
	// is an empty list, not an error.
	GuildRoster(ctx context.Context, realm, guild string) ([]CharRef, error)
	// Character reads one character. Hidden (403) and not-found (404) are not errors: they return a
	// character with the matching Status.
	Character(ctx context.Context, realm, name string) (domain.ArmoryCharacter, error)
}

// CharRef identifies a character.
type CharRef struct{ Realm, Name string }

// Config configures the client. Zero values of the optional fields take the defaults noted. Every default
// path and the namespace are PROVISIONAL guesses (see mapping.go).
type Config struct {
	BaseURL          string        // optional; default https://{Region}.api.blizzard.com (https, no userinfo/query)
	Region           string        // default "us"
	Namespace        string        // default DefaultNamespace(Region) (UNVERIFIED which namespace WoW Forever uses)
	RosterNamespace  string        // default Namespace
	Locale           string        // default "en_US"
	TokenURL         string        // default DefaultTokenURL (https)
	ClientID         Secret        // required
	ClientSecret     Secret        // required
	SummaryPath      string        // {realmSlug} and {characterName}; defaults are the Default* constants
	EquipmentPath    string        // same placeholders
	SpecsPath        string        // same placeholders
	StatsPath        string        // same placeholders
	PvPPath          string        // same placeholders
	RosterPath       string        // {realmSlug} and {guildSlug}
	MaxRosterMembers int           // members kept per guild, default 1000, at most 10000
	Timeout          time.Duration // per attempt, default 15s
	MaxResponseBytes int64         // default 1 MiB
	RatePerSecond    float64       // outbound token bucket refill, default 5
	Burst            int           // bucket size, default 5
	MaxRetries       int           // retries after the first attempt for 429/5xx/network errors, default 3 (negative: none)
	BackoffMin       time.Duration // default 500ms
	BackoffMax       time.Duration // default 30s; also caps Retry-After
	BreakerThreshold int           // consecutive 429/5xx before opening, default 5
	BreakerCooldown  time.Duration // default 60s
	UserAgent        string        // default "radar-armory"
}

// DefaultNamespace is the provisional namespace guess for a region.
func DefaultNamespace(region string) string { return "profile-classic-" + region }

// String prints the config without the credentials (Secret redacts itself; this also keeps %v short).
func (c Config) String() string {
	return fmt.Sprintf("armoryclient.Config{BaseURL:%q Region:%q Namespace:%q ClientID:%v ClientSecret:%v}",
		c.BaseURL, c.Region, c.Namespace, c.ClientID, c.ClientSecret)
}

// Options replaces the environment-dependent parts, mainly for tests.
type Options struct {
	Transport http.RoundTripper // nil: a clone of the default transport (TLS verified)
	Now       func() time.Time
	Sleep     func(ctx context.Context, d time.Duration) error
	Jitter    func(limit time.Duration) time.Duration // extra delay in [0, limit]

	// AllowHTTP accepts http:// base and token URLs. TEST-ONLY seam (httptest fakes in cmd/radar-armory and
	// this package): production wiring never sets it, so credentials and tokens only travel over https.
	AllowHTTP bool
}

// Client implements Source.
type Client struct {
	cfg     Config
	base    string
	hc      *http.Client
	now     func() time.Time
	sleep   func(ctx context.Context, d time.Duration) error
	jitter  func(time.Duration) time.Duration
	limiter *limiter
	breaker *breaker
	ts      *TokenSource
}

var _ Source = (*Client)(nil)

// New validates cfg and builds a client.
func New(cfg Config, opt Options) (*Client, error) {
	cfg = withDefaults(cfg)
	base, err := validateConfig(cfg, opt.AllowHTTP)
	if err != nil {
		return nil, err
	}
	c := &Client{cfg: cfg, base: base, now: opt.Now, sleep: opt.Sleep, jitter: opt.Jitter}
	if c.now == nil {
		c.now = time.Now
	}
	if c.sleep == nil {
		c.sleep = sleepCtx
	}
	if c.jitter == nil {
		c.jitter = randomJitter
	}
	rt := opt.Transport
	if rt == nil {
		rt = http.DefaultTransport.(*http.Transport).Clone()
	}
	c.hc = &http.Client{
		Transport: rt,
		Timeout:   cfg.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse // a 3xx is returned and rejected as ErrRedirect
		},
	}
	c.limiter = newLimiter(cfg.RatePerSecond, cfg.Burst, c.now())
	c.breaker = newBreaker(cfg.BreakerThreshold, cfg.BreakerCooldown)
	c.ts = newTokenSource(c)
	return c, nil
}

func withDefaults(c Config) Config {
	if c.Region == "" {
		c.Region = "us"
	}
	if c.BaseURL == "" && regionRE.MatchString(c.Region) {
		c.BaseURL = "https://" + c.Region + ".api.blizzard.com"
	}
	if c.Namespace == "" {
		c.Namespace = DefaultNamespace(c.Region)
	}
	if c.RosterNamespace == "" {
		c.RosterNamespace = c.Namespace
	}
	if c.Locale == "" {
		c.Locale = "en_US"
	}
	if c.TokenURL == "" {
		c.TokenURL = DefaultTokenURL
	}
	for p, def := range map[*string]string{&c.SummaryPath: DefaultSummaryPath, &c.EquipmentPath: DefaultEquipmentPath,
		&c.SpecsPath: DefaultSpecsPath, &c.StatsPath: DefaultStatsPath, &c.PvPPath: DefaultPvPPath, &c.RosterPath: DefaultRosterPath} {
		if *p == "" {
			*p = def
		}
	}
	if c.MaxRosterMembers == 0 {
		c.MaxRosterMembers = 1000
	}
	if c.Timeout <= 0 {
		c.Timeout = 15 * time.Second
	}
	if c.MaxResponseBytes <= 0 {
		c.MaxResponseBytes = 1 << 20
	}
	if c.RatePerSecond <= 0 {
		c.RatePerSecond = 5
	}
	if c.Burst <= 0 {
		c.Burst = 5
	}
	if c.MaxRetries == 0 {
		c.MaxRetries = 3
	}
	if c.BackoffMin <= 0 {
		c.BackoffMin = 500 * time.Millisecond
	}
	if c.BackoffMax < c.BackoffMin {
		c.BackoffMax = max(30*time.Second, c.BackoffMin)
	}
	if c.BreakerThreshold <= 0 {
		c.BreakerThreshold = 5
	}
	if c.BreakerCooldown <= 0 {
		c.BreakerCooldown = 60 * time.Second
	}
	if c.UserAgent == "" {
		c.UserAgent = "radar-armory"
	}
	return c
}

var (
	regionRE    = regexp.MustCompile(`^[a-z]{2,3}$`)
	namespaceRE = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)
	localeRE    = regexp.MustCompile(`^[a-z]{2}_[A-Z]{2}$`)
)

const maxRosterMembersLimit = 10000

func configErr(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrConfig, fmt.Sprintf(format, a...))
}

// validateConfig checks the config and returns the normalised base URL (no trailing slash).
func validateConfig(c Config, allowHTTP bool) (string, error) {
	base, err := checkURL(c.BaseURL, allowHTTP, "base URL")
	if err != nil {
		return "", err
	}
	if _, err := checkURL(c.TokenURL, allowHTTP, "token URL"); err != nil {
		return "", err
	}
	switch {
	case !regionRE.MatchString(c.Region):
		return "", configErr("region must be 2 or 3 lowercase letters")
	case !namespaceRE.MatchString(c.Namespace) || !namespaceRE.MatchString(c.RosterNamespace):
		return "", configErr("namespace must be lowercase letters, digits and hyphens")
	case !localeRE.MatchString(c.Locale):
		return "", configErr("locale must look like en_US")
	case !c.ClientID.IsSet() || !c.ClientSecret.IsSet():
		return "", configErr("client id and client secret must both be set")
	case strings.ContainsAny(c.ClientID.Reveal(), "\r\n\x00:") || strings.ContainsAny(c.ClientSecret.Reveal(), "\r\n\x00"):
		return "", configErr("client id or secret is not valid in a credentials header")
	case c.MaxRosterMembers < 0 || c.MaxRosterMembers > maxRosterMembersLimit:
		return "", configErr("roster member cap must be from 0 to %d", maxRosterMembersLimit)
	}
	for name, tpl := range map[string]string{"summary": c.SummaryPath, "equipment": c.EquipmentPath, "specializations": c.SpecsPath,
		"statistics": c.StatsPath, "pvp-summary": c.PvPPath} {
		if !validTemplate(tpl, "{realmSlug}", "{characterName}") {
			return "", configErr("%s path must start with / and contain {realmSlug} and {characterName} once each", name)
		}
	}
	if !validTemplate(c.RosterPath, "{realmSlug}", "{guildSlug}") {
		return "", configErr("roster path must start with / and contain {realmSlug} and {guildSlug} once each")
	}
	return base, nil
}

// checkURL validates an absolute URL and returns it without a trailing slash.
func checkURL(raw string, allowHTTP bool, what string) (string, error) {
	u, err := url.Parse(raw)
	switch {
	case err != nil || u.Host == "" || u.Hostname() == "":
		return "", configErr("%s is not a valid absolute URL", what)
	case u.Scheme != "https" && !(allowHTTP && u.Scheme == "http"):
		return "", configErr("%s must use https", what)
	case u.User != nil:
		return "", configErr("%s must not contain credentials", what)
	case u.RawQuery != "" || u.Fragment != "" || u.ForceQuery:
		return "", configErr("%s must not contain a query or fragment", what)
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// validTemplate: an absolute path with each given placeholder exactly once and no other braces.
func validTemplate(p string, holders ...string) bool {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.ContainsAny(p, "?#\\ \r\n\t") || strings.Contains(p, "..") {
		return false
	}
	rest := p
	for _, h := range holders {
		if strings.Count(p, h) != 1 {
			return false
		}
		rest = strings.Replace(rest, h, "", 1)
	}
	return !strings.ContainsAny(rest, "{}")
}

// validateRef checks a realm and character name with the domain rules and the realm's slug form before
// they are used in a URL. It returns the realm slug.
func validateRef(realm, name string) error {
	probe := domain.ArmoryCharacter{Realm: realm, Name: name, Status: domain.ArmoryStatusOK, FetchedAt: domain.MinTimestampMs}
	if err := probe.Validate(); err != nil {
		return ErrBadName
	}
	return nil
}

// RealmSlug converts a realm name to the lowercase hyphenated form used in Battle.net paths: lowercase,
// apostrophes dropped, spaces to hyphens. Only ASCII letters, digits and inner hyphens (at most 48
// characters) are accepted; anything else is refused so a hostile realm can never shape a URL.
func RealmSlug(realm string) (string, bool) {
	s := slugBase(realm)
	if s == "" || len(s) > domain.ArmoryRealmMaxRunes || s[0] == '-' || s[len(s)-1] == '-' {
		return "", false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return "", false
		}
	}
	return s, true
}

// GuildSlug is the same conversion for a guild name; letters of any script and digits are accepted.
func GuildSlug(guild string) (string, bool) {
	s := slugBase(guild)
	if s == "" || utf8.RuneCountInString(s) > 64 || !utf8.ValidString(s) || s[0] == '-' || s[len(s)-1] == '-' {
		return "", false
	}
	for _, r := range s {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-') {
			return "", false
		}
	}
	return s, true
}

func slugBase(s string) string {
	s = strings.ToLower(s)
	s = strings.NewReplacer("'", "", "’", "", " ", "-").Replace(s)
	return s
}

// api performs one authenticated GET (path plus namespace and locale). A 401 invalidates the token and is
// retried once with a fresh one; a second 401 is ErrAuth.
func (c *Client) api(ctx context.Context, path, namespace string) (response, error) {
	q := url.Values{"namespace": {namespace}, "locale": {c.cfg.Locale}}
	target := c.base + path + "?" + q.Encode()
	for attempt := 0; ; attempt++ {
		tok, err := c.ts.Token(ctx)
		if err != nil {
			return response{}, err
		}
		res, err := c.get(ctx, request{method: http.MethodGet, url: target, bearer: tok})
		if err != nil {
			return response{}, err
		}
		if res.status != http.StatusUnauthorized {
			return res, nil
		}
		c.ts.Invalidate(tok)
		if attempt >= 1 {
			return response{}, ErrAuth
		}
	}
}

func fill(tpl string, pairs ...string) string { return strings.NewReplacer(pairs...).Replace(tpl) }

// Character implements Source: the summary decides the status; the four sub-resources add detail and
// degrade to "missing" on 403/404.
func (c *Client) Character(ctx context.Context, realm, name string) (domain.ArmoryCharacter, error) {
	slug, ok := RealmSlug(realm)
	if !ok || validateRef(realm, name) != nil {
		return domain.ArmoryCharacter{}, ErrBadName
	}
	pairs := []string{"{realmSlug}", slug, "{characterName}", url.PathEscape(strings.ToLower(name))}
	res, err := c.api(ctx, fill(c.cfg.SummaryPath, pairs...), c.cfg.Namespace)
	if err != nil {
		return domain.ArmoryCharacter{}, err
	}
	nowMs := c.now().UnixMilli()
	switch res.status {
	case http.StatusOK:
	case http.StatusForbidden:
		return statusCharacter(realm, name, domain.ArmoryStatusHidden, nowMs), nil
	case http.StatusNotFound:
		return statusCharacter(realm, name, domain.ArmoryStatusNotFound, nowMs), nil
	default:
		return domain.ArmoryCharacter{}, &StatusError{Code: res.status}
	}
	p := parts{summary: res.body}
	for _, sub := range []struct {
		path string
		dst  *[]byte
	}{{c.cfg.EquipmentPath, &p.equipment}, {c.cfg.SpecsPath, &p.specs}, {c.cfg.StatsPath, &p.stats}, {c.cfg.PvPPath, &p.pvp}} {
		r, err := c.api(ctx, fill(sub.path, pairs...), c.cfg.Namespace)
		if err != nil {
			return domain.ArmoryCharacter{}, err
		}
		switch r.status {
		case http.StatusOK:
			*sub.dst = r.body
		case http.StatusForbidden, http.StatusNotFound:
		default:
			return domain.ArmoryCharacter{}, &StatusError{Code: r.status}
		}
	}
	return mapCharacter(realm, name, p, nowMs)
}

// GuildRoster implements Source.
func (c *Client) GuildRoster(ctx context.Context, realm, guild string) ([]CharRef, error) {
	rs, ok := RealmSlug(realm)
	gs, gok := GuildSlug(guild)
	if !ok || !gok || validateRef(rs, "Roster") != nil {
		return nil, ErrBadName
	}
	res, err := c.api(ctx, fill(c.cfg.RosterPath, "{realmSlug}", rs, "{guildSlug}", url.PathEscape(gs)), c.cfg.RosterNamespace)
	if err != nil {
		return nil, err
	}
	switch res.status {
	case http.StatusOK:
		return parseRoster(res.body, rs, c.cfg.MaxRosterMembers)
	case http.StatusNotFound, http.StatusForbidden:
		return nil, nil
	}
	return nil, &StatusError{Code: res.status}
}

// scrub removes the client id, secret and the current token from an error's text while keeping its cause.
func (c *Client) scrub(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	changed := false
	for _, s := range []string{c.cfg.ClientSecret.Reveal(), c.cfg.ClientID.Reveal(), c.ts.known()} {
		if s != "" && strings.Contains(msg, s) {
			msg, changed = strings.ReplaceAll(msg, s, redactedText), true
		}
	}
	if !changed {
		return err
	}
	return &scrubbed{msg: msg, err: errors.Unwrap(err)}
}
