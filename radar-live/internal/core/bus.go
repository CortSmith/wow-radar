package core

import (
	"fmt"
	"log/slog"
	"sync"
)

// Bus is a tiny synchronous publish/subscribe channel keyed by topic. Payload types are checked
// at delivery: a subscriber only receives payloads of its own type.
type Bus struct {
	log  *slog.Logger
	mu   sync.RWMutex
	next int
	subs map[string][]subscription
}

type subscription struct {
	id      int
	deliver func(payload any)
}

// NewBus creates a bus that reports subscriber panics and type mismatches to log.
func NewBus(log *slog.Logger) *Bus {
	return &Bus{log: log, subs: map[string][]subscription{}}
}

// Subscribe registers fn for payloads of type T published on topic and returns a cancel function.
func Subscribe[T any](b *Bus, topic string, fn func(T)) (cancel func()) {
	return b.add(topic, func(payload any) {
		typed, ok := payload.(T)
		if !ok {
			b.log.Warn("bus type mismatch", "topic", topic, "got", fmt.Sprintf("%T", payload))
			return
		}
		fn(typed)
	})
}

// Publish delivers payload to every subscriber of topic, in subscription order. A panicking
// subscriber is logged and does not affect the others.
func Publish[T any](b *Bus, topic string, payload T) {
	b.mu.RLock()
	subs := append([]subscription(nil), b.subs[topic]...)
	b.mu.RUnlock()
	for _, s := range subs {
		b.safeDeliver(topic, s, payload)
	}
}

func (b *Bus) safeDeliver(topic string, s subscription, payload any) {
	defer func() {
		if p := recover(); p != nil {
			b.log.Error("bus subscriber panic", "topic", topic, "panic", fmt.Sprint(p))
		}
	}()
	s.deliver(payload)
}

func (b *Bus) add(topic string, deliver func(any)) (cancel func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.next++
	id := b.next
	b.subs[topic] = append(b.subs[topic], subscription{id: id, deliver: deliver})
	return func() { b.remove(topic, id) }
}

func (b *Bus) remove(topic string, id int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	kept := b.subs[topic][:0:0]
	for _, s := range b.subs[topic] {
		if s.id != id {
			kept = append(kept, s)
		}
	}
	b.subs[topic] = kept
}
