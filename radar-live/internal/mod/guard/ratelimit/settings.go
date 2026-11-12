package ratelimit

import (
	"fmt"

	"radarlive/internal/core"
)

// Setting keys (flags; the environment names are RADAR_RATELIMIT_ENABLED and so on).
const (
	SettingEnabled        = "ratelimit-enabled"
	SettingTrustedProxies = "ratelimit-trusted-proxies"
	SettingMaxEntries     = "ratelimit-max-entries"
	SettingTTL            = "ratelimit-ttl"
	SettingHeaders        = "ratelimit-headers"
)

// classSettings names the rate and burst settings of every class: "ratelimit-<class>-rate" is the
// refill in requests per minute per client address, "-burst" the bucket size. The write and ingest
// classes also have a per-API-key budget ("-key-rate", "-key-burst").
func classKey(c class, suffix string) string { return "ratelimit-" + c.String() + "-" + suffix }

func keyedClass(c class) bool { return c == classWrite || c == classIngest }

// declare adds the module's settings to the host configuration.
func declare(cfg *core.Config) error {
	settings := []core.Setting{
		{Key: SettingEnabled, Kind: core.KindBool, Default: "true", Usage: "enable rate limiting (kill switch)"},
		{Key: SettingTrustedProxies, Kind: core.KindString, Usage: "comma separated CIDRs/addresses of reverse proxies whose CF-Connecting-IP / X-Forwarded-For is believed (default none)"},
		{Key: SettingMaxEntries, Kind: core.KindInt, Default: fmt.Sprint(defaultMaxEntries), Usage: "max tracked client/key identities; beyond it new ones share a global bucket"},
		{Key: SettingTTL, Kind: core.KindDuration, Default: defaultTTL.String(), Usage: "forget an identity idle for this long"},
		{Key: SettingHeaders, Kind: core.KindBool, Default: "false", Usage: "send X-RateLimit-* headers"},
	}
	for c := class(0); c < numClasses; c++ {
		d := defaultRates[c]
		who := "per client address"
		settings = append(settings,
			core.Setting{Key: classKey(c, "rate"), Kind: core.KindInt, Default: fmt.Sprint(int(d.ipRate)), Usage: c.String() + " requests per minute " + who},
			core.Setting{Key: classKey(c, "burst"), Kind: core.KindInt, Default: fmt.Sprint(int(d.ipBurst)), Usage: c.String() + " burst size " + who})
		if keyedClass(c) {
			settings = append(settings,
				core.Setting{Key: classKey(c, "key-rate"), Kind: core.KindInt, Default: fmt.Sprint(int(d.keyRate)), Usage: c.String() + " requests per minute per API key"},
				core.Setting{Key: classKey(c, "key-burst"), Kind: core.KindInt, Default: fmt.Sprint(int(d.keyBurst)), Usage: c.String() + " burst size per API key"})
		}
	}
	for _, s := range settings {
		if err := cfg.Declare(s); err != nil {
			return err
		}
	}
	return nil
}

// load reads and validates the settings; a nonsensical value stops the start instead of running unprotected.
func load(cfg *core.Config) (limiterConfig, error) {
	out := limiterConfig{
		enabled: cfg.Bool(SettingEnabled), headers: cfg.Bool(SettingHeaders),
		maxEntries: cfg.Int(SettingMaxEntries), ttl: cfg.Duration(SettingTTL),
	}
	if !out.enabled {
		return out, nil
	}
	if out.maxEntries < 1 {
		return out, fmt.Errorf("ratelimit: %s must be at least 1, got %d", SettingMaxEntries, out.maxEntries)
	}
	if out.ttl <= 0 {
		return out, fmt.Errorf("ratelimit: %s must be positive, got %v", SettingTTL, out.ttl)
	}
	trusted, err := parseTrusted(cfg.String(SettingTrustedProxies))
	if err != nil {
		return out, fmt.Errorf("ratelimit: %s: %w", SettingTrustedProxies, err)
	}
	out.trusted = trusted
	for c := class(0); c < numClasses; c++ {
		var err error
		if out.ip[c], err = loadBudget(cfg, classKey(c, "rate"), classKey(c, "burst")); err != nil {
			return out, err
		}
		if keyedClass(c) {
			if out.key[c], err = loadBudget(cfg, classKey(c, "key-rate"), classKey(c, "key-burst")); err != nil {
				return out, err
			}
		}
	}
	return out, nil
}

func loadBudget(cfg *core.Config, rateKey, burstKey string) (budget, error) {
	rate, burst := cfg.Int(rateKey), cfg.Int(burstKey)
	if rate < 1 || burst < 1 {
		return budget{}, fmt.Errorf("ratelimit: %s and %s must be at least 1, got %d and %d", rateKey, burstKey, rate, burst)
	}
	return perMinute(float64(rate), float64(burst)), nil
}
