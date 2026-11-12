package sqlstore

import (
	"context"

	"radarlive/internal/domain"
)

// The lazyStore forwards the optional core.ArmoryStore methods so hosts that type-assert the
// registered store find them.

func (l *lazyStore) ApplyArmoryBatch(ctx context.Context, b domain.ArmoryBatch) (domain.ArmoryApplyResult, error) {
	s, err := l.get()
	if err != nil {
		return domain.ArmoryApplyResult{}, err
	}
	return s.ApplyArmoryBatch(ctx, b)
}

func (l *lazyStore) GetArmoryCharacter(ctx context.Context, realm, name string) (domain.ArmoryCharacter, error) {
	s, err := l.get()
	if err != nil {
		return domain.ArmoryCharacter{}, err
	}
	return s.GetArmoryCharacter(ctx, realm, name)
}

func (l *lazyStore) ListArmoryCharacters(ctx context.Context, realm string, names []string) ([]domain.ArmoryCharacter, error) {
	s, err := l.get()
	if err != nil {
		return nil, err
	}
	return s.ListArmoryCharacters(ctx, realm, names)
}

func (l *lazyStore) ArmorySnapshots(ctx context.Context, realm, name string) ([]domain.ArmorySnapshot, error) {
	s, err := l.get()
	if err != nil {
		return nil, err
	}
	return s.ArmorySnapshots(ctx, realm, name)
}

func (l *lazyStore) PruneArmorySnapshots(ctx context.Context, olderThanDay int64) (int, error) {
	s, err := l.get()
	if err != nil {
		return 0, err
	}
	return s.PruneArmorySnapshots(ctx, olderThanDay)
}

func (l *lazyStore) RecordArmoryRun(ctx context.Context, r domain.ArmoryRun) error {
	s, err := l.get()
	if err != nil {
		return err
	}
	return s.RecordArmoryRun(ctx, r)
}

func (l *lazyStore) LastArmoryRun(ctx context.Context) (domain.ArmoryRun, error) {
	s, err := l.get()
	if err != nil {
		return domain.ArmoryRun{}, err
	}
	return s.LastArmoryRun(ctx)
}

func (l *lazyStore) ArmorySeeds(ctx context.Context, cursor string, limit int) (domain.ArmorySeedPage, error) {
	s, err := l.get()
	if err != nil {
		return domain.ArmorySeedPage{}, err
	}
	return s.ArmorySeeds(ctx, cursor, limit)
}
