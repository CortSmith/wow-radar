package readapi_test

import (
	"context"

	"radarlive/internal/domain"
)

// fakeArmory is a minimal in-test core.ArmoryStore on top of fakeStore (modules must not import
// other modules, so the real memstore is not used here).
type fakeArmory struct {
	*fakeStore
	chars map[string]domain.ArmoryCharacter
}

func akey(realm, name string) string {
	return domain.ArmoryRealmKey(realm) + "/" + domain.ArmoryNameKey(name)
}

func (f *fakeArmory) ApplyArmoryBatch(context.Context, domain.ArmoryBatch) (domain.ArmoryApplyResult, error) {
	return domain.ArmoryApplyResult{}, nil
}

func (f *fakeArmory) GetArmoryCharacter(_ context.Context, realm, name string) (domain.ArmoryCharacter, error) {
	c, ok := f.chars[akey(realm, name)]
	if !ok {
		return c, domain.ErrNotFound
	}
	return c, nil
}

func (f *fakeArmory) ListArmoryCharacters(_ context.Context, realm string, names []string) ([]domain.ArmoryCharacter, error) {
	var out []domain.ArmoryCharacter
	for _, n := range names {
		if c, ok := f.chars[akey(realm, n)]; ok {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeArmory) ArmorySnapshots(context.Context, string, string) ([]domain.ArmorySnapshot, error) {
	return nil, nil
}

func (f *fakeArmory) PruneArmorySnapshots(context.Context, int64) (int, error) { return 0, nil }

func (f *fakeArmory) RecordArmoryRun(context.Context, domain.ArmoryRun) error { return nil }

func (f *fakeArmory) LastArmoryRun(context.Context) (domain.ArmoryRun, error) {
	return domain.ArmoryRun{}, domain.ErrNotFound
}
