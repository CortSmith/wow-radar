package memstore

import (
	"context"
	"testing"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/domain"
	"radarlive/internal/mod/storetest"
)

// TestStoreContract runs the shared behavioural suite that every core.Store must pass.
func TestStoreContract(t *testing.T) {
	storetest.Run(t, func(now func() time.Time) core.Store { return New(now) })
}

func TestNewDefaultsToWallClock(t *testing.T) {
	s := New(nil)
	before := time.Now().UnixMilli()
	res, err := s.Sweep(context.Background(), domain.Sector{RS: domain.RulesetPvP, Scope: domain.ScopeAll})
	if err != nil {
		t.Fatal(err)
	}
	if res.Now < before || res.Now > time.Now().UnixMilli() {
		t.Fatalf("Now = %d, want about %d", res.Now, before)
	}
}

func TestBoundedSetEvictsOldestFirst(t *testing.T) {
	s := newBoundedSet(3)
	for _, k := range []string{"a", "b", "c"} {
		if !s.add(k) {
			t.Fatalf("%s should be new", k)
		}
	}
	if s.add("a") {
		t.Fatal("a is a duplicate")
	}
	if !s.add("d") || s.len() != 3 {
		t.Fatalf("len = %d after overflow", s.len())
	}
	if !s.add("a") {
		t.Fatal("a was evicted and must count as new again")
	}
	if s.add("d") {
		t.Fatal("d must still be remembered")
	}
}

func TestModuleRegistersStoreWithHost(t *testing.T) {
	host := core.NewHost()
	if err := host.Register(NewModule()); err != nil {
		t.Fatal(err)
	}
	store, err := host.Stores.Default()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.(*Store); !ok {
		t.Fatalf("default store is %T", store)
	}
	if got := NewModule().Name(); got != ModuleName {
		t.Fatalf("name %q", got)
	}
}

func TestStoreImplementsCoreStore(t *testing.T) {
	var _ core.Store = (*Store)(nil)
}
