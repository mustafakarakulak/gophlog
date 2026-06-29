// Package logging is an enterprise-grade structured JSON logging library for
// Go, designed for Kubernetes, FluentBit and OpenSearch — a feature-for-feature
// port of the .NET Odeal.Logging package.
//
// # Highlights
//
//   - Structured single-line JSON written to stdout.
//   - Fluent builder API with rich context (HTTP, integration, queue, job, …).
//   - Distributed-tracing friendly: trace_id/span_id resolved from context with
//     a pluggable TraceExtractor (e.g. OpenTelemetry).
//   - Field masking with eight strategies, plus `mask` / `logextra` struct tags.
//   - net/http server middleware and an http.RoundTripper for outbound calls,
//     both with automatic request/response logging and masking.
//
// # Quick start
//
//	log := logging.New()
//	log.Info("Invoice created", "invoice_created").
//	    WithPayload(map[string]any{"invoice_id": "INV-001", "amount": 1000.0}).
//	    Log()
//
// # Masking via struct tags
//
//	type PaymentRequest struct {
//	    Amount     float64 `json:"amount"`
//	    CreditCard string  `json:"creditCard" mask:"creditcard"`
//	    TxnID      string  `json:"txnId" logextra:"true"`
//	}
//
// `creditCard` is masked in place; `txnId` is moved into the searchable
// `extra` object.
package logging
