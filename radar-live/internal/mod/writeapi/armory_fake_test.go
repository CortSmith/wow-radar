package writeapi_test

import (
	"context"
	"sync"

	"radarlive/internal/domain"
)

// fakeArmory is a minimal in-test core.ArmoryStore on top of fakeStore (modules must not import
// other modules, so the real memstore is not used here).
type fakeArmory struct {
	*fakeStore
	amu   sync.Mutex
	chars map[string]domain.ArmoryCharacter
	seen  map[string]bool
	run   *domain.ArmoryRun
}

func newFakeArmory() *fakeArmory {
	return &fakeArmory{fakeStore: &fakeStore{seen: map[string]bool{}}, chars: map[string]domain.ArmoryCharacter{},
		seen: map[string]bool{}}
}

func akey(realm, name string) string {
	return domain.ArmoryRealmKey(realm) + "/" + domain.ArmoryNameKey(name)
}

func (f *fakeArmory) ApplyArmoryBatch(ctx context.Context, b domain.ArmoryBatch) (domain.ArmoryApplyResult, error) {
	if err := ctx.Err(); err != nil {
		return domain.ArmoryApplyResult{}, err
	}
	if err := b.Validate(); err != nil {
		return domain.ArmoryApplyResult{}, err
	}
	f.amu.Lock()
	defer f.amu.Unlock()
	if f.seen[b.BatchID] {
		return domain.ArmoryApplyResult{Duplicate: true}, nil
	}
	f.seen[b.BatchID] = true
	for _, c := range b.Characters {
		f.chars[akey(c.Realm, c.Name)] = c
	}
	return domain.ArmoryApplyResult{Stored: len(b.Characters)}, nil
}

func (f *fakeArmory) GetArmoryCharacter(_ context.Context, realm, name string) (domain.ArmoryCharacter, error) {
	f.amu.Lock()
	defer f.amu.Unlock()
	c, ok := f.chars[akey(realm, name)]
	if !ok {
		return c, domain.ErrNotFound
	}
	return c, nil
}

func (f *fakeArmory) ListArmoryCharacters(context.Context, string, []string) ([]domain.ArmoryCharacter, error) {
	return nil, nil
}

func (f *fakeArmory) ArmorySnapshots(context.Context, string, string) ([]domain.ArmorySnapshot, error) {
	return nil, nil
}

func (f *fakeArmory) PruneArmorySnapshots(context.Context, int64) (int, error) { return 0, nil }

func (f *fakeArmory) RecordArmoryRun(_ context.Context, r domain.ArmoryRun) error {
	f.amu.Lock()
	defer f.amu.Unlock()
	f.run = &r
	return nil
}

func (f *fakeArmory) LastArmoryRun(context.Context) (domain.ArmoryRun, error) {
	f.amu.Lock()
	defer f.amu.Unlock()
	if f.run == nil {
		return domain.ArmoryRun{}, domain.ErrNotFound
	}
	return *f.run, nil
}
