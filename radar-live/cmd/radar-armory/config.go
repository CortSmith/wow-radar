package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/domain"
)

// Environment variable names. The three key variables are secrets: env (or a *_FILE path) only, never flags.
// WOW_ARMORY_API_KEY (the Battle.net client SECRET) and WOW_ARMORY_CLIENT_ID (the client id, treated as a
// secret too) are the deliberate exceptions to the RADAR_ prefix rule (docs/security/secrets-policy.md).
const (
	envAPIKey      = "WOW_ARMORY_API_KEY"
	envClientID    = "WOW_ARMORY_CLIENT_ID"
	envDBKey       = "RADAR_ARMORY_DB_KEY"
	envPrefix      = "RADAR_ARMORY_"
	fileSuffix     = "_FILE"
	defaultDBURL   = "http://127.0.0.1:8789"
	defaultRuntime = 6 * time.Hour
	maxKeyFileSize = 4 << 10
)

// secretVal holds a key read from the environment. It prints "<redacted>" through every fmt verb; only
// Reveal returns the value, and only modules.go and dbsink.go call it (to build an armoryclient.Secret or
// the DB header).
type secretVal struct{ v string }

func (s secretVal) Reveal() string             { return s.v }
func (s secretVal) String() string             { return "<redacted>" }
func (s secretVal) GoString() string           { return "<redacted>" }
func (s secretVal) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, "<redacted>") }

// config is the validated command line plus environment.
type config struct {
	// Battle.net client. Every endpoint detail is PROVISIONAL (unverified) and optional: an empty value
	// means the armoryclient default. The client validates them when it is built.
	baseURL, region, namespace, rosterNamespace, locale, tokenURL         string
	summaryPath, equipmentPath, specsPath, statsPath, pvpPath, rosterPath string

	defaultRealm             string // slug form; seeds without a realm use it
	seedPageSize             int
	maxGuildMembers          int
	rate                     float64
	concurrency, maxRequests int
	maxRuntime               time.Duration
	dbURL                    string
	location                 *time.Location
	apiKey, clientID, dbKey  secretVal // Battle.net client secret, client id, radar-db ingest key
	showVersion              bool
	warnings                 []string // configuration notes for stderr; names settings, never values
}

// configError is a configuration problem (exit code 2). Its text never contains a setting value.
type configError struct{ msg string }

func (e *configError) Error() string { return e.msg }

func cfgErr(format string, a ...any) error { return &configError{msg: fmt.Sprintf(format, a...)} }

func isConfigError(err error) bool {
	var ce *configError
	return errors.As(err, &ce)
}

