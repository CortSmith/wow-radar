package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"unicode"
)

// Armory limits. The field set is PROVISIONAL (armory API notes are not available yet); the
// variable-shape data lives in size-capped JSON objects so the real API shape needs no schema change.
const (
	// ArmoryBlobMaxBytes caps each of gear, talents, stats and pvp.
	ArmoryBlobMaxBytes = 16 * 1024
	// ArmoryBatchMax caps the characters of one batch.
	ArmoryBatchMax = 100
	// ArmoryCursorMax caps the crawl cursor in runes.
	ArmoryCursorMax = 256
	// ArmoryNameMaxRunes caps a character name; ArmoryRealmMaxRunes a realm name.
	ArmoryNameMaxRunes  = 24
	ArmoryRealmMaxRunes = 48
	// ArmoryValueMax bounds health and power (as reported maxima).
	ArmoryValueMax = int64(1e9)
	armoryTextMax  = 64
)

// Character status values: how the last fetch of one character went.
const (
	ArmoryStatusOK       = "ok"
	ArmoryStatusHidden   = "hidden"
	ArmoryStatusNotFound = "notfound"
	ArmoryStatusError    = "error"
)

// Run status values.
const (
	ArmoryRunRunning = "running"
	ArmoryRunOK      = "ok"
	ArmoryRunPartial = "partial"
	ArmoryRunFailed  = "failed"
)

// msPerDay converts epoch milliseconds to UTC days.
const msPerDay = int64(86_400_000)

// ArmoryCharacter is one character as last read from the armory. Gear, Talents, Stats and PvP are
// JSON objects (or empty) whose shape is owned by the armory API.
type ArmoryCharacter struct {
	Realm        string          `json:"realm"`
	Name         string          `json:"name"`
	Class        string          `json:"class"`
	Race         string          `json:"race"`
	Faction      string          `json:"faction"`
	Level        int             `json:"level"`
	Guild        string          `json:"guild"`
	Health       int64           `json:"health"`
	PowerType    string          `json:"powerType"`
	Power        int64           `json:"power"`
	Gear         json.RawMessage `json:"gear,omitempty"`
	Talents      json.RawMessage `json:"talents,omitempty"`
	Stats        json.RawMessage `json:"stats,omitempty"`
	PvP          json.RawMessage `json:"pvp,omitempty"`
	APIUpdatedAt *int64          `json:"apiUpdatedAt,omitempty"`
	FetchedAt    int64           `json:"fetchedAt"`
	Status       string          `json:"status"`
}

// ArmoryBatch is up to ArmoryBatchMax characters sent together. Replaying a BatchID is a no-op.
type ArmoryBatch struct {
	BatchID    string            `json:"batchId"`
	RunID      string            `json:"runId"`
	Characters []ArmoryCharacter `json:"characters"`
	Cursor     string            `json:"cursor"`
}

// ArmoryApplyResult reports one applied batch. Stored counts characters written, Skipped those
// ignored because the store already held a newer fetch.
type ArmoryApplyResult struct {
	Duplicate bool `json:"duplicate"`
	Stored    int  `json:"stored"`
	Skipped   int  `json:"skipped"`
}

// ArmorySnapshot is the compact per-day history entry of one character.
type ArmorySnapshot struct {
	Day      int64           `json:"day"` // UTC days since the epoch
	Level    int             `json:"level"`
	GearHash string          `json:"gearHash"`
	PvP      json.RawMessage `json:"pvp,omitempty"`
}

// ArmoryRun summarises one crawl run (observability and resume checkpoint).
type ArmoryRun struct {
	ID         string `json:"id"`
	StartedAt  int64  `json:"startedAt"`
	FinishedAt int64  `json:"finishedAt"`
	Enumerated int    `json:"enumerated"`
	Fetched    int    `json:"fetched"`
	Stored     int    `json:"stored"`
	Skipped    int    `json:"skipped"`
	Failed     int    `json:"failed"`
	Status     string `json:"status"`
	Cursor     string `json:"cursor"`
}

