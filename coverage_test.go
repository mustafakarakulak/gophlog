package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"testing"
	"time"
)

func testTime() time.Time {
	return time.Date(2026, 1, 11, 0, 15, 34, 123_000_000, time.UTC)
}

// TestEntryAllBuilders chains every With* builder and verifies the field lands
// in the emitted event.
func TestEntryAllBuilders(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)

	log.Info("msg", "evt").
		WithTraceID("trace-1").
		WithLogType(LogTypeAudit).
		WithCategory("billing").
		WithTenant("t1").
		WithUser("u1").
		WithClientIP("1.2.3.4").
		WithSession("s1").
		WithSpanID("span-1").
		WithRequestID("req-1").
		WithHTTP("POST", "/pay").
		WithStatus(201).
		WithDuration(12.5).
		WithQueryParams(map[string]string{"a": "b"}).
		WithBytes(10, 20).
		WithRequestBody(`{"x":1}`).
		WithResponseBody(`{"ok":true}`).
		WithStackTrace("at main()").
		WithExtraField("k", "v").
		WithWorkflow("cw", "run", "pw").
		WithQueueMessage("q1", "m1", 2, true).
		WithJobInfo("nightly", "0 0 * * *", "r9").
		Log()

	m := parseLine(t, &buf)
	checks := map[string]any{
		"log_type":           "audit",
		"category":           "billing",
		"tenant_id":          "t1",
		"user_id":            "u1",
		"client_ip":          "1.2.3.4",
		"session_id":         "s1",
		"span_id":            "span-1",
		"request_id":         "req-1",
		"http_method":        "POST",
		"http_path":          "/pay",
		"request_body":       `{"x":1}`,
		"response_body":      `{"ok":true}`,
		"stack_trace":        "at main()",
		"child_workflow_id":  "cw",
		"run_id":             "run",
		"parent_workflow_id": "pw",
	}
	for k, want := range checks {
		if m[k] != want {
			t.Errorf("%s = %v; want %v", k, m[k], want)
		}
	}
	if int(m["http_status"].(float64)) != 201 {
		t.Errorf("http_status = %v", m["http_status"])
	}
	if m["duration_ms"].(float64) != 12.5 {
		t.Errorf("duration_ms = %v", m["duration_ms"])
	}
	if m["extra"].(map[string]any)["k"] != "v" {
		t.Errorf("extra = %v", m["extra"])
	}
	if m["queue"].(map[string]any)["queue_name"] != "q1" {
		t.Errorf("queue = %v", m["queue"])
	}
	if m["job"].(map[string]any)["name"] != "nightly" {
		t.Errorf("job = %v", m["job"])
	}
}

func TestEntryWithIntegrationQueueJobStructs(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)

	log.Info("m", "e").
		WithTraceID("x").
		WithIntegration(&IntegrationInfo{Target: "svc", Status: IntegrationFail}).
		WithQueue(&QueueInfo{QueueName: "q"}).
		WithJob(&JobInfo{Name: "j"}).
		Log()

	m := parseLine(t, &buf)
	if m["integration"].(map[string]any)["target"] != "svc" {
		t.Errorf("integration = %v", m["integration"])
	}
}

func TestEntryWithPayloadMaskedAndMaskMany(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)

	log.Info("m", "e").
		WithTraceID("x").
		WithPayloadMasked(map[string]any{"pin": "1234", "card": "4111111111111111"},
			map[string]MaskingStrategy{"pin": HideAll}).
		MaskMany(map[string]MaskingStrategy{"card": CreditCard}).
		Log()

	m := parseLine(t, &buf)
	payload := m["payload"].(string)
	if strings.Contains(payload, "1234") {
		t.Errorf("pin should be masked: %s", payload)
	}
	if strings.Contains(payload, "4111111111111111") {
		t.Errorf("card should be masked: %s", payload)
	}
}

func TestEntryWithErrorNilNoop(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)
	log.Info("m", "e").WithTraceID("x").WithError(nil).Log()
	m := parseLine(t, &buf)
	if _, ok := m["error_type"]; ok {
		t.Error("nil error should not set error fields")
	}
}

func TestEntryWithErrorCapturesStack(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)
	log.Error("failed", "op").WithTraceID("x").WithError(errors.New("boom")).Log()
	m := parseLine(t, &buf)
	if m["error_type"] != "error" {
		t.Errorf("error_type = %v", m["error_type"])
	}
	if m["error_message"] != "boom" {
		t.Errorf("error_message = %v", m["error_message"])
	}
	if st, _ := m["stack_trace"].(string); !strings.Contains(st, "at ") {
		t.Errorf("stack_trace should be captured: %q", st)
	}
}