// parseConfig reads flags and RADAR_ARMORY_* variables (flag > env > default) and the three keys (env or
// *_FILE only). Error texts name the setting, never its value.
func parseConfig(args []string, lookup func(string) (string, bool)) (config, error) {
	var c config
	if err := core.RejectSecretFlags(args, map[string]string{
		"api-key": envAPIKey, "client-secret": envAPIKey, "client-id": envClientID, "db-key": envDBKey,
		"api-key-file": envAPIKey + fileSuffix, "client-secret-file": envAPIKey + fileSuffix,
		"client-id-file": envClientID + fileSuffix, "db-key-file": envDBKey + fileSuffix,
	}); err != nil {
		return c, cfgErr("%v", err)
	}
	fromEnv := func(name, fallback string) string {
		if v, ok := lookup(envPrefix + name); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
		return fallback
	}
	var rate, runtime, location, concurrency, maxRequests, seedPage, guildMembers string
	fs := flag.NewFlagSet("radar-armory", flag.ContinueOnError)
	var help bytes.Buffer
	fs.SetOutput(&help)
	fs.StringVar(&c.baseURL, "base-url", fromEnv("BASE_URL", ""), "https API base URL, default https://REGION.api.blizzard.com [RADAR_ARMORY_BASE_URL]")
	fs.StringVar(&c.region, "region", fromEnv("REGION", ""), "Battle.net region, default us [RADAR_ARMORY_REGION]")
	fs.StringVar(&c.namespace, "namespace", fromEnv("NAMESPACE", ""), "PROVISIONAL API namespace, default profile-classic-REGION [RADAR_ARMORY_NAMESPACE]")
	fs.StringVar(&c.rosterNamespace, "roster-namespace", fromEnv("ROSTER_NAMESPACE", ""), "PROVISIONAL namespace of the guild roster call, default: namespace [RADAR_ARMORY_ROSTER_NAMESPACE]")
	fs.StringVar(&c.locale, "locale", fromEnv("LOCALE", ""), "locale query value, default en_US [RADAR_ARMORY_LOCALE]")
	fs.StringVar(&c.tokenURL, "token-url", fromEnv("TOKEN_URL", ""), "OAuth2 token endpoint, default https://oauth.battle.net/token [RADAR_ARMORY_TOKEN_URL]")
	fs.StringVar(&c.summaryPath, "summary-path", fromEnv("SUMMARY_PATH", ""), "PROVISIONAL character summary path with {realmSlug} and {characterName} [RADAR_ARMORY_SUMMARY_PATH]")
	fs.StringVar(&c.equipmentPath, "equipment-path", fromEnv("EQUIPMENT_PATH", ""), "PROVISIONAL equipment path [RADAR_ARMORY_EQUIPMENT_PATH]")
	fs.StringVar(&c.specsPath, "specs-path", fromEnv("SPECS_PATH", ""), "PROVISIONAL specializations path [RADAR_ARMORY_SPECS_PATH]")
	fs.StringVar(&c.statsPath, "stats-path", fromEnv("STATS_PATH", ""), "PROVISIONAL statistics path [RADAR_ARMORY_STATS_PATH]")
	fs.StringVar(&c.pvpPath, "pvp-path", fromEnv("PVP_PATH", ""), "PROVISIONAL pvp-summary path [RADAR_ARMORY_PVP_PATH]")
	fs.StringVar(&c.rosterPath, "roster-path", fromEnv("ROSTER_PATH", ""), "PROVISIONAL guild roster path with {realmSlug} and {guildSlug} [RADAR_ARMORY_ROSTER_PATH]")
	fs.StringVar(&c.defaultRealm, "default-realm", fromEnv("DEFAULT_REALM", ""), "realm (slug form) for seeds without one; unset: such seeds are skipped [RADAR_ARMORY_DEFAULT_REALM]")
	fs.StringVar(&seedPage, "seed-page-size", fromEnv("SEED_PAGE_SIZE", "200"), "seeds per request to radar-db, 1 to 500 [RADAR_ARMORY_SEED_PAGE_SIZE]")
	fs.StringVar(&guildMembers, "max-guild-members", fromEnv("MAX_GUILD_MEMBERS", "1000"), "roster members crawled per guild [RADAR_ARMORY_MAX_GUILD_MEMBERS]")
	fs.StringVar(&rate, "rate", fromEnv("RATE", "5"), "armory requests per second [RADAR_ARMORY_RATE]")
	fs.StringVar(&concurrency, "concurrency", fromEnv("CONCURRENCY", "3"), "parallel character fetches [RADAR_ARMORY_CONCURRENCY]")
	fs.StringVar(&maxRequests, "max-requests", fromEnv("MAX_REQUESTS", "0"), "budget of character and roster fetches per run, 0 = unlimited [RADAR_ARMORY_MAX_REQUESTS]")
	fs.StringVar(&runtime, "max-runtime", fromEnv("MAX_RUNTIME", defaultRuntime.String()), "wall-clock budget per run [RADAR_ARMORY_MAX_RUNTIME]")
	fs.StringVar(&c.dbURL, "db-url", fromEnv("DB_URL", defaultDBURL), "radar-db internal listener URL [RADAR_ARMORY_DB_URL]")
	fs.StringVar(&location, "location", fromEnv("LOCATION", "Local"), "IANA time zone for the same-day resume window [RADAR_ARMORY_LOCATION]")
	fs.BoolVar(&c.showVersion, "version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return c, cfgErr("usage of radar-armory (keys come from %s, %s and %s or their %s variants, never flags):\n%s",
				envClientID, envAPIKey, envDBKey, fileSuffix, help.String())
		}
		return c, cfgErr("bad command line: %v", err)
	}
	if c.showVersion {
		return c, nil
	}
	if fs.NArg() > 0 {
		return c, cfgErr("unexpected command-line argument")
	}
	if err := c.parseNumbers(rate, concurrency, maxRequests, runtime, location, seedPage, guildMembers); err != nil {
		return c, err
	}
	// The armory URLs, region, namespace and paths are validated by the client when it is built (https is
	// enforced there); only the radar-db URL is checked here.
	if err := checkDBURL(c.dbURL); err != nil {
		return c, cfgErr("%v", err)
	}
	var err error
	for _, k := range []struct {
		dst  *secretVal
		name string
	}{{&c.clientID, envClientID}, {&c.apiKey, envAPIKey}, {&c.dbKey, envDBKey}} {
		if *k.dst, err = readKey(lookup, k.name); err != nil {
			return c, err
		}
		if w := shadowedFile(lookup, k.name); w != "" {
			c.warnings = append(c.warnings, w)
		}
	}
	if err := checkCredentials(c.clientID.Reveal(), c.apiKey.Reveal(), c.dbKey.Reveal()); err != nil {
		return c, err
	}
	return c, nil
}

