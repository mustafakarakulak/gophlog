package logging

import (
	"context"
	"errors"
	"io"
	"testing"
)

// payload mirrors a realistic domain object with mask / logextra tags, so the
// reflection + masking path is exercised the way callers actually use it.
type benchPayload struct {
	InvoiceID  string  `json:"invoice_id" logextra:"true"`
	Amount     float64 `json:"amount"`
	Currency   string  `json:"currency"`
	CardNumber string  `json:"card_number" mask:"creditcard"`
	CVV        string  `json:"cvv" mask:"hideall"`
	Customer   string  `json:"customer"`
}

func benchLogger() *Logger {
	return New(WithWriter(io.Discard))
}

func BenchmarkInfoSimple(b *testing.B) {
	log := benchLogger()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		log.Info("order processed", "order_processed").WithTraceID("t").Log()
	}
}

func BenchmarkInfoWithFields(b *testing.B) {
	log := benchLogger()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		log.Info("request handled", "http_request").
			WithTraceID("t").
			WithHTTPResult("POST", "/api/orders", 201, 12.5).
			WithUser("u-1").
			WithTenant("tn-1").
			WithBytes(120, 340).
			Log()
	}
}

func BenchmarkInfoWithPayloadStruct(b *testing.B) {
	log := benchLogger()
	p := benchPayload{
		InvoiceID:  "INV-1001",
		Amount:     249.90,
		Currency:   "TRY",
		CardNumber: "5101521234564582",
		CVV:        "123",
		Customer:   "acme-corp",
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		log.Info("payment", "payment_processed").WithTraceID("t").WithPayload(p).Log()
	}
}

func BenchmarkInfoWithPayloadMap(b *testing.B) {
	log := benchLogger()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		log.Info("payment", "payment_processed").
			WithTraceID("t").
			WithPayload(map[string]any{"amount": 249.90, "currency": "TRY", "customer": "acme"}).
			Log()
	}
}

func BenchmarkInfoWithMasking(b *testing.B) {
	log := benchLogger()
	strategies := map[string]MaskingStrategy{"card_number": CreditCard, "cvv": HideAll}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		log.Info("payment", "payment_processed").
			WithTraceID("t").
			WithPayloadMasked(map[string]any{"card_number": "5101521234564582", "cvv": "123", "amount": 100}, strategies).
			Log()
	}
}

// BenchmarkInfoWithBoundFields exercises a child logger carrying typical
// module-level bindings, to keep the bound-field fallback path visibly cheap.
func BenchmarkInfoWithBoundFields(b *testing.B) {
	log := benchLogger().With().
		Category("payments").
		Tenant("tn-1").
		ExtraField("service", "billing").
		Logger()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		log.Info("charged", "payment_charged").WithTraceID("t").Log()
	}
}

// BenchmarkInfoWithContextFields exercises the context-resolution path the way
// the middleware populates it (correlation ID, workflow, client IP, tenant).
func BenchmarkInfoWithContextFields(b *testing.B) {
	log := benchLogger()
	ctx := WithCorrelationID(context.Background(), "c-1")
	ctx = WithWorkflow(ctx, "wf-child", "run-1", "wf-parent")
	ctx = WithClientIP(ctx, "10.0.0.1")
	ctx = WithTenantID(ctx, "tn-1")
	ctx = WithUserID(ctx, "u-1")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		log.Info("handled", "http_request").Ctx(ctx).Log()
	}
}

func BenchmarkWithError(b *testing.B) {
	log := benchLogger()
	err := errors.New("something failed")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		log.Error("failed", "op_failed").WithTraceID("t").WithError(err).Log()
	}
}

// BenchmarkDisabledLevel measures the cost of a call whose level is filtered out;
// it should be dramatically cheaper than an emitted log.
func BenchmarkDisabledLevel(b *testing.B) {
	log := New(WithWriter(io.Discard), WithMinLevel(ERROR))
	err := errors.New("noise")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		log.Debug("verbose", "debug_event").WithTraceID("t").WithError(err).Log()
	}
}

func BenchmarkSlogInfo(b *testing.B) {
	sl := NewSlogLogger(benchLogger(), nil)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sl.InfoContext(ctx, "order processed",
			"event", "order_processed",
			"order_id", "o-1",
			"amount", 249.90,
		)
	}
}

func BenchmarkSlogWithGroup(b *testing.B) {
	sl := NewSlogLogger(benchLogger(), nil).WithGroup("http").With("service", "orders")
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sl.InfoContext(ctx, "handled", "method", "GET", "status", 200)
	}
}

func BenchmarkMaskStringCreditCard(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = MaskString("5101521234564582", CreditCard)
	}
}

func BenchmarkApplyMaskingJSON(b *testing.B) {
	decoded := map[string]any{
		"amount": 100.0,
		"card":   "1111999988883333",
		"nested": map[string]any{"password": "supersecret", "pin": "9999"},
	}
	strategies := map[string]MaskingStrategy{"card": CreditCard, "password": HideAll, "pin": HideAll}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = MaskJSON(decoded, strategies)
	}
}