// ArmoryNameKey is the case-folded name used as the storage key (name_lc).
func ArmoryNameKey(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

// ArmoryRealmKey is the case-folded realm used as the storage key.
func ArmoryRealmKey(realm string) string { return strings.ToLower(strings.TrimSpace(realm)) }

// ArmoryDay returns the UTC day number of an epoch-millisecond time.
func ArmoryDay(ms int64) int64 {
	d := ms / msPerDay
	if ms%msPerDay < 0 {
		d--
	}
	return d
}

// GearHash is a short, whitespace-insensitive digest of a gear blob; empty gear has no hash.
func GearHash(gear json.RawMessage) string {
	if len(bytes.TrimSpace(gear)) == 0 {
		return ""
	}
	var buf bytes.Buffer
	data := []byte(gear)
	if json.Compact(&buf, data) == nil {
		data = buf.Bytes()
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

// validName accepts letters (and optionally digits) with apostrophes, hyphens and inner spaces,
// capped in runes.
func validName(s string, maxRunes int, allowDigits bool) bool {
	n := 0
	for _, r := range s {
		n++
		ok := unicode.IsLetter(r) || r == '\'' || r == '-' || r == ' ' || (allowDigits && unicode.IsDigit(r))
		if !ok {
			return false
		}
	}
	return n >= 1 && n <= maxRunes && strings.TrimSpace(s) == s
}

// validBlob accepts an empty value or a JSON object within ArmoryBlobMaxBytes.
func validBlob(b json.RawMessage) bool {
	if len(b) == 0 {
		return true
	}
	if len(b) > ArmoryBlobMaxBytes {
		return false
	}
	var obj map[string]json.RawMessage
	return json.Unmarshal(b, &obj) == nil && obj != nil
}

func validStatus(s string) bool {
	switch s {
	case ArmoryStatusOK, ArmoryStatusHidden, ArmoryStatusNotFound, ArmoryStatusError:
		return true
	}
	return false
}

// Validate checks names, text, number bounds, JSON blobs and status. It never modifies the value and
// its messages name the field, never the content. Errors wrap ErrInvalid.
func (c ArmoryCharacter) Validate() error {
	switch {
	case !validName(c.Name, ArmoryNameMaxRunes, false):
		return invalidf("armory name is empty, too long or has unsafe characters")
	case !validName(c.Realm, ArmoryRealmMaxRunes, true):
		return invalidf("armory realm is empty, too long or has unsafe characters")
	case !validStatus(c.Status):
		return invalidf("armory status must be ok, hidden, notfound or error")
	case !c.validText():
		return invalidf("armory class, race, faction, guild or power type is too long or unsafe")
	case c.Level < 0 || c.Level > PingLevelMax:
		return invalidf("armory level out of range")
	case c.Health < 0 || c.Health > ArmoryValueMax || c.Power < 0 || c.Power > ArmoryValueMax:
		return invalidf("armory health or power out of range")
	case c.FetchedAt < MinTimestampMs || (c.APIUpdatedAt != nil && *c.APIUpdatedAt < MinTimestampMs):
		return invalidf("armory timestamp out of range")
	}
	for _, b := range []json.RawMessage{c.Gear, c.Talents, c.Stats, c.PvP} {
		if !validBlob(b) {
			return invalidf("armory gear, talents, stats and pvp must be JSON objects up to %d bytes", ArmoryBlobMaxBytes)
		}
	}
	return nil
}

func (c ArmoryCharacter) validText() bool {
	for _, s := range []string{c.Class, c.Race, c.Faction, c.Guild, c.PowerType} {
		if !validText(s, armoryTextMax) {
			return false
		}
	}
	return true
}

func clampInt64(v, hi int64) int64 { return max(0, min(v, hi)) }

// Sanitise returns a repaired copy: text cleaned and cut, numbers clamped, "null" blobs emptied.
// It cannot repair names, status, timestamps or oversized blobs; Validate still rejects those.
func (c ArmoryCharacter) Sanitise() ArmoryCharacter {
	for _, p := range []*string{&c.Class, &c.Race, &c.Faction, &c.Guild, &c.PowerType} {
		*p, _ = cleanText(*p, armoryTextMax)
	}
	c.Name, _ = cleanText(c.Name, ArmoryNameMaxRunes)
	c.Realm, _ = cleanText(c.Realm, ArmoryRealmMaxRunes)
	c.Level = int(clampInt64(int64(c.Level), PingLevelMax))
	c.Health = clampInt64(c.Health, ArmoryValueMax)
	c.Power = clampInt64(c.Power, ArmoryValueMax)
	for _, p := range []*json.RawMessage{&c.Gear, &c.Talents, &c.Stats, &c.PvP} {
		if t := bytes.TrimSpace(*p); len(t) == 0 || string(t) == "null" {
			*p = nil
		}
	}
	return c
}

// Validate checks ids, the cursor, the character count and every character.
func (b ArmoryBatch) Validate() error {
	switch {
	case !batchIDPattern.MatchString(b.BatchID):
		return invalidf("batchId must match %s", batchIDPattern)
	case b.RunID != "" && !batchIDPattern.MatchString(b.RunID):
		return invalidf("runId must match %s", batchIDPattern)
	case len(b.Characters) > ArmoryBatchMax:
		return invalidf("more than %d characters", ArmoryBatchMax)
	case !validText(b.Cursor, ArmoryCursorMax):
		return invalidf("cursor is too long or has unsafe characters")
	}
	for i, c := range b.Characters {
		if err := c.Validate(); err != nil {
			return invalidf("character %d: %v", i, err)
		}
	}
	return nil
}

// Sanitise returns a copy whose characters are sanitised; the input is not modified.
func (b ArmoryBatch) Sanitise() ArmoryBatch {
	out := b
	out.Characters = make([]ArmoryCharacter, len(b.Characters))
	for i, c := range b.Characters {
		out.Characters[i] = c.Sanitise()
	}
	out.Cursor, _ = cleanText(b.Cursor, ArmoryCursorMax)
	return out
}

// Validate checks a run summary.
func (r ArmoryRun) Validate() error {
	switch r.Status {
	case ArmoryRunRunning, ArmoryRunOK, ArmoryRunPartial, ArmoryRunFailed:
	default:
		return invalidf("run status must be running, ok, partial or failed")
	}
	switch {
	case !batchIDPattern.MatchString(r.ID):
		return invalidf("run id must match %s", batchIDPattern)
	case r.StartedAt < MinTimestampMs || r.FinishedAt < 0:
		return invalidf("run timestamps out of range")
	case min(r.Enumerated, r.Fetched, r.Stored, r.Skipped, r.Failed) < 0:
		return invalidf("run counters must not be negative")
	case !validText(r.Cursor, ArmoryCursorMax):
		return invalidf("cursor is too long or has unsafe characters")
	}
	return nil
}