func TestEntryWithErrorSkipsStackWhenDisabled(t *testing.T) {
	var buf bytes.Buffer
	log := New(WithWriter(&buf), WithMinLevel(ERROR))
	// DEBUG is below ERROR: the entry is dropped and the stack must not be captured.
	e := log.Debug("m", "e").WithError(errors.New("x"))
	if e.stackTrace != "" {
		t.Errorf("stack should not be captured for a disabled level, got %q", e.stackTrace)
	}
}

func TestErrorTypeNameVariants(t *testing.T) {
	if got := errorTypeName(fmt.Errorf("wrap: %w", errors.New("x"))); got != "error" {
		t.Errorf("fmt.Errorf → %q", got)
	}
	if got := errorTypeName(&ptrErr{}); got != "ptrErr" {
		t.Errorf("pointer custom → %q", got)
	}
	if got := errorTypeName(valErr{}); got != "valErr" {
		t.Errorf("value custom → %q", got)
	}
}

type ptrErr struct{}

func (*ptrErr) Error() string { return "ptr" }

type valErr struct{}

func (valErr) Error() string { return "val" }

func TestContextHelpersResolved(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)

	ctx := context.Background()
	ctx, id := EnsureCorrelationID(ctx)
	if id == "" {
		t.Fatal("EnsureCorrelationID should generate an id")
	}
	// Second call returns the same id.
	if _, id2 := EnsureCorrelationID(ctx); id2 != id {
		t.Errorf("EnsureCorrelationID changed id: %q vs %q", id, id2)
	}
	ctx = WithSpanID(ctx, "span-9")
	ctx = WithRequestID(ctx, "req-9")
	ctx = WithTenantID(ctx, "ten-9")
	ctx = WithUserID(ctx, "usr-9")
	ctx = WithClientIP(ctx, "9.9.9.9")
	ctx = WithSessionID(ctx, "ses-9")
	ctx = WithWorkflow(ctx, "cw", "run", "pw")

	log.Info("m", "e").Ctx(ctx).Log()
	m := parseLine(t, &buf)
	want := map[string]any{
		"trace_id":           id,
		"span_id":            "span-9",
		"request_id":         "req-9",
		"tenant_id":          "ten-9",
		"user_id":            "usr-9",
		"client_ip":          "9.9.9.9",
		"session_id":         "ses-9",
		"child_workflow_id":  "cw",
		"run_id":             "run",
		"parent_workflow_id": "pw",
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %v; want %v", k, m[k], v)
		}
	}
}

func TestNewCorrelationIDFormat(t *testing.T) {
	id := NewCorrelationID()
	if len(id) != 32 {
		t.Errorf("len = %d; want 32", len(id))
	}
	if id == NewCorrelationID() {
		t.Error("two ids should differ")
	}
}

func TestDefaultPackageFunctions(t *testing.T) {
	saved := Default()
	defer SetDefault(saved)

	SetDefault(nil) // nil is ignored
	if Default() != saved {
		t.Error("SetDefault(nil) should be a no-op")
	}

	var buf bytes.Buffer
	SetDefault(New(WithWriter(&buf)))

	Trace("a", "e").Log()
	Debug("b", "e").Log()
	Info("c", "e").Log()
	Warn("d", "e").Log()
	Error("f", "e").Log()
	Fatal("g", "e").Log()

	lines := strings.Count(strings.TrimSpace(buf.String()), "\n") + 1
	if lines != 6 {
		t.Errorf("expected 6 log lines, got %d", lines)
	}
}

func TestLoggerOptions(t *testing.T) {
	var buf bytes.Buffer
	log := New(
		WithWriter(&buf),
		WithKubernetes(&KubernetesInfo{PodName: "pod-1", Namespace: "ns"}),
		WithStackTraceLimit(5),
		WithTraceExtractor(func(ctx context.Context) (string, string) {
			return "tid-from-extractor", "sid-from-extractor"
		}),
	)

	log.Error("m", "e").WithError(errors.New("boom")).Log()
	m := parseLine(t, &buf)
	if m["kubernetes"].(map[string]any)["pod_name"] != "pod-1" {
		t.Errorf("kubernetes = %v", m["kubernetes"])
	}
	if m["trace_id"] != "tid-from-extractor" {
		t.Errorf("trace_id from extractor = %v", m["trace_id"])
	}
	if m["span_id"] != "sid-from-extractor" {
		t.Errorf("span_id from extractor = %v", m["span_id"])
	}
	if st := m["stack_trace"].(string); !strings.HasSuffix(st, "... [truncated]") {
		t.Errorf("stack should be truncated at limit 5: %q", st)
	}
}

