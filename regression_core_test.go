package gophlog

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"testing"
	"time"
)

// Regression tests for the v1.1.1 core-logger fixes.

// noPanic runs f and fails the test instead of crashing the whole binary when f
// panics, so a regression shows up as one failing test.
func noPanic(t *testing.T, f func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("unexpected panic: %v", r)
		}
	}()
	f()
}

// --- A writer panic must not leave the core mutex locked ------------------

// panicOnceWriter panics on its first Write and records every later one.
type panicOnceWriter struct {
	calls int
	buf   bytes.Buffer
}

func (w *panicOnceWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls == 1 {
		panic("writer boom")
	}
	return w.buf.Write(p)
}

func TestWriterPanicReleasesLock(t *testing.T) {
	w := &panicOnceWriter{}
	log := New(WithWriter(w))

	func() {
		defer func() {
			if r := recover(); r != "writer boom" {
				t.Errorf("the writer panic should reach the caller unchanged, got %v", r)
			}
		}()
		log.Info("first", "e").Log()
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		log.With().Category("child").Logger().Info("second", "e").Log() // shares the core mutex
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Log blocked after a writer panic: the core mutex was never released")
	}
	if !strings.Contains(w.buf.String(), `"message":"second"`) {
		t.Errorf("the next entry should be written, got %q", w.buf.String())
	}
}

// --- A faulty Error method must not crash the caller ----------------------

// nilDerefErr dereferences its receiver, so a typed-nil value panics in Error.
type nilDerefErr struct{ msg string }

func (e *nilDerefErr) Error() string { return e.msg }

// nilSafeErr handles a nil receiver itself; its own text must be kept.
type nilSafeErr struct{}

func (e *nilSafeErr) Error() string {
	if e == nil {
		return "nil-safe"
	}
	return "set"
}

// panicErr panics in Error on a non-nil receiver.
type panicErr struct{}

func (panicErr) Error() string { panic("boom") }

func TestErrorStringMatchesFmt(t *testing.T) {
	var typedNil *nilDerefErr
	var nilSafe *nilSafeErr
	for _, err := range []error{typedNil, nilSafe, panicErr{}, errors.New("plain")} {
		var got string
		noPanic(t, func() { got = errorString(err) })
		if want := fmt.Sprint(err); got != want {
			t.Errorf("errorString(%T) = %q; want fmt's %q", err, got, want)
		}
	}
}

func TestWithErrorTypedNil(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)
	var e *nilDerefErr

	noPanic(t, func() { log.Error("m", "e").WithError(e).Log() })

	m := parseLine(t, &buf)
	if m["error_message"] != "<nil>" {
		t.Errorf("error_message = %v; want <nil>", m["error_message"])
	}
	if m["error_type"] != "nilDerefErr" {
		t.Errorf("error_type = %v", m["error_type"])
	}
}

func TestWithErrorPanickingErrorMethod(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)

	noPanic(t, func() { log.Error("m", "e").WithError(panicErr{}).Log() })

	m := parseLine(t, &buf)
	if m["error_message"] != "%!v(PANIC=Error method: boom)" {
		t.Errorf("error_message = %v", m["error_message"])
	}
}

func TestSlogTypedNilErrorAttr(t *testing.T) {
	var buf bytes.Buffer
	sl := slog.New(NewSlogHandler(newTestLogger(&buf), nil))
	var e *nilDerefErr

	noPanic(t, func() { sl.Error("m", "err", error(e), "k", "v") })

	extra, _ := parseLine(t, &buf)["extra"].(map[string]any)
	if extra["err"] != "<nil>" || extra["k"] != "v" {
		t.Errorf("extra = %v; want err=<nil> alongside the other attrs", extra)
	}
}

// --- A non-finite float must not cost the rest of the line ----------------

func TestNonFiniteExtraKeepsLine(t *testing.T) {
	var buf bytes.Buffer
	var hookErrs []error
	log := New(WithWriter(&buf), WithOnError(func(err error) { hookErrs = append(hookErrs, err) }))

	log.Info("m", "e").
		WithTenant("t1").
		WithStatus(200).
		WithPayload(map[string]int{"a": 1}).
		WithExtraField("ratio", math.NaN()).
		WithExtraField("order_id", "o-1").
		Log()

	m := parseLine(t, &buf)
	if m["tenant_id"] != "t1" || m["http_status"] != float64(200) || m["payload"] != `{"a":1}` {
		t.Errorf("fields next to the NaN were dropped: %v", m)
	}
	extra, _ := m["extra"].(map[string]any)
	if extra["ratio"] != "NaN" || extra["order_id"] != "o-1" {
		t.Errorf("extra = %v; want ratio=\"NaN\" and order_id kept", extra)
	}
	if _, present := m["error_type"]; present {
		t.Errorf("nothing was dropped, so no serialization error should be recorded: %v", m["error_type"])
	}
	if len(hookErrs) != 1 {
		t.Errorf("OnError should still hear about the encode failure once, got %v", hookErrs)
	}
}

