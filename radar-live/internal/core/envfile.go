package core

import (
	"fmt"
	"os"
	"strings"
)

// readEnvFile loads the KEY=value file named by --env-file or the environment; no file means no variables.
func (c *Config) readEnvFile(flags map[string]string, lookupEnv func(string) (string, bool)) (map[string]string, error) {
	path, ok := flags[envFileKey]
	if !ok {
		path, ok = lookupEnv(c.EnvName(envFileKey))
	}
	if !ok || path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read env file: %w", err)
	}
	vars, err := parseEnvFile(string(data))
	if err != nil {
		return nil, fmt.Errorf("config: env file %q: %w", path, err)
	}
	return vars, nil
}

// parseEnvFile understands blank lines, "# comments", optional "export " and single or double quoted values.
func parseEnvFile(text string) (map[string]string, error) {
	vars := map[string]string{}
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("line %d: expected KEY=value", i+1)
		}
		vars[strings.TrimSpace(key)] = unquote(strings.TrimSpace(value))
	}
	return vars, nil
}

// unquote removes one pair of matching surrounding quotes.
func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}
