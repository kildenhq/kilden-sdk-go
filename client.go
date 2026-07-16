// Package kilden is the official Kilden server-side SDK for Go: event
// capture, identity-token signing and feature flags against a Kilden
// project's secret write key.
//
// The SDK implements the Kilden server SDK specification
// (https://github.com/kildenhq/kilden-sdk-spec); its behavior
// contracts are the authority for anything this documentation leaves open.
package kilden

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultHost          = "https://ingest.kilden.io"
	defaultFlushAt       = 20
	defaultFlushInterval = 10 * time.Second
	defaultMaxQueueSize  = 10000
	defaultTimeout       = 3 * time.Second
	closeDeadline        = 10 * time.Second

	// Version is the SDK version reported in the User-Agent header.
	Version = "0.1.0-alpha.2"
)

// Client queues events in memory and delivers them in batches from a
// background goroutine. It is safe for concurrent use. Programs must call
// Close before exiting — Go has no process shutdown hook, so anything still
// queued when main returns is lost:
//
//	client, err := kilden.New(os.Getenv("KILDEN_SECRET_KEY"))
//	if err != nil { log.Fatal(err) }
//	defer client.Close()
type Client struct {
	writeKey string
	host     string
	flushAt  int
	interval time.Duration
	maxQueue int
	timeout  time.Duration
	debug    bool
	enabled  bool

	httpClient *http.Client
	sender     *sender
	flags      *flagClient
	log        *slog.Logger

	mu     sync.Mutex
	queue  []event
	closed bool

	dropped atomic.Int64

	wake    chan struct{}
	flushCh chan chan struct{}
	done    chan struct{}
	wg      sync.WaitGroup
	once    sync.Once
}

// Option configures a Client (SPEC.md §2.1; the set is closed on purpose).
type Option func(*Client)

// WithHost points the client at a different Kilden host (self-hosted or the
// spec mock server).
func WithHost(host string) Option {
	return func(c *Client) { c.host = strings.TrimRight(host, "/") }
}

// WithFlushAt sets the queue length that triggers a flush (default 20).
func WithFlushAt(n int) Option {
	return func(c *Client) { c.flushAt = n }
}

// WithFlushInterval sets the periodic flush interval (default 10s).
func WithFlushInterval(d time.Duration) Option {
	return func(c *Client) { c.interval = d }
}

// WithMaxQueueSize caps the in-memory queue (default 10000). When full, new
// events are dropped and counted — the queue never grows unbounded and never
// blocks the caller (contract 7).
func WithMaxQueueSize(n int) Option {
	return func(c *Client) { c.maxQueue = n }
}

// WithTimeout sets the per-request HTTP timeout (default 3s).
func WithTimeout(d time.Duration) Option {
	return func(c *Client) { c.timeout = d }
}

// WithTransport plugs a custom http.RoundTripper (proxies, instrumentation,
// test doubles). nil keeps http.DefaultTransport.
func WithTransport(rt http.RoundTripper) Option {
	return func(c *Client) {
		if rt != nil {
			c.httpClient.Transport = rt
		}
	}
}

// WithDebug turns on verbose logging, including the $-prefix warnings
// (contract 5).
func WithDebug(debug bool) Option {
	return func(c *Client) { c.debug = debug }
}

// WithEnabled set to false makes every method a no-op — tests, CI, local
// development (SPEC.md §2.1).
func WithEnabled(enabled bool) Option {
	return func(c *Client) { c.enabled = enabled }
}

// New builds a Client. It is the one place that fails fast (contract 2):
// missing key, public (wk_) key. Everything after construction never
// panics and never returns an error (contract 1).
func New(secretWriteKey string, opts ...Option) (*Client, error) {
	if secretWriteKey == "" {
		return nil, errors.New("kilden: secret write key is required")
	}
	if strings.HasPrefix(secretWriteKey, "wk_") {
		return nil, errors.New("kilden: this is a public write key; server SDKs need the project's secret key — public keys degrade events to source=client and must stay in browsers only")
	}

	c := &Client{
		writeKey:   secretWriteKey,
		host:       defaultHost,
		flushAt:    defaultFlushAt,
		interval:   defaultFlushInterval,
		maxQueue:   defaultMaxQueueSize,
		timeout:    defaultTimeout,
		enabled:    true,
		httpClient: &http.Client{},
		wake:       make(chan struct{}, 1),
		flushCh:    make(chan chan struct{}),
		done:       make(chan struct{}),
		// slog.Default keeps the option set closed (SPEC.md §2.1) while
		// still letting applications route logs by configuring the default
		// logger.
		log: slog.Default().With("component", "kilden"),
	}
	for _, opt := range opts {
		opt(c)
	}
	if c.flushAt < 1 {
		c.flushAt = 1
	}
	if c.maxQueue < 1 {
		c.maxQueue = 1
	}
	if c.interval <= 0 {
		c.interval = defaultFlushInterval
	}
	c.httpClient.Timeout = c.timeout

	c.sender = newSender(c)
	c.flags = newFlagClient(c)

	if c.enabled {
		c.wg.Add(1)
		go c.worker()
	}
	return c, nil
}

