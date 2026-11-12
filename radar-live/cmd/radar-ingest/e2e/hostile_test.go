package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"radarlive/internal/domain"
)

// advanced builds a SWING_DAMAGE line of the log owner with the given advanced-block values.
func advanced(ts time.Time, raw, hp, x, y, level string) string {
	stamp := fmt.Sprintf("%d/%d/%d %02d:%02d:%02d.%03d", int(ts.Month()), ts.Day(), ts.Year(), ts.Hour(), ts.Minute(), ts.Second(), ts.Nanosecond()/1e6)
	return fmt.Sprintf(`%s  SWING_DAMAGE,Player-1-0A,"%s",0x511,0x0,Creature-0-1,"Dummy",0xa48,0x0,Player-1-0A,0000000000000000,%s,1000,0,0,0,0,0,0,0,0,0,0,%s,%s,1500,1.0,%s,50,50,0,1,0,0,0,nil,nil,nil`,
		stamp, raw, hp, x, y, level)
}

// A poisoned log goes through the real chain: bad lines are counted as rejected, the good one and
// the repaired name reach the database, and the database refuses nothing.
func TestPoisonedLogIsRejectedOrRepairedEndToEnd(t *testing.T) {
	db := newTestDB(t)
	in := startIngest(t, Ingest(), append(relayArgs(db), "--logparse-allow-replay=false")...)
	now := time.Now().UTC()
	lines := []string{
		advanced(now, "Ann-Bee-PvP-US", "900", "10.0", "20.0", "60"),                                             // good
		advanced(now.Add(-48*time.Hour), "Old-PvP-US", "900", "10.0", "20.0", "60"),                              // two days old
		advanced(now.Add(72*time.Hour), "Future-PvP-US", "900", "10.0", "20.0", "60"),                            // three days ahead
		advanced(now, "Huge-PvP-US", "900", "1e12", "20.0", "60"),                                                // coordinate out of range
		advanced(now, "Lvl-PvP-US", "900", "10.0", "20.0", "255"),                                                // level out of range
		advanced(now, "Neg-PvP-US", "-5", "10.0", "20.0", "60"),                                                  // negative hit points
		advanced(now, "Nan-PvP-US", "NaN", "Inf", "20.0", "60"),                                                  // not finite: parsed without them
		advanced(now, "Ev‮il\x01-PvP-US", "900", "11.0", "21.0", "61"),                                           // bidi and control characters
		"\x00\x01\x02\xff\xfe" + strings.Repeat("\x7f", 100),                                                     // binary garbage
		strings.Repeat("A", domainLineCap()) + "\n" + advanced(now, "After-PvP-US", "900", "12.0", "22.0", "62"), // overlong, then fine
	}
	sum := in.stream(t, "?source=hostile", strings.Join(lines, "\n")+"\n")
	if got := sum["rejected"].(float64); got < 6 {
		t.Fatalf("rejected %v, summary %v", got, sum)
	}
	waitFor(t, "contact in the database", func() bool { return len(db.sweep(t, domain.RulesetPvP).Contacts) >= 1 })
	time.Sleep(300 * time.Millisecond) // let the other flushes land
	contacts := db.sweep(t, domain.RulesetPvP).Contacts
	if len(contacts) != 1 {
		t.Fatalf("%d contacts, want the one log owner: %+v", len(contacts), contacts)
	}
	c := contacts[0]
	all := strings.Join(append([]string{c.Name, c.Raw}, c.PastNames...), "|")
	for _, bad := range []string{"Old", "Future", "Huge", "Lvl", "Neg"} {
		if strings.Contains(all, bad) {
			t.Fatalf("a rejected line left %q in %q", bad, all)
		}
	}
	if strings.ContainsAny(all, "\u202e\x01\x00") || !strings.Contains(all, "Ann Bee") || !strings.Contains(all, "Evil PvP US") {
		t.Fatalf("names %q", all)
	}
	if c.Level != 62 || c.Level > domain.PingLevelMax {
		t.Fatalf("level %d", c.Level)
	}
}

func domainLineCap() int { return 1<<20 + 10 }
