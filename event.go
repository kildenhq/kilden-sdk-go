package kilden

import (
	"encoding/json"
	"time"
)

const (
	maxEventName  = 200
	maxDistinctID = 512
	maxBatchSize  = 1000
)

// timestampLayout is the frozen wire format (SPEC.md §4.4): UTC, exactly
// three fractional digits, Z suffix.
const timestampLayout = "2006-01-02T15:04:05.000Z"

func formatTimestamp(t time.Time) string {
	return t.UTC().Format(timestampLayout)
}

// event is one queued wire event, properties already marshaled so that a
// non-serializable value is caught (and the event dropped) at call time,
// not at flush time.
type event struct {
	UUID       string          `json:"uuid"`
	Event      string          `json:"event"`
	DistinctID string          `json:"distinct_id"`
	Properties json.RawMessage `json:"properties"`
	Timestamp  string          `json:"timestamp"`
}

type batchPayload struct {
	WriteKey string  `json:"write_key"`
	SentAt   string  `json:"sent_at"`
	Batch    []event `json:"batch"`
}

// EventOption customizes one Track/Identify call (SPEC.md §2.2: exactly
// timestamp and uuid).
type EventOption func(*eventOpts)

type eventOpts struct {
	timestamp *time.Time
	uuid      string
}

// WithTimestamp sets the event time explicitly (backfills, importers). It is
// converted to the frozen wire format in UTC.
func WithTimestamp(t time.Time) EventOption {
	return func(o *eventOpts) { o.timestamp = &t }
}

// WithUUID sets the event UUID explicitly, making caller-level retries
// idempotent. Must be a canonical-form RFC 4122 UUID; it is sent verbatim.
func WithUUID(uuid string) EventOption {
	return func(o *eventOpts) { o.uuid = uuid }
}

// buildEvent validates per contracts 1/4/6 and freezes the event for the
// queue. ok=false means the event must be dropped (already logged by the
// caller with the returned reason).
func buildEvent(distinctID, name string, properties map[string]any, opts []EventOption) (event, string) {
	var o eventOpts
	for _, opt := range opts {
		opt(&o)
	}

	if distinctID == "" {
		return event{}, "distinct_id is empty"
	}
	if len(distinctID) > maxDistinctID {
		return event{}, "distinct_id exceeds 512 bytes"
	}
	if name == "" {
		return event{}, "event is empty"
	}
	if len(name) > maxEventName {
		return event{}, "event exceeds 200 bytes"
	}

	if properties == nil {
		properties = map[string]any{}
	}
	props, err := json.Marshal(properties)
	if err != nil {
		return event{}, "properties are not JSON-serializable: " + err.Error()
	}

	id := o.uuid
	if id == "" {
		id = newUUIDv7()
	} else if !uuidRe.MatchString(id) {
		return event{}, "uuid is not a canonical RFC 4122 UUID"
	}

	ts := time.Now()
	if o.timestamp != nil {
		ts = *o.timestamp
	}

	return event{
		UUID:       id,
		Event:      name,
		DistinctID: distinctID,
		Properties: props,
		Timestamp:  formatTimestamp(ts),
	}, ""
}
