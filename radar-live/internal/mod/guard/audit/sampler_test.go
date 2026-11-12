package audit

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"radarlive/internal/core"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }
func newClock() *fakeClock                   { return &fakeClock{t: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)} }

func collect(c *fakeClock, win time.Duration, maxKeys, perSec int) (*sampler, *[]record) {
	var out []record
	return newSampler(c.now, win, maxKeys, perSec, func(r record) { out = append(out, r) }), &out
}

func rec(event, ip string) record {
	return sanitize(core.AuditEvent{Event: event, IP: ip, Class: "public", Reason: "bad_key"})
}

func TestSamplerOneLinePerKeyPerWindowWithCount(t *testing.T) {
	c := newClock()
	s, out := collect(c, time.Minute, 100, 1000)
	for i := 0; i < 50; i++ {
		s.add(rec("rate_limited", "192.0.2.1"))
	}
	s.add(rec("rate_limited", "192.0.2.2"))
	if len(*out) != 2 || (*out)[0].count != 1 {
		t.Fatalf("first window lines = %+v", *out)
	}
	c.advance(30 * time.Second)
	s.flush()
	if len(*out) != 2 {
		t.Fatal("summary written before the window ended")
	}
	c.advance(31 * time.Second)
	s.flush()
	if len(*out) != 3 || (*out)[2].count != 49 || (*out)[2].ip != "192.0.2.1" {
		t.Fatalf("summary = %+v", *out)
	}
	s.add(rec("rate_limited", "192.0.2.1"))
	if len(*out) != 4 || (*out)[3].count != 1 {
		t.Fatalf("after window: %+v", *out)
	}
}

func TestSamplerAddAfterWindowSummarisesOldBurstFirst(t *testing.T) {
	c := newClock()
	s, out := collect(c, time.Minute, 100, 1000)
	for i := 0; i < 5; i++ {
		s.add(rec("auth_failure", "192.0.2.1"))
	}
	c.advance(2 * time.Minute)
	s.add(rec("auth_failure", "192.0.2.1"))
	if len(*out) != 3 || (*out)[1].count != 4 || (*out)[2].count != 1 {
		t.Fatalf("lines = %+v", *out)
	}
}

func TestSamplerFlushAllWritesPendingSummaries(t *testing.T) {
	c := newClock()
	s, out := collect(c, time.Minute, 100, 1000)
	for i := 0; i < 3; i++ {
		s.add(rec("ban", "192.0.2.1"))
	}
	s.flushAll()
	if len(*out) != 2 || (*out)[1].count != 2 {
		t.Fatalf("lines = %+v", *out)
	}
}

func TestSamplerBoundedKeysFoldAddressesTogether(t *testing.T) {
	c := newClock()
	s, out := collect(c, time.Minute, 4, 100000)
	for i := 0; i < 1000; i++ {
		s.add(rec("rate_limited", "192.0.2."+strconv.Itoa(i%250+1)))
	}
	s.mu.Lock()
	n := len(s.open)
	s.mu.Unlock()
	if n > 5 {
		t.Fatalf("open windows = %d, want bounded", n)
	}
	if len(*out) > 6 {
		t.Fatalf("%d lines for a flood", len(*out))
	}
}

func TestSamplerLineCeilingSumsOverflow(t *testing.T) {
	c := newClock()
	s, out := collect(c, time.Minute, 10000, 5)
	for i := 1; i <= 20; i++ {
		s.add(rec("rate_limited", "192.0.2."+strconv.Itoa(i)))
	}
	if len(*out) != 5 {
		t.Fatalf("%d lines written in one second, want 5", len(*out))
	}
	c.advance(time.Second)
	s.flush()
	last := (*out)[len(*out)-1]
	if last.event != "audit_overflow" || last.count != 15 {
		t.Fatalf("overflow line = %+v", last)
	}
	if _, dropped := s.stats(); dropped != 15 {
		t.Fatalf("dropped = %d", dropped)
	}
}

func TestLineFormatIsStableJSON(t *testing.T) {
	var buf bytes.Buffer
	log := newLogger(&buf)
	writeRecord(log, sanitize(core.AuditEvent{Event: "auth_failure", IP: "::ffff:192.0.2.7", Class: "public", Reason: "bad_key", Count: 3}))
	writeRecord(log, sanitize(core.AuditEvent{Event: "integrity_degraded", Reason: "quick_check"}))
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines: %q", buf.String())
	}
	if !strings.HasPrefix(lines[0], `{"ts":"`) || !strings.Contains(lines[0], `Z","event":"auth_failure","ip":"192.0.2.7","class":"public","reason":"bad_key","count":3}`) {
		t.Fatalf("line 0 = %s", lines[0])
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &m); err != nil {
		t.Fatal(err)
	}
	if _, has := m["ip"]; has {
		t.Fatalf("empty ip not omitted: %s", lines[1])
	}
	if _, has := m["level"]; has {
		t.Fatalf("level not omitted: %s", lines[1])
	}
}

func TestSanitizeReducesInjectedData(t *testing.T) {
	r := sanitize(core.AuditEvent{Event: "bad event\n{\"x\":1}", IP: "1.2.3.4\nevil", Class: "a b", Reason: strings.Repeat("A", 500) + "\n"})
	if r.ip != "invalid" || strings.ContainsAny(r.event+r.class+r.reason, " \n\"{}") || len(r.reason) > maxReasonLen {
		t.Fatalf("record = %+v", r)
	}
	if sanitize(core.AuditEvent{}).event != "unknown" {
		t.Fatal("empty event not named")
	}
}