func TestWithKubernetesFromEnv(t *testing.T) {
	t.Setenv("POD_NAME", "pod-x")
	t.Setenv("POD_NAMESPACE", "ns-x")
	t.Setenv("NODE_NAME", "node-x")
	t.Setenv("CONTAINER_NAME", "cont-x")

	var buf bytes.Buffer
	log := New(WithWriter(&buf), WithKubernetesFromEnv())
	log.Info("m", "e").WithTraceID("x").Log()
	m := parseLine(t, &buf)
	k := m["kubernetes"].(map[string]any)
	if k["pod_name"] != "pod-x" || k["namespace"] != "ns-x" || k["node_name"] != "node-x" || k["container_name"] != "cont-x" {
		t.Errorf("kubernetes from env = %v", k)
	}
}

func TestMaskStringEdgeCases(t *testing.T) {
	cases := []struct {
		in       string
		strategy MaskingStrategy
		want     string
	}{
		{"", HideAll, ""},                            // empty passthrough
		{"1", ShowFirst1, "*"},                       // single char, show-first collapses
		{"ab", ShowFirst2, "**"},                     // n<=2 fully hidden
		{"ab", ShowFirst1AndLast1, "**"},             // n<=2 fully hidden
		{"abcd", ShowFirst2AndLast2, "****"},         // n<=4 fully hidden
		{"abcde", ShowFirst2AndLast2, "ab*de"},       // normal
		{"verylongsecretvalue", HideAll, "********"}, // capped at 8
	}
	for _, c := range cases {
		if got := MaskString(c.in, c.strategy); got != c.want {
			t.Errorf("MaskString(%q,%q) = %q; want %q", c.in, c.strategy, got, c.want)
		}
	}
}

func TestMaskCreditCardEdge(t *testing.T) {
	if got := MaskString("123", CreditCard); got != "***" {
		t.Errorf("<=4 cc = %q", got)
	}
	if got := MaskString("", CreditCard); got != "" {
		t.Errorf("empty cc = %q", got)
	}
}

func TestParseStrategyAll(t *testing.T) {
	all := []MaskingStrategy{HideAll, ShowFirst1, ShowLast1, ShowFirst2, ShowLast2, ShowFirst1AndLast1, ShowFirst2AndLast2, CreditCard}
	for _, s := range all {
		if got, ok := parseStrategy(string(s)); !ok || got != s {
			t.Errorf("parseStrategy(%q) = %q,%v", s, got, ok)
		}
	}
	if _, ok := parseStrategy("nonsense"); ok {
		t.Error("unknown strategy should not parse")
	}
	if got, ok := parseStrategy("  HIDEALL  "); !ok || got != HideAll {
		t.Errorf("parseStrategy trims/lowercases: %q,%v", got, ok)
	}
}

func TestMaskScalarTypes(t *testing.T) {
	if maskScalar(nil, HideAll) != nil {
		t.Error("nil stays nil")
	}
	if maskScalar("", HideAll) != "" {
		t.Error("empty string stays empty")
	}
	if got := maskScalar(true, HideAll); got != "****" {
		t.Errorf("bool masked = %v", got)
	}
	if got := maskScalar(12345, HideAll); got != "*****" {
		t.Errorf("int masked = %v", got)
	}
	// Non-scalar (slice) returned unchanged.
	in := []any{1, 2}
	if got := maskScalar(in, HideAll); fmt.Sprint(got) != fmt.Sprint(in) {
		t.Errorf("non-scalar should pass through: %v", got)
	}
}

func TestScalarToString(t *testing.T) {
	cases := map[any]string{
		"s":              "s",
		true:             "true",
		float64(1.5):     "1.5",
		float32(2.5):     "2.5",
		int(7):           "7",
		int64(9):         "9",
		json.Number("3"): "3",
	}
	for in, want := range cases {
		if got := scalarToString(in); got != want {
			t.Errorf("scalarToString(%v) = %q; want %q", in, got, want)
		}
	}
	// default branch marshals.
	if got := scalarToString([]int{1, 2}); got != "[1,2]" {
		t.Errorf("default marshal = %q", got)
	}
}

