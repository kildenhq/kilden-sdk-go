package kilden

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func flagServer(t *testing.T, calls *atomic.Int64, flags map[string]any) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/decide" {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		json.NewEncoder(w).Encode(map[string]any{
			"flags":            flags,
			"sessionRecording": map[string]any{"enabled": false, "sampleRate": 0},
		})
	}))
	t.Cleanup(srv.Close)
	c, err := New("sk_test_secret", WithHost(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func TestFlagValuesAndIsEnabled(t *testing.T) {
	var calls atomic.Int64
	c := flagServer(t, &calls, map[string]any{"on": true, "off": false, "exp": "variant_b"})

	if got := c.FeatureFlag("on", "u1"); got != true {
		t.Fatalf("on = %v", got)
	}
	if got := c.FeatureFlag("off", "u1"); got != false {
		t.Fatalf("off = %v", got)
	}
	if got := c.FeatureFlag("exp", "u1"); got != "variant_b" {
		t.Fatalf("exp = %v", got)
	}
	if !c.IsEnabled("on", "u1") || c.IsEnabled("off", "u1") || !c.IsEnabled("exp", "u1") {
		t.Fatal("IsEnabled truth table broken")
	}
	// Unknown flag → default.
	if c.IsEnabled("ghost", "u1") {
		t.Fatal("unknown flag should be false by default")
	}
	if got := c.FeatureFlag("ghost", "u1", WithDefault("fallback")); got != "fallback" {
		t.Fatalf("ghost with default = %v", got)
	}
}

func TestFlagCache(t *testing.T) {
	var calls atomic.Int64
	c := flagServer(t, &calls, map[string]any{"on": true})

	c.IsEnabled("on", "u1")
	c.IsEnabled("on", "u1")
	c.FeatureFlag("on", "u1")
	if calls.Load() != 1 {
		t.Fatalf("decide calls = %d, want 1 (cached)", calls.Load())
	}

	// A different distinct_id misses the cache.
	c.IsEnabled("on", "u2")
	if calls.Load() != 2 {
		t.Fatalf("decide calls = %d, want 2", calls.Load())
	}

	// person_properties bypass the cache entirely — no read, no write.
	c.IsEnabled("on", "u1", WithPersonProperties(map[string]any{"plan": "pro"}))
	c.IsEnabled("on", "u1", WithPersonProperties(map[string]any{"plan": "pro"}))
	if calls.Load() != 4 {
		t.Fatalf("decide calls = %d, want 4 (bypass)", calls.Load())
	}
	c.IsEnabled("on", "u1")
	if calls.Load() != 4 {
		t.Fatalf("decide calls = %d, want 4 (cache intact after bypass)", calls.Load())
	}
}

func TestFlagCacheExpiry(t *testing.T) {
	var calls atomic.Int64
	c := flagServer(t, &calls, map[string]any{"on": true})

	fake := time.Now()
	c.flags.now = func() time.Time { return fake }

	c.IsEnabled("on", "u1")
	fake = fake.Add(31 * time.Second)
	c.IsEnabled("on", "u1")
	if calls.Load() != 2 {
		t.Fatalf("decide calls = %d, want 2 (expired)", calls.Load())
	}
}

func TestFlagCacheLRUBound(t *testing.T) {
	var calls atomic.Int64
	c := flagServer(t, &calls, map[string]any{"on": true})

	for i := range flagCacheSize + 10 {
		c.IsEnabled("on", fmt.Sprintf("u%d", i))
	}
	c.flags.mu.Lock()
	size := c.flags.order.Len()
	c.flags.mu.Unlock()
	if size != flagCacheSize {
		t.Fatalf("cache size = %d, want %d", size, flagCacheSize)
	}
	// u0 was evicted; asking again refetches.
	before := calls.Load()
	c.IsEnabled("on", "u0")
	if calls.Load() != before+1 {
		t.Fatal("evicted id should refetch")
	}
}

func TestFlagFailuresReturnDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", 500)
	}))
	t.Cleanup(srv.Close)
	c, err := New("sk_test_secret", WithHost(srv.URL), WithTimeout(200*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)

	if c.IsEnabled("on", "u1") {
		t.Fatal("500 should yield the default false")
	}
	if !c.IsEnabled("on", "u1", WithDefault(true)) {
		t.Fatal("500 should yield the default true")
	}
	if got := c.FeatureFlag("on", "u1", WithDefault("x")); got != "x" {
		t.Fatalf("got %v", got)
	}

	// Unreachable host: same story.
	dead, err := New("sk_test_secret", WithHost("http://127.0.0.1:1"), WithTimeout(200*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dead.Close)
	if dead.IsEnabled("on", "u1", WithDefault(false)) {
		t.Fatal("network error should yield the default")
	}
}
