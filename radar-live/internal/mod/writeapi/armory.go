package writeapi

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"radarlive/internal/core"
	"radarlive/internal/domain"
	"radarlive/internal/validate"
)

// Armory JSON shape limits: a batch is a few levels deeper than a ping (characters, character,
// blob, slot, list, item) and holds up to domain.ArmoryBatchMax characters.
const (
	armoryMaxDepth = 12
	armoryMaxArray = 1000
)

// initArmory registers the armory sync routes on the internal listener behind the ingest role.
// They answer 404 when the default store does not implement core.ArmoryStore.
func (m *Module) initArmory() error {
	routes := []struct {
		method, path string
		fn           http.HandlerFunc
	}{
		{http.MethodPost, ArmoryBatchPath, m.armoryBatch},
		{http.MethodPost, ArmoryRunPath, m.armoryRunPost},
		{http.MethodGet, ArmoryRunPath, m.armoryRunGet},
		{http.MethodGet, ArmorySeedsPath, m.armorySeeds},
	}
	for _, rt := range routes {
		guarded := m.host.Auth.Require(core.RoleIngest, rt.fn)
		if err := m.host.Routes.HandleFuncOn(core.ClassInternal, rt.method, rt.path, guarded.ServeHTTP); err != nil {
			return fmt.Errorf("writeapi: %w", err)
		}
	}
	return nil
}

// armoryStore returns the default store's armory half; ok is false after the 404 was written.
func (m *Module) armoryStore(w http.ResponseWriter, r *http.Request) (core.ArmoryStore, bool) {
	st, err := m.host.Stores.Default()
	if err != nil {
		m.fail(w, r, "armory", fmt.Errorf("default store: %w", err))
		return nil, false
	}
	as, ok := st.(core.ArmoryStore)
	if !ok {
		core.WriteError(w, http.StatusNotFound, "not_found", "armory data is not available on this server")
		return nil, false
	}
	return as, true
}

func (m *Module) decodeArmory(w http.ResponseWriter, r *http.Request, dst any, what string) error {
	body := http.MaxBytesReader(w, r.Body, int64(m.host.Config.Int(core.SettingMaxBody)))
	lim := validate.JSONLimits{MaxDepth: armoryMaxDepth, MaxArray: armoryMaxArray}
	if err := validate.DecodeJSON(body, dst, lim); err != nil {
		return wrapBody(err, what)
	}
	return nil
}

func (m *Module) armoryBatch(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	as, ok := m.armoryStore(w, r)
	if !ok {
		return
	}
	var batch domain.ArmoryBatch
	if err := m.decodeArmory(w, r, &batch, "armory batch"); err != nil {
		m.fail(w, r, "armory", err)
		return
	}
	batch = batch.Sanitise()
	if err := batch.Validate(); err != nil {
		m.fail(w, r, "armory", err)
		return
	}
	res, err := as.ApplyArmoryBatch(r.Context(), batch)
	if err != nil {
		m.fail(w, r, "armory", err)
		return
	}
	core.WriteJSON(w, http.StatusOK, res)
}

func (m *Module) armoryRunPost(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	as, ok := m.armoryStore(w, r)
	if !ok {
		return
	}
	var run domain.ArmoryRun
	if err := m.decodeArmory(w, r, &run, "armory run"); err != nil {
		m.fail(w, r, "armory", err)
		return
	}
	if err := run.Validate(); err != nil {
		m.fail(w, r, "armory", err)
		return
	}
	if err := as.RecordArmoryRun(r.Context(), run); err != nil {
		m.fail(w, r, "armory", err)
		return
	}
	core.WriteJSON(w, http.StatusOK, run)
}

func (m *Module) armoryRunGet(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	as, ok := m.armoryStore(w, r)
	if !ok {
		return
	}
	run, err := as.LastArmoryRun(r.Context())
	if err != nil {
		m.fail(w, r, "armory", err)
		return
	}
	core.WriteJSON(w, http.StatusOK, run)
}

// seedParams parses the strict query of the seed feed: only cursor and limit, each at most once.
// The cursor is validated by the domain, so hostile text never reaches a store.
func seedParams(r *http.Request) (cursor string, limit int, err error) {
	limit = domain.ArmorySeedLimitDefault
	q, perr := url.ParseQuery(r.URL.RawQuery)
	if perr != nil || len(r.URL.RawQuery) > 2*domain.ArmoryCursorMax+64 {
		return "", 0, fmt.Errorf("%w: malformed query", domain.ErrInvalid)
	}
	for k, v := range q {
		if (k != "cursor" && k != "limit") || len(v) != 1 {
			return "", 0, fmt.Errorf("%w: unknown or repeated parameter", domain.ErrInvalid)
		}
	}
	if v, ok := q["limit"]; ok {
		n, aerr := strconv.Atoi(v[0])
		if aerr != nil {
			return "", 0, fmt.Errorf("%w: limit must be a number", domain.ErrInvalid)
		}
		limit = n
	}
	if err := domain.ValidateArmorySeedLimit(limit); err != nil {
		return "", 0, err
	}
	cursor = q.Get("cursor")
	if _, _, _, err := domain.ParseArmorySeedCursor(cursor); err != nil {
		return "", 0, err
	}
	return cursor, limit, nil
}

func (m *Module) armorySeeds(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	as, ok := m.armoryStore(w, r)
	if !ok {
		return
	}
	ss, ok := as.(core.ArmorySeedStore)
	if !ok {
		core.WriteError(w, http.StatusNotFound, "not_found", "armory seeds are not available on this server")
		return
	}
	cursor, limit, err := seedParams(r)
	if err != nil {
		m.fail(w, r, "armory", err)
		return
	}
	page, err := ss.ArmorySeeds(r.Context(), cursor, limit)
	if err != nil {
		m.fail(w, r, "armory", err)
		return
	}
	if page.Items == nil {
		page.Items = []domain.ArmorySeed{}
	}
	core.WriteJSON(w, http.StatusOK, page)
}
