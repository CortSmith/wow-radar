package armoryclient

import (
	"strings"
	"testing"

	"radarlive/internal/domain"
)

// radar-db rejects a whole batch whose JSON nests deeper than its decoder limit, so one character
// with a deeply nested blob must not reach it: the blob is dropped, the character is kept.
func TestDeeplyNestedBlobIsDropped(t *testing.T) {
	nest := func(n int) string { return strings.Repeat(`{"a":`, n) + "1" + strings.Repeat("}", n) }
	now := int64(domain.MinTimestampMs + 1)
	for depth, wantKept := range map[int]bool{1: true, maxBlobDepth: true, maxBlobDepth + 1: false, 60: false, 5000: false} {
		p := parts{summary: []byte(`{"level":10,"character_class":"Mage"}`), specs: []byte(nest(depth)), pvp: []byte(`{"ok":1}`)}
		c, err := mapCharacter("Testrealm", "Synthetic", p, now)
		if err != nil {
			t.Fatalf("depth %d: %v", depth, err)
		}
		if kept := len(c.Talents) > 0; kept != wantKept {
			t.Errorf("depth %d: talents kept = %v, want %v", depth, kept, wantKept)
		}
		if string(c.PvP) != `{"ok":1}` || c.Class != "Mage" {
			t.Errorf("depth %d: the rest of the character was damaged: %+v", depth, c)
		}
	}
	// Arrays count as nesting too.
	deep := `{"a":` + strings.Repeat("[", maxBlobDepth) + strings.Repeat("]", maxBlobDepth) + `}`
	if c, _ := mapCharacter("Testrealm", "Synthetic", parts{summary: []byte(`{}`), stats: []byte(deep)}, now); len(c.Stats) != 0 {
		t.Error("a deep array blob was kept")
	}
	// A hostile equipment document cannot bloat the gear summary.
	hostile := `{"equipped_items":[{"item":{"id":1},"slot":{"type":"HEAD"},"name":"` + strings.Repeat("n", 100000) + `","quality":{"type":"` + strings.Repeat("q", 100000) + `"}}]}`
	c, err := mapCharacter("Testrealm", "Synthetic", parts{summary: []byte(`{}`), equipment: []byte(hostile)}, now)
	if err != nil || c.Validate() != nil || len(c.Gear) > domain.ArmoryBlobMaxBytes {
		t.Fatalf("hostile gear: %v %d", err, len(c.Gear))
	}
	// The item count is capped.
	var b strings.Builder
	b.WriteString(`{"equipped_items":[`)
	for i := 0; i < 500; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"item":{"id":5},"slot":{"type":"HEAD"},"name":"Synthetic Item","quality":{"type":"EPIC"},"level":{"value":9}}`)
	}
	b.WriteString(`]}`)
	c, err = mapCharacter("Testrealm", "Synthetic", parts{summary: []byte(`{}`), equipment: []byte(b.String())}, now)
	if err != nil || strings.Count(string(c.Gear), `"slot"`) != maxGearItems || len(c.Gear) > domain.ArmoryBlobMaxBytes {
		t.Fatalf("gear cap: %v items=%d size=%d", err, strings.Count(string(c.Gear), `"slot"`), len(c.Gear))
	}
}