func TestNonFiniteNestedExtra(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)
	callerMap := map[string]any{"x": math.Inf(-1), "y": 2.5}

	log.Info("m", "e").
		WithExtraField("g", callerMap).
		WithExtraField("series", []float64{1, math.Inf(1)}).
		WithExtraField("p", &[]any{float32(math.NaN())}).
		Log()

	extra, _ := parseLine(t, &buf)["extra"].(map[string]any)
	if g, _ := extra["g"].(map[string]any); g["x"] != "-Inf" || g["y"] != 2.5 {
		t.Errorf("extra.g = %v", extra["g"])
	}
	if s, _ := extra["series"].([]any); len(s) != 2 || s[0] != float64(1) || s[1] != "+Inf" {
		t.Errorf("extra.series = %v", extra["series"])
	}
	if p, _ := extra["p"].([]any); len(p) != 1 || p[0] != "NaN" {
		t.Errorf("extra.p = %v", extra["p"])
	}
	if x, _ := callerMap["x"].(float64); !math.IsInf(x, -1) {
		t.Errorf("the caller's map must not be rewritten, got %v", callerMap["x"])
	}
}

func TestNonFiniteBoundExtraNotMutated(t *testing.T) {
	var buf bytes.Buffer
	child := newTestLogger(&buf).With().ExtraField("r", math.NaN()).Logger()

	for i := 0; i < 2; i++ {
		buf.Reset()
		child.Info("m", "e").Log()
		if extra, _ := parseLine(t, &buf)["extra"].(map[string]any); extra["r"] != "NaN" {
			t.Fatalf("line %d: extra = %v", i, extra)
		}
	}
	if r, _ := child.bound.extra["r"].(float64); !math.IsNaN(r) {
		t.Errorf("bound extras are shared and must stay untouched, got %v", child.bound.extra["r"])
	}
}

// TestNonFiniteDurationOmitted covers the typed schema field: duration_ms is
// numeric in the index mapping, so a "NaN" string there would get the whole
// document rejected. The field is omitted and the rest of the line kept.
func TestNonFiniteDurationOmitted(t *testing.T) {
	for _, d := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		var buf bytes.Buffer
		var hookErrs []error
		log := New(WithWriter(&buf), WithOnError(func(err error) { hookErrs = append(hookErrs, err) }))

		log.Info("m", "e").WithTenant("t1").WithStatus(503).WithDuration(d).Log()

		m := parseLine(t, &buf)
		if _, present := m["duration_ms"]; present {
			t.Errorf("duration %v: duration_ms should be omitted, got %v", d, m["duration_ms"])
		}
		if m["tenant_id"] != "t1" || m["http_status"] != float64(503) || m["message"] != "m" {
			t.Errorf("duration %v: the rest of the line was dropped: %v", d, m)
		}
		if _, present := m["error_type"]; present {
			t.Errorf("duration %v: want the full line, not the minimal record: %v", d, m)
		}
		if len(hookErrs) != 1 {
			t.Errorf("duration %v: OnError should hear about the failure once, got %v", d, hookErrs)
		}
	}
}

func TestNonFiniteIntegrationDurationOmitted(t *testing.T) {
	var buf bytes.Buffer
	info := &IntegrationInfo{Target: "svc", Status: IntegrationFail, ExternalDurationMs: ptrTo(math.NaN()), RetryCount: ptrTo(2)}

	newTestLogger(&buf).Info("m", "e").
		WithDuration(12.5).
		WithIntegration(info).
		WithExtraField("k", "v").
		Log()

	m := parseLine(t, &buf)
	in, _ := m["integration"].(map[string]any)
	if _, present := in["external_duration_ms"]; present {
		t.Errorf("external_duration_ms should be omitted, got %v", in["external_duration_ms"])
	}
	if in["target"] != "svc" || in["status"] != "fail" || in["retry_count"] != float64(2) {
		t.Errorf("integration = %v", m["integration"])
	}
	if m["duration_ms"] != 12.5 {
		t.Errorf("a finite duration must survive, got %v", m["duration_ms"])
	}
	if extra, _ := m["extra"].(map[string]any); extra["k"] != "v" {
		t.Errorf("extra = %v", m["extra"])
	}
	if info.ExternalDurationMs == nil {
		t.Error("the caller's IntegrationInfo must not be modified")
	}
}

func ptrTo[T any](v T) *T { return &v }

func TestSlogNonFiniteAttrKeepsOthers(t *testing.T) {
	var buf bytes.Buffer
	sl := slog.New(NewSlogHandler(newTestLogger(&buf), nil))

	sl.Info("m", "user", "u1", "ratio", math.NaN(), slog.Group("g", "inf", math.Inf(1)))

	extra, _ := parseLine(t, &buf)["extra"].(map[string]any)
	g, _ := extra["g"].(map[string]any)
	if extra["user"] != "u1" || extra["ratio"] != "NaN" || g["inf"] != "+Inf" {
		t.Errorf("extra = %v", extra)
	}
}

