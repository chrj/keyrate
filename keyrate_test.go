package keyrate_test

import (
	"net/netip"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"golang.org/x/time/rate"

	"github.com/chrj/keyrate"
)

// newMap returns a plain Limiters with no eviction (1 req/s, burst 2).
func newMap() *keyrate.Limiters[string] {
	return keyrate.New[string](rate.Every(time.Second), 2)
}

// ---- core behaviour (unchanged) ----

func TestKeysAreIndependent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := newMap()

		if !m.Allow("alice") {
			t.Fatal("alice[0]: want allow")
		}
		if !m.Allow("alice") {
			t.Fatal("alice[1]: want allow (burst=2)")
		}
		if m.Allow("alice") {
			t.Fatal("alice[2]: want deny, burst exhausted")
		}
		if !m.Allow("bob") {
			t.Fatal("bob[0]: want allow (independent limiter)")
		}
	})
}

func TestTokensRefillOverTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := keyrate.New[string](rate.Every(time.Second), 1)

		if !m.Allow("alice") {
			t.Fatal("first event: want allow")
		}
		if m.Allow("alice") {
			t.Fatal("immediately after: want deny")
		}
		time.Sleep(time.Second)
		if !m.Allow("alice") {
			t.Fatal("after 1s: want allow")
		}
	})
}

func TestPartialRefill(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// 2 tokens/sec, burst 2 → one token every 500ms.
		m := keyrate.New[string](2, 2)
		m.Allow("x")
		m.Allow("x") // empty
		time.Sleep(500 * time.Millisecond)
		if !m.Allow("x") {
			t.Fatal("after 500ms: want allow (one token refilled)")
		}
		if m.Allow("x") {
			t.Fatal("after consuming refilled token: want deny")
		}
	})
}

func TestDeleteResetsLimiter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := keyrate.New[string](rate.Every(time.Hour), 1)

		m.Allow("alice")
		if m.Allow("alice") {
			t.Fatal("second: want deny")
		}
		m.Delete("alice")
		if m.Len() != 0 {
			t.Fatalf("Len after Delete: got %d, want 0", m.Len())
		}
		if !m.Allow("alice") {
			t.Fatal("after Delete: want allow (fresh limiter)")
		}
	})
}

func TestLimiterReturnedIsSame(t *testing.T) {
	m := newMap()
	l1 := m.Get("k")
	l2 := m.Get("k")
	if l1 != l2 {
		t.Fatal("expected the same *rate.Limiter for the same key")
	}
}

func TestLen(t *testing.T) {
	m := newMap()
	if m.Len() != 0 {
		t.Fatalf("empty: got %d, want 0", m.Len())
	}
	m.Allow("a")
	m.Allow("b")
	if m.Len() != 2 {
		t.Fatalf("after 2 keys: got %d, want 2", m.Len())
	}
	m.Delete("a")
	if m.Len() != 1 {
		t.Fatalf("after Delete: got %d, want 1", m.Len())
	}
}

func TestGenericKeyTypes(t *testing.T) {
	t.Run("int", func(t *testing.T) {
		m := keyrate.New[int](rate.Every(time.Second), 1)
		m.Allow(42)
		if m.Allow(42) {
			t.Fatal("want deny")
		}
		if !m.Allow(99) {
			t.Fatal("different key: want allow")
		}
	})

	t.Run("netip.Addr", func(t *testing.T) {
		m := keyrate.New[netip.Addr](rate.Every(time.Second), 1)
		ip1 := netip.MustParseAddr("192.0.2.1")
		ip2 := netip.MustParseAddr("192.0.2.2")
		m.Allow(ip1)
		if m.Allow(ip1) {
			t.Fatal("ip1: want deny after burst")
		}
		if !m.Allow(ip2) {
			t.Fatal("ip2: want allow (own limiter)")
		}
	})
}

func TestConcurrentAccess(t *testing.T) {
	m := keyrate.New[int](rate.Inf, 0)
	var wg sync.WaitGroup
	for g := range 10 {
		wg.Go(func() {
			for range 100 {
				m.Allow(g % 20)
				m.Get(g % 20)
				if g%50 == 0 {
					m.Delete(g % 20)
				}
			}
		})
	}
	wg.Wait()
}

// ---- LRU eviction ----

func TestLRUEvictsLeastRecentlyUsed(t *testing.T) {
	m := keyrate.New[string](rate.Every(time.Second), 1, keyrate.WithMaxSize(2))

	m.Allow("alice")
	m.Allow("bob")
	// Touch alice so bob becomes the LRU.
	m.Allow("alice")
	// Inserting charlie must evict bob.
	m.Allow("charlie")

	if m.Len() != 2 {
		t.Fatalf("Len: got %d, want 2", m.Len())
	}
	if m.Has("bob") {
		t.Fatal("bob should have been evicted (LRU)")
	}
	if !m.Has("alice") {
		t.Fatal("alice should still be present (recently used)")
	}
	if !m.Has("charlie") {
		t.Fatal("charlie should be present (just inserted)")
	}
}

func TestLRUSizeNeverExceeded(t *testing.T) {
	const max = 5
	m := keyrate.New[int](rate.Every(time.Second), 1, keyrate.WithMaxSize(max))
	for i := range 20 {
		m.Allow(i)
		if n := m.Len(); n > max {
			t.Fatalf("after Allow(%d): Len %d exceeds maxSize %d", i, n, max)
		}
	}
}

