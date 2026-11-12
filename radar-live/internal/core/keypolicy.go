package core

import (
	"fmt"
	"net"
	"strings"
)

// SettingRequireKeys decides whether the host refuses to start with open (key-less) auth. "true"
// always refuses, "false" never does, and empty or "auto" refuses unless every listener is bound
// to loopback.
const SettingRequireKeys = "require-keys"

// addrIsLoopback reports whether a listen address is bound to a loopback interface only. The empty
// host and the wildcard addresses are not loopback.
func addrIsLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// checkKeyPolicy enforces the startup key policy: when keys are required (see SettingRequireKeys),
// every role the binary uses must have at least one authenticator. addrs are the enabled listeners.
func checkKeyPolicy(setting string, addrs []string, auth *Auth) error {
	var required bool
	switch strings.ToLower(strings.TrimSpace(setting)) {
	case "true":
		required = true
	case "false":
		required = false
	case "", "auto":
		for _, a := range addrs {
			if !addrIsLoopback(a) {
				required = true
			}
		}
	default:
		return fmt.Errorf("invalid %s %q (use true, false or auto)", envHint(SettingRequireKeys), setting)
	}
	if !required {
		return nil
	}
	var open []string
	for _, role := range auth.RolesInUse() {
		if !auth.HasAuthenticator(role) {
			open = append(open, string(role))
		}
	}
	if len(open) > 0 {
		return fmt.Errorf("refusing to start with open authentication for role(s) %s: set keys (RADAR_VIEW_KEYS / RADAR_INGEST_KEYS), "+
			"bind to loopback, or set %s=false to accept an open API", strings.Join(open, ", "), envHint(SettingRequireKeys))
	}
	return nil
}

func envHint(key string) string { return "RADAR_" + strings.ToUpper(strings.ReplaceAll(key, "-", "_")) }

// enabledAddrs lists the addresses of the public listener and every enabled extra class.
func (h *Host) enabledAddrs() []string {
	addrs := []string{h.Config.String(SettingListen)}
	for _, c := range h.classes {
		if a := h.Config.String(c.key); a != "" {
			addrs = append(addrs, a)
		}
	}
	return addrs
}

func (h *Host) checkKeys() error {
	return checkKeyPolicy(h.Config.String(SettingRequireKeys), h.enabledAddrs(), h.Auth)
}
