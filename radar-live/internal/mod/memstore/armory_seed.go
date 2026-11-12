package memstore

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"radarlive/internal/domain"
)

// ArmorySeeds implements core.ArmorySeedStore: player contacts of every ruleset (de-duplicated by
// case-folded realm and name, invalid names skipped), then the distinct guilds of stored armory
// characters. The order and cursor rules are shared with sqlstore through domain.BuildArmorySeedPage.
func (s *Store) ArmorySeeds(ctx context.Context, cursor string, limit int) (domain.ArmorySeedPage, error) {
	if err := ctx.Err(); err != nil {
		return domain.ArmorySeedPage{}, fmt.Errorf("memstore: %w", err)
	}
	chars := func(afterName, afterRealm string, n int) ([]domain.ArmorySeed, error) {
		return s.characterSeeds(afterName, afterRealm, n), nil
	}
	guilds := func(afterRealm, afterGuild string, n int) ([]domain.ArmorySeed, error) {
		return s.guildSeeds(afterRealm, afterGuild, n), nil
	}
	return domain.BuildArmorySeedPage(cursor, limit, chars, guilds)
}

func (s *Store) characterSeeds(afterName, afterRealm string, n int) []domain.ArmorySeed {
	type key struct{ name, realm string }
	best := map[key]domain.ArmorySeed{}
	s.mu.RLock()
	for _, part := range s.partitions {
		for _, rec := range part.contacts {
			c := rec.contact
			if domain.KindOf(c.GUID) != domain.KindPlayer {
				continue
			}
			seed := domain.ArmorySeed{Kind: domain.ArmorySeedCharacter, Realm: domain.ArmoryRealmFromRaw(c.Raw), Name: c.Name}
			if seed.Validate() != nil {
				continue
			}
			k := key{domain.ArmoryNameKey(seed.Name), domain.ArmoryRealmKey(seed.Realm)}
			if k.name < afterName || (k.name == afterName && k.realm <= afterRealm) {
				continue
			}
			// de-duplicate deterministically: the smallest spelling wins
			if old, ok := best[k]; !ok || strings.Compare(seed.Realm+"\x00"+seed.Name, old.Realm+"\x00"+old.Name) < 0 {
				best[k] = seed
			}
		}
	}
	s.mu.RUnlock()
	keys := make([]key, 0, len(best))
	for k := range best {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b key) int { return cmp.Or(cmp.Compare(a.name, b.name), cmp.Compare(a.realm, b.realm)) })
	out := []domain.ArmorySeed{}
	for _, k := range keys[:min(n, len(keys))] {
		out = append(out, best[k])
	}
	return out
}

func (s *Store) guildSeeds(afterRealm, afterGuild string, n int) []domain.ArmorySeed {
	type key struct{ realm, guild string }
	best := map[key]string{} // key -> smallest realm spelling
	a := &s.armory
	a.mu.Lock()
	for _, rec := range a.chars {
		c := rec.char
		if c.Guild == "" {
			continue
		}
		k := key{domain.ArmoryRealmKey(c.Realm), c.Guild}
		if k.realm < afterRealm || (k.realm == afterRealm && k.guild <= afterGuild) {
			continue
		}
		if old, ok := best[k]; !ok || c.Realm < old {
			best[k] = c.Realm
		}
	}
	a.mu.Unlock()
	keys := make([]key, 0, len(best))
	for k := range best {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b key) int { return cmp.Or(cmp.Compare(a.realm, b.realm), cmp.Compare(a.guild, b.guild)) })
	out := []domain.ArmorySeed{}
	for _, k := range keys[:min(n, len(keys))] {
		out = append(out, domain.ArmorySeed{Kind: domain.ArmorySeedGuild, Realm: best[k], Guild: k.guild})
	}
	return out
}