func TestStringifyJSON(t *testing.T) {
	if stringifyJSON(nil) != "" {
		t.Error("nil → empty")
	}
	if stringifyJSON("raw") != "raw" {
		t.Error("string passthrough")
	}
	if stringifyJSON(map[string]int{"a": 1}) != `{"a":1}` {
		t.Error("marshal map")
	}
	if stringifyJSON(make(chan int)) != "" {
		t.Error("unmarshalable → empty")
	}
}

func TestTruncate(t *testing.T) {
	if truncate("abc", 0) != "abc" {
		t.Error("max<=0 disables")
	}
	if truncate("abc", 10) != "abc" {
		t.Error("under limit unchanged")
	}
	if got := truncate("abcdef", 3); got != "abc... [truncated]" {
		t.Errorf("truncated = %q", got)
	}
}

func TestProcessPayloadKinds(t *testing.T) {
	type inner struct {
		B int `json:"b"`
	}
	type outer struct {
		A    string         `json:"a"`
		In   inner          `json:"in"`
		Buf  []byte         `json:"buf"`
		Nums []int          `json:"nums"`
		M    map[string]int `json:"m"`
		Skip string         `json:"-"`
		priv string
	}
	v := outer{A: "x", In: inner{B: 2}, Buf: []byte("hi"), Nums: []int{1, 2}, M: map[string]int{"k": 9}, Skip: "no", priv: "no"}
	out, _ := processPayload(v)
	mm := out.(map[string]any)
	if mm["a"] != "x" {
		t.Errorf("a = %v", mm["a"])
	}
	if mm["in"].(map[string]any)["b"] != 2 {
		t.Errorf("nested = %v", mm["in"])
	}
	if _, ok := mm["-"]; ok {
		t.Error("json:- should be skipped")
	}
	if _, ok := mm["Skip"]; ok {
		t.Error("skipped field present")
	}
	if mm["nums"].([]any)[0] != 1 {
		t.Errorf("nums = %v", mm["nums"])
	}
	if mm["m"].(map[string]any)["k"] != 9 {
		t.Errorf("map = %v", mm["m"])
	}
}

func TestProcessPayloadNilPointer(t *testing.T) {
	var p *struct{ A int }
	out, extra := processPayload(p)
	if out != nil || extra != nil {
		t.Errorf("nil pointer payload → nil,nil; got %v,%v", out, extra)
	}
}

func TestProcessPayloadLogExtra(t *testing.T) {
	type withExtra struct {
		ID    string `json:"id" logextra:"true"`
		Other string `json:"other"`
	}
	out, extra := processPayload(withExtra{ID: "E1", Other: "o"})
	if extra["id"] != "E1" {
		t.Errorf("logextra should lift id: %v", extra)
	}
	mm := out.(map[string]any)
	if _, ok := mm["id"]; ok {
		t.Error("logextra field should be removed from payload")
	}
}

// --- slog internals ---

func TestSlogNewSlogLogger(t *testing.T) {
	var buf bytes.Buffer
	l := NewSlogLogger(New(WithWriter(&buf)), nil)
	l.Info("hi")
	if buf.Len() == 0 {
		t.Error("NewSlogLogger should emit")
	}
}

func TestSlogInlineGroupAndEmptyGroup(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)
	h := NewSlogHandler(log, nil)

	rec := slog.NewRecord(testTime(), slog.LevelInfo, "m", 0)
	rec.AddAttrs(
		slog.Group("", slog.String("inlined", "yes")), // empty-key group → inlined
		slog.Group("empty"),                           // empty group → omitted
		slog.Group("g", slog.Int("n", 1)),
	)
	_ = h.Handle(context.Background(), rec)

	m := parseLine(t, &buf)
	extra := m["extra"].(map[string]any)
	if extra["inlined"] != "yes" {
		t.Errorf("inline group should be promoted: %v", extra)
	}
	if _, ok := extra["empty"]; ok {
		t.Errorf("empty group should be omitted: %v", extra)
	}
	if extra["g"].(map[string]any)["n"] != float64(1) {
		t.Errorf("named group = %v", extra["g"])
	}
}

func TestSlogAddSource(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)
	h := NewSlogHandler(log, &SlogOptions{AddSource: true})

	pcs := make([]uintptr, 1)
	runtime.Callers(1, pcs)
	rec := slog.NewRecord(testTime(), slog.LevelInfo, "m", pcs[0])
	_ = h.Handle(context.Background(), rec)

	m := parseLine(t, &buf)
	src, ok := m["extra"].(map[string]any)["source"].(map[string]any)
	if !ok {
		t.Fatalf("source should be present: %v", m["extra"])
	}
	if src["file"] == "" || src["function"] == "" {
		t.Errorf("source incomplete: %v", src)
	}
}
