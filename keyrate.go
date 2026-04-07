// Package keyrate extends golang.org/x/time/rate with per-key rate limiters,
// suitable for per-IP, per-user, or any other keyed rate limiting.
package keyrate

import (
	"sync"

	"golang.org/x/time/rate"
)

// Map holds an independent [rate.Limiter] for each distinct key.
// The zero value is not usable; create one with [New].
type Map[K comparable] struct {
	mu      sync.Mutex
	entries map[K]*rate.Limiter
	limit   rate.Limit
	burst   int
}

// New returns a Map whose per-key limiters allow up to burst events
// instantaneously, then refill at r events per second.
func New[K comparable](r rate.Limit, burst int) *Map[K] {
	return &Map[K]{
		entries: make(map[K]*rate.Limiter),
		limit:   r,
		burst:   burst,
	}
}

// Limiter returns the [rate.Limiter] for key, creating one on first use.
// The returned limiter can be used directly for Reserve/Wait calls.
func (m *Map[K]) Limiter(key K) *rate.Limiter {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.entries[key]
	if !ok {
		l = rate.NewLimiter(m.limit, m.burst)
		m.entries[key] = l
	}
	return l
}

// Allow reports whether key's limiter permits one event right now.
// It is shorthand for m.Limiter(key).Allow().
func (m *Map[K]) Allow(key K) bool {
	return m.Limiter(key).Allow()
}

// Delete removes the limiter for key. The next call for that key
// starts fresh with a full burst. A no-op if key is not present.
func (m *Map[K]) Delete(key K) {
	m.mu.Lock()
	delete(m.entries, key)
	m.mu.Unlock()
}

// Len returns the number of keys currently tracked.
func (m *Map[K]) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}
