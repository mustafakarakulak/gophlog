package httplog

import (
	"io"
	"net/url"
	"reflect"
	"strings"
	"testing"

	logging "github.com/mustafakarakulak/go-logging"
)

func TestCaptureBodyShort(t *testing.T) {
	body := io.NopCloser(strings.NewReader("hello"))
	captured, restored, truncated := CaptureBody(body, 100)
	if truncated {
		t.Fatal("should not be truncated")
	}
	if string(captured) != "hello" {
		t.Errorf("captured = %q", captured)
	}
	all, _ := io.ReadAll(restored)
	if string(all) != "hello" {
		t.Errorf("restored = %q", all)
	}
	if err := restored.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
}

func TestCaptureBodyTruncated(t *testing.T) {
	body := io.NopCloser(strings.NewReader("0123456789ABCDEF"))
	captured, restored, truncated := CaptureBody(body, 8)
	if !truncated {
		t.Fatal("should be truncated")
	}
	if string(captured) != "01234567" {
		t.Errorf("captured = %q", captured)
	}
	// The restored reader must still yield the COMPLETE original body.
	all, _ := io.ReadAll(restored)
	if string(all) != "0123456789ABCDEF" {
		t.Errorf("restored should be complete, got %q", all)
	}
	_ = restored.Close()
}

func TestCaptureBodyNegativeMax(t *testing.T) {
	body := io.NopCloser(strings.NewReader("abc"))
	captured, _, truncated := CaptureBody(body, -5)
	if !truncated || len(captured) != 0 {
		t.Errorf("negative max should clamp to 0: captured=%q truncated=%v", captured, truncated)
	}
}

func TestMatchPattern(t *testing.T) {
	cases := []struct {
		value, pattern string
		want           bool
	}{
		{"/health", "/health", true},
		{"/HEALTH", "/health", true}, // case-insensitive
		{"/api/v1/x", "/api/*", true},
		{"/api/v2/y", "/api/v1/x/*", false},
		{"/swagger/index", "/swagger", true}, // prefix
		{"/other", "/api", false},
		{"/x", "", false},
		{"/api/v1", "/api/v1*", true},
	}
	for _, c := range cases {
		if got := MatchPattern(c.value, c.pattern); got != c.want {
			t.Errorf("MatchPattern(%q,%q) = %v; want %v", c.value, c.pattern, got, c.want)
		}
	}
}

func TestShouldExcludeInclude(t *testing.T) {
	if !ShouldExclude("/health", []string{"/health"}) {
		t.Error("should exclude /health")
	}
	if ShouldExclude("/api", []string{"/health"}) {
		t.Error("should not exclude /api")
	}
	if !ShouldInclude("/anything", nil) {
		t.Error("empty include list includes everything")
	}
	if ShouldInclude("/api", []string{"/admin"}) {
		t.Error("/api should not be included")
	}
	if !ShouldInclude("/admin/x", []string{"/admin/*"}) {
		t.Error("/admin/x should be included")
	}
}

func TestFormatJSON(t *testing.T) {
	if got := FormatJSON(`{ "a" : 1 }`); got != `{"a":1}` {
		t.Errorf("FormatJSON compact = %q", got)
	}
	if got := FormatJSON("not json"); got != "not json" {
		t.Errorf("non-JSON should pass through, got %q", got)
	}
	if got := FormatJSON("   "); got != "   " {
		t.Errorf("blank should pass through, got %q", got)
	}
}

func TestCapBody(t *testing.T) {
	if got := CapBody("short", 100); got != "short" {
		t.Errorf("under cap = %q", got)
	}
	if got := CapBody("0123456789", 4); got != "0123... [truncated]" {
		t.Errorf("capped = %q", got)
	}
	if got := CapBody("x", 0); got != "x" {
		t.Errorf("zero cap disables, got %q", got)
	}
}

func TestMaskQueryValues(t *testing.T) {
	q := url.Values{"token": {"secret"}, "page": {"2"}}
	MaskQueryValues(q, map[string]logging.MaskingStrategy{"TOKEN": logging.HideAll})
	if q.Get("token") == "secret" {
		t.Errorf("token should be masked: %v", q.Get("token"))
	}
	if q.Get("page") != "2" {
		t.Errorf("page should be unchanged: %v", q.Get("page"))
	}
	// No strategies → no-op.
	q2 := url.Values{"token": {"secret"}}
	MaskQueryValues(q2, nil)
	if q2.Get("token") != "secret" {
		t.Error("nil strategies should be a no-op")
	}
}

func TestMaskFormBody(t *testing.T) {
	masked, ok := MaskFormBody("user=alice&password=hunter2", map[string]logging.MaskingStrategy{"password": logging.HideAll})
	if !ok {
		t.Fatal("valid form should parse")
	}
	if strings.Contains(masked, "hunter2") {
		t.Errorf("password should be masked: %s", masked)
	}
	if !strings.Contains(masked, "user=alice") {
		t.Errorf("user should be preserved: %s", masked)
	}
	// Empty / unparseable.
	if _, ok := MaskFormBody("", nil); ok {
		t.Error("empty body should report ok=false")
	}
}

func TestRenderQuery(t *testing.T) {
	q := url.Values{"b": {"2"}, "a": {"1"}, "m": {"****"}}
	got := RenderQuery(q)
	if got != "a=1&b=2&m=****" {
		t.Errorf("RenderQuery sorted/unescaped = %q", got)
	}
	if RenderQuery(nil) != "" {
		t.Error("empty values render to empty string")
	}
}

func TestJoinQuery(t *testing.T) {
	q := url.Values{"a": {"1", "2"}, "b": {"x"}}
	got := JoinQuery(q)
	want := map[string]string{"a": "1,2", "b": "x"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("JoinQuery = %v; want %v", got, want)
	}
	if JoinQuery(nil) != nil {
		t.Error("empty values yields nil map")
	}
}

func TestCollectExtra(t *testing.T) {
	decoded := map[string]any{
		"externalId": "EXT-1",
		"nested": map[string]any{
			"externalId": "EXT-2",
			"items":      []any{map[string]any{"externalId": "EXT-3"}},
		},
	}
	extra := map[string]any{}
	CollectExtra(decoded, []string{"externalId"}, "request_", extra)
	if extra["request_externalId"] == nil {
		t.Errorf("should collect at least one externalId: %v", extra)
	}
	// No fields → no-op.
	extra2 := map[string]any{}
	CollectExtra(decoded, nil, "p_", extra2)
	if len(extra2) != 0 {
		t.Error("nil fields should collect nothing")
	}
}

func TestMessage(t *testing.T) {
	if got := Message("GET", "/x", 200, 12.5); got != "GET /x - 200 (12.50ms)" {
		t.Errorf("Message = %q", got)
	}
}
