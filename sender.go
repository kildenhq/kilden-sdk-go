package kilden

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

const (
	maxRetries    = 3
	baseBackoff   = 500 * time.Millisecond
	maxBackoff    = 30 * time.Second
	gzipThreshold = 1024
)

// sender owns delivery of one batch: build, compress, POST, retry
// (SPEC.md §4.3). It never returns errors — a terminally failed batch is
// dropped and logged, and send reports false so the caller counts it.
type sender struct {
	c *Client
	// sleep is swapped in tests; it must return false when the client shut
	// down mid-backoff so retries abort instead of leaking goroutines.
	sleep func(d time.Duration) bool
}

func newSender(c *Client) *sender {
	return &sender{c: c, sleep: func(d time.Duration) bool {
		select {
		case <-time.After(d):
			return true
		case <-c.done:
			return false
		}
	}}
}

func (s *sender) send(batch []event) bool {
	if len(batch) == 0 {
		return true
	}

	for attempt := 0; ; attempt++ {
		status, retryAfter, err := s.post(batch)

		switch {
		case err == nil && status == http.StatusOK:
			s.c.debugf("batch of %d delivered", len(batch))
			return true
		case err == nil && !retryable(status):
			s.c.log.Warn("batch dropped", "events", len(batch), "status", status)
			return false
		}

		if attempt >= maxRetries {
			s.c.log.Warn("batch dropped after retries", "events", len(batch), "attempts", attempt+1)
			return false
		}
		if !s.sleep(backoff(attempt+1, retryAfter)) {
			return false
		}
	}
}

// post returns the HTTP status, the Retry-After value in seconds (0 when
// absent) and any transport error. A 200 with a body that is not valid JSON
// counts as a corrupt response and is reported as an error (retryable).
func (s *sender) post(batch []event) (int, int, error) {
	payload := batchPayload{
		WriteKey: s.c.writeKey,
		SentAt:   formatTimestamp(time.Now()),
		Batch:    batch,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		// Cannot happen: properties were marshaled at enqueue time.
		return 0, 0, err
	}

	var reader io.Reader = bytes.NewReader(body)
	compressed := len(body) > gzipThreshold
	if compressed {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		gz.Write(body)
		gz.Close()
		reader = &buf
	}

	req, err := http.NewRequest(http.MethodPost, s.c.host+"/capture", reader)
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "kilden-go/"+Version)
	if compressed {
		req.Header.Set("Content-Encoding", "gzip")
	}

	resp, err := s.c.httpClient.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, 0, err
	}
	if resp.StatusCode == http.StatusOK && !json.Valid(respBody) {
		return 0, 0, errCorruptResponse
	}

	retryAfter := 0
	if v := resp.Header.Get("Retry-After"); v != "" {
		retryAfter, _ = strconv.Atoi(v)
	}
	return resp.StatusCode, retryAfter, nil
}

var errCorruptResponse = &corruptResponseError{}

type corruptResponseError struct{}

func (*corruptResponseError) Error() string { return "corrupt response body" }

// retryable per SPEC.md §4.3: 429 and 5xx retry; any other 4xx (including
// 400/401/403/413) does not — retrying a 401 is spam.
func retryable(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

// backoff computes the pause before retry n (1-based):
// min(0.5 * 2^(n-1), 30)s times a jitter factor in [0.5, 1.5]. A positive
// Retry-After replaces the computed value verbatim (no jitter).
func backoff(n, retryAfterSeconds int) time.Duration {
	if retryAfterSeconds > 0 {
		return time.Duration(retryAfterSeconds) * time.Second
	}
	d := baseBackoff << (n - 1)
	if d > maxBackoff {
		d = maxBackoff
	}
	jitter := 0.5 + rand.Float64()
	return time.Duration(float64(d) * jitter)
}
