package memstore

// boundedSet remembers up to max string keys, forgetting the oldest first.
type boundedSet struct {
	max   int
	keys  map[string]struct{}
	order []string
}

func newBoundedSet(max int) *boundedSet {
	return &boundedSet{max: max, keys: map[string]struct{}{}}
}

// add records key and reports whether it was new.
func (s *boundedSet) add(key string) bool {
	if _, seen := s.keys[key]; seen {
		return false
	}
	s.keys[key] = struct{}{}
	s.order = append(s.order, key)
	if len(s.order) > s.max {
		delete(s.keys, s.order[0])
		s.order = s.order[1:]
	}
	return true
}

// len returns the number of remembered keys.
func (s *boundedSet) len() int { return len(s.keys) }
