package audit

import (
	"sync"
	"time"
)

// sampler keeps the log from flooding: the first occurrence of an (event, ip, class, reason) in a
// window is written at once, further ones are only counted and written as one summary line
// (count = how many) when the window ends. Memory is bounded: at most maxKeys windows are open;
// beyond that the address is dropped from the key so an address-rotating flood shares one window.
// A global ceiling on lines per second protects the disk; what it refuses is summed into an
// audit_overflow line.
type sampler struct {
	mu       sync.Mutex
	now      func() time.Time
	window   time.Duration
	maxKeys  int
	perSec   int
	out      func(record)
	open     map[record]*window // key: the record with its count zeroed
	second   int64
	inSecond int
	overflow int
	lines    int64
	dropped  int64
}

type window struct {
	start      time.Time
	suppressed int
}

func newSampler(now func() time.Time, win time.Duration, maxKeys, perSec int, out func(record)) *sampler {
	return &sampler{now: now, window: win, maxKeys: maxKeys, perSec: perSec, out: out, open: map[record]*window{}}
}

func keyOf(r record) record { r.count = 0; return r }

// add offers one occurrence (r.count of them).
func (s *sampler) add(r record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	k := keyOf(r)
	w, ok := s.open[k]
	if ok && now.Sub(w.start) < s.window {
		w.suppressed += r.count
		return
	}
	if ok { // window over: summarise it, then start a new one
		s.summarise(k, w)
		delete(s.open, k)
	} else if len(s.open) >= s.maxKeys {
		s.flushLocked(now)
		if len(s.open) >= s.maxKeys {
			k.ip, r.ip = "", ""
			if w2, ok2 := s.open[k]; ok2 && now.Sub(w2.start) < s.window {
				w2.suppressed += r.count
				return
			}
		}
	}
	s.open[k] = &window{start: now}
	s.emit(r)
}

func (s *sampler) summarise(k record, w *window) {
	if w.suppressed > 0 {
		k.count = w.suppressed
		s.emit(k)
	}
}

// flush writes summaries of windows that ended and forgets them.
func (s *sampler) flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushLocked(s.now())
	if s.overflow > 0 {
		n := s.overflow
		s.overflow = 0
		s.write(record{event: "audit_overflow", reason: "line_budget", count: n})
	}
}

// flushAll writes every pending summary regardless of age (shutdown).
func (s *sampler) flushAll() {
	s.mu.Lock()
	for k, w := range s.open {
		s.summarise(k, w)
		delete(s.open, k)
	}
	s.mu.Unlock()
	s.flush()
}

func (s *sampler) flushLocked(now time.Time) {
	for k, w := range s.open {
		if now.Sub(w.start) >= s.window {
			s.summarise(k, w)
			delete(s.open, k)
		}
	}
}

// emit applies the lines-per-second ceiling.
func (s *sampler) emit(r record) {
	sec := s.now().Unix()
	if sec != s.second {
		s.second, s.inSecond = sec, 0
	}
	if s.inSecond >= s.perSec {
		s.overflow += r.count
		s.dropped++
		return
	}
	s.inSecond++
	s.write(r)
}

func (s *sampler) write(r record) {
	s.lines++
	s.out(r)
}

// stats returns lines written and lines refused by the ceiling.
func (s *sampler) stats() (lines, dropped int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lines, s.dropped
}
