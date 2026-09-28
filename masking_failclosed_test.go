package gophlog

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// secretText implements json.Marshaler, mirroring custom string types (secret
// wrappers, enums) that payloads commonly contain.
type secretText string

func (s secretText) MarshalJSON() ([]byte, error) { return json.Marshal(string(s)) }

// failClosedPayload covers every scalar shape a mask tag must handle: standard
// types, non-standard numeric widths, named types and json.Marshaler values.
type failClosedPayload struct {
	CVVInt     int32      `json:"cvv_int" mask:"hideall"`
	CVVUint    uint16     `json:"cvv_uint" mask:"hideall"`
	Rate       float32    `json:"rate" mask:"hideall"`
	AccountNo  int64      `json:"account_no" mask:"showlast2"`
	Token      secretText `json:"token" mask:"hideall"`
	BirthDate  time.Time  `json:"birth_date" mask:"hideall"`
	CardHolder string     `json:"card_holder" mask:"showfirst1"`
}

// TestMaskTagFailClosed asserts that no raw value of a mask-tagged field can
// reach the log output, regardless of the field's Go type. This is the
// regression guard for the silently-skipped masking bugs.
func TestMaskTagFailClosed(t *testing.T) {
	p := failClosedPayload{
		CVVInt:     123,
		CVVUint:    456,
		Rate:       0.42,
		AccountNo:  1234567890,
		Token:      "super-secret-token",
		BirthDate:  time.Date(1990, 5, 17, 0, 0, 0, 0, time.UTC),
		CardHolder: "Mustafa",
	}

	normalized, _ := processPayload(p)
	out, err := json.Marshal(normalized)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(out)

	for _, raw := range []string{"123", "456", "0.42", "super-secret-token", "1990-05-17", "Mustafa"} {
		if strings.Contains(s, raw) {
			t.Errorf("raw value %q leaked into masked payload: %s", raw, s)
		}
	}
	m := normalized.(map[string]any)
	if m["account_no"] != "********90" {
		t.Errorf("account_no = %v; want ********90", m["account_no"])
	}
	if m["cvv_int"] != "***" {
		t.Errorf("cvv_int = %v; want ***", m["cvv_int"])
	}
	if m["card_holder"] != "M******" {
		t.Errorf("card_holder = %v; want M******", m["card_holder"])
	}
}

// TestMaskStrategyOnContainerMasksAllLeaves asserts that a strategy explicitly
// targeting an object masks every scalar underneath it, so unknown nested field
// names cannot leak.
func TestMaskStrategyOnContainerMasksAllLeaves(t *testing.T) {
	decoded := map[string]any{
		"card": map[string]any{
			"number": "5101521234564582",
			"expiry": "12/29",
			"tags":   []any{"vip", "corporate"},
		},
	}
	out := MaskJSON(decoded, map[string]MaskingStrategy{"card": HideAll}).(map[string]any)
	card := out["card"].(map[string]any)

	if card["number"] != "********" {
		t.Errorf("nested number = %v; want ********", card["number"])
	}
	if card["expiry"] != "*****" {
		t.Errorf("nested expiry = %v; want *****", card["expiry"])
	}
	tags := card["tags"].([]any)
	if tags[0] != "***" || tags[1] != "********" {
		t.Errorf("nested array leaves not masked: %v", tags)
	}
}

// TestMaskScalarNonStandardTypes pins the reflect fallback for values arriving
// via runtime strategies (not struct tags).
func TestMaskScalarNonStandardTypes(t *testing.T) {
	if got := maskScalar(int32(9999), HideAll); got != "****" {
		t.Errorf("int32 = %v; want ****", got)
	}
	if got := maskScalar(uint64(123456789), HideAll); got != "********" {
		t.Errorf("uint64 = %v; want ********", got)
	}
	if got := maskScalar(json.Number("42"), HideAll); got != "**" {
		t.Errorf("json.Number = %v; want **", got)
	}
	if got := maskScalar(secretText("abc"), HideAll); got != "***" {
		t.Errorf("named string via Marshaler = %v; want ***", got)
	}
	if got := maskScalar(nil, HideAll); got != nil {
		t.Errorf("nil = %v; want nil", got)
	}
}

// TestTruncateUTF8Boundary asserts that truncation never splits a multi-byte
// character and always yields valid UTF-8.
func TestTruncateUTF8Boundary(t *testing.T) {
	s := strings.Repeat("ğ", 10) // 2 bytes each
	for limit := 1; limit < len(s); limit++ {
		got := truncate(s, limit)
		if !utf8.ValidString(got) {
			t.Fatalf("truncate(%d) produced invalid UTF-8: %q", limit, got)
		}
	}
	if got := truncate("abc", 10); got != "abc" {
		t.Errorf("short string should be unchanged, got %q", got)
	}
}
