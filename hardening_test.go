package gophlog

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// --- Payload parity with encoding/json ------------------------------------

// TestPayloadMatchesEncodingJSON locks the payload renderer to encoding/json's
// own output, so a struct logged as a payload looks exactly like the same struct
// marshalled directly.
func TestPayloadMatchesEncodingJSON(t *testing.T) {
	type Inner struct {
		X string `json:"x"`
	}
	type Embedded struct {
		E string `json:"e"`
	}
	type Payload struct {
		Kept      string            `json:"kept"`
		EmptyStr  string            `json:"emptyStr,omitempty"`
		EmptyNum  int               `json:"emptyNum,omitempty"`
		EmptyBool bool              `json:"emptyBool,omitempty"`
		EmptySlic []string          `json:"emptySlice,omitempty"`
		EmptyMap  map[string]string `json:"emptyMap,omitempty"`
		NilPtr    *Inner            `json:"nilPtr,omitempty"`
		ZeroKept  int               `json:"zeroKept"`
		*Embedded
	}

	in := Payload{Kept: "yes"}

	want, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal reference: %v", err)
	}

	normalized, _ := processPayload(in)
	got, err := json.Marshal(normalized)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	var wantMap, gotMap map[string]any
	if err := json.Unmarshal(want, &wantMap); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got, &gotMap); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(wantMap, gotMap) {
		t.Errorf("payload = %s\nencoding/json = %s", got, want)
	}
}

// TestPayloadPromotesEmbeddedOnlyWhenUntagged mirrors encoding/json: an embedded
// field with an explicit json name is a normal nested field, not promoted.
func TestPayloadPromotesEmbeddedOnlyWhenUntagged(t *testing.T) {
	type Embedded struct {
		E string `json:"e"`
	}
	type Promoted struct {
		Embedded
		A string `json:"a"`
	}
	type Named struct {
		Embedded `json:"nested"`
		A        string `json:"a"`
	}

	out, _ := processPayload(Promoted{Embedded: Embedded{E: "v"}, A: "a"})
	m := out.(map[string]any)
	if m["e"] != "v" {
		t.Errorf("untagged embedded should be promoted: %v", m)
	}

	out, _ = processPayload(Named{Embedded: Embedded{E: "v"}, A: "a"})
	m = out.(map[string]any)
	nested, ok := m["nested"].(map[string]any)
	if !ok || nested["e"] != "v" {
		t.Errorf("tagged embedded should stay nested: %v", m)
	}
}

// payloadEqualsStdlib fails the test unless processPayload renders v exactly as
// encoding/json would.
func payloadEqualsStdlib(t *testing.T, v any) {
	t.Helper()
	want, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal reference: %v", err)
	}
	normalized, _ := processPayload(v)
	got, err := json.Marshal(normalized)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var wantAny, gotAny any
	if err := json.Unmarshal(want, &wantAny); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got, &gotAny); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(wantAny, gotAny) {
		t.Errorf("payload = %s\nencoding/json = %s", got, want)
	}
}

// namedKey is a named string type used as a map key; it must render without
// literal quotes, exactly as encoding/json renders it.
type namedKey string

// intKey is a named integer map key.
type intKey int

// textKey is a string-kind map key implementing encoding.TextMarshaler; for map
// keys encoding/json resolves the string kind FIRST, so MarshalText is ignored.
type textKey string

func (k textKey) MarshalText() ([]byte, error) { return []byte("tk-" + string(k)), nil }

// textIntKey is an int-kind map key implementing encoding.TextMarshaler; here
// encoding/json does use MarshalText.
type textIntKey int

func (k textIntKey) MarshalText() ([]byte, error) {
	return []byte("tik-" + strconv.Itoa(int(k))), nil
}

// TestPayloadMapKeysMatchEncodingJSON locks map-key rendering to encoding/json:
// named string kinds by value, integer kinds in decimal, TextMarshaler by its
// marshalled text.
func TestPayloadMapKeysMatchEncodingJSON(t *testing.T) {
	payloadEqualsStdlib(t, map[namedKey]int{"foo": 1, "bar": 2})
	payloadEqualsStdlib(t, map[intKey]string{7: "seven", -3: "minus"})
	payloadEqualsStdlib(t, map[uint16]bool{42: true})
	payloadEqualsStdlib(t, map[textKey]int{"a": 1})
	payloadEqualsStdlib(t, map[textIntKey]int{7: 1})
	payloadEqualsStdlib(t, map[string]any{"plain": map[namedKey]int{"nested": 9}})
}

