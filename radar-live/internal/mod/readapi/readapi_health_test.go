package readapi_test

import (
	"net/http/httptest"
	"testing"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/domain"
	"radarlive/internal/mod/readapi"
)

// healthStore is a fake store that also reports data-integrity health.
type healthStore struct {
	*fakeStore
	h domain.Health
}

func (s healthStore) Health() domain.Health { return s.h }

func healthRig(t *testing.T, h domain.Health) *httptest.ResponseRecorder {
	t.Helper()
	host := core.NewHost()
	if err := host.Stores.Add("fake", healthStore{&fakeStore{}, h}); err != nil {
		t.Fatal(err)
	}
	if err := host.Register(readapi.NewModule(readapi.WithClock(func() time.Time { return t0 }))); err != nil {
		t.Fatal(err)
	}
	if err := host.Config.Load(nil, func(string) (string, bool) { return "", false }); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	host.Routes.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/status", nil))
	return rec
}

func TestStatusReportsIntegrityAndLastCheck(t *testing.T) {
	for _, tc := range []struct {
		name string
		h    domain.Health
		want string
	}{
		{"ok", domain.Health{Integrity: domain.IntegrityOK, LastCheck: 1234}, "ok"},
		{"degraded", domain.Health{Integrity: domain.IntegrityDegraded, LastCheck: 99, Full: true}, "degraded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := healthRig(t, tc.h)
			var st struct {
				Integrity string
				LastCheck int64
				Full      bool
			}
			decode(t, rec, &st)
			if st.Integrity != tc.want || st.LastCheck != tc.h.LastCheck || st.Full != tc.h.Full {
				t.Fatalf("status = %s", rec.Body)
			}
		})
	}
}

func TestStatusWithoutHealthOmitsIntegrity(t *testing.T) {
	r := newRig(t)
	var st map[string]any
	decode(t, r.get("GET", "/api/v1/status"), &st)
	if _, ok := st["integrity"]; ok {
		t.Fatalf("a store without Health must not claim integrity: %v", st)
	}
}
