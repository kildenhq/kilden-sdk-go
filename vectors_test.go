package kilden

// Runners for the frozen spec vectors (kilden-sdk-spec, SPEC.md §9). The
// spec checkout is located via KILDEN_SPEC_DIR (default ../kilden-sdk-spec);
// tests skip when it is absent so `go test` works on a bare clone, and CI
// sets the variable to make skipping impossible there.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func specDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("KILDEN_SPEC_DIR")
	explicit := dir != ""
	if !explicit {
		dir = "../kilden-sdk-spec"
	}
	if _, err := os.Stat(dir); err != nil {
		if explicit {
			t.Fatalf("KILDEN_SPEC_DIR set but unreadable: %v", err)
		}
		t.Skip("kilden-sdk-spec checkout not found; set KILDEN_SPEC_DIR")
	}
	return dir
}

func loadVectors(t *testing.T, name string, v any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(specDir(t), "vectors", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatal(err)
	}
}

func TestIdentityVectors(t *testing.T) {
	var doc struct {
		Vectors []struct {
			Name   string         `json:"name"`
			Secret string         `json:"secret"`
			Kid    string         `json:"kid"`
			Sub    string         `json:"sub"`
			Iat    int64          `json:"iat"`
			Exp    int64          `json:"exp"`
			Traits map[string]any `json:"traits"`
			Token  string         `json:"token"`
		} `json:"vectors"`
	}
	loadVectors(t, "identity.json", &doc)
	if len(doc.Vectors) < 10 {
		t.Fatalf("suspiciously few identity vectors: %d", len(doc.Vectors))
	}

	for _, v := range doc.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			signer, err := NewIdentitySigner(v.Secret, v.Kid)
			if err != nil {
				t.Fatal(err)
			}
			// Numbers inside traits decode as float64; the canonical form
			// renders integers without a decimal point, which json.Marshal
			// does for float64 integral values — no conversion needed.
			got, err := signer.signAt(v.Sub, v.Iat, v.Exp, v.Traits)
			if err != nil {
				t.Fatal(err)
			}
			if got != v.Token {
				t.Errorf("token mismatch\n got: %s\nwant: %s", got, v.Token)
			}
		})
	}
}

func TestHashingVectors(t *testing.T) {
	var doc struct {
		Rollout []struct {
			FlagKey     string `json:"flag_key"`
			DistinctID  string `json:"distinct_id"`
			HashInput   string `json:"hash_input"`
			Uint64      string `json:"uint64"`
			Bucket      string `json:"bucket"`
			BucketFloor int    `json:"bucket_floor"`
		} `json:"rollout"`
		Variants []struct {
			FlagKey    string `json:"flag_key"`
			DistinctID string `json:"distinct_id"`
			Variants   []struct {
				Key               string `json:"key"`
				RolloutPercentage int    `json:"rollout_percentage"`
			} `json:"variants"`
			Expected any `json:"expected"`
		} `json:"variants"`
	}
	loadVectors(t, "flag-hashing.json", &doc)
	if len(doc.Rollout) < 200 {
		t.Fatalf("suspiciously few rollout vectors: %d", len(doc.Rollout))
	}

	for _, v := range doc.Rollout {
		b := rolloutBucket(v.FlagKey, v.DistinctID)
		if int(b) != v.BucketFloor {
			t.Errorf("bucket(%q, %q) floor = %d, want %d", v.FlagKey, v.DistinctID, int(b), v.BucketFloor)
		}
		want, err := strconv.ParseFloat(v.Bucket, 64)
		if err != nil {
			t.Fatal(err)
		}
		if b != want {
			t.Errorf("bucket(%q, %q) = %v, want %v", v.FlagKey, v.DistinctID, b, want)
		}
	}

	for _, v := range doc.Variants {
		point := variantPoint(v.FlagKey, v.DistinctID)
		cumulative := 0.0
		var got any = true
		for _, variant := range v.Variants {
			cumulative += float64(variant.RolloutPercentage)
			if point < cumulative {
				got = variant.Key
				break
			}
		}
		if got != v.Expected {
			t.Errorf("variant(%q, %q) = %v, want %v", v.FlagKey, v.DistinctID, got, v.Expected)
		}
	}
}

