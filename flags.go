package kilden

import (
	"bytes"
	"container/list"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

const (
	flagCacheTTL  = 30 * time.Second
	flagCacheSize = 1000
)

// FlagOption customizes one flag lookup (SPEC.md §2.2: exactly
// person_properties and default).
type FlagOption func(*flagOpts)

type flagOpts struct {
	personProperties map[string]any
	defaultValue     any
}

// WithPersonProperties sends person property overrides to /decide for this
// evaluation only. Calls carrying overrides bypass the cache entirely. The
// signature exists today so local evaluation lands later without an API
// change (SPEC.md §8).
func WithPersonProperties(props map[string]any) FlagOption {
	return func(o *flagOpts) { o.personProperties = props }
}

// WithDefault sets what to return when Kilden cannot answer: timeout,
// network error, non-200, unknown flag. Default false.
func WithDefault(v any) FlagOption {
	return func(o *flagOpts) { o.defaultValue = v }
}

// IsEnabled reports whether flagKey is on for distinctID: true when the flag
// evaluates to true or to a variant key (contract: SPEC.md §8.2).
func (c *Client) IsEnabled(flagKey, distinctID string, opts ...FlagOption) bool {
	v := c.FeatureFlag(flagKey, distinctID, opts...)
	if _, ok := v.(string); ok {
		return true
	}
	return v == true
}

// FeatureFlag returns the raw flag value: false, true, or the variant key.
func (c *Client) FeatureFlag(flagKey, distinctID string, opts ...FlagOption) any {
	var o flagOpts
	for _, opt := range opts {
		opt(&o)
	}
	if o.defaultValue == nil {
		o.defaultValue = false
	}
	if !c.enabled || flagKey == "" || distinctID == "" {
		return o.defaultValue
	}

	flags, ok := c.flags.get(distinctID, o.personProperties)
	if !ok {
		return o.defaultValue
	}
	v, ok := flags[flagKey]
	if !ok {
		return o.defaultValue
	}
	return v
}

// flagClient wraps /decide with the spec cache: per-distinct_id, 30s TTL,
// LRU-bounded at 1000 ids, bypassed when person properties are present.
type flagClient struct {
	c *Client

	mu      sync.Mutex
	entries map[string]*list.Element
	order   *list.List // front = most recent
	now     func() time.Time
}

type flagEntry struct {
	distinctID string
	flags      map[string]any
	expires    time.Time
}

func newFlagClient(c *Client) *flagClient {
	return &flagClient{
		c:       c,
		entries: map[string]*list.Element{},
		order:   list.New(),
		now:     time.Now,
	}
}

func (f *flagClient) get(distinctID string, personProperties map[string]any) (map[string]any, bool) {
	bypass := len(personProperties) > 0
	if !bypass {
		if flags, ok := f.cached(distinctID); ok {
			return flags, true
		}
	}

	flags, ok := f.fetch(distinctID, personProperties)
	if !ok {
		return nil, false
	}
	if !bypass {
		f.store(distinctID, flags)
	}
	return flags, true
}

func (f *flagClient) cached(distinctID string) (map[string]any, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	el, ok := f.entries[distinctID]
	if !ok {
		return nil, false
	}
	entry := el.Value.(*flagEntry)
	if f.now().After(entry.expires) {
		f.order.Remove(el)
		delete(f.entries, distinctID)
		return nil, false
	}
	f.order.MoveToFront(el)
	return entry.flags, true
}

func (f *flagClient) store(distinctID string, flags map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if el, ok := f.entries[distinctID]; ok {
		el.Value.(*flagEntry).flags = flags
		el.Value.(*flagEntry).expires = f.now().Add(flagCacheTTL)
		f.order.MoveToFront(el)
		return
	}
	f.entries[distinctID] = f.order.PushFront(&flagEntry{
		distinctID: distinctID,
		flags:      flags,
		expires:    f.now().Add(flagCacheTTL),
	})
	if f.order.Len() > flagCacheSize {
		oldest := f.order.Back()
		f.order.Remove(oldest)
		delete(f.entries, oldest.Value.(*flagEntry).distinctID)
	}
}

// fetch is one attempt against /decide — flag lookups are never retried
// (SPEC.md §8.2): an answer arriving after a retry budget is useless to the
// caller.
func (f *flagClient) fetch(distinctID string, personProperties map[string]any) (map[string]any, bool) {
	reqBody := map[string]any{
		"write_key":   f.c.writeKey,
		"distinct_id": distinctID,
	}
	if len(personProperties) > 0 {
		reqBody["person_properties"] = personProperties
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		f.c.log.Warn("flag lookup failed", "reason", "person_properties are not JSON-serializable")
		return nil, false
	}

	req, err := http.NewRequest(http.MethodPost, f.c.host+"/decide", bytes.NewReader(body))
	if err != nil {
		return nil, false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "kilden-go/"+Version)

	resp, err := f.c.httpClient.Do(req)
	if err != nil {
		f.c.log.Warn("flag lookup failed", "error", err)
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		f.c.log.Warn("flag lookup failed", "status", resp.StatusCode)
		return nil, false
	}

	var decoded struct {
		Flags map[string]any `json:"flags"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil || decoded.Flags == nil {
		f.c.log.Warn("flag lookup failed", "reason", "malformed response body")
		return nil, false
	}
	return decoded.Flags, true
}
