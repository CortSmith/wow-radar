package armorysync

import (
	"context"
	"strconv"
	"strings"

	"radarlive/internal/domain"
)

// The crawl walks the seed feed page by page. One page is processed as consecutive "units": a run of
// character seeds is one unit, and every guild seed is its own unit (its roster members are crawled as
// characters). A checkpoint is written after every unit, so the cursor is "seed page cursor" plus "number
// of items of that page already done". The server's cursor is opaque and may contain any text, so the
// position is appended after a '#': base + "#" + n, parsed from the LAST '#'.

// formatCursor builds the checkpoint cursor for position n inside the page that starts at base.
// The start of the feed (base "" and n 0) is the empty cursor.
func formatCursor(base string, n int) string {
	if base == "" && n == 0 {
		return ""
	}
	return base + "#" + strconv.Itoa(n)
}

// parseCursor splits a checkpoint cursor. Text without a valid "#digits" suffix is taken as a plain seed
// cursor at position 0.
func parseCursor(c string) (base string, n int) {
	i := strings.LastIndexByte(c, '#')
	if i < 0 {
		return c, 0
	}
	digits := c[i+1:]
	if digits == "" || len(digits) > 6 {
		return c, 0
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return c, 0
		}
	}
	n, _ = strconv.Atoi(digits)
	return c[:i], n
}

// dedupeKey identifies a character within a run: realm spelling ("Area 52", "area-52", "AREA52") and name
// case do not matter.
func dedupeKey(realm, name string) string {
	r := strings.NewReplacer(" ", "", "-", "", "'", "", "’", "").Replace(strings.ToLower(realm))
	return r + "|" + strings.ToLower(name)
}

// queue adds ref to refs unless it was already queued in this run.
func (st *runState) queue(refs []CharRef, ref CharRef) []CharRef {
	k := dedupeKey(ref.Realm, ref.Name)
	if st.seen[k] {
		return refs
	}
	st.seen[k] = true
	st.run.Enumerated++
	return append(refs, ref)
}

// realmOf applies the default realm; "" means the seed cannot be used.
func (st *runState) realmOf(realm string) string {
	if realm != "" {
		return realm
	}
	return st.s.cfg.DefaultRealm
}

// crawl walks the seed pages from the saved cursor until done, a budget, cancel or an error.
func (st *runState) crawl(ctx context.Context) end {
	base, skip := parseCursor(st.run.Cursor)
	for {
		if st.budget.timeUp() {
			return st.stopped(ctx)
		}
		page, e := st.seedPage(ctx, base)
		if e != nil {
			return *e
		}
		items := page.Items
		nextPage := ""
		if page.Next != "" {
			nextPage = formatCursor(page.Next, 0)
		}
		i := min(skip, len(items))
		if i >= len(items) && page.Next != "" {
			if err := st.checkpoint(ctx, nextPage); err != nil {
				return end{status: StatusPartial, note: "checkpoint failed: " + err.Error()}
			}
		}
		for i < len(items) {
			start := formatCursor(base, i)
			j := i + 1
			var refs []CharRef
			if items[i].Kind == domain.ArmorySeedGuild {
				var e *end
				if refs, e = st.expandGuild(ctx, items[i]); e != nil {
					return *e
				}
			} else {
				for j = i; j < len(items) && items[j].Kind != domain.ArmorySeedGuild; j++ {
					refs = st.queueSeed(refs, items[j])
				}
			}
			after := nextPage
			if j < len(items) {
				after = formatCursor(base, j)
			}
			if e := st.process(ctx, refs, start, after); e != nil {
				return *e
			}
			i = j
		}
		if page.Next == "" {
			return end{status: StatusOK}
		}
		base, skip = page.Next, 0
	}
}

// queueSeed adds a character seed; unusable seeds (no realm and no default, unknown kind, no name) are
// counted as skipped.
func (st *runState) queueSeed(refs []CharRef, s domain.ArmorySeed) []CharRef {
	realm := st.realmOf(s.Realm)
	if s.Kind != domain.ArmorySeedCharacter || realm == "" || s.Name == "" {
		st.run.Skipped++
		return refs
	}
	return st.queue(refs, CharRef{Realm: realm, Name: s.Name})
}

// expandGuild reads a guild roster (one request) and returns its members, capped, as characters to crawl.
// A roster that cannot be read is counted as failed and the crawl goes on; auth errors end the run.
func (st *runState) expandGuild(ctx context.Context, s domain.ArmorySeed) ([]CharRef, *end) {
	realm := st.realmOf(s.Realm)
	if realm == "" || s.Guild == "" {
		st.run.Skipped++
		return nil, nil
	}
	if !st.budget.take() {
		e := st.stopped(ctx)
		return nil, &e
	}
	members, err := st.s.src.GuildRoster(ctx, realm, s.Guild)
	switch {
	case err == nil:
	case st.s.isAuth(err):
		return nil, authEnd(err)
	case ctx.Err() != nil:
		return nil, &end{status: StatusInterrupted, note: "cancelled"}
	case st.s.isInvalid(err):
		st.run.Skipped++
		return nil, nil
	default:
		st.run.Failed++
		return nil, nil
	}
	members = members[:min(len(members), st.s.cfg.MaxGuildMembers)]
	var refs []CharRef
	for _, m := range members {
		refs = st.queue(refs, m)
	}
	return refs, nil
}

// process crawls one unit: it fetches the characters, stores them and checkpoints at after. When a budget or
// a cancel cut the fetch short, the fetched prefix is stored and the cursor stays at start, so a resume
// repeats at most this unit.
func (st *runState) process(ctx context.Context, refs []CharRef, start, after string) *end {
	chars, complete, fatal := st.fetchPage(ctx, st.filter(ctx, refs))
	if fatal != nil {
		return authEnd(fatal)
	}
	next := after
	if !complete {
		next = start
	}
	if e := st.applyPage(ctx, chars, start, next); e != nil {
		return e
	}
	if !complete {
		e := st.stopped(ctx)
		return &e
	}
	return nil
}

// seedPage reads one page of seeds, retrying with backoff. It does not use the request budget (it is a
// call to radar-db, not to the armory) and it honours cancellation.
func (st *runState) seedPage(ctx context.Context, cursor string) (domain.ArmorySeedPage, *end) {
	var page domain.ArmorySeedPage
	err := st.retryWith(ctx, ctx, func(c context.Context) (err error) {
		page, err = st.s.seeds.Seeds(c, cursor, st.s.cfg.SeedPageSize)
		return err
	})
	switch {
	case err == nil:
		return page, nil
	case st.s.isAuth(err):
		return page, authEnd(err)
	case ctx.Err() != nil:
		return page, &end{status: StatusInterrupted, note: "cancelled"}
	}
	return page, &end{status: StatusPartial, note: "seeds failed: " + err.Error()}
}
