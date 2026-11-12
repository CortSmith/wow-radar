package sqlstore

import (
	"encoding/json"
	"fmt"
	"strings"

	"radarlive/internal/domain"
)

// contactColumns are the Contact fields in storage order; the SELECT and the upsert are built from it.
var contactColumns = []string{
	"guid", "name", "raw", "past_names", "reaction", "level", "max_hp", "x", "y", "map_id", "zone", "pos_time",
	"first_seen", "last_seen", "events", "src_events", "dmg_total", "dmg_hits", "dmg_events",
	"hp_sum", "hp_samples", "kb", "assists",
}

var (
	selectContacts = `SELECT ` + strings.Join(contactColumns, ",") + ` FROM contacts`
	upsertContact  = buildUpsert()
)

// buildUpsert makes the INSERT .. ON CONFLICT DO UPDATE statement; it only joins fixed column names.
func buildUpsert() string {
	cols := append([]string{"rs", "name_lc", "search"}, contactColumns...)
	marks := strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",")
	sets := make([]string, 0, len(cols))
	for _, c := range cols[1:] {
		if c != "guid" {
			sets = append(sets, c+"=excluded."+c)
		}
	}
	return `INSERT INTO contacts (` + strings.Join(cols, ",") + `) VALUES (` + marks + `)` +
		` ON CONFLICT(rs, guid) DO UPDATE SET ` + strings.Join(sets, ",")
}

// searchSep joins the searchable names; it cannot appear in a query (see searchTerm).
const searchSep = "\x1f"

// searchText is the lowercase haystack of a contact: name, raw name and past names.
func searchText(c domain.Contact) string {
	parts := append([]string{c.Name, c.Raw}, c.PastNames...)
	return strings.ToLower(strings.Join(parts, searchSep))
}

// upsertArgs lists the bind values of upsertContact for one contact.
func upsertArgs(rs domain.Ruleset, c domain.Contact) []any {
	return []any{
		string(rs), strings.ToLower(c.Name), searchText(c),
		c.GUID, c.Name, c.Raw, encodeNames(c.PastNames), string(c.Reaction), c.Level, c.MaxHP, c.X, c.Y, c.MapID,
		c.Zone, c.PosTime, c.FirstSeen, c.LastSeen, c.Events, c.SrcEvents, c.DmgTotal, c.DmgHits, c.DmgEvents,
		c.HPSum, c.HPSamples, c.KB, c.Assists,
	}
}

func encodeNames(names []string) string {
	if len(names) == 0 {
		return ""
	}
	b, _ := json.Marshal(names) // a []string always marshals
	return string(b)
}

func decodeNames(text string) ([]string, error) {
	if text == "" {
		return nil, nil
	}
	var names []string
	if err := json.Unmarshal([]byte(text), &names); err != nil {
		return nil, fmt.Errorf("decode past names: %w", err)
	}
	return names, nil
}

// scanner is satisfied by *sql.Row and *sql.Rows.
type scanner interface{ Scan(dest ...any) error }

// scanContact reads one row of selectContacts.
func scanContact(sc scanner) (domain.Contact, error) {
	var c domain.Contact
	var names, reaction string
	err := sc.Scan(&c.GUID, &c.Name, &c.Raw, &names, &reaction, &c.Level, &c.MaxHP, &c.X, &c.Y, &c.MapID, &c.Zone,
		&c.PosTime, &c.FirstSeen, &c.LastSeen, &c.Events, &c.SrcEvents, &c.DmgTotal, &c.DmgHits, &c.DmgEvents,
		&c.HPSum, &c.HPSamples, &c.KB, &c.Assists)
	if err != nil {
		return domain.Contact{}, err
	}
	c.Reaction = domain.Reaction(reaction)
	c.PastNames, err = decodeNames(names)
	return c, err
}

// Fix kind codes stored in trail_fixes.kind.
const (
	kindExact  = 0
	kindApprox = 1
)

func kindCode(k domain.FixKind) int {
	if k == domain.FixApprox {
		return kindApprox
	}
	return kindExact
}

func kindName(code int) domain.FixKind {
	if code == kindApprox {
		return domain.FixApprox
	}
	return domain.FixExact
}

// scanFix reads one (t, map_id, x, y, kind) row.
func scanFix(sc scanner) (domain.Fix, error) {
	var f domain.Fix
	var kind int
	if err := sc.Scan(&f.T, &f.MapID, &f.X, &f.Y, &kind); err != nil {
		return domain.Fix{}, err
	}
	f.Kind = kindName(kind)
	return f, nil
}