// TestNonFiniteInStructFallsBack pins the limit of the retry: struct values are
// not rewritten (their JSON shape depends on tags), so the entry still falls
// back to the minimal record.
func TestNonFiniteInStructFallsBack(t *testing.T) {
	var buf bytes.Buffer
	newTestLogger(&buf).Info("m", "e").WithExtraField("s", struct{ F float64 }{math.NaN()}).Log()

	if m := parseLine(t, &buf); m["error_type"] != "LogSerializationError" || m["message"] != "m" {
		t.Errorf("want the minimal record, got %v", m)
	}
}

// TestNonFiniteCyclicExtraTerminates guards against a value that references
// itself (twice, so a depth cap alone would be exponential) next to a NaN: the
// cycle is cut with its marker, the NaN is written as a string and the rest of
// the entry is kept.
func TestNonFiniteCyclicExtraTerminates(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)
	cyclic := []any{nil, nil, math.NaN()}
	cyclic[0], cyclic[1] = cyclic, cyclic

	done := make(chan struct{})
	go func() {
		defer close(done)
		log.Info("m", "e").WithExtraField("c", cyclic).Log()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("logging a cyclic extra with a NaN did not return")
	}
	m := parseLine(t, &buf)
	if m["event"] != "e" || m["message"] != "m" {
		t.Errorf("want the entry kept, got %v", m)
	}
	extra, _ := m["extra"].(map[string]any)
	c, _ := extra["c"].([]any)
	if len(c) != 3 || c[0] != cycleMarker || c[1] != cycleMarker || c[2] != "NaN" {
		t.Errorf("extra.c = %v; want [%s %s NaN]", extra["c"], cycleMarker, cycleMarker)
	}
}

// --- Level names are case-insensitive and written canonically -------------

func TestLevelCaseInsensitive(t *testing.T) {
	var buf bytes.Buffer
	log := New(WithWriter(&buf), WithMinLevel(Level("error")))

	log.Warn("dropped", "e").Log()
	if buf.Len() != 0 {
		t.Errorf("WithMinLevel(\"error\") should drop WARN, got %q", buf.String())
	}
	if !log.Enabled(Level("Error")) || log.Enabled(Level("warn")) {
		t.Error("Enabled should resolve level names case-insensitively")
	}

	log.At(Level("error"), "kept", "e").Log()
	if m := parseLine(t, &buf); m["level"] != "ERROR" {
		t.Errorf("level = %v; want the canonical ERROR", m["level"])
	}

	log.SetMinLevel(Level("debug"))
	if !log.Enabled(DEBUG) || log.Enabled(TRACE) {
		t.Error("SetMinLevel(\"debug\") should enable DEBUG and nothing below it")
	}
}

// TestLevelOutputSpelling pins what is written as "level": a known name in any
// casing becomes its canonical spelling, while an unrecognised value is written
// exactly as given (as in v1.1.0) and only filters as INFO.
func TestLevelOutputSpelling(t *testing.T) {
	for in, want := range map[Level]string{
		"trace": "TRACE", "Debug": "DEBUG", "wArN": "WARN", "fatal": "FATAL",
		"warning": "warning", "NOTICE": "NOTICE", "": "",
	} {
		var buf bytes.Buffer
		newTestLogger(&buf).At(in, "m", "e").Log()

		got, present := parseLine(t, &buf)["level"].(string)
		if !present || got != want {
			t.Errorf("At(%q) wrote level %q; want %q", in, got, want)
		}
	}

	var buf bytes.Buffer
	log := New(WithWriter(&buf), WithMinLevel(WARN))
	log.At(Level("NOTICE"), "m", "e").Log()
	if buf.Len() != 0 {
		t.Errorf("an unrecognised level should filter as INFO, got %q", buf.String())
	}
}

// --- WithError on a disabled level does no work ---------------------------

// countingErr counts Error calls, so the test can see whether WithError did
// any work for an entry that is never emitted.
type countingErr struct{ calls *int }

func (e countingErr) Error() string { *e.calls++; return "counted" }

// TestDisabledLevelAllocatesNothing pins the documented "a filtered-out call
// allocates nothing": the entry points must stay inlinable so the Entry never
// reaches the heap, whatever the level's spelling.
func TestDisabledLevelAllocatesNothing(t *testing.T) {
	log := New(WithWriter(&bytes.Buffer{}), WithMinLevel(ERROR))
	err := errors.New("noise")

	allocs := testing.AllocsPerRun(100, func() {
		log.Debug("m", "e").WithTraceID("t").WithError(err).Log()
		log.At(Level("warn"), "m", "e").WithError(err).Log()
	})
	if allocs != 0 {
		t.Errorf("a disabled log call allocated %v times; want 0", allocs)
	}
}

func TestWithErrorDisabledLevelDoesNoWork(t *testing.T) {
	log := New(WithWriter(&bytes.Buffer{}), WithMinLevel(ERROR))
	calls := 0

	e := log.Debug("m", "e").WithError(countingErr{&calls})

	if calls != 0 || e.errorType != "" || e.errorMessage != "" || e.stackTrace != "" {
		t.Errorf("disabled WithError did work: calls=%d type=%q msg=%q", calls, e.errorType, e.errorMessage)
	}
}
