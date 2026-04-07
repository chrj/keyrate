package keyrate_test

import (
	"net/netip"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"golang.org/x/time/rate"

	"github.com/razor/keyrate"
)

// one event per second, burst of 2.
func newMap() *keyrate.Map[string] {
	return keyrate.New[string](rate.Every(time.Second), 2)
}

func TestKeysAreIndependent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := newMap()

		// Exhaust alice's burst.
		if !m.Allow("alice") {
			t.Fatal("alice[0]: want allow")
		}
		if !m.Allow("alice") {
			t.Fatal("alice[1]: want allow (burst=2)")
		}
		if m.Allow("alice") {
			t.Fatal("alice[2]: want deny, burst exhausted")
		}

		// bob has his own fresh limiter.
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

		time.Sleep(time.Second) // fake clock advances; token refills

		if !m.Allow("alice") {
			t.Fatal("after 1s: want allow")
		}
	})
}

func TestPartialRefill(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// 2 tokens/sec, burst 2 → one token every 500ms.
		m := keyrate.New[string](2, 2)

		m.Allow("x") // consume one
		m.Allow("x") // consume two — empty

		time.Sleep(500 * time.Millisecond) // one token added back

		if !m.Allow("x") {
			t.Fatal("after 500ms: want allow (one token refilled)")
		}
		if m.Allow("x") {
			t.Fatal("after consuming the refilled token: want deny")
		}
	})
}

func TestDeleteResetsLimiter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := keyrate.New[string](rate.Every(time.Hour), 1)

		if !m.Allow("alice") {
			t.Fatal("first: want allow")
		}
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
	l1 := m.Limiter("k")
	l2 := m.Limiter("k")
	if l1 != l2 {
		t.Fatal("expected the same *rate.Limiter for the same key")
	}
}

func TestLen(t *testing.T) {
	m := newMap()
	if m.Len() != 0 {
		t.Fatalf("empty map: got Len %d, want 0", m.Len())
	}
	m.Allow("a")
	m.Allow("b")
	if m.Len() != 2 {
		t.Fatalf("after 2 keys: got Len %d, want 2", m.Len())
	}
	m.Delete("a")
	if m.Len() != 1 {
		t.Fatalf("after Delete: got Len %d, want 1", m.Len())
	}
}

// TestGenericKeyTypes demonstrates the Map works with non-string keys.
func TestGenericKeyTypes(t *testing.T) {
	t.Run("int key", func(t *testing.T) {
		m := keyrate.New[int](rate.Every(time.Second), 1)
		if !m.Allow(42) {
			t.Fatal("want allow")
		}
		if m.Allow(42) {
			t.Fatal("want deny")
		}
		if !m.Allow(99) {
			t.Fatal("different key should have own limiter")
		}
	})

	t.Run("IP key", func(t *testing.T) {
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

// TestConcurrentAccess verifies no races under concurrent use.
func TestConcurrentAccess(t *testing.T) {
	m := keyrate.New[int](rate.Inf, 0)
	keys := 20
	goroutines := 10

	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				m.Allow(g % keys)
				m.Limiter(g % keys)
				if g%50 == 0 {
					m.Delete(g % keys)
				}
			}
		}()
	}
	wg.Wait()
}
