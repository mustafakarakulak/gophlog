package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// parseLine unmarshals a single emitted JSON log line into a map.
func parseLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	line := strings.TrimSpace(buf.String())
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("invalid JSON log line %q: %v", line, err)
	}
	return m
}

func newTestLogger(buf *bytes.Buffer) *Logger {
	fixed := time.Date(2026, 1, 11, 0, 15, 34, 123_000_000, time.UTC)
	return New(WithWriter(buf), WithClock(func() time.Time { return fixed }))
}

func TestBasicInfoLog(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)

	log.Info("Invoice created successfully", "invoice_created").
		WithTraceID("trace-abc").
		WithPayload(map[string]any{"invoice_id": "INV-001", "amount": 1000.0}).
		Log()

	m := parseLine(t, &buf)
	if m["level"] != "INFO" {
		t.Errorf("level = %v", m["level"])
	}
	if m["timestamp"] != "2026-01-11T00:15:34.123Z" {
		t.Errorf("timestamp = %v", m["timestamp"])
	}
	if m["trace_id"] != "trace-abc" {
		t.Errorf("trace_id = %v", m["trace_id"])
	}
	if m["event"] != "invoice_created" {
		t.Errorf("event = %v", m["event"])
	}

	// payload must be a stringified JSON value.
	payloadStr, ok := m["payload"].(string)
	if !ok {
		t.Fatalf("payload should be a string, got %T", m["payload"])
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(payloadStr), &payload); err != nil {
		t.Fatalf("payload is not valid inner JSON: %v", err)
	}
	if payload["invoice_id"] != "INV-001" {
		t.Errorf("payload invoice_id = %v", payload["invoice_id"])
	}
}

func TestNullSafeOmitsEmptyFields(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)
	log.Info("msg", "evt").WithTraceID("t").Log()

	m := parseLine(t, &buf)
	for _, field := range []string{"tenant_id", "user_id", "payload", "integration", "queue", "job", "extra", "http_status"} {
		if _, ok := m[field]; ok {
			t.Errorf("expected %q to be omitted, but it was present", field)
		}
	}
}

func TestLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	log := New(WithWriter(&buf), WithMinLevel(WARN))
	log.Info("dropped", "evt").Log()
	if buf.Len() != 0 {
		t.Errorf("INFO should be dropped at WARN min level, got %q", buf.String())
	}
	log.Error("kept", "evt").Log()
	if buf.Len() == 0 {
		t.Error("ERROR should be emitted at WARN min level")
	}
}

func TestErrorFields(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)
	log.Error("failed", "op_failed").WithTraceID("t").WithError(errors.New("boom")).Log()

	m := parseLine(t, &buf)
	if m["error_message"] != "boom" {
		t.Errorf("error_message = %v", m["error_message"])
	}
	if m["error_type"] == nil {
		t.Error("error_type should be set")
	}
	if m["stack_trace"] == nil {
		t.Error("stack_trace should be set")
	}
}

func TestStackTraceContainsCaller(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)
	log.Error("failed", "op_failed").WithTraceID("t").WithError(errors.New("boom")).Log()

	m := parseLine(t, &buf)
	stack, _ := m["stack_trace"].(string)
	if !strings.Contains(stack, "TestStackTraceContainsCaller") {
		t.Errorf("stack_trace should start at the caller, got:\n%s", stack)
	}
}

type validationError struct{ msg string }

func (e *validationError) Error() string { return e.msg }

func TestErrorTypeName(t *testing.T) {
	if got := errorTypeName(errors.New("x")); got != "error" {
		t.Errorf("errors.New type = %q; want error", got)
	}
	if got := errorTypeName(&validationError{"bad"}); got != "validationError" {
		t.Errorf("custom error type = %q; want validationError", got)
	}
}

func TestContextCorrelationID(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)
	ctx := WithCorrelationID(context.Background(), "ctx-trace-123")
	log.Info("msg", "evt").Ctx(ctx).Log()

	m := parseLine(t, &buf)
	if m["trace_id"] != "ctx-trace-123" {
		t.Errorf("trace_id from context = %v", m["trace_id"])
	}
}

func TestExtraIsRealObject(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)
	log.Info("msg", "evt").WithTraceID("t").
		WithExtraField("region", "EU").
		WithExtra(map[string]any{"tier": "premium"}).
		Log()

	m := parseLine(t, &buf)
	extra, ok := m["extra"].(map[string]any)
	if !ok {
		t.Fatalf("extra should be a JSON object, got %T", m["extra"])
	}
	if extra["region"] != "EU" || extra["tier"] != "premium" {
		t.Errorf("extra = %v", extra)
	}
}

func TestIntegrationStringifiedBody(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)
	dur := 80.5
	retry := 0
	log.Info("Payment processed", "payment").WithTraceID("t").
		WithIntegration(&IntegrationInfo{
			Target:             "bank-gateway",
			Status:             IntegrationSuccess,
			ExternalDurationMs: &dur,
			RetryCount:         &retry,
			ResponseBody:       map[string]any{"transaction_id": "TXN-123"},
		}).Log()

	m := parseLine(t, &buf)
	integ := m["integration"].(map[string]any)
	if integ["status"] != "success" {
		t.Errorf("integration status = %v", integ["status"])
	}
	body, ok := integ["response_body"].(string)
	if !ok {
		t.Fatalf("integration response_body should be stringified, got %T", integ["response_body"])
	}
	if !strings.Contains(body, "TXN-123") {
		t.Errorf("response_body = %q", body)
	}
}