// TestPayloadVectors replays every call vector through a real client against
// the live mock server and compares what the mock captured.
func TestPayloadVectors(t *testing.T) {
	var doc struct {
		Vectors []struct {
			Name string `json:"name"`
			Call struct {
				Method string          `json:"method"`
				Args   json.RawMessage `json:"args"`
			} `json:"call"`
			ExpectEvent map[string]any `json:"expect_event"`
			Expect      string         `json:"expect"`
		} `json:"vectors"`
	}
	loadVectors(t, "payload.json", &doc)
	mock := mockServer(t)

	for _, v := range doc.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			mock.reset(t)
			client, err := New("sk_test_secret", WithHost(mock.url), WithFlushAt(1000))
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()

			var args struct {
				DistinctID string         `json:"distinct_id"`
				PreviousID string         `json:"previous_id"`
				Event      string         `json:"event"`
				Properties map[string]any `json:"properties"`
				Traits     map[string]any `json:"traits"`
				Opts       struct {
					Timestamp string `json:"timestamp"`
					UUID      string `json:"uuid"`
				} `json:"opts"`
			}
			if err := json.Unmarshal(v.Call.Args, &args); err != nil {
				t.Fatal(err)
			}

			var opts []EventOption
			if args.Opts.Timestamp != "" {
				ts, err := time.Parse(time.RFC3339, args.Opts.Timestamp)
				if err != nil {
					t.Fatal(err)
				}
				opts = append(opts, WithTimestamp(ts))
			}
			if args.Opts.UUID != "" {
				opts = append(opts, WithUUID(args.Opts.UUID))
			}

			switch v.Call.Method {
			case "track":
				client.Track(args.DistinctID, args.Event, args.Properties, opts...)
			case "identify":
				client.Identify(args.DistinctID, args.Traits, opts...)
			case "alias":
				client.Alias(args.PreviousID, args.DistinctID)
			default:
				t.Fatalf("unknown method %q", v.Call.Method)
			}
			client.Flush()

			events := mock.captured(t)
			if v.Expect == "discarded" {
				if len(events) != 0 {
					t.Fatalf("expected discard, captured %d events", len(events))
				}
				return
			}
			if len(events) != 1 {
				t.Fatalf("captured %d events, want 1", len(events))
			}
			compareEvent(t, events[0], v.ExpectEvent)
		})
	}
}

func compareEvent(t *testing.T, got map[string]any, want map[string]any) {
	t.Helper()
	for key, wantValue := range want {
		gotValue, ok := got[key]
		if !ok {
			t.Errorf("captured event missing %q", key)
			continue
		}
		switch {
		case wantValue == "<uuid_v7>":
			if s, _ := gotValue.(string); !v7Re.MatchString(s) {
				t.Errorf("%s = %v, want a v7 uuid", key, gotValue)
			}
		case wantValue == "<iso8601_utc_ms>":
			if s, _ := gotValue.(string); !tsRe.MatchString(s) {
				t.Errorf("%s = %v, want an iso8601 utc ms timestamp", key, gotValue)
			}
		default:
			if !jsonEqual(gotValue, wantValue) {
				g, _ := json.Marshal(gotValue)
				w, _ := json.Marshal(wantValue)
				t.Errorf("%s = %s, want %s", key, g, w)
			}
		}
	}
	for key := range got {
		if _, ok := want[key]; !ok {
			t.Errorf("captured event has unexpected key %q", key)
		}
	}
}

func jsonEqual(a, b any) bool {
	aj, err1 := json.Marshal(a)
	bj, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return false
	}
	// Marshal normalizes both sides (map key order); numbers compare via
	// their canonical float64 representation.
	var an, bn any
	json.Unmarshal(aj, &an)
	json.Unmarshal(bj, &bn)
	return fmt.Sprintf("%#v", an) == fmt.Sprintf("%#v", bn)
}
