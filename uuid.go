package kilden

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"regexp"
	"time"
)

// uuidRe accepts any canonical-form RFC 4122 UUID: explicit UUIDs travel
// verbatim (contract 6), but a malformed one would make the server reject
// the whole batch, so it is validated at enqueue time.
var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// newUUIDv7 returns a lowercase canonical UUID v7 (contract 6): 48-bit
// unix-ms timestamp, version and variant bits, 74 random bits.
func newUUIDv7() string {
	var b [16]byte
	ms := uint64(time.Now().UnixMilli())
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	if _, err := rand.Read(b[6:]); err != nil {
		// crypto/rand failing means the OS entropy source is broken; fall
		// back to a time-derived filler rather than panicking in the hot
		// path (contract 1).
		binary.BigEndian.PutUint64(b[8:], uint64(time.Now().UnixNano()))
	}
	b[6] = (b[6] & 0x0f) | 0x70 // version 7
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10

	var dst [36]byte
	hex.Encode(dst[0:8], b[0:4])
	dst[8] = '-'
	hex.Encode(dst[9:13], b[4:6])
	dst[13] = '-'
	hex.Encode(dst[14:18], b[6:8])
	dst[18] = '-'
	hex.Encode(dst[19:23], b[8:10])
	dst[23] = '-'
	hex.Encode(dst[24:36], b[10:16])
	return string(dst[:])
}
