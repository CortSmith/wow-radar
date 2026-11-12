package readapi

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"unicode"

	"radarlive/internal/core"
	"radarlive/internal/domain"
)

// Byte caps for armory query text: names and realms are capped in runes by the domain, a rune is at
// most 4 bytes; the names list holds up to domain.ArmoryBatchMax comma-separated names.
const (
	armoryRealmMaxBytes = domain.ArmoryRealmMaxRunes * 4
	armoryNameMaxBytes  = domain.ArmoryNameMaxRunes * 4
	armoryNamesMaxBytes = domain.ArmoryBatchMax * (armoryNameMaxBytes + 1)
)

// armoryStore returns the store's armory half, or a not-found error when the host has none.
func armoryStore(st core.Store) (core.ArmoryStore, error) {
	as, ok := st.(core.ArmoryStore)
	if !ok {
		return nil, fmt.Errorf("%w: armory data is not available on this server", domain.ErrNotFound)
	}
	return as, nil
}

// required returns v, or an invalid-request error naming the parameter when it is empty.
func required(key, v string) (string, error) {
	if v == "" {
		return "", fmt.Errorf("%w: %s: is required", domain.ErrInvalid, key)
	}
	return v, nil
}

// plainName reports whether s only has the characters the domain allows in a character name
// (letters, apostrophe, hyphen, space) or, with digits, a realm name. Anything else cannot be stored,
// so it is a bad request instead of a lookup.
func plainName(s string, digits bool) bool {
	for _, r := range s {
		if !(unicode.IsLetter(r) || r == '\'' || r == '-' || r == ' ' || (digits && unicode.IsDigit(r))) {
			return false
		}
	}
	return true
}

func badChars(key string) error {
	return fmt.Errorf("%w: %s: contains characters that are not allowed", domain.ErrInvalid, key)
}

func (m *Module) armoryCharacter(ctx context.Context, st core.Store, q url.Values) (any, error) {
	p := m.newParams(q, armoryOneParams)
	realm, name := p.q.Text("realm", armoryRealmMaxBytes), p.q.Text("name", armoryNameMaxBytes)
	if err := p.err(); err != nil {
		return nil, err
	}
	for k, v := range map[string]string{"realm": realm, "name": name} {
		if _, err := required(k, v); err != nil {
			return nil, err
		}
	}
	if !plainName(realm, true) {
		return nil, badChars("realm")
	}
	if !plainName(name, false) {
		return nil, badChars("name")
	}
	as, err := armoryStore(st)
	if err != nil {
		return nil, err
	}
	return as.GetArmoryCharacter(ctx, realm, name)
}

func (m *Module) armoryCharacters(ctx context.Context, st core.Store, q url.Values) (any, error) {
	p := m.newParams(q, armoryManyParams)
	realm, raw := p.q.Text("realm", armoryRealmMaxBytes), p.q.Text("names", armoryNamesMaxBytes)
	if err := p.err(); err != nil {
		return nil, err
	}
	for k, v := range map[string]string{"realm": realm, "names": raw} {
		if _, err := required(k, v); err != nil {
			return nil, err
		}
	}
	names := strings.Split(raw, ",")
	if len(names) > domain.ArmoryBatchMax {
		return nil, fmt.Errorf("%w: names: more than %d names", domain.ErrInvalid, domain.ArmoryBatchMax)
	}
	for i, n := range names {
		if n = strings.TrimSpace(n); n == "" || len(n) > armoryNameMaxBytes {
			return nil, fmt.Errorf("%w: names: entry %d is empty or too long", domain.ErrInvalid, i+1)
		}
		if !plainName(n, false) {
			return nil, badChars("names")
		}
		names[i] = n
	}
	if !plainName(realm, true) {
		return nil, badChars("realm")
	}
	as, err := armoryStore(st)
	if err != nil {
		return nil, err
	}
	chars, err := as.ListArmoryCharacters(ctx, realm, names)
	if err != nil {
		return nil, err
	}
	if chars == nil {
		chars = []domain.ArmoryCharacter{}
	}
	return map[string]any{"characters": chars}, nil
}
