package kilden

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// captureServer is a minimal in-process capture endpoint for unit tests;
// contract-level fidelity is covered by the spec mock server tests.
type captureServer struct {
	mu       sync.Mutex
	batches  []batchPayload
	statuses []int // consumed FIFO; empty = 200
	gzipped  int
}

func (cs *captureServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cs.mu.Lock()
		status := http.StatusOK
		if len(cs.statuses) > 0 {
			status = cs.statuses[0]
			cs.statuses = cs.statuses[1:]
		}
		cs.mu.Unlock()
		if status != http.StatusOK {
			if status == http.StatusTooManyRequests {
				w.Header().Set("Retry-After", "0")
			}
			http.Error(w, http.StatusText(status), status)
			return
		}

		var reader io.Reader = r.Body
		if r.Header.Get("Content-Encoding") == "gzip" {
			gz, err := gzip.NewReader(r.Body)
			if err != nil {
				http.Error(w, "bad gzip", 400)
				return
			}
			reader = gz
			cs.mu.Lock()
			cs.gzipped++
			cs.mu.Unlock()
		}
		var payload batchPayload
		if err := json.NewDecoder(reader).Decode(&payload); err != nil {
			http.Error(w, "bad json", 400)
			return
		}
		cs.mu.Lock()
		cs.batches = append(cs.batches, payload)
		cs.mu.Unlock()
		w.Write([]byte(`{"status":"ok"}`))
	})
}

func (cs *captureServer) events() []event {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	var out []event
	for _, b := range cs.batches {
		out = append(out, b.Batch...)
	}
	return out
}

