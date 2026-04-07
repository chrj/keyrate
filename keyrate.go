// Package keyrate extends golang.org/x/time/rate with per-key rate limiters,
// suitable for per-IP, per-user, or any other keyed rate limiting.
package keyrate

import (
	"container/list"
	"context"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// entry holds a limiter and its eviction metadata.
type entry[K comparable] struct {
	limiter  *rate.Limiter
	lastUsed time.Time
	lruElem  *list.Element // value = K; nil when LRU is disabled
}

// Limiters holds an independent [rate.Limiter] for each distinct key.
// The zero value is not usable; create one with [New].
type Limiters[K comparable] struct {
	mu      sync.Mutex
	entries map[K]*entry[K]
	limit   rate.Limit
	burst   int
	ttl     time.Duration // 0 = disabled
	maxSize int           // 0 = disabled
	lru     *list.List    // non-nil when maxSize > 0

	done chan struct{}
	once sync.Once
}

// Option configures eviction behaviour for [New].
type Option func(*evictConfig)

type evictConfig struct {
	ttl     time.Duration
	maxSize int
	auto    bool
}

// WithTTL evicts keys that have not been accessed for at least d.
// A background goroutine sweeps every d/2; call [Limiters.Stop] when done.
func WithTTL(d time.Duration) Option {
	return func(c *evictConfig) { c.ttl = d }
}

// WithMaxSize caps the map at n keys, evicting the least-recently-used
// entry on each insert. No background goroutine is started.
func WithMaxSize(n int) Option {
	return func(c *evictConfig) { c.maxSize = n }
}

// WithAutoEvict derives the TTL from the rate parameters: once a bucket has
// been idle long enough to fully refill (burst÷r seconds), the limiter is
// indistinguishable from a fresh one, so eviction is semantically free.
// A background goroutine is started; call [Limiters.Stop] when done.
// WithAutoEvict is a no-op when r is [rate.Inf] or burst is 0.
func WithAutoEvict() Option {
	return func(c *evictConfig) { c.auto = true }
}

// New returns a Limiters whose per-key limiters allow up to burst events
// instantaneously, then refill at r events per second.
func New[K comparable](r rate.Limit, burst int, opts ...Option) *Limiters[K] {
	cfg := &evictConfig{}
	for _, o := range opts {
		o(cfg)
	}
	if cfg.auto && r != rate.Inf && burst > 0 {
		cfg.ttl = time.Duration(float64(burst) / float64(r) * float64(time.Second))
	}

	m := &Limiters[K]{
		entries: make(map[K]*entry[K]),
		limit:   r,
		burst:   burst,
		ttl:     cfg.ttl,
		maxSize: cfg.maxSize,
		done:    make(chan struct{}),
	}
	if cfg.maxSize > 0 {
		m.lru = list.New()
	}
	if m.ttl > 0 {
		go m.sweepLoop()
	}
	return m
}

// Stop halts the background eviction goroutine started by [WithTTL] or
// [WithAutoEvict]. It is safe to call multiple times and is a no-op when
// neither option was used.
func (m *Limiters[K]) Stop() {
	m.once.Do(func() { close(m.done) })
}

// remove deletes key k and removes it from the LRU list if applicable.
// Must be called with m.mu held.
func (m *Limiters[K]) remove(k K, e *entry[K]) {
	if e.lruElem != nil {
		m.lru.Remove(e.lruElem)
	}
	delete(m.entries, k)
}

func (m *Limiters[K]) sweepLoop() {
	interval := max(m.ttl/2, time.Millisecond)
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			now := time.Now()
			m.mu.Lock()
			for k, e := range m.entries {
				if now.Sub(e.lastUsed) >= m.ttl {
					m.remove(k, e)
				}
			}
			m.mu.Unlock()
		case <-m.done:
			return
		}
	}
}

