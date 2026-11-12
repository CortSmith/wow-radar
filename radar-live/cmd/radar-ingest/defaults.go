package main

import (
	"os"
	"strings"
)

// ingestDefaults are the radar-ingest defaults that differ from the module defaults. They are
// applied below flags, the environment and the env file, and above the module defaults.
var ingestDefaults = map[string]string{
	"listen":         "127.0.0.1:8788", // LAN-private by default
	"contactx-flush": "2s",             // near-real-time pings
	"bountyx-flush":  "2s",
	// A stream is one long request; the 8 MiB body cap of radar-db would cut it after minutes.
	// streamapi enforces its own per-stream cap (stream-max-bytes).
	"max-body": "1099511627776",
}

const (
	envPrefix  = "RADAR_"
	envFileKey = "env-file"
)

// withDefaults wraps lookup so unset settings fall back to ingestDefaults. A value present in the
// environment or in the env file (named by --env-file / RADAR_ENV_FILE) always wins.
func withDefaults(args []string, lookup func(string) (string, bool)) func(string) (string, bool) {
	fileVars := envFileKeys(envFilePath(args, lookup))
	return func(name string) (string, bool) {
		if v, ok := lookup(name); ok {
			return v, true
		}
		if fileVars[name] {
			return "", false
		}
		for key, v := range ingestDefaults {
			if envName(key) == name {
				return v, true
			}
		}
		return "", false
	}
}

func envName(key string) string {
	return envPrefix + strings.ToUpper(strings.ReplaceAll(key, "-", "_"))
}

// envFilePath finds the env file from "--env-file=x", "--env-file x" or RADAR_ENV_FILE.
func envFilePath(args []string, lookup func(string) (string, bool)) string {
	for i, a := range args {
		a = strings.TrimLeft(a, "-")
		if v, ok := strings.CutPrefix(a, envFileKey+"="); ok {
			return v
		}
		if a == envFileKey && i+1 < len(args) {
			return args[i+1]
		}
	}
	v, _ := lookup(envName(envFileKey))
	return v
}

// envFileKeys returns the variable names assigned in the env file (empty when there is none).
func envFileKeys(path string) map[string]bool {
	keys := map[string]bool{}
	if path == "" {
		return keys
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return keys // core reports the unreadable file when it loads it
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimPrefix(strings.TrimSpace(line), "export ")
		if name, _, found := strings.Cut(line, "="); found {
			keys[strings.TrimSpace(name)] = true
		}
	}
	return keys
}
