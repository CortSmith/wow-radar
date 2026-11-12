package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"radarlive/internal/domain"
)

// Parser consumes parsed events. The event type belongs to the parsing modules; the core only
// passes it along, which keeps it ignorant of log formats.
type Parser interface {
	Name() string
	Parse(ctx context.Context, event any) error
}

// Parsers is the socket extractors plug into; each fed event reaches every parser.
type Parsers struct {
	mu   sync.RWMutex
	list []Parser
}

// NewParsers creates an empty socket.
func NewParsers() *Parsers { return &Parsers{} }

// Add registers a parser.
func (p *Parsers) Add(parser Parser) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.list = append(p.list, parser)
}

// Names lists registered parsers in registration order.
func (p *Parsers) Names() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	names := make([]string, len(p.list))
	for i, parser := range p.list {
		names[i] = parser.Name()
	}
	return names
}

// Feed offers event to every parser; one failing parser does not stop the others.
func (p *Parsers) Feed(ctx context.Context, event any) error {
	p.mu.RLock()
	list := slices.Clone(p.list)
	p.mu.RUnlock()
	var errs []error
	for _, parser := range list {
		if err := parser.Parse(ctx, event); err != nil {
			errs = append(errs, fmt.Errorf("parser %q: %w", parser.Name(), err))
		}
	}
	return errors.Join(errs...)
}

// Sink receives the pings that extractors produce.
type Sink interface {
	Name() string
	Accept(ctx context.Context, ping domain.Ping) error
}

// Sinks is the socket pings are published to.
type Sinks struct {
	mu   sync.RWMutex
	list []Sink
}

// NewSinks creates an empty socket.
func NewSinks() *Sinks { return &Sinks{} }

// Add registers a sink.
func (s *Sinks) Add(sink Sink) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.list = append(s.list, sink)
}

// Len returns the number of registered sinks.
func (s *Sinks) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.list)
}

// Publish hands ping to every sink; one failing sink does not stop the others.
func (s *Sinks) Publish(ctx context.Context, ping domain.Ping) error {
	s.mu.RLock()
	list := slices.Clone(s.list)
	s.mu.RUnlock()
	var errs []error
	for _, sink := range list {
		if err := sink.Accept(ctx, ping); err != nil {
			errs = append(errs, fmt.Errorf("sink %q: %w", sink.Name(), err))
		}
	}
	return errors.Join(errs...)
}
