package domain

import "strings"

// Seed feed limits and kinds. The armory has no list-all endpoint, so the crawler seeds from the
// player contacts and guilds radar-db already holds.
const (
	ArmorySeedLimitDefault = 200
	ArmorySeedLimitMax     = 500

	ArmorySeedCharacter = "character"
	ArmorySeedGuild     = "guild"
)

// ArmorySeed is one crawl seed: a character (Realm may be empty: the client applies a default) or
// a guild (Realm and Guild set).
type ArmorySeed struct {
	Kind  string `json:"kind"`
	Realm string `json:"realm"`
	Name  string `json:"name,omitempty"`
	Guild string `json:"guild,omitempty"`
}

// ArmorySeedPage is one page of seeds; Next is empty when the feed is exhausted.
type ArmorySeedPage struct {
	Items []ArmorySeed `json:"items"`
	Next  string       `json:"next"`
}

// Validate checks a seed. Errors wrap ErrInvalid and never echo content.
func (s ArmorySeed) Validate() error {
	switch s.Kind {
	case ArmorySeedCharacter:
		switch {
		case !validName(s.Name, ArmoryNameMaxRunes, false):
			return invalidf("seed name is empty, too long or has unsafe characters")
		case s.Realm != "" && !validName(s.Realm, ArmoryRealmMaxRunes, true):
			return invalidf("seed realm is too long or has unsafe characters")
		}
	case ArmorySeedGuild:
		switch {
		case !validName(s.Realm, ArmoryRealmMaxRunes, true):
			return invalidf("seed realm is empty, too long or has unsafe characters")
		case s.Guild == "" || !validText(s.Guild, armoryTextMax):
			return invalidf("seed guild is empty, too long or has unsafe characters")
		}
	default:
		return invalidf("seed kind must be character or guild")
	}
	return nil
}

// ValidateArmorySeedLimit checks a page size in [1, ArmorySeedLimitMax].
func ValidateArmorySeedLimit(n int) error {
	if n < 1 || n > ArmorySeedLimitMax {
		return invalidf("limit must be between 1 and %d", ArmorySeedLimitMax)
	}
	return nil
}

// ArmoryRealmFromRaw returns the realm suffix of a raw contact name ("Name-Realm"), or "" when
// there is none or it is not a plausible realm.
func ArmoryRealmFromRaw(raw string) string {
	_, realm, ok := strings.Cut(raw, "-")
	if !ok || !validName(realm, ArmoryRealmMaxRunes, true) {
		return ""
	}
	return realm
}

// Seed cursor kinds: characters come first (c), then guilds (g). The empty cursor starts the feed.
const (
	ArmorySeedCursorStart = ""
	ArmorySeedCursorChars = "c"
	ArmorySeedCursorGuild = "g"
)

// FormatArmorySeedCursor builds the opaque cursor. For kind "c" a and b are the last name key and
// realm key; for kind "g" the last realm key and guild name. Kind "" gives the empty cursor.
func FormatArmorySeedCursor(kind, a, b string) string {
	if kind == ArmorySeedCursorStart {
		return ""
	}
	return kind + "|" + a + "|" + b
}

// ParseArmorySeedCursor validates and splits a cursor. Errors wrap ErrInvalid.
func ParseArmorySeedCursor(cursor string) (kind, a, b string, err error) {
	if cursor == "" {
		return "", "", "", nil
	}
	if !validText(cursor, ArmoryCursorMax) {
		return "", "", "", invalidf("cursor is too long or has unsafe characters")
	}
	parts := strings.SplitN(cursor, "|", 3)
	if len(parts) != 3 {
		return "", "", "", invalidf("cursor is malformed")
	}
	kind, a, b = parts[0], parts[1], parts[2]
	switch kind {
	case ArmorySeedCursorChars:
		if (a != "" && !validName(a, ArmoryNameMaxRunes, false)) || (b != "" && !validName(b, ArmoryRealmMaxRunes, true)) {
			return "", "", "", invalidf("cursor is malformed")
		}
	case ArmorySeedCursorGuild:
		if (a != "" && !validName(a, ArmoryRealmMaxRunes, true)) || !validText(b, armoryTextMax) {
			return "", "", "", invalidf("cursor is malformed")
		}
	default:
		return "", "", "", invalidf("cursor is malformed")
	}
	return kind, a, b, nil
}

// ArmorySeedSource yields up to n seeds strictly after the key (a, b) in the feed's order, which
// is the same for every store: characters by (name key, realm key), guilds by (realm key, guild).
type ArmorySeedSource func(afterA, afterB string, n int) ([]ArmorySeed, error)

// BuildArmorySeedPage pages the feed (all characters first, then all guilds) over two sources, so
// every store shares one cursor and boundary rule. The cursor names the last item returned.
func BuildArmorySeedPage(cursor string, limit int, chars, guilds ArmorySeedSource) (ArmorySeedPage, error) {
	if err := ValidateArmorySeedLimit(limit); err != nil {
		return ArmorySeedPage{}, err
	}
	kind, a, b, err := ParseArmorySeedCursor(cursor)
	if err != nil {
		return ArmorySeedPage{}, err
	}
	want := limit + 1
	items := []ArmorySeed{}
	if kind != ArmorySeedCursorGuild {
		got, err := chars(a, b, want)
		if err != nil {
			return ArmorySeedPage{}, err
		}
		items = append(items, got...)
		a, b = "", ""
	}
	if len(items) < want {
		got, err := guilds(a, b, want-len(items))
		if err != nil {
			return ArmorySeedPage{}, err
		}
		items = append(items, got...)
	}
	page := ArmorySeedPage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		last := page.Items[limit-1]
		if last.Kind == ArmorySeedGuild {
			page.Next = FormatArmorySeedCursor(ArmorySeedCursorGuild, ArmoryRealmKey(last.Realm), last.Guild)
		} else {
			page.Next = FormatArmorySeedCursor(ArmorySeedCursorChars, ArmoryNameKey(last.Name), ArmoryRealmKey(last.Realm))
		}
	}
	return page, nil
}