// TestPayloadEmbeddedShadowingMatchesEncodingJSON locks field dominance to
// encoding/json: the shallower (outer) field wins regardless of declaration
// order, and a name promoted by two embedded siblings is dropped entirely.
func TestPayloadEmbeddedShadowingMatchesEncodingJSON(t *testing.T) {
	type Inner struct {
		Name string `json:"name"`
	}
	type OuterFirst struct {
		Name string `json:"name"`
		Inner
	}
	type OuterLast struct {
		Inner
		Name string `json:"name"`
	}
	payloadEqualsStdlib(t, OuterFirst{Name: "outer", Inner: Inner{Name: "inner"}})
	payloadEqualsStdlib(t, OuterLast{Name: "outer", Inner: Inner{Name: "inner"}})

	// Two embedded siblings promoting the same name: encoding/json drops it.
	// The conflicting struct is assembled with reflect.StructOf because vet's
	// structtag checker (rightly) rejects declaring the same conflict in source.
	type A struct {
		X string `json:"x"`
	}
	type B struct {
		X string `json:"x"`
	}
	conflictType := reflect.StructOf([]reflect.StructField{
		{Name: "A", Type: reflect.TypeOf(A{}), Anonymous: true},
		{Name: "B", Type: reflect.TypeOf(B{}), Anonymous: true},
		{Name: "Kept", Type: reflect.TypeOf(""), Tag: `json:"kept"`},
	})
	conflict := reflect.New(conflictType).Elem()
	conflict.Field(0).Set(reflect.ValueOf(A{X: "a"}))
	conflict.Field(1).Set(reflect.ValueOf(B{X: "b"}))
	conflict.Field(2).SetString("k")
	payloadEqualsStdlib(t, conflict.Interface())

	// The outer field owns the name even when omitempty drops its value:
	// resolution happens at the type level, so the embedded field must not
	// surface through the gap.
	type OuterEmpty struct {
		Name string `json:"name,omitempty"`
		Inner
	}
	payloadEqualsStdlib(t, OuterEmpty{Inner: Inner{Name: "inner"}})
}

// TestPayloadOmitEmptyDropsLogExtra keeps an empty logextra field out of the
// extra object rather than surfacing it as an empty searchable value.
func TestPayloadOmitEmptyDropsLogExtra(t *testing.T) {
	type P struct {
		ID    string `json:"id,omitempty" logextra:"true"`
		Other string `json:"other"`
	}
	_, extra := processPayload(P{Other: "o"})
	if _, present := extra["id"]; present {
		t.Errorf("empty omitempty logextra should be dropped: %v", extra)
	}

	_, extra = processPayload(P{ID: "E1", Other: "o"})
	if extra["id"] != "E1" {
		t.Errorf("non-empty logextra should still be lifted: %v", extra)
	}
}

// --- Serialization failures are reported, never silent --------------------

func TestUnserializablePayloadIsReported(t *testing.T) {
	var buf bytes.Buffer
	var hookErrs []error
	log := New(
		WithWriter(&buf),
		WithOnError(func(err error) { hookErrs = append(hookErrs, err) }),
	)

	log.Info("m", "e").WithPayload(map[string]any{"x": math.NaN()}).Log()

	m := parseLine(t, &buf)
	payload, _ := m["payload"].(string)
	if !strings.HasPrefix(payload, "[unserializable:") {
		t.Errorf("payload should carry a visible marker, got %q", payload)
	}
	if m["error_type"] != "LogSerializationError" {
		t.Errorf("error_type = %v", m["error_type"])
	}
	if m["error_message"] == nil || m["error_message"] == "" {
		t.Errorf("error_message should describe the failure: %v", m["error_message"])
	}
	if len(hookErrs) != 1 {
		t.Fatalf("OnError should fire once, got %d", len(hookErrs))
	}
}

// TestUnserializablePayloadKeepsCallerError verifies a serialization failure
// never overwrites an error the caller attached to the entry.
func TestUnserializablePayloadKeepsCallerError(t *testing.T) {
	var buf bytes.Buffer
	log := New(WithWriter(&buf))

	log.Error("m", "e").
		WithError(errors.New("original failure")).
		WithPayload(map[string]any{"x": math.Inf(1)}).
		Log()

	m := parseLine(t, &buf)
	if m["error_message"] != "original failure" {
		t.Errorf("caller error should win, got %v", m["error_message"])
	}
}

// errWriter fails every write, standing in for a full disk or closed pipe.
type errWriter struct{ err error }

func (w errWriter) Write([]byte) (int, error) { return 0, w.err }

func TestWriteErrorReachesOnError(t *testing.T) {
	want := errors.New("disk full")
	var got []error
	log := New(
		WithWriter(errWriter{err: want}),
		WithOnError(func(err error) { got = append(got, err) }),
	)

	log.Info("m", "e").Log()

	if len(got) != 1 || !errors.Is(got[0], want) {
		t.Errorf("write error should reach OnError, got %v", got)
	}
}

func TestOnErrorAbsentIsSafe(t *testing.T) {
	// No hook configured: a failing writer must not panic.
	log := New(WithWriter(errWriter{err: errors.New("nope")}))
	log.Info("m", "e").WithPayload(map[string]any{"x": math.NaN()}).Log()
}

