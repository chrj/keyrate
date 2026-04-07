# keyrate

Per-key rate limiting for Go, built on top of [`golang.org/x/time/rate`](https://pkg.go.dev/golang.org/x/time/rate).

Attach an independent token-bucket limiter to any comparable key — IP address, user ID, API token, or anything else — with optional LRU and TTL eviction to bound memory use.

## Installation

```
go get github.com/chrj/keyrate
```

## Usage

```go
// 10 requests per second, burst of 20, keyed by IP address.
limiters := keyrate.New[netip.Addr](rate.Every(100*time.Millisecond), 20)

func handler(w http.ResponseWriter, r *http.Request) {
    ip := mustParseAddr(r.RemoteAddr)
    if !limiters.Allow(ip) {
        http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
        return
    }
    // ...
}
```

The key type is a generic type parameter constrained to `comparable`, so any comparable Go type works: `string`, `int`, `netip.Addr`, a struct, etc.

## API

### Creating a Limiters

```go
// New returns a Limiters where each key gets its own token-bucket limiter
// with the given rate and burst. Options configure eviction (see below).
func New[K comparable](r rate.Limit, burst int, opts ...Option) *Limiters[K]
```

`rate.Limit` is a `float64` representing tokens per second. Use `rate.Every(d)` to express it as a duration between events, or `rate.Inf` for no limit.

### Core methods

```go
// Allow reports whether the key's limiter permits one event right now.
// Shorthand for Get(key).Allow().
func (m *Limiters[K]) Allow(key K) bool

// AllowN reports whether the key's limiter permits n events at time t.
func (m *Limiters[K]) AllowN(key K, t time.Time, n int) bool

// Get returns the *rate.Limiter for key, creating one on first use.
// Use this when you need Reserve or Wait instead of Allow.
func (m *Limiters[K]) Get(key K) *rate.Limiter

// Has reports whether key currently has an active limiter.
// Unlike Get, it does not create one if absent.
func (m *Limiters[K]) Has(key K) bool

// Delete removes the limiter for key. The next access starts fresh
// with a full burst. No-op if the key is not present.
func (m *Limiters[K]) Delete(key K)

// Len returns the number of keys currently tracked.
func (m *Limiters[K]) Len() int

// Stop halts the background eviction goroutine. Safe to call multiple
// times. No-op when neither WithTTL nor WithAutoEvict is used.
func (m *Limiters[K]) Stop()
```

### Forwarded methods

These methods delegate directly to the underlying `*rate.Limiter` for each key, creating the limiter on first use. See the [`rate.Limiter` documentation](https://pkg.go.dev/golang.org/x/time/rate#Limiter) for full details.

```go
// Burst returns the burst size of key's limiter.
func (m *Limiters[K]) Burst(key K) int

// Limit returns the rate limit of key's limiter.
func (m *Limiters[K]) Limit(key K) rate.Limit

// Tokens returns the number of tokens available in key's limiter right now.
func (m *Limiters[K]) Tokens(key K) float64

// TokensAt returns the number of tokens available in key's limiter at time t.
func (m *Limiters[K]) TokensAt(key K, t time.Time) float64

// SetBurst updates the burst size of key's limiter.
func (m *Limiters[K]) SetBurst(key K, newBurst int)

// SetBurstAt updates the burst size of key's limiter as of time t.
func (m *Limiters[K]) SetBurstAt(key K, t time.Time, newBurst int)

// SetLimit updates the rate limit of key's limiter.
func (m *Limiters[K]) SetLimit(key K, newLimit rate.Limit)

// SetLimitAt updates the rate limit of key's limiter as of time t.
func (m *Limiters[K]) SetLimitAt(key K, t time.Time, newLimit rate.Limit)

// Reserve returns a *rate.Reservation for one event from key's limiter.
func (m *Limiters[K]) Reserve(key K) *rate.Reservation

// ReserveN returns a *rate.Reservation for n events at time t from key's limiter.
func (m *Limiters[K]) ReserveN(key K, t time.Time, n int) *rate.Reservation

// Wait blocks until key's limiter permits one event, or ctx is done.
func (m *Limiters[K]) Wait(ctx context.Context, key K) error

// WaitN blocks until key's limiter permits n events, or ctx is done.
func (m *Limiters[K]) WaitN(ctx context.Context, key K, n int) error
```

## Eviction

Without eviction the map grows unboundedly — one entry per distinct key seen. The three eviction options can be combined freely.

### WithMaxSize(n int) — LRU cap

Keeps at most `n` keys. When a new key would exceed the cap, the least-recently-used key is dropped. Eviction is synchronous on insert; no background goroutine is started.

```go
// At most 10 000 IPs in memory at once.
limiters := keyrate.New[netip.Addr](rate.Every(time.Second), 10,
    keyrate.WithMaxSize(10_000),
)
```

### WithTTL(d time.Duration) — idle TTL

Evicts keys that have not been accessed for at least `d`. A background goroutine sweeps every `d/2`. Call `Stop()` when the Limiters is no longer needed.

```go
limiters := keyrate.New[string](rate.Every(time.Second), 5,
    keyrate.WithTTL(10*time.Minute),
)
defer limiters.Stop()
```

Accessing a key resets its idle timer, so active keys are never evicted.

### WithAutoEvict() — semantic TTL

Derives the TTL automatically as `burst ÷ r` — the time needed to fully refill an empty bucket. Once a bucket is full it is indistinguishable from a freshly created one, so eviction is semantically free: no observable difference to callers. This is a no-op when `r` is `rate.Inf` or `burst` is `0`.

```go
// rate=1/s, burst=60 → auto TTL = 60s
limiters := keyrate.New[string](rate.Every(time.Second), 60,
    keyrate.WithAutoEvict(),
)
defer limiters.Stop()
```

### Combining options

```go
// Hard cap of 50 000 keys; also sweep out idle keys after 5 minutes.
limiters := keyrate.New[netip.Addr](rate.Every(time.Second), 100,
    keyrate.WithMaxSize(50_000),
    keyrate.WithTTL(5*time.Minute),
)
defer limiters.Stop()
```

LRU and TTL work independently: LRU evicts synchronously on insert when the cap is reached; TTL evicts asynchronously in the background.

## Using the underlying rate.Limiter

`Allow` and `AllowN` handle the common case. The full `rate.Limiter` API is exposed directly on `Limiters`, with the key as the first argument (or second for methods that take a `context.Context`):

```go
// Block until the event is permitted, or the context is cancelled.
if err := limiters.Wait(r.Context(), userID); err != nil {
    http.Error(w, "cancelled", http.StatusRequestTimeout)
    return
}

// Consume n tokens, returning how long to wait before proceeding.
res := limiters.ReserveN(userID, time.Now(), n)
if !res.OK() {
    // n exceeds the burst; this reservation can never be satisfied.
}
time.Sleep(res.Delay())

// Inspect or adjust a key's limiter at runtime.
limiters.SetLimit(userID, newRate)
limiters.SetBurst(userID, newBurst)
fmt.Println(limiters.Tokens(userID))
```

`Get(key)` is also available when you need to pass a `*rate.Limiter` directly to another function.

## Reservations and eviction

`Reserve`, `ReserveN`, `Wait`, and `WaitN` all operate on the limiter that exists at the moment of the call. If the key is evicted — by `WithTTL`, `WithAutoEvict`, `WithMaxSize`, or `Delete` — while a reservation is outstanding or a `Wait` is blocking, the following happens:

The call completes against the now-detached limiter: the caller proceeds normally. The next access to the same key creates a fresh limiter with a full burst, unaware of the in-flight reservation or wait. Both the original caller and new callers may proceed concurrently, causing one-time over-admission for that key.

Calling `Cancel()` on a stale reservation refunds tokens to the detached limiter, not to the replacement — the refund has no effect on live rate limiting.

`Allow` and `AllowN` are not affected: the token is consumed atomically before any eviction can occur.

**Recommendation:** prefer `Allow`/`AllowN` when eviction is active. Use `Reserve`/`Wait` only when you can accept the over-admission edge case or when eviction is infrequent relative to reservation lifetimes.
