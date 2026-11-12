package sqlstore

import (
	"strings"

	"radarlive/internal/domain"
)

// filter builds a WHERE clause from fixed fragments; every value travels as a bind argument, so
// no caller-supplied text ever becomes part of the SQL.
type filter struct {
	clauses []string
	args    []any
	never   bool // the filter can match nothing; skip the query
}

func newFilter(rs domain.Ruleset) *filter {
	f := &filter{}
	f.add("rs = ?", string(rs))
	return f
}

func (f *filter) add(clause string, args ...any) {
	f.clauses = append(f.clauses, clause)
	f.args = append(f.args, args...)
}

// group restricts to a faction group (friendly, hostile, or everything else).
func (f *filter) group(g domain.Group) {
	switch g {
	case domain.GroupFriendly:
		f.add("reaction = ?", string(domain.ReactionFriendly))
	case domain.GroupHostile:
		f.add("reaction = ?", string(domain.ReactionHostile))
	case domain.GroupOther:
		f.add("reaction NOT IN (?, ?)", string(domain.ReactionFriendly), string(domain.ReactionHostile))
	}
}

// onMap restricts to contacts with a known position on one chart.
func (f *filter) onMap(mapID *int) {
	if mapID != nil {
		f.add("pos_time > 0 AND map_id = ?", *mapID)
	}
}

// text restricts to contacts whose name, raw name or a past name contains q (case-insensitive).
// instr compares plain text, so LIKE wildcards in q need no escaping and stay literal.
func (f *filter) text(q string) {
	term := strings.ToLower(strings.TrimSpace(q))
	switch {
	case term == "":
	case strings.Contains(term, searchSep):
		f.never = true
	default:
		f.add("instr(search, ?) > 0", term)
	}
}

func (f *filter) where() string { return strings.Join(f.clauses, " AND ") }
