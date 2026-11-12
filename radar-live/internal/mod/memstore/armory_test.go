package memstore

import (
	"testing"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/mod/storetest"
)

// TestArmoryContract runs the shared ArmoryStore suite.
func TestArmoryContract(t *testing.T) {
	storetest.RunArmory(t, func(now func() time.Time) core.Store { return New(now) })
}

func TestStoreImplementsArmoryStore(t *testing.T) {
	var _ core.ArmoryStore = (*Store)(nil)
}

func TestStoreImplementsArmorySeedStore(t *testing.T) {
	var _ core.ArmorySeedStore = (*Store)(nil)
}
