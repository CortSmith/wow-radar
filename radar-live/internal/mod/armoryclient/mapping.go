package armoryclient

// PROVISIONAL: this is the ONLY file that knows what the Battle.net WoW Classic Profile API answers look
// like, and it is a community-sourced GUESS. The official documentation page
// (community.developer.battle.net/documentation/world-of-warcraft-classic/profile-apis) is rendered with
// JavaScript and could not be read, so none of the field names below are verified. In particular:
//
//   - character summary:  {"level":N, "character_class":{"name":".."}, "race":{"name":".."},
//     "faction":{"type":"ALLIANCE","name":".."}, "guild":{"name":".."}, "last_login_timestamp":<ms>}
//     (a "name" may also be a plain string or a locale object {"en_US":".."})
//   - equipment:          {"equipped_items":[{"item":{"id":N}, "slot":{"type":"HEAD"}, "quality":{"type":".."},
//     "name":"..", "level":{"value":N}}]}   (summarised into gear)
//   - specializations:    stored as the talents object (whatever shape it has)
//   - statistics:         {"health":N, "power":N, "power_type":{"name":".."}} (also stored as the stats blob)
//   - pvp-summary:        stored as the pvp object
//   - guild roster:       {"members":[{"character":{"name":"..","realm":{"slug":".."}}}]}
//
// Everything is defensive: wrong types read as missing, blobs are size and depth capped, and a document
// that is not a JSON object is an error. When the real shapes are known, change only this file and the
// Default* paths below.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"radarlive/internal/domain"
)

// Provisional default endpoint guesses (see above). Every one is configurable.
const (
	DefaultTokenURL      = "https://oauth.battle.net/token"
	DefaultSummaryPath   = "/profile/wow/character/{realmSlug}/{characterName}"
	DefaultEquipmentPath = "/profile/wow/character/{realmSlug}/{characterName}/equipment"
	DefaultSpecsPath     = "/profile/wow/character/{realmSlug}/{characterName}/specializations"
	DefaultStatsPath     = "/profile/wow/character/{realmSlug}/{characterName}/statistics"
	DefaultPvPPath       = "/profile/wow/character/{realmSlug}/{characterName}/pvp-summary"
	DefaultRosterPath    = "/data/wow/guild/{realmSlug}/{guildSlug}/roster"

	// maxBlobDepth bounds the nesting of talents, stats and pvp. radar-db rejects the WHOLE batch
	// when its JSON nests deeper than its decoder limit (12 levels; the blob sits 3 levels down), so
	// one deeply nested blob would cost the 99 good characters beside it. Deeper blobs are dropped.
	maxBlobDepth = 8
	// maxGearItems and the text caps keep the gear summary far below the blob size limit.
	maxGearItems  = 40
	maxGearText   = 64
	maxGearTypeLn = 24
	// numberCap bounds any number read from the API (above any epoch-millisecond time).
	numberCap = int64(1e15)
)

// parts are the raw documents of one character; everything but summary may be empty (missing).
type parts struct {
	summary, equipment, specs, stats, pvp []byte
}

// mapCharacter turns the documents into a sanitised, validated character. realm and name are the
// (already validated) requested values; the documents cannot rename a character.
func mapCharacter(realm, name string, p parts, nowMs int64) (domain.ArmoryCharacter, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(p.summary, &doc); err != nil || doc == nil {
		return domain.ArmoryCharacter{}, fmt.Errorf("%w: summary is not a JSON object", ErrBadResponse)
	}
	stats := objectOf(p.stats)
	c := domain.ArmoryCharacter{
		Realm: realm, Name: name, FetchedAt: nowMs, Status: domain.ArmoryStatusOK,
		Level:     int(numberField(doc["level"])),
		Class:     localName(doc["character_class"]),
		Race:      localName(doc["race"]),
		Faction:   factionName(doc["faction"]),
		Guild:     localName(doc["guild"]),
		Health:    numberField(stats["health"]),
		Power:     numberField(stats["power"]),
		PowerType: localName(stats["power_type"]),
		Gear:      summarizeGear(p.equipment),
		Talents:   cleanBlob(p.specs),
		Stats:     cleanBlob(p.stats),
		PvP:       cleanBlob(p.pvp),
	}
	if u := numberField(doc["last_login_timestamp"]); u > 0 {
		if u < 100_000_000_000 { // epoch seconds
			u *= 1000
		}
		c.APIUpdatedAt = &u
	}
	c = c.Sanitise()
	if err := c.Validate(); err != nil {
		return domain.ArmoryCharacter{}, fmt.Errorf("%w: %v", ErrBadResponse, err)
	}
	return c, nil
}

// statusCharacter is the placeholder record for a hidden or not-found character.
func statusCharacter(realm, name, status string, nowMs int64) domain.ArmoryCharacter {
	return domain.ArmoryCharacter{Realm: realm, Name: name, Status: status, FetchedAt: nowMs}
}