// ---- TTL eviction ----

func TestTTLEvictsIdleKeys(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const ttl = 2 * time.Second
		m := keyrate.New[string](rate.Every(time.Second), 1, keyrate.WithTTL(ttl))
		defer m.Stop()

		m.Allow("alice")
		time.Sleep(ttl + time.Second) // sweep fires at ttl/2 and ttl; entry gone by ttl
		synctest.Wait()

		if m.Has("alice") {
			t.Fatal("alice should have been evicted after TTL")
		}
	})
}

func TestTTLResetOnAccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const ttl = 2 * time.Second
		m := keyrate.New[string](rate.Every(time.Second), 1, keyrate.WithTTL(ttl))
		defer m.Stop()

		m.Allow("alice")

		// Access alice just before TTL expires, resetting the clock.
		time.Sleep(ttl - 100*time.Millisecond)
		m.Allow("alice")

		// Sleep again; alice should still be present (TTL restarted from re-access).
		time.Sleep(ttl - 100*time.Millisecond)
		synctest.Wait()

		if !m.Has("alice") {
			t.Fatal("alice should still be present (TTL was reset on access)")
		}
	})
}

// ---- auto eviction ----

// TestAutoEvictTTL verifies the derived TTL equals burst/r.
func TestAutoEvictTTL(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// rate=1/s, burst=3 → auto TTL = 3s
		m := keyrate.New[string](rate.Every(time.Second), 3, keyrate.WithAutoEvict())
		defer m.Stop()

		m.Allow("alice")
		time.Sleep(4 * time.Second) // past the 3s auto-TTL
		synctest.Wait()

		if m.Has("alice") {
			t.Fatal("alice should be evicted (auto TTL = burst/r = 3s)")
		}
	})
}

func TestAutoEvictKeepsActiveKeys(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// rate=1/s, burst=2 → auto TTL = 2s
		m := keyrate.New[string](rate.Every(time.Second), 2, keyrate.WithAutoEvict())
		defer m.Stop()

		m.Allow("alice")

		// Keep touching alice before the TTL expires.
		time.Sleep(time.Second)
		m.Allow("alice") // resets TTL

		time.Sleep(time.Second)
		synctest.Wait()

		if !m.Has("alice") {
			t.Fatal("alice should still be present (kept alive by accesses)")
		}
	})
}

// WithTTL before WithAutoEvict: min(10s, 2s) = 2s.
func TestAutoEvictWithTTL_TTLFirst(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// rate=1/s, burst=2 → auto TTL = 2s. Explicit TTL = 10s.
		m := keyrate.New[string](rate.Every(time.Second), 2,
			keyrate.WithTTL(10*time.Second),
			keyrate.WithAutoEvict(),
		)
		defer m.Stop()

		m.Allow("alice")
		time.Sleep(3 * time.Second) // past auto TTL of 2s
		synctest.Wait()

		if m.Has("alice") {
			t.Fatal("alice should be evicted (min TTL = 2s)")
		}
	})
}

// WithAutoEvict before WithTTL: same result, min(2s, 10s) = 2s.
func TestAutoEvictWithTTL_AutoFirst(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// rate=1/s, burst=2 → auto TTL = 2s. Explicit TTL = 10s.
		m := keyrate.New[string](rate.Every(time.Second), 2,
			keyrate.WithAutoEvict(),
			keyrate.WithTTL(10*time.Second),
		)
		defer m.Stop()

		m.Allow("alice")
		time.Sleep(3 * time.Second) // past auto TTL of 2s
		synctest.Wait()

		if m.Has("alice") {
			t.Fatal("alice should be evicted (min TTL = 2s)")
		}
	})
}

// Explicit TTL shorter than auto: explicit wins.
func TestAutoEvictWithShorterExplicitTTL(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// rate=1/s, burst=10 → auto TTL = 10s. Explicit TTL = 2s.
		m := keyrate.New[string](rate.Every(time.Second), 10,
			keyrate.WithAutoEvict(),
			keyrate.WithTTL(2*time.Second),
		)
		defer m.Stop()

		m.Allow("alice")
		time.Sleep(3 * time.Second) // past explicit TTL of 2s
		synctest.Wait()

		if m.Has("alice") {
			t.Fatal("alice should be evicted (explicit TTL = 2s wins over auto 10s)")
		}
	})
}

// ---- combined strategies ----

func TestLRUAndTTLCombined(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const ttl = 2 * time.Second
		m := keyrate.New[string](rate.Every(time.Second), 1,
			keyrate.WithMaxSize(2),
			keyrate.WithTTL(ttl),
		)
		defer m.Stop()

		m.Allow("alice") // LRU order: alice
		m.Allow("bob")   // LRU order: bob, alice (alice is LRU)

		// charlie triggers LRU eviction → alice is dropped.
		m.Allow("charlie") // LRU order: charlie, bob
		if m.Has("alice") {
			t.Fatal("alice should be evicted by LRU cap")
		}
		if m.Len() != 2 {
			t.Fatalf("Len: got %d, want 2", m.Len())
		}

		// After TTL, both remaining keys are swept away.
		time.Sleep(ttl + time.Second)
		synctest.Wait()

		if m.Len() != 0 {
			t.Fatalf("Len after TTL: got %d, want 0", m.Len())
		}
	})
}