// placeholderPrefix marks example values from radar.env.example (same convention as radar-db's keys module).
const placeholderPrefix = "CHANGE_ME"

// checkCredentials refuses credentials that are set but cannot work, so a run fails at once with a clear
// message instead of as an armory 401 "Bad credentials". Messages name the setting, never the value.
func checkCredentials(clientID, secret, dbKey string) error {
	for _, k := range []struct{ name, v string }{{envClientID, clientID}, {envAPIKey, secret}, {envDBKey, dbKey}} {
		switch {
		case strings.HasPrefix(k.v, placeholderPrefix):
			return cfgErr("%s is the CHANGE_ME placeholder from radar.env.example, not a real value: set it (or %s%s)", k.name, k.name, fileSuffix)
		case !printableNoSpace(k.v):
			return cfgErr("%s contains spaces, line breaks or control characters inside the value: retype it", k.name)
		}
	}
	switch {
	case clientID == secret:
		return cfgErr("%s and %s hold the same value: the id goes in the client-id file, the client secret in the api-key file", envClientID, envAPIKey)
	case strings.Contains(clientID, ":"):
		return cfgErr("%s contains ':', which no Battle.net client id does (it would break the HTTP Basic login)", envClientID)
	case looksLikeAccessToken(secret):
		return cfgErr("%s looks like an OAuth access token (region prefix + 32 characters), not the client secret: "+
			"put the client secret from the developer portal there; radar-armory requests its own token on every run", envAPIKey)
	case looksLikeAccessToken(clientID):
		return cfgErr("%s looks like an OAuth access token, not the client id from the developer portal", envClientID)
	}
	return nil
}

// looksLikeAccessToken matches the shape of a Battle.net access token ("US" + 32 letters/digits, likewise
// EU, KR, TW, CN, APAC). Client ids and secrets are 32 characters with no region prefix.
func looksLikeAccessToken(v string) bool {
	for _, p := range []string{"US", "EU", "KR", "TW", "CN", "APAC"} {
		rest, ok := strings.CutPrefix(v, p)
		if ok && len(rest) == 32 && isAlnum(rest) {
			return true
		}
	}
	return false
}

func isAlnum(s string) bool {
	for i := 0; i < len(s); i++ {
		b := s[i]
		if !('a' <= b && b <= 'z' || 'A' <= b && b <= 'Z' || '0' <= b && b <= '9') {
			return false
		}
	}
	return true
}

func printableNoSpace(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] <= ' ' || s[i] >= 0x7f {
			return false
		}
	}
	return true
}

