package kilden

import (
	"crypto/sha256"
	"encoding/binary"
)

// The frozen rollout hashing (SPEC.md §8.3). v1 never evaluates flags
// locally — /decide does — but the algorithm is pinned now, tested against
// the spec vectors, so local evaluation lands later without a bucketing
// flicker. Deliberately unexported until then.

func rolloutBucket(flagKey, distinctID string) float64 {
	return hashFraction(flagKey+":"+distinctID) * 100
}

func variantPoint(flagKey, distinctID string) float64 {
	return hashFraction(flagKey+":"+distinctID+":variant") * 100
}

func hashFraction(input string) float64 {
	sum := sha256.Sum256([]byte(input))
	v := binary.BigEndian.Uint64(sum[:8])
	return float64(v) / (float64(1<<63) * 2)
}
