package kilden

import (
	"regexp"
	"testing"
)

var v7Re = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewUUIDv7(t *testing.T) {
	seen := map[string]bool{}
	for range 1000 {
		u := newUUIDv7()
		if !v7Re.MatchString(u) {
			t.Fatalf("not a canonical v7 uuid: %s", u)
		}
		if seen[u] {
			t.Fatalf("duplicate uuid: %s", u)
		}
		seen[u] = true
	}
}

func TestUUIDv7Monotonicity(t *testing.T) {
	// Not full monotonicity — only that the embedded timestamp advances.
	a, b := newUUIDv7(), newUUIDv7()
	if a[:13] > b[:13] {
		t.Fatalf("timestamp prefix went backwards: %s then %s", a, b)
	}
}