// parseRoster maps a guild roster to character references (realm in slug form). Members with unusable
// names or realms are skipped; at most limit references are returned. A member without a realm
// belongs to the guild's realm.
func parseRoster(body []byte, guildRealmSlug string, limit int) ([]CharRef, error) {
	var doc struct {
		Members []json.RawMessage `json:"members"`
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(body, &probe); err != nil || probe == nil {
		return nil, fmt.Errorf("%w: roster is not a JSON object", ErrBadResponse)
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("%w: roster members are malformed", ErrBadResponse)
	}
	refs := make([]CharRef, 0, min(len(doc.Members), limit))
	for _, m := range doc.Members {
		if len(refs) >= limit {
			break
		}
		var member struct {
			Character struct {
				Name  string          `json:"name"`
				Realm json.RawMessage `json:"realm"`
			} `json:"character"`
		}
		if json.Unmarshal(m, &member) != nil || member.Character.Name == "" {
			continue
		}
		realm := guildRealmSlug
		if r := objectOf(member.Character.Realm); r != nil {
			if slug := stringField(r["slug"]); slug != "" {
				realm = slug
			}
		}
		if s, ok := RealmSlug(realm); !ok || s != realm || validateRef(realm, member.Character.Name) != nil {
			continue
		}
		refs = append(refs, CharRef{Realm: realm, Name: member.Character.Name})
	}
	return refs, nil
}

// objectOf parses a JSON object; anything else gives nil (reads of a nil map are safe).
func objectOf(raw []byte) map[string]json.RawMessage {
	var o map[string]json.RawMessage
	if json.Unmarshal(raw, &o) != nil {
		return nil
	}
	return o
}

func stringField(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

// plainOrLocale reads a string, or a locale object such as {"en_US":"Mage"}.
func plainOrLocale(raw json.RawMessage) string {
	if s := stringField(raw); s != "" {
		return s
	}
	return stringField(objectOf(raw)["en_US"])
}

// localName reads a display name given as a string, a locale object, or an object whose "name" is one
// of those.
func localName(raw json.RawMessage) string {
	if s := plainOrLocale(raw); s != "" {
		return s
	}
	return plainOrLocale(objectOf(raw)["name"])
}

// factionName prefers the display name and falls back to the type ("HORDE" becomes "Horde").
func factionName(raw json.RawMessage) string {
	if s := localName(raw); s != "" {
		return s
	}
	t := stringField(objectOf(raw)["type"])
	if t == "" {
		return ""
	}
	r, size := utf8.DecodeRuneInString(t)
	return string(unicode.ToUpper(r)) + strings.ToLower(t[size:])
}

// numberField reads a JSON number as a non-negative integer; anything else is 0.
func numberField(raw json.RawMessage) int64 {
	var f float64
	if json.Unmarshal(raw, &f) != nil || math.IsNaN(f) || f < 0 {
		return 0
	}
	if f > float64(numberCap) { // far above any sane value or timestamp; Sanitise clamps the rest
		return numberCap
	}
	return int64(f)
}

// cleanBlob returns a JSON object within the blob caps: boilerplate keys are removed, the key order is
// stable, and anything that is not an object, is too big or nests too deep is dropped.
func cleanBlob(raw []byte) json.RawMessage {
	o := objectOf(bytes.TrimSpace(raw))
	if o == nil {
		return nil
	}
	delete(o, "_links")
	delete(o, "character")
	out, err := json.Marshal(o)
	if err != nil || len(out) > domain.ArmoryBlobMaxBytes || jsonDepth(out) > maxBlobDepth {
		return nil
	}
	return out
}

type gearItem struct {
	Slot    string `json:"slot"`
	ID      int64  `json:"id"`
	Name    string `json:"name,omitempty"`
	Quality string `json:"quality,omitempty"`
	Level   int64  `json:"level,omitempty"`
}

// summarizeGear reduces the equipment document to {"count":N,"items":[{slot,id,name,quality,level}]}.
// At most maxGearItems items are kept and every text is capped, so the result cannot grow with the input.
func summarizeGear(raw []byte) json.RawMessage {
	var doc struct {
		Items []json.RawMessage `json:"equipped_items"`
	}
	if json.Unmarshal(raw, &doc) != nil || doc.Items == nil {
		return nil
	}
	items := make([]gearItem, 0, min(len(doc.Items), maxGearItems))
	for _, r := range doc.Items {
		if len(items) >= maxGearItems {
			break
		}
		o := objectOf(r)
		if o == nil {
			continue
		}
		items = append(items, gearItem{
			Slot:    shortText(typeOrName(o["slot"]), maxGearTypeLn),
			ID:      numberField(objectOf(o["item"])["id"]),
			Name:    shortText(localName(o["name"]), maxGearText),
			Quality: shortText(typeOrName(o["quality"]), maxGearTypeLn),
			Level:   levelValue(o["level"]),
		})
	}
	out, err := json.Marshal(struct {
		Count int        `json:"count"`
		Items []gearItem `json:"items"`
	}{len(items), items})
	if err != nil || len(out) > domain.ArmoryBlobMaxBytes {
		return nil
	}
	return out
}

// typeOrName reads {"type":"HEAD","name":"Head"} (the type wins) or a plain string.
func typeOrName(raw json.RawMessage) string {
	if s := stringField(raw); s != "" {
		return s
	}
	if t := stringField(objectOf(raw)["type"]); t != "" {
		return t
	}
	return localName(raw)
}

// levelValue reads a number or {"value":N}.
func levelValue(raw json.RawMessage) int64 {
	if n := numberField(raw); n > 0 {
		return n
	}
	return numberField(objectOf(raw)["value"])
}

// shortText drops control and bidirectional-override characters and truncates to max runes.
func shortText(s string, max int) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if unicode.IsControl(r) || (r >= '‪' && r <= '‮') || (r >= '⁦' && r <= '⁩') || r == utf8.RuneError {
			continue
		}
		if n++; n > max {
			break
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// jsonDepth is the deepest nesting of objects and arrays in valid JSON (strings are skipped).
func jsonDepth(b []byte) int {
	depth, deepest, inString := 0, 0, false
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case inString:
			if c == '\\' {
				i++
			} else if c == '"' {
				inString = false
			}
		case c == '"':
			inString = true
		case c == '{' || c == '[':
			depth++
			deepest = max(deepest, depth)
		case c == '}' || c == ']':
			depth--
		}
	}
	return deepest
}
