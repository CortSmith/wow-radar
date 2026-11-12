package static

import (
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// SecurityContactKey is the config key (RADAR_SECURITY_CONTACT) holding the vulnerability
	// report contact published at /.well-known/security.txt. The repository never carries one.
	SecurityContactKey = "security-contact"
	securityTxtPath    = "/.well-known/security.txt"
	securityTxtLife    = 365 * 24 * time.Hour
)

// serveSecurityTxt answers with an RFC 9116 file built from the configured contact, or 404 when
// the contact is unset or is not a mailto:/https: URI (control characters would inject fields).
func (m *Module) serveSecurityTxt(w http.ResponseWriter, r *http.Request) {
	contact, ok := normaliseContact(m.host.Config.String(SecurityContactKey))
	if !ok {
		notFound(w, r)
		return
	}
	body := "Contact: " + contact + "\nExpires: " + time.Now().UTC().Add(securityTxtLife).Format(time.RFC3339) + "\n"
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", htmlCache)
	_, _ = w.Write([]byte(body))
}

// normaliseContact turns an address into a mailto: URI and accepts only mailto: and https: URIs.
func normaliseContact(raw string) (string, bool) {
	c := strings.TrimSpace(raw)
	if c == "" || strings.ContainsFunc(c, func(r rune) bool { return r < 0x21 || r == 0x7f }) {
		return "", false
	}
	if !strings.Contains(c, ":") && strings.Count(c, "@") == 1 {
		c = "mailto:" + c
	}
	u, err := url.Parse(c)
	if err != nil || (u.Scheme != "mailto" && u.Scheme != "https") {
		return "", false
	}
	return c, true
}