func newTestClient(t *testing.T, cs *captureServer, opts ...Option) *Client {
	t.Helper()
	srv := httptest.NewServer(cs.handler())
	t.Cleanup(srv.Close)
	c, err := New("sk_test_secret", append([]Option{WithHost(srv.URL), WithFlushAt(1000)}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	c.sender.sleep = func(time.Duration) bool { return true } // fast retries
	t.Cleanup(c.Close)
	return c
}

func TestNewRejectsBadKeys(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Fatal("empty key must fail")
	}
	_, err := New("wk_public_key")
	if err == nil {
		t.Fatal("public key must fail")
	}
	if !strings.Contains(err.Error(), "secret") {
		t.Fatalf("error must teach the fix, got: %v", err)
	}
}

func TestTrackDeliversAndFlushBlocks(t *testing.T) {
	cs := &captureServer{}
	c := newTestClient(t, cs)

	c.Track("user_1", "signup", map[string]any{"plan": "pro"})
	c.Flush()

	events := cs.events()
	if len(events) != 1 || events[0].Event != "signup" {
		t.Fatalf("events = %+v", events)
	}
	if !v7Re.MatchString(events[0].UUID) {
		t.Fatalf("uuid = %q", events[0].UUID)
	}
	if !tsRe.MatchString(events[0].Timestamp) {
		t.Fatalf("timestamp = %q", events[0].Timestamp)
	}
}

func TestQueueDropsNewWhenFull(t *testing.T) {
	cs := &captureServer{}
	c := newTestClient(t, cs, WithMaxQueueSize(3), WithFlushInterval(time.Hour))

	for i := range 5 {
		c.Track("user_1", "e", map[string]any{"i": i})
	}
	if got := c.DroppedCount(); got != 2 {
		t.Fatalf("dropped = %d, want 2", got)
	}
	c.Flush()
	events := cs.events()
	if len(events) != 3 {
		t.Fatalf("delivered %d, want 3 (oldest kept)", len(events))
	}
	var props map[string]int
	json.Unmarshal(events[0].Properties, &props)
	if props["i"] != 0 {
		t.Fatalf("first delivered event should be the oldest, got i=%d", props["i"])
	}
}

func TestRetryOn429ThenSuccess(t *testing.T) {
	cs := &captureServer{statuses: []int{429, 429}}
	c := newTestClient(t, cs)

	c.Track("user_1", "retried", nil)
	c.Flush()

	if len(cs.events()) != 1 {
		t.Fatalf("event should survive two 429s, got %d", len(cs.events()))
	}
}

func TestNoRetryOn401(t *testing.T) {
	cs := &captureServer{statuses: []int{401}}
	c := newTestClient(t, cs)

	c.Track("user_1", "rejected", nil)
	c.Flush()

	if len(cs.events()) != 0 {
		t.Fatal("401 must not be retried")
	}
	if c.DroppedCount() != 1 {
		t.Fatalf("dropped = %d, want 1", c.DroppedCount())
	}
}

func TestRetriesExhaust(t *testing.T) {
	cs := &captureServer{statuses: []int{500, 500, 500, 500, 500}}
	c := newTestClient(t, cs)

	c.Track("user_1", "doomed", nil)
	c.Flush()

	if len(cs.events()) != 0 {
		t.Fatal("batch should be dropped after retries")
	}
	if c.DroppedCount() != 1 {
		t.Fatalf("dropped = %d, want 1", c.DroppedCount())
	}
}

func TestGzipOverThreshold(t *testing.T) {
	cs := &captureServer{}
	c := newTestClient(t, cs)

	big := strings.Repeat("x", 2000)
	c.Track("user_1", "big", map[string]any{"blob": big})
	c.Track("user_2", "small", nil)
	c.Flush()

	if cs.gzipped == 0 {
		t.Fatal("a >1KiB body must be gzip-compressed")
	}
	if len(cs.events()) != 2 {
		t.Fatalf("events = %d", len(cs.events()))
	}
}

func TestCloseIsIdempotentAndDefinesAfterUse(t *testing.T) {
	cs := &captureServer{}
	c := newTestClient(t, cs)

	c.Track("user_1", "before_close", nil)
	c.Close()
	c.Close()
	c.Track("user_1", "after_close", nil)
	c.Flush()

	events := cs.events()
	if len(events) != 1 || events[0].Event != "before_close" {
		t.Fatalf("events = %+v", events)
	}
}

func TestDisabledClientIsANoOp(t *testing.T) {
	c, err := New("sk_test_secret", WithEnabled(false))
	if err != nil {
		t.Fatal(err)
	}
	c.Track("user_1", "e", nil)
	c.Flush()
	c.Close()
	if !c.IsEnabled("flag", "user_1", WithDefault(true)) {
		t.Fatal("disabled client should return the default")
	}
}

func TestBatchChunking(t *testing.T) {
	cs := &captureServer{}
	c := newTestClient(t, cs, WithMaxQueueSize(2500), WithFlushInterval(time.Hour), WithFlushAt(100000))

	for range 2500 {
		c.Track("user_1", "bulk", nil)
	}
	c.Flush()

	cs.mu.Lock()
	defer cs.mu.Unlock()
	if len(cs.batches) != 3 {
		t.Fatalf("batches = %d, want 3 (1000+1000+500)", len(cs.batches))
	}
	for i, b := range cs.batches[:2] {
		if len(b.Batch) != 1000 {
			t.Fatalf("batch %d has %d events", i, len(b.Batch))
		}
	}
}

func TestSignerValidation(t *testing.T) {
	if _, err := NewIdentitySigner("", "k1"); err == nil {
		t.Fatal("empty secret must fail")
	}
	if _, err := NewIdentitySigner("s3cret", ""); err == nil {
		t.Fatal("empty kid must fail")
	}
	signer, err := NewIdentitySigner("s3cret", "k1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signer.Sign(""); err == nil {
		t.Fatal("empty sub must fail")
	}
	if _, err := signer.Sign("u1", WithTTL(8*24*time.Hour)); err == nil {
		t.Fatal("ttl above 7 days must fail")
	}
	if _, err := signer.Sign("u1", WithTTL(-time.Hour)); err == nil {
		t.Fatal("negative ttl must fail")
	}
	token, err := signer.Sign("u1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(token, ".") != 2 {
		t.Fatalf("token = %q", token)
	}
	if strings.Contains(token, "s3cret") {
		t.Fatal("token must not leak the secret")
	}
}
