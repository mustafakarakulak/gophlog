package gophlog

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func decodeLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("invalid log line %q: %v", buf.String(), err)
	}
	return m
}

func TestWithBindsFields(t *testing.T) {
	var buf bytes.Buffer
	child := New(WithWriter(&buf)).With().
		LogType(LogTypeAudit).
		Category("payments").
		Tenant("acme").
		User("u-42").
		ClientIP("10.0.0.1").
		Session("s-1").
		RequestID("r-1").
		ExtraField("service", "billing").
		Logger()

	child.Info("charged", "payment_charged").Log()

	m := decodeLine(t, &buf)
	want := map[string]string{
		"log_type": "audit", "category": "payments", "tenant_id": "acme",
		"user_id": "u-42", "client_ip": "10.0.0.1", "session_id": "s-1",
		"request_id": "r-1",
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %v; want %v", k, m[k], v)
		}
	}
	extra := m["extra"].(map[string]any)
	if extra["service"] != "billing" {
		t.Errorf("extra.service = %v; want billing", extra["service"])
	}
}

// TestWithPrecedence pins the resolution order: explicit entry value beats the
// context value, which beats the bound value.
func TestWithPrecedence(t *testing.T) {
	var buf bytes.Buffer
	child := New(WithWriter(&buf)).With().
		Tenant("bound-tenant").
		User("bound-user").
		Session("bound-session").
		Logger()

	ctx := WithTenantID(context.Background(), "ctx-tenant")
	ctx = WithUserID(ctx, "ctx-user")

	child.Info("m", "e").Ctx(ctx).WithTenant("entry-tenant").Log()

	m := decodeLine(t, &buf)
	if m["tenant_id"] != "entry-tenant" {
		t.Errorf("tenant_id = %v; want entry value to win", m["tenant_id"])
	}
	if m["user_id"] != "ctx-user" {
		t.Errorf("user_id = %v; want context value to beat bound", m["user_id"])
	}
	if m["session_id"] != "bound-session" {
		t.Errorf("session_id = %v; want bound fallback", m["session_id"])
	}
}

func TestWithExtraMergePriority(t *testing.T) {
	var buf bytes.Buffer
	child := New(WithWriter(&buf)).With().
		Extra(map[string]any{"env": "prod", "service": "billing"}).
		Logger()

	child.Info("m", "e").WithExtraField("service", "override").Log()

	extra := decodeLine(t, &buf)["extra"].(map[string]any)
	if extra["service"] != "override" {
		t.Errorf("entry extra should win: %v", extra["service"])
	}
	if extra["env"] != "prod" {
		t.Errorf("bound extra should survive: %v", extra["env"])
	}
}

func TestWithBoundMasking(t *testing.T) {
	var buf bytes.Buffer
	child := New(WithWriter(&buf)).With().
		Mask("CardNumber", CreditCard). // mixed case: lookup is case-insensitive
		Mask("cvv", HideAll).
		Logger()

	child.Info("pay", "pay").
		WithPayload(map[string]any{"cardNumber": "5101521234564582", "cvv": "123", "amount": 10}).
		Mask("cvv", ShowLast1). // entry strategy overrides bound per key
		Log()

	payload := decodeLine(t, &buf)["payload"].(string)
	if strings.Contains(payload, "5101521234564582") {
		t.Errorf("bound mask not applied: %s", payload)
	}
	if !strings.Contains(payload, `"cvv":"**3"`) {
		t.Errorf("entry mask should override bound: %s", payload)
	}
	if !strings.Contains(payload, `"amount":10`) {
		t.Errorf("unrelated field changed: %s", payload)
	}
}

func TestWithSharesCoreState(t *testing.T) {
	var buf bytes.Buffer
	parent := New(WithWriter(&buf))
	child := parent.With().Category("c").Logger()

	// Changing the level on the child changes it for the whole family.
	child.SetMinLevel(ERROR)
	parent.Info("hidden", "e").Log()
	child.Info("hidden", "e").Log()
	if buf.Len() != 0 {
		t.Fatalf("INFO should be filtered after child.SetMinLevel(ERROR): %s", buf.String())
	}
	parent.Error("shown", "e").Log()
	if buf.Len() == 0 {
		t.Fatal("ERROR should be emitted")
	}
}

// TestWithBuilderIsolation asserts that extending a builder (or the parent's
// bound fields) after Logger() does not leak into already-derived loggers.
func TestWithBuilderIsolation(t *testing.T) {
	var buf bytes.Buffer
	w := New(WithWriter(&buf)).With().Tenant("t-1")
	first := w.Logger()
	w.Tenant("t-2").ExtraField("k", "v")
	second := w.Logger()

	first.Info("m", "e").Log()
	m := decodeLine(t, &buf)
	if m["tenant_id"] != "t-1" {
		t.Errorf("first logger mutated by later builder use: %v", m["tenant_id"])
	}
	if _, ok := m["extra"]; ok {
		t.Errorf("first logger should have no extra: %v", m["extra"])
	}

	buf.Reset()
	second.Info("m", "e").Log()
	if got := decodeLine(t, &buf)["tenant_id"]; got != "t-2" {
		t.Errorf("second logger tenant = %v; want t-2", got)
	}
}

// TestWithDeriveFromChild asserts grandchildren inherit and can override the
// child's bound fields.
func TestWithDeriveFromChild(t *testing.T) {
	var buf bytes.Buffer
	child := New(WithWriter(&buf)).With().Category("payments").Tenant("acme").Logger()
	grandchild := child.With().Tenant("globex").Logger()

	grandchild.Info("m", "e").Log()
	m := decodeLine(t, &buf)
	if m["category"] != "payments" {
		t.Errorf("category should be inherited: %v", m["category"])
	}
	if m["tenant_id"] != "globex" {
		t.Errorf("tenant should be overridden: %v", m["tenant_id"])
	}
}
