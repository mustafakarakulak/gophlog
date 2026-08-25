package gophlog

import (
	"encoding/hex"
	"errors"
	"regexp"
	"testing"
	"time"
)

// uuidv7Pattern is the canonical UUIDv7 form: lowercase 8-4-4-4-12 hex with the
// version nibble pinned to 7 and the variant nibble to one of 8, 9, a, b.
var uuidv7Pattern = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// uuidBytes decodes a canonical UUID string back into its 16 bytes.
func uuidBytes(t *testing.T, id string) [16]byte {
	t.Helper()
	if len(id) != 36 {
		t.Fatalf("len(%q) = %d; want 36", id, len(id))
	}
	raw := id[0:8] + id[9:13] + id[14:18] + id[19:23] + id[24:36]
	b, err := hex.DecodeString(raw)
	if err != nil {
		t.Fatalf("hex.DecodeString(%q): %v", raw, err)
	}
	var out [16]byte
	copy(out[:], b)
	return out
}

// uuidTime returns the unix_ts_ms field of a UUIDv7 as a time.Time.
func uuidTime(t *testing.T, id string) time.Time {
	t.Helper()
	b := uuidBytes(t, id)
	ms := int64(b[0])<<40 | int64(b[1])<<32 | int64(b[2])<<24 |
		int64(b[3])<<16 | int64(b[4])<<8 | int64(b[5])
	return time.UnixMilli(ms)
}

func TestNewCorrelationIDIsCanonicalUUIDv7(t *testing.T) {
	for i := 0; i < 100; i++ {
		id := NewCorrelationID()
		if !uuidv7Pattern.MatchString(id) {
			t.Fatalf("id = %q; want canonical uuidv7", id)
		}
	}
}

func TestNewCorrelationIDVersionAndVariantBits(t *testing.T) {
	for i := 0; i < 100; i++ {
		id := NewCorrelationID()
		b := uuidBytes(t, id)
		if got := b[6] >> 4; got != 0x7 {
			t.Fatalf("version nibble of %q = %#x; want 0x7", id, got)
		}
		if got := b[8] >> 6; got != 0b10 {
			t.Fatalf("variant bits of %q = %#b; want 0b10", id, got)
		}
	}
}

func TestNewCorrelationIDEmbedsCurrentTime(t *testing.T) {
	before := time.Now()
	id := NewCorrelationID()
	after := time.Now()

	got := uuidTime(t, id)
	// UnixMilli truncates, so allow a millisecond of slack on each side on top
	// of the tolerance the test asserts.
	const tolerance = 2 * time.Second
	if got.Before(before.Add(-tolerance)) || got.After(after.Add(tolerance)) {
		t.Errorf("embedded timestamp = %v; want within %v of %v", got, tolerance, before)
	}
}

// TestNewCorrelationIDSortsInTimeOrder is the point of moving to uuidv7: IDs
// minted in different milliseconds must compare in creation order as strings.
func TestNewCorrelationIDSortsInTimeOrder(t *testing.T) {
	const steps = 5
	ids := make([]string, steps)
	for i := range ids {
		ids[i] = NewCorrelationID()
		if i < steps-1 {
			time.Sleep(2 * time.Millisecond)
		}
	}
	for i := 1; i < len(ids); i++ {
		if ids[i-1] >= ids[i] {
			t.Errorf("ids[%d] = %q is not lexicographically before ids[%d] = %q",
				i-1, ids[i-1], i, ids[i])
		}
	}
}

func TestNewCorrelationIDUnique(t *testing.T) {
	const n = 10_000
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		id := NewCorrelationID()
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id after %d generations: %q", i, id)
		}
		seen[id] = struct{}{}
	}
}

func TestNewCorrelationIDIsValidCorrelationID(t *testing.T) {
	if id := NewCorrelationID(); !IsValidCorrelationID(id) {
		t.Errorf("IsValidCorrelationID(%q) = false; want true", id)
	}
}

// TestNewCorrelationIDEntropyFailure covers the crypto/rand error path: the
// result must still be a well-formed uuidv7 carrying a real timestamp, and must
// never be the nil UUID.
func TestNewCorrelationIDEntropyFailure(t *testing.T) {
	saved := randRead
	t.Cleanup(func() { randRead = saved })
	randRead = func(b []byte) (int, error) {
		// Scribble over the buffer first: a partial write must not leak into the
		// result either.
		for i := range b {
			b[i] = 0xff
		}
		return 0, errors.New("no entropy")
	}

	before := time.Now()
	id := NewCorrelationID()

	if !uuidv7Pattern.MatchString(id) {
		t.Fatalf("id = %q; want canonical uuidv7 even without entropy", id)
	}
	if id == "00000000-0000-0000-0000-000000000000" {
		t.Fatal("fallback returned the nil UUID")
	}
	if !IsValidCorrelationID(id) {
		t.Errorf("IsValidCorrelationID(%q) = false; want true", id)
	}
	if got := uuidTime(t, id); got.Before(before.Add(-2 * time.Second)) {
		t.Errorf("fallback timestamp = %v; want a real clock reading near %v", got, before)
	}
	// The random fields are zeroed, which is what makes an entropy failure
	// recognisable in the logs.
	b := uuidBytes(t, id)
	if b[6] != 0x70 || b[7] != 0x00 || b[8] != 0x80 {
		t.Errorf("rand_a/version/variant bytes = %#x %#x %#x; want 0x70 0x00 0x80",
			b[6], b[7], b[8])
	}
	for i := 9; i < 16; i++ {
		if b[i] != 0 {
			t.Errorf("rand_b byte %d = %#x; want 0", i, b[i])
		}
	}
}
