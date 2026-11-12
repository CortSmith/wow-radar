package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

// TestEveryEnvVarIsDocumentedInTheExample: each setting of this binary has its RADAR_* variable in
// deploy/radar.env.example (set or as a commented example), so nothing is configurable in secret.
func TestEveryEnvVarIsDocumentedInTheExample(t *testing.T) {
	data, err := os.ReadFile("../../deploy/radar.env.example")
	if err != nil {
		t.Fatal(err)
	}
	host, err := buildHost(nil, noEnv, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range host.Config.EnvNames() {
		if !strings.Contains(string(data), name) {
			t.Errorf("%s is not documented in deploy/radar.env.example", name)
		}
	}
}
