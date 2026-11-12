package sqlstore

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"radarlive/internal/domain"
)

// ArmorySeeds implements core.ArmorySeedStore on the reader pool. Cursor values only ever travel
// as bound parameters, and the cursor is validated by the domain before any query runs.
func (s *Store) ArmorySeeds(ctx context.Context, cursor string, limit int) (domain.ArmorySeedPage, error) {
	if err := ctx.Err(); err != nil {
		return domain.ArmorySeedPage{}, fmt.Errorf("sqlstore: %w", err)
	}
	chars := func(afterName, afterRealm string, n int) ([]domain.ArmorySeed, error) {
		return s.characterSeeds(ctx, afterName, afterRealm, n)
	}
	guilds := func(afterRealm, afterGuild string, n int) ([]domain.ArmorySeed, error) {
		return s.guildSeeds(ctx, afterRealm, afterGuild, n)
	}
	return domain.BuildArmorySeedPage(cursor, limit, chars, guilds)
}

// Player contacts from every ruleset in name order. The realm lives only in the raw name
// ("Name-Realm"), so grouping by name key happens here and the realm tie-break in Go; a name group
// is always read to its end so a page never splits it.
const selectSeedContacts = `SELECT name, name_lc, raw FROM contacts
	WHERE substr(guid, 1, 7) = 'Player-' AND name_lc >= ? ORDER BY name_lc`

func (s *Store) characterSeeds(ctx context.Context, afterName, afterRealm string, n int) ([]domain.ArmorySeed, error) {
	rows, err := s.readers.QueryContext(ctx, selectSeedContacts, afterName)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: armory seeds: %w", err)
	}
	defer rows.Close()
	out := []domain.ArmorySeed{}
	var group map[string]domain.ArmorySeed // realm key -> smallest spelling
	var groupName string
	flush := func() {
		keys := make([]string, 0, len(group))
		for k := range group {
			if groupName == afterName && k <= afterRealm {
				continue
			}
			keys = append(keys, k)
		}
		slices.SortFunc(keys, cmp.Compare[string])
		for _, k := range keys {
			out = append(out, group[k])
		}
		group = nil
	}
	for rows.Next() {
		var name, nameLC, raw string
		if err := rows.Scan(&name, &nameLC, &raw); err != nil {
			return nil, fmt.Errorf("sqlstore: armory seeds: %w", err)
		}
		if group != nil && nameLC != groupName {
			flush()
			if len(out) >= n {
				break
			}
		}
		seed := domain.ArmorySeed{Kind: domain.ArmorySeedCharacter, Realm: domain.ArmoryRealmFromRaw(raw), Name: name}
		if seed.Validate() != nil {
			continue
		}
		if group == nil {
			group, groupName = map[string]domain.ArmorySeed{}, nameLC
		}
		k := domain.ArmoryRealmKey(seed.Realm)
		if old, ok := group[k]; !ok || seed.Realm+"\x00"+seed.Name < old.Realm+"\x00"+old.Name {
			group[k] = seed
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlstore: armory seeds: %w", err)
	}
	if group != nil {
		flush()
	}
	return out[:min(n, len(out))], nil
}

const selectSeedGuilds = `SELECT MIN(realm), guild FROM armory_characters
	WHERE guild != '' AND (realm_lc > ? OR (realm_lc = ? AND guild > ?))
	GROUP BY realm_lc, guild ORDER BY realm_lc, guild LIMIT ?`

func (s *Store) guildSeeds(ctx context.Context, afterRealm, afterGuild string, n int) ([]domain.ArmorySeed, error) {
	rows, err := s.readers.QueryContext(ctx, selectSeedGuilds, afterRealm, afterRealm, afterGuild, n)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: armory seeds: %w", err)
	}
	defer rows.Close()
	out := []domain.ArmorySeed{}
	for rows.Next() {
		g := domain.ArmorySeed{Kind: domain.ArmorySeedGuild}
		if err := rows.Scan(&g.Realm, &g.Guild); err != nil {
			return nil, fmt.Errorf("sqlstore: armory seeds: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