// --- trace_id is no longer invented per line ------------------------------

func TestNoAutoTraceIDByDefault(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)

	log.Info("m", "e").Log()

	m := parseLine(t, &buf)
	if _, present := m["trace_id"]; present {
		t.Errorf("trace_id should be omitted when none is resolved, got %v", m["trace_id"])
	}
}

func TestWithAutoTraceID(t *testing.T) {
	var buf bytes.Buffer
	log := New(WithWriter(&buf), WithAutoTraceID())

	log.Info("m", "e").Log()

	m := parseLine(t, &buf)
	id, _ := m["trace_id"].(string)
	if len(id) != 32 {
		t.Errorf("auto trace_id = %q; want 32 hex chars", id)
	}
}

func TestExplicitTraceIDStillWins(t *testing.T) {
	var buf bytes.Buffer
	log := New(WithWriter(&buf), WithAutoTraceID())

	log.Info("m", "e").WithTraceID("given").Log()

	if m := parseLine(t, &buf); m["trace_id"] != "given" {
		t.Errorf("trace_id = %v", m["trace_id"])
	}
}

// --- Correlation ID validation -------------------------------------------

func TestIsValidCorrelationID(t *testing.T) {
	valid := []string{
		"abc123",
		"b7f5e0b3b78b4b0fb2df8e5a9c3e22e5",
		"550e8400-e29b-41d4-a716-446655440000",
		"00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		"svc.api:req_1",
	}
	for _, id := range valid {
		if !IsValidCorrelationID(id) {
			t.Errorf("IsValidCorrelationID(%q) = false; want true", id)
		}
	}

	invalid := []string{
		"",
		strings.Repeat("a", MaxCorrelationIDLen+1),
		"has space",
		"quote\"injected",
		"brace{}",
		"newline\nsplit",
		"türkçe",
	}
	for _, id := range invalid {
		if IsValidCorrelationID(id) {
			t.Errorf("IsValidCorrelationID(%q) = true; want false", id)
		}
	}
}

// TestMaskCoversLogExtra: an entry-level (or bound) mask strategy must follow a
// field that a logextra tag lifts into the extra object — extra must never
// carry a value the payload already hides.
func TestMaskCoversLogExtra(t *testing.T) {
	type P struct {
		RefID string `json:"refId" logextra:"true"`
		Note  string `json:"note"`
	}

	var buf bytes.Buffer
	log := New(WithWriter(&buf))
	log.Info("t", "e").WithPayload(P{RefID: "REF-SECRET-1", Note: "n"}).
		Mask("refId", HideAll).Log()

	line := buf.String()
	if strings.Contains(line, "REF-SECRET-1") {
		t.Errorf("masked logextra field leaked through extra: %s", line)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &m); err != nil {
		t.Fatal(err)
	}
	extra := m["extra"].(map[string]any)
	if extra["refId"] != "********" {
		t.Errorf("extra.refId should be masked: %v", extra["refId"])
	}

	// The bound (With) variant must behave the same.
	buf.Reset()
	bound := log.With().Mask("refId", HideAll).Logger()
	bound.Info("t", "e").WithPayload(P{RefID: "REF-SECRET-2", Note: "n"}).Log()
	if strings.Contains(buf.String(), "REF-SECRET-2") {
		t.Errorf("bound mask should cover logextra too: %s", buf.String())
	}
}

// unexpBase is an unexported struct embedded in payload types below; its
// exported fields must be promoted exactly as encoding/json promotes them.
type unexpBase struct {
	ID string `json:"id"`
	N  int    `json:"n"`
}

// TestPayloadUnexportedEmbeddedMatchesEncodingJSON locks the unexported
// embedded shapes to encoding/json: untagged promotes, a named tag nests,
// `json:"-"` drops, and pointer variants follow the pointer rules.
func TestPayloadUnexportedEmbeddedMatchesEncodingJSON(t *testing.T) {
	type Promoted struct {
		unexpBase
		Name string `json:"name"`
	}
	type Named struct {
		unexpBase `json:"base"`
		Name      string `json:"name"`
	}
	type Dropped struct {
		unexpBase `json:"-"`
		Name      string `json:"name"`
	}
	type PtrPromoted struct {
		*unexpBase
		Name string `json:"name"`
	}

	payloadEqualsStdlib(t, Promoted{unexpBase: unexpBase{ID: "42", N: 7}, Name: "n"})
	payloadEqualsStdlib(t, Named{unexpBase: unexpBase{ID: "42"}, Name: "n"})
	payloadEqualsStdlib(t, Dropped{unexpBase: unexpBase{ID: "42"}, Name: "n"})
	payloadEqualsStdlib(t, PtrPromoted{unexpBase: &unexpBase{ID: "42"}, Name: "n"})
	payloadEqualsStdlib(t, PtrPromoted{Name: "n"}) // nil embedded pointer
}