// Get returns the [rate.Limiter] for key, creating one on first use.
// The returned limiter can be used directly for Reserve/Wait calls.
func (m *Limiters[K]) Get(key K) *rate.Limiter {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	if e, ok := m.entries[key]; ok {
		e.lastUsed = now
		if m.lru != nil {
			m.lru.MoveToFront(e.lruElem)
		}
		return e.limiter
	}

	// LRU cap: evict the least-recently-used key before inserting.
	if m.maxSize > 0 && len(m.entries) >= m.maxSize {
		if tail := m.lru.Back(); tail != nil {
			oldest := tail.Value.(K)
			m.remove(oldest, m.entries[oldest])
		}
	}

	l := rate.NewLimiter(m.limit, m.burst)
	e := &entry[K]{limiter: l, lastUsed: now}
	if m.lru != nil {
		e.lruElem = m.lru.PushFront(key)
	}
	m.entries[key] = e
	return l
}

// Allow reports whether key's limiter permits one event right now.
// It is shorthand for m.Get(key).Allow().
func (m *Limiters[K]) Allow(key K) bool {
	return m.Get(key).Allow()
}

// AllowN reports whether key's limiter permits n events at time t.
func (m *Limiters[K]) AllowN(key K, t time.Time, n int) bool {
	return m.Get(key).AllowN(t, n)
}

// Burst returns the burst size of key's limiter.
func (m *Limiters[K]) Burst(key K) int {
	return m.Get(key).Burst()
}

// Limit returns the rate limit of key's limiter.
func (m *Limiters[K]) Limit(key K) rate.Limit {
	return m.Get(key).Limit()
}

// Reserve returns a [rate.Reservation] for one event from key's limiter.
func (m *Limiters[K]) Reserve(key K) *rate.Reservation {
	return m.Get(key).Reserve()
}

// ReserveN returns a [rate.Reservation] for n events at time t from key's limiter.
func (m *Limiters[K]) ReserveN(key K, t time.Time, n int) *rate.Reservation {
	return m.Get(key).ReserveN(t, n)
}

// SetBurst updates the burst size of key's limiter.
func (m *Limiters[K]) SetBurst(key K, newBurst int) {
	m.Get(key).SetBurst(newBurst)
}

// SetBurstAt updates the burst size of key's limiter as of time t.
func (m *Limiters[K]) SetBurstAt(key K, t time.Time, newBurst int) {
	m.Get(key).SetBurstAt(t, newBurst)
}

// SetLimit updates the rate limit of key's limiter.
func (m *Limiters[K]) SetLimit(key K, newLimit rate.Limit) {
	m.Get(key).SetLimit(newLimit)
}

// SetLimitAt updates the rate limit of key's limiter as of time t.
func (m *Limiters[K]) SetLimitAt(key K, t time.Time, newLimit rate.Limit) {
	m.Get(key).SetLimitAt(t, newLimit)
}

// Tokens returns the number of tokens available in key's limiter right now.
func (m *Limiters[K]) Tokens(key K) float64 {
	return m.Get(key).Tokens()
}

// TokensAt returns the number of tokens available in key's limiter at time t.
func (m *Limiters[K]) TokensAt(key K, t time.Time) float64 {
	return m.Get(key).TokensAt(t)
}

// Wait blocks until key's limiter permits one event, or ctx is done.
func (m *Limiters[K]) Wait(ctx context.Context, key K) error {
	return m.Get(key).Wait(ctx)
}

// WaitN blocks until key's limiter permits n events, or ctx is done.
func (m *Limiters[K]) WaitN(ctx context.Context, key K, n int) error {
	return m.Get(key).WaitN(ctx, n)
}

// Has reports whether key has an active limiter without creating one.
func (m *Limiters[K]) Has(key K) bool {
	m.mu.Lock()
	_, ok := m.entries[key]
	m.mu.Unlock()
	return ok
}

// Delete removes the limiter for key. The next call for that key starts
// fresh with a full burst. A no-op if key is not present.
func (m *Limiters[K]) Delete(key K) {
	m.mu.Lock()
	if e, ok := m.entries[key]; ok {
		m.remove(key, e)
	}
	m.mu.Unlock()
}

// Len returns the number of keys currently tracked.
func (m *Limiters[K]) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}
