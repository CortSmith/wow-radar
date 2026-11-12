// Package feed holds the pieces of the radar-feed client that are not file tailing: a bounded
// line queue, a byte-rate limiter and the streamer that posts lines to radar-ingest.
package feed

import (
	"context"
	"sync"
)

const minQueueLines = 1

// Queue is a bounded FIFO of log lines. When it is full the oldest line is dropped, so a long
// outage costs the oldest history and never memory.
type Queue struct {
	mu      sync.Mutex
	lines   []string
	max     int
	dropped int64
	wake    chan struct{} // capacity 1: a token means "something was pushed"
}

// NewQueue returns a queue holding at most size lines (at least one).
func NewQueue(size int) *Queue {
	return &Queue{max: max(size, minQueueLines), wake: make(chan struct{}, 1)}
}

// Push appends a line, dropping the oldest when the queue is full.
func (q *Queue) Push(line string) {
	q.mu.Lock()
	if len(q.lines) >= q.max {
		q.lines = q.lines[1:]
		q.dropped++
	}
	q.lines = append(q.lines, line)
	q.mu.Unlock()
	q.signal()
}

// PushFront puts a line back at the head (a line that could not be sent). When the queue is full
// the newest line is dropped instead, which keeps the oldest history intact.
func (q *Queue) PushFront(line string) {
	q.mu.Lock()
	if len(q.lines) >= q.max {
		q.lines = q.lines[:len(q.lines)-1]
		q.dropped++
	}
	q.lines = append([]string{line}, q.lines...)
	q.mu.Unlock()
	q.signal()
}

func (q *Queue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// TryPop removes and returns the oldest line without waiting.
func (q *Queue) TryPop() (string, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.lines) == 0 {
		return "", false
	}
	line := q.lines[0]
	q.lines[0] = ""
	q.lines = q.lines[1:]
	return line, true
}

// Pop waits for a line or for ctx to end.
func (q *Queue) Pop(ctx context.Context) (string, error) {
	for {
		if line, ok := q.TryPop(); ok {
			return line, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-q.wake:
		}
	}
}

// Len is the number of queued lines.
func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.lines)
}

// Dropped counts the lines discarded because the queue was full.
func (q *Queue) Dropped() int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.dropped
}
