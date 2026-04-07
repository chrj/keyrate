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

## Eviction

Without eviction the map grows unboundedly — one entry per distinct key seen. The three eviction options can be combined freely.

### `WithMaxSize(n int)` — LRU cap

Keeps at most `n` keys. When a new key would exceed the cap, the least-recently-used key is dropped. Eviction is synchronous on insert; no background goroutine is started.

```go
// At most 10 000 IPs in memory at once.
limiters := keyrate.New[netip.Addr](rate.Every(time.Second), 10,
    keyrate.WithMaxSize(10_000),
)
```

### `WithTTL(d time.Duration)` — idle TTL

Evicts keys that have not been accessed for at least `d`. A background goroutine sweeps every `d/2`. Call `Stop()` when the `Limiters` is no longer needed.

```go
limiters := keyrate.New[string](rate.Every(time.Second), 5,
    keyrate.WithTTL(10*time.Minute),
)
defer limiters.Stop()
```

Accessing a key resets its idle timer, so active keys are never evicted.

### `WithAutoEvict()` — semantic TTL

Derives the TTL automatically as `burst ÷ r` — the time needed to fully refill an empty bucket. Once a bucket is full it is indistinguishable from a freshly created one, so eviction is semantically free: no observable difference to callers. This is a no-op when `r` is `rate.Inf` or `burst` is 0.

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

## Using the underlying `rate.Limiter`

`Allow` handles the common case, but the full `rate.Limiter` API is available via `Get(key)`:

```go
// Wait until the limiter allows the event (respects context cancellation).
if err := limiters.Get(userID).Wait(r.Context()); err != nil {
    http.Error(w, "cancelled", http.StatusRequestTimeout)
    return
}

// Reserve n tokens at once.
r := limiters.Get(userID).ReserveN(time.Now(), n)
if !r.OK() {
    // n exceeds burst; will never be satisfiable.
}
time.Sleep(r.Delay())
```
