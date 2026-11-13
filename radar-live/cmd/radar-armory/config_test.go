package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	fakeAPIKey   = "test-armory-secret-0000000000000000"
	fakeClientID = "test-armory-client-5555555555555555"
	fakeDBKey    = "test-db-key-1111111111111111"
)

func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func goodEnv() map[string]string {
	return map[string]string{
		"WOW_ARMORY_API_KEY":    fakeAPIKey,
		"WOW_ARMORY_CLIENT_ID":  fakeClientID,
		"RADAR_ARMORY_DB_KEY":   fakeDBKey,
		"RADAR_ARMORY_BASE_URL": "https://armory.example.test",
	}
}

func TestDefaults(t *testing.T) {
	c, err := parseConfig(nil, env(goodEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if c.dbURL != "http://127.0.0.1:8789" || c.maxRuntime != 6*time.Hour || c.location != time.Local || c.seedPageSize != 200 || c.maxGuildMembers != 1000 ||
		c.region != "" || c.namespace != "" || c.tokenURL != "" || c.defaultRealm != "" {
		t.Fatalf("defaults: %+v", c)
	}
	if c.apiKey.Reveal() != fakeAPIKey || c.dbKey.Reveal() != fakeDBKey || c.clientID.Reveal() != fakeClientID {
		t.Fatal("keys not read")
	}
}

func TestEnvAndFlagPrecedence(t *testing.T) {
	e := goodEnv()
	e["RADAR_ARMORY_RATE"] = "2.5"
	e["RADAR_ARMORY_CONCURRENCY"] = "7"
	e["RADAR_ARMORY_MAX_REQUESTS"] = "99"
	e["RADAR_ARMORY_MAX_RUNTIME"] = "30m"
	e["RADAR_ARMORY_LOCATION"] = "UTC"
	e["RADAR_ARMORY_REGION"] = "eu"
	e["RADAR_ARMORY_NAMESPACE"] = "profile-x-eu"
	e["RADAR_ARMORY_ROSTER_NAMESPACE"] = "profile-y-eu"
	e["RADAR_ARMORY_LOCALE"] = "de_DE"
	e["RADAR_ARMORY_TOKEN_URL"] = "https://oauth.example.test/token"
	e["RADAR_ARMORY_SUMMARY_PATH"] = "/s/{realmSlug}/{characterName}"
	e["RADAR_ARMORY_EQUIPMENT_PATH"] = "/e/{realmSlug}/{characterName}"
	e["RADAR_ARMORY_SPECS_PATH"] = "/sp/{realmSlug}/{characterName}"
	e["RADAR_ARMORY_STATS_PATH"] = "/st/{realmSlug}/{characterName}"
	e["RADAR_ARMORY_PVP_PATH"] = "/p/{realmSlug}/{characterName}"
	e["RADAR_ARMORY_ROSTER_PATH"] = "/r/{realmSlug}/{guildSlug}"
	e["RADAR_ARMORY_DEFAULT_REALM"] = "area-52"
	e["RADAR_ARMORY_SEED_PAGE_SIZE"] = "50"
	e["RADAR_ARMORY_MAX_GUILD_MEMBERS"] = "300"
	c, err := parseConfig([]string{"--concurrency=3", "--db-url=https://db.example.test:8789"}, env(e))
	if err != nil {
		t.Fatal(err)
	}
	if c.rate != 2.5 || c.concurrency != 3 || c.maxRequests != 99 || c.maxRuntime != 30*time.Minute ||
		c.location != time.UTC || c.region != "eu" || c.namespace != "profile-x-eu" || c.rosterNamespace != "profile-y-eu" ||
		c.locale != "de_DE" || c.tokenURL != "https://oauth.example.test/token" || c.summaryPath != "/s/{realmSlug}/{characterName}" ||
		c.equipmentPath != "/e/{realmSlug}/{characterName}" || c.specsPath != "/sp/{realmSlug}/{characterName}" ||
		c.statsPath != "/st/{realmSlug}/{characterName}" || c.pvpPath != "/p/{realmSlug}/{characterName}" ||
		c.rosterPath != "/r/{realmSlug}/{guildSlug}" || c.defaultRealm != "area-52" || c.seedPageSize != 50 || c.maxGuildMembers != 300 ||
		c.dbURL != "https://db.example.test:8789" {
		t.Fatalf("%+v", c)
	}
}

func TestKeyFileVariantAndPrecedence(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "key")
	if err := os.WriteFile(f, []byte(" file-key-2222 \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cf := filepath.Join(dir, "clientid")
	if err := os.WriteFile(cf, []byte("client-file-id-4444\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	g := filepath.Join(dir, "dbkey")
	if err := os.WriteFile(g, []byte("db-file-key-3333\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := goodEnv()
	delete(e, "WOW_ARMORY_API_KEY")
	delete(e, "WOW_ARMORY_CLIENT_ID")
	delete(e, "RADAR_ARMORY_DB_KEY")
	e["WOW_ARMORY_API_KEY_FILE"] = f
	e["WOW_ARMORY_CLIENT_ID_FILE"] = cf
	e["RADAR_ARMORY_DB_KEY_FILE"] = g
	c, err := parseConfig(nil, env(e))
	if err != nil {
		t.Fatal(err)
	}
	if c.apiKey.Reveal() != "file-key-2222" || c.dbKey.Reveal() != "db-file-key-3333" || c.clientID.Reveal() != "client-file-id-4444" {
		t.Fatal("file keys not read or not trimmed")
	}
	e["WOW_ARMORY_API_KEY"] = fakeAPIKey // plain env wins
	c, err = parseConfig(nil, env(e))
	if err != nil || c.apiKey.Reveal() != fakeAPIKey {
		t.Fatalf("plain env must win: %v", err)
	}
	// ...but it must say so: a stale radar.env value silently shadowing a freshly typed secret file
	// is exactly what produced the armory's 401 "Bad credentials" on the first real run.
	if len(c.warnings) != 1 || !strings.Contains(c.warnings[0], "WOW_ARMORY_API_KEY is set directly and overrides WOW_ARMORY_API_KEY_FILE") {
		t.Fatalf("want one override warning, got %q", c.warnings)
	}
	for _, w := range c.warnings {
		if strings.Contains(w, fakeAPIKey) || strings.Contains(w, "file-key-2222") || strings.Contains(w, f) {
			t.Fatal("override warning must not contain a key or a path")
		}
	}
}

func TestInvalidConfigs(t *testing.T) {
	cases := map[string]struct {
		mut  func(map[string]string)
		args []string
	}{
		"missing api key":       {mut: func(e map[string]string) { delete(e, "WOW_ARMORY_API_KEY") }},
		"empty api key":         {mut: func(e map[string]string) { e["WOW_ARMORY_API_KEY"] = "  " }},
		"missing db key":        {mut: func(e map[string]string) { delete(e, "RADAR_ARMORY_DB_KEY") }},
		"missing client id":     {mut: func(e map[string]string) { delete(e, "WOW_ARMORY_CLIENT_ID") }},
		"empty client id":       {mut: func(e map[string]string) { e["WOW_ARMORY_CLIENT_ID"] = " " }},
		"placeholder secret":    {mut: func(e map[string]string) { e["WOW_ARMORY_API_KEY"] = "CHANGE_ME_CLIENT_SECRET" }},
		"placeholder client id": {mut: func(e map[string]string) { e["WOW_ARMORY_CLIENT_ID"] = "CHANGE_ME" }},
		"placeholder db key":    {mut: func(e map[string]string) { e["RADAR_ARMORY_DB_KEY"] = "CHANGE_ME_INGEST_KEY" }},
		"id equals secret":      {mut: func(e map[string]string) { e["WOW_ARMORY_CLIENT_ID"] = fakeAPIKey }},
		"secret inner space":    {mut: func(e map[string]string) { e["WOW_ARMORY_API_KEY"] = "test-armory secret-000000000000000" }},
		"client id colon":       {mut: func(e map[string]string) { e["WOW_ARMORY_CLIENT_ID"] = "test-armory:client-55555555555555" }},
		"secret is a token":     {mut: func(e map[string]string) { e["WOW_ARMORY_API_KEY"] = "US" + strings.Repeat("aB3", 10) + "x9" }},
		"secret is an eu token": {mut: func(e map[string]string) { e["WOW_ARMORY_API_KEY"] = "EU" + strings.Repeat("Zz09", 8) }},
		"bad seed page":         {mut: func(e map[string]string) { e["RADAR_ARMORY_SEED_PAGE_SIZE"] = "0" }},
		"huge seed page":        {mut: func(e map[string]string) { e["RADAR_ARMORY_SEED_PAGE_SIZE"] = "501" }},
		"bad guild cap":         {mut: func(e map[string]string) { e["RADAR_ARMORY_MAX_GUILD_MEMBERS"] = "x" }},
		"zero guild cap":        {mut: func(e map[string]string) { e["RADAR_ARMORY_MAX_GUILD_MEMBERS"] = "0" }},
		"remote http db":        {mut: func(e map[string]string) { e["RADAR_ARMORY_DB_URL"] = "http://db.example.test:8789" }},
		"db url query":          {mut: func(e map[string]string) { e["RADAR_ARMORY_DB_URL"] = "http://127.0.0.1:8789/?x=1" }},
		"db url userinfo":       {mut: func(e map[string]string) { e["RADAR_ARMORY_DB_URL"] = "https://u:p@127.0.0.1:8789" }},
		"bad rate":              {mut: func(e map[string]string) { e["RADAR_ARMORY_RATE"] = "fast" }},
		"negative rate":         {mut: func(e map[string]string) { e["RADAR_ARMORY_RATE"] = "-1" }},
		"bad concurrency":       {mut: func(e map[string]string) { e["RADAR_ARMORY_CONCURRENCY"] = "0" }},
		"neg max requests":      {mut: func(e map[string]string) { e["RADAR_ARMORY_MAX_REQUESTS"] = "-5" }},
		"bad runtime":           {mut: func(e map[string]string) { e["RADAR_ARMORY_MAX_RUNTIME"] = "soon" }},
		"zero runtime":          {mut: func(e map[string]string) { e["RADAR_ARMORY_MAX_RUNTIME"] = "0s" }},
		"bad location":          {mut: func(e map[string]string) { e["RADAR_ARMORY_LOCATION"] = "Mars/Base" }},
		"unreadable keyfile": {mut: func(e map[string]string) {
			delete(e, "WOW_ARMORY_API_KEY")
			e["WOW_ARMORY_API_KEY_FILE"] = filepath.Join(os.TempDir(), "does-not-exist-radar-armory")
		}},
		"api key flag":        {args: []string{"--api-key=" + fakeAPIKey}},
		"client secret flag":  {args: []string{"--client-secret=" + fakeAPIKey}},
		"client id flag":      {args: []string{"--client-id", fakeClientID}},
		"client id file flag": {args: []string{"--client-id-file=/x"}},
		"db key flag":         {args: []string{"--db-key", fakeDBKey}},
		"unknown flag":        {args: []string{"--nope"}},
		"extra arg":           {args: []string{"stray"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			e := goodEnv()
			if c.mut != nil {
				c.mut(e)
			}
			_, err := parseConfig(c.args, env(e))
			if err == nil {
				t.Fatal("want error")
			}
			if !isConfigError(err) {
				t.Fatalf("not a config error: %v", err)
			}
			for _, secret := range []string{fakeAPIKey, fakeClientID, fakeDBKey, "u:p"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error leaks a secret: %v", err)
				}
			}
		})
	}
}

func TestLocalhostDBURLAllowed(t *testing.T) {
	for _, u := range []string{"http://127.0.0.1:8789", "http://localhost:8789", "http://[::1]:8789", "https://db.example.test"} {
		e := goodEnv()
		e["RADAR_ARMORY_DB_URL"] = u
		if _, err := parseConfig(nil, env(e)); err != nil {
			t.Errorf("%s: %v", u, err)
		}
	}
}

func TestConfigNeverPrintsSecrets(t *testing.T) {
	c, err := parseConfig(nil, env(goodEnv()))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{fmt.Sprintf("%v", c), fmt.Sprintf("%+v", c), fmt.Sprintf("%#v", c), fmt.Sprint(c.apiKey, c.dbKey, c.clientID)} {
		if strings.Contains(s, fakeAPIKey) || strings.Contains(s, fakeDBKey) || strings.Contains(s, fakeClientID) {
			t.Fatalf("secret printed: %s", s)
		}
	}
}

func TestVersionFlag(t *testing.T) {
	c, err := parseConfig([]string{"--version"}, env(nil))
	if err != nil || !c.showVersion {
		t.Fatalf("--version needs no config: %v", err)
	}
}