// Track queues one event for distinctID. Invalid input is dropped and
// logged, never raised (contract 1).
func (c *Client) Track(distinctID, eventName string, properties map[string]any, opts ...EventOption) {
	c.warnDollarPrefix(eventName, properties)
	c.enqueue(distinctID, eventName, properties, opts)
}

// Identify sets person traits for distinctID ($identify with a $set body,
// SPEC.md §4.6).
func (c *Client) Identify(distinctID string, traits map[string]any, opts ...EventOption) {
	if traits == nil {
		traits = map[string]any{}
	}
	c.enqueue(distinctID, "$identify", map[string]any{"$set": traits}, opts)
}

// Alias attaches distinctID as a new identity of the person previousID
// already resolves to (SPEC.md §4.6).
func (c *Client) Alias(previousID, distinctID string) {
	if distinctID == "" {
		c.log.Warn("alias dropped", "reason", "distinct_id is empty")
		return
	}
	c.enqueue(previousID, "$alias", map[string]any{"$alias": distinctID}, nil)
}

func (c *Client) enqueue(distinctID, eventName string, properties map[string]any, opts []EventOption) {
	if !c.enabled {
		return
	}
	e, reason := buildEvent(distinctID, eventName, properties, opts)
	if reason != "" {
		c.log.Warn("event dropped", "event", eventName, "reason", reason)
		return
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		c.log.Warn("event dropped", "event", eventName, "reason", "client is closed")
		return
	}
	if len(c.queue) >= c.maxQueue {
		c.mu.Unlock()
		c.dropped.Add(1)
		c.log.Warn("event dropped", "event", eventName, "reason", "queue is full (max_queue_size)")
		return
	}
	c.queue = append(c.queue, e)
	full := len(c.queue) >= c.flushAt
	c.mu.Unlock()

	if full {
		select {
		case c.wake <- struct{}{}:
		default:
		}
	}
}

func (c *Client) warnDollarPrefix(eventName string, properties map[string]any) {
	if !c.debug {
		return
	}
	if strings.HasPrefix(eventName, "$") {
		c.log.Warn("$-prefixed event names are reserved for Kilden; sending anyway", "event", eventName)
	}
	for k := range properties {
		if strings.HasPrefix(k, "$") {
			c.log.Warn("$-prefixed property keys are reserved for Kilden; sending anyway", "property", k)
		}
	}
}

// DroppedCount reports events dropped because of a full queue or exhausted
// retries (contract 7).
func (c *Client) DroppedCount() int64 {
	return c.dropped.Load()
}

// Flush blocks until everything queued at the moment of the call has been
// delivered (including retries) or terminally failed.
func (c *Client) Flush() {
	if !c.enabled {
		return
	}
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return
	}
	ack := make(chan struct{})
	select {
	case c.flushCh <- ack:
		<-ack
	case <-c.done:
	}
}

// Close flushes with a 10-second deadline, stops the worker and makes the
// client permanently inert. Idempotent (contract 10).
func (c *Client) Close() {
	if !c.enabled {
		return
	}
	c.once.Do(func() {
		ack := make(chan struct{})
		select {
		case c.flushCh <- ack:
			select {
			case <-ack:
			case <-time.After(closeDeadline):
				c.log.Warn("close deadline exceeded; remaining events dropped")
			}
		case <-time.After(closeDeadline):
			c.log.Warn("close deadline exceeded; remaining events dropped")
		}

		c.mu.Lock()
		c.closed = true
		remaining := len(c.queue)
		c.queue = nil
		c.mu.Unlock()
		if remaining > 0 {
			c.dropped.Add(int64(remaining))
		}

		close(c.done)
		c.wg.Wait()
	})
}

// worker drains the queue every interval, on flushAt, and on demand.
func (c *Client) worker() {
	defer c.wg.Done()
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.drain()
		case <-c.wake:
			c.drain()
		case ack := <-c.flushCh:
			c.drain()
			close(ack)
		case <-c.done:
			return
		}
	}
}

// drain takes everything queued right now and sends it in chunks of at most
// 1000 events (SPEC.md §4.2).
func (c *Client) drain() {
	c.mu.Lock()
	batch := c.queue
	c.queue = nil
	c.mu.Unlock()

	for start := 0; start < len(batch); start += maxBatchSize {
		end := min(start+maxBatchSize, len(batch))
		if !c.sender.send(batch[start:end]) {
			c.dropped.Add(int64(end - start))
		}
	}
}

func (c *Client) debugf(format string, args ...any) {
	if c.debug {
		c.log.Debug(fmt.Sprintf(format, args...))
	}
}
