package logging_test

import (
	"context"
	"log/slog"
	"os"
	"time"

	logging "github.com/mustafakarakulak/go-logging"
)

// fixedLogger returns a logger that writes to stdout with a fixed clock so the
// examples below produce deterministic, verifiable output.
func fixedLogger() *logging.Logger {
	return logging.New(
		logging.WithWriter(os.Stdout),
		logging.WithClock(func() time.Time {
			return time.Date(2026, 1, 11, 0, 15, 34, 123_000_000, time.UTC)
		}),
	)
}

// Basic structured logging with a payload.
func ExampleLogger() {
	log := fixedLogger()

	log.Info("Resource created successfully", "resource_created").
		WithTraceID("b7f5e0b3b78b4b0fb2df8e5a9c3e22e5").
		WithPayload(map[string]any{"id": "123", "name": "example"}).
		Log()
	// Output: {"timestamp":"2026-01-11T00:15:34.123Z","level":"INFO","trace_id":"b7f5e0b3b78b4b0fb2df8e5a9c3e22e5","event":"resource_created","message":"Resource created successfully","payload":"{\"id\":\"123\",\"name\":\"example\"}"}
}

// Masking a sensitive payload field with the fluent API.
func ExampleEntry_Mask() {
	log := fixedLogger()

	log.Info("Payment processed", "payment_processed").
		WithTraceID("b7f5e0b3b78b4b0fb2df8e5a9c3e22e5").
		WithPayload(map[string]any{"cardNumber": "1111999988883333", "amount": 100}).
		Mask("cardNumber", logging.CreditCard).
		Log()
	// Output: {"timestamp":"2026-01-11T00:15:34.123Z","level":"INFO","trace_id":"b7f5e0b3b78b4b0fb2df8e5a9c3e22e5","event":"payment_processed","message":"Payment processed","payload":"{\"amount\":100,\"cardNumber\":\"1111 99 **** ** 3333\"}"}
}

// Bridging the standard log/slog API onto this library's JSON format.
func ExampleNewSlogHandler() {
	h := logging.NewSlogHandler(fixedLogger(), nil)

	// A fixed correlation ID in the context keeps trace_id deterministic; a zero
	// record time falls back to the logger clock.
	ctx := logging.WithCorrelationID(context.Background(), "b7f5e0b3b78b4b0fb2df8e5a9c3e22e5")
	r := slog.NewRecord(time.Time{}, slog.LevelInfo, "User created", 0)
	r.Add("event", "user_created", "user_id", "u-123")
	_ = h.Handle(ctx, r)
	// Output: {"timestamp":"2026-01-11T00:15:34.123Z","level":"INFO","trace_id":"b7f5e0b3b78b4b0fb2df8e5a9c3e22e5","event":"user_created","message":"User created","extra":{"user_id":"u-123"}}
}

// Attaching integration context.
func ExampleEntry_WithIntegrationResult() {
	log := fixedLogger()

	log.Info("External call completed", "external_call_succeeded").
		WithTraceID("b7f5e0b3b78b4b0fb2df8e5a9c3e22e5").
		WithIntegrationResult("external-gateway", logging.IntegrationSuccess, 80.5, 0).
		Log()
	// Output: {"timestamp":"2026-01-11T00:15:34.123Z","level":"INFO","trace_id":"b7f5e0b3b78b4b0fb2df8e5a9c3e22e5","event":"external_call_succeeded","message":"External call completed","integration":{"target":"external-gateway","status":"success","external_duration_ms":80.5,"retry_count":0}}
}