func (c *config) parseNumbers(rate, concurrency, maxRequests, runtime, location, seedPage, guildMembers string) error {
	var err error
	if c.rate, err = strconv.ParseFloat(rate, 64); err != nil || c.rate <= 0 || c.rate > 1000 {
		return cfgErr("rate must be a number of requests per second above 0 and at most 1000")
	}
	if c.concurrency, err = strconv.Atoi(concurrency); err != nil || c.concurrency < 1 || c.concurrency > 64 {
		return cfgErr("concurrency must be a whole number from 1 to 64")
	}
	if c.maxRequests, err = strconv.Atoi(maxRequests); err != nil || c.maxRequests < 0 {
		return cfgErr("max-requests must be a whole number, 0 or more")
	}
	if c.seedPageSize, err = strconv.Atoi(seedPage); err != nil || c.seedPageSize < 1 || c.seedPageSize > domain.ArmorySeedLimitMax {
		return cfgErr("seed-page-size must be a whole number from 1 to %d", domain.ArmorySeedLimitMax)
	}
	if c.maxGuildMembers, err = strconv.Atoi(guildMembers); err != nil || c.maxGuildMembers < 1 || c.maxGuildMembers > 10000 {
		return cfgErr("max-guild-members must be a whole number from 1 to 10000")
	}
	if c.maxRuntime, err = time.ParseDuration(runtime); err != nil || c.maxRuntime <= 0 {
		return cfgErr("max-runtime must be a positive duration such as 6h")
	}
	if location == "" || location == "Local" {
		c.location = time.Local
	} else if c.location, err = time.LoadLocation(location); err != nil {
		return cfgErr("location is not a known IANA time zone")
	}
	return nil
}

// checkDBURL accepts https anywhere and http only for loopback (the internal listener); no credentials,
// query or fragment. The error never repeats the value.
func checkDBURL(raw string) error {
	u, err := url.Parse(raw)
	switch {
	case err != nil || u.Hostname() == "":
		return errors.New("the radar-db URL is not a valid URL with a host")
	case u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(raw, "?#"):
		return errors.New("the radar-db URL must not contain credentials, a query or a fragment")
	case u.Scheme == "https":
		return nil
	case u.Scheme == "http" && isLoopbackHost(u.Hostname()):
		return nil
	}
	return errors.New("the radar-db URL must be https, or http only for a loopback host")
}

func isLoopbackHost(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// readKey returns the secret named name: the plain variable wins, else the file named by name_FILE.
// Neither the value nor the file's content ever appears in an error.
func readKey(lookup func(string) (string, bool), name string) (secretVal, error) {
	if v, ok := lookup(name); ok && strings.TrimSpace(v) != "" {
		return secretVal{v: strings.TrimSpace(v)}, nil
	}
	if path, ok := lookup(name + fileSuffix); ok && strings.TrimSpace(path) != "" {
		data, err := readSmallFile(strings.TrimSpace(path))
		if err != nil {
			return secretVal{}, cfgErr("%s%s: cannot read the key file", name, fileSuffix)
		}
		if v := strings.TrimSpace(string(data)); v != "" {
			return secretVal{v: v}, nil
		}
		return secretVal{}, cfgErr("%s%s: the key file is empty", name, fileSuffix)
	}
	return secretVal{}, cfgErr("%s is not set (set it, or %s%s to a file holding it)", name, name, fileSuffix)
}

// shadowedFile reports when the plain variable name is set and so overrides name_FILE. That combination
// silently ignores a freshly typed secret file (a stale or placeholder value in radar.env wins), which shows
// up only as the armory answering 401 "Bad credentials". The note names the settings, never a value.
func shadowedFile(lookup func(string) (string, bool), name string) string {
	v, ok := lookup(name)
	path, fok := lookup(name + fileSuffix)
	if !ok || strings.TrimSpace(v) == "" || !fok || strings.TrimSpace(path) == "" {
		return ""
	}
	return fmt.Sprintf("%s is set directly and overrides %s%s; the key file is ignored (remove the %s= line from radar.env to use the file)",
		name, name, fileSuffix, name)
}

func readSmallFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxKeyFileSize+1))
	if err == nil && len(data) > maxKeyFileSize {
		err = errors.New("too large")
	}
	return data, err
}

// String, GoString and Format keep the keys out of any print of the whole config (fmt cannot call
// secretVal's own methods on unexported fields).
func (c config) String() string {
	return fmt.Sprintf("config{base:%q region:%q db:%q rate:%v concurrency:%d maxRequests:%d maxRuntime:%v keys:<redacted>}",
		c.baseURL, c.region, c.dbURL, c.rate, c.concurrency, c.maxRequests, c.maxRuntime)
}
func (c config) GoString() string           { return c.String() }
func (c config) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, c.String()) }
