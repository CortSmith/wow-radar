package sqlstore

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/mod/storetest"
)

// TestStoreContract runs the shared behavioural suite that every core.Store must pass.
func TestStoreContract(t *testing.T) {
	dir := t.TempDir()
	n := 0
	storetest.Run(t, func(now func() time.Time) core.Store {
		n++
		s, err := Open(filepath.Join(dir, fmt.Sprintf("c%d.db", n)), Options{Now: now})
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		return s
	})
}

// TestArmoryContract runs the shared armory suite against sqlstore.
func TestArmoryContract(t *testing.T) {
	dir := t.TempDir()
	n := 0
	storetest.RunArmory(t, func(now func() time.Time) core.Store {
		n++
		s, err := Open(filepath.Join(dir, fmt.Sprintf("a%d.db", n)), Options{Now: now})
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		return s
	})
}
