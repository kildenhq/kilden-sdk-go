package kilden

// End-to-end behavior against the real spec mock server, exercising the
// failure simulation the local httptest doubles cannot reproduce.

import (
	"runtime"
	"testing"
	"time"
)

func TestIntegrationRetryAfterAgainstMock(t *testing.T) {
	m := mockServer(t)
	m.reset(t)
	m.post(t, "/__mock/fail", `{"times":2,"status":429,"retry_after":1}`)

	c, err := New("sk_test_secret", WithHost(m.url), WithFlushAt(1000))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var slept []time.Duration
	c.sender.sleep = func(d time.Duration) bool {
		slept = append(slept, d)
		return true
	}

	c.Track("user_1", "survives_429", nil)
	c.Flush()

	events := m.captured(t)
	if len(events) != 1 {
		t.Fatalf("captured %d events, want 1", len(events))
	}
	if len(slept) != 2 {
		t.Fatalf("slept %d times, want 2", len(slept))
	}
	for _, d := range slept {
		if d != time.Second {
			t.Fatalf("Retry-After: 1 must override the backoff verbatim, slept %s", d)
		}
	}
}

func TestIntegrationCorruptResponseIsRetried(t *testing.T) {
	m := mockServer(t)
	m.reset(t)
	m.post(t, "/__mock/fail", `{"times":1,"mode":"corrupt"}`)

	c, err := New("sk_test_secret", WithHost(m.url), WithFlushAt(1000))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.sender.sleep = func(time.Duration) bool { return true }

	c.Track("user_1", "survives_corruption", nil)
	c.Flush()

	if events := m.captured(t); len(events) != 1 {
		t.Fatalf("captured %d events, want 1", len(events))
	}
}

func TestIntegrationGzipAcceptedByMock(t *testing.T) {
	m := mockServer(t)
	m.reset(t)

	c, err := New("sk_test_secret", WithHost(m.url), WithFlushAt(1000))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	big := make(map[string]any)
	for i := 0; i < 50; i++ {
		big["key_"+string(rune('a'+i%26))+string(rune('0'+i/26))] = "some reasonably long property value to cross the gzip threshold"
	}
	c.Track("user_1", "big_event", big)
	c.Flush()

	if events := m.captured(t); len(events) != 1 {
		t.Fatalf("captured %d events, want 1", len(events))
	}
}

func TestCloseLeavesNoGoroutines(t *testing.T) {
	before := runtime.NumGoroutine()
	for range 10 {
		c, err := New("sk_test_secret", WithHost("http://127.0.0.1:1"), WithTimeout(100*time.Millisecond))
		if err != nil {
			t.Fatal(err)
		}
		c.Track("u", "e", nil)
		c.Close()
	}
	time.Sleep(200 * time.Millisecond)
	after := runtime.NumGoroutine()
	if after > before+2 {
		t.Fatalf("goroutines grew from %d to %d", before, after)
	}
}
