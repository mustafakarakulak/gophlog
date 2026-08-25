package gophlog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"
)

// CorrelationHeader is the HTTP header used to propagate the correlation ID
// (trace_id) across services.
const CorrelationHeader = "X-Correlation-ID"

// Workflow propagation headers used to carry workflow identifiers across HTTP
// calls.
const (
	HeaderChildWorkflowID  = "X-Child-Workflow-Id"
	HeaderRunID            = "X-Run-Id"
	HeaderParentWorkflowID = "X-Parent-Workflow-Id"
)

type ctxKey int

const ctxKeyFields ctxKey = 0

// ctxFields carries every propagated log field in a single context value, so
// resolving all of them at emit time costs one context lookup instead of one
// chain walk per field.
type ctxFields struct {
	correlationID    string
	spanID           string
	requestID        string
	tenantID         string
	userID           string
	clientIP         string
	sessionID        string
	childWorkflowID  string
	runID            string
	parentWorkflowID string
}

// emptyCtxFields is returned when the context carries no fields, so callers can
// read fields without a nil check.
var emptyCtxFields ctxFields

func fieldsFromCtx(ctx context.Context) *ctxFields {
	if ctx == nil {
		return &emptyCtxFields
	}
	if f, ok := ctx.Value(ctxKeyFields).(*ctxFields); ok {
		return f
	}
	return &emptyCtxFields
}

// withField clones the current field set, applies set to the copy, and stores
// it back, so contexts stay immutable while lookups stay a single Value call.
func withField(ctx context.Context, set func(*ctxFields)) context.Context {
	next := *fieldsFromCtx(ctx)
	set(&next)
	return context.WithValue(ctx, ctxKeyFields, &next)
}

// randRead is crypto/rand.Read, indirected so tests can exercise the
// entropy-failure path in NewCorrelationID.
var randRead = rand.Read

// NewCorrelationID returns a new correlation ID as a UUIDv7 (RFC 9562 §5.7) in
// canonical lowercase form, for example "019baa68-80eb-7b0f-b2df-8e5a9c3e22e5".
//
// The leading 48 bits hold the generation time as Unix milliseconds, so the
// timestamp can be recovered from the ID and two IDs created in different
// milliseconds compare lexicographically in the order they were created. The
// remaining 74 bits come from crypto/rand. IDs created within the same
// millisecond are unique but carry no order relative to each other.
func NewCorrelationID() string {
	var b [16]byte

	// unix_ts_ms: bits 0-47, big-endian milliseconds since the Unix epoch.
	ms := uint64(time.Now().UnixMilli())
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)

	// rand_a and rand_b: the remaining bits, minus the version and variant
	// fields overwritten below.
	if _, err := randRead(b[6:]); err != nil {
		// crypto/rand can still fail on Go 1.23 (it became infallible in Go
		// 1.24). Keep the real timestamp and zero the random bits: the result
		// is a well-formed, ordered UUIDv7 rather than a malformed value or the
		// nil UUID, and the zeroed entropy stays recognisable.
		clear(b[6:])
	}
	b[6] = b[6]&0x0f | 0x70 // ver: bits 48-51 = 0b0111
	b[8] = b[8]&0x3f | 0x80 // var: bits 64-65 = 0b10

	return formatUUID(&b)
}

// formatUUID renders 16 bytes in the canonical lowercase 8-4-4-4-12 form.
func formatUUID(b *[16]byte) string {
	var out [36]byte
	hex.Encode(out[0:8], b[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], b[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], b[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], b[8:10])
	out[23] = '-'
	hex.Encode(out[24:36], b[10:16])
	return string(out[:])
}

// MaxCorrelationIDLen is the longest correlation ID accepted from an untrusted
// source by IsValidCorrelationID.
const MaxCorrelationIDLen = 128

// IsValidCorrelationID reports whether id is safe to adopt from an untrusted
// source such as an inbound HTTP header.
//
// An accepted ID is 1..MaxCorrelationIDLen bytes of ASCII letters, digits and
// the separators '-', '_', '.' and ':' — enough for hex IDs, UUIDs and W3C
// trace-context values. Rejecting anything else keeps a client from steering
// audit logs with oversized or structured values; callers should generate a
// fresh ID instead of trusting a rejected one.
func IsValidCorrelationID(id string) bool {
	if id == "" || len(id) > MaxCorrelationIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_', c == '.', c == ':':
		default:
			return false
		}
	}
	return true
}

// WithCorrelationID stores the correlation ID (trace_id) in the context.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return withField(ctx, func(f *ctxFields) { f.correlationID = id })
}

// CorrelationID returns the correlation ID stored in the context, if any.
func CorrelationID(ctx context.Context) string { return fieldsFromCtx(ctx).correlationID }

// EnsureCorrelationID returns the context's correlation ID, generating and
// storing a new one when absent.
func EnsureCorrelationID(ctx context.Context) (context.Context, string) {
	if id := CorrelationID(ctx); id != "" {
		return ctx, id
	}
	id := NewCorrelationID()
	return WithCorrelationID(ctx, id), id
}

// WithSpanID stores a span ID in the context.
func WithSpanID(ctx context.Context, id string) context.Context {
	return withField(ctx, func(f *ctxFields) { f.spanID = id })
}

// WithRequestID stores a request ID in the context.
func WithRequestID(ctx context.Context, id string) context.Context {
	return withField(ctx, func(f *ctxFields) { f.requestID = id })
}

// WithTenantID stores a tenant ID in the context.
func WithTenantID(ctx context.Context, id string) context.Context {
	return withField(ctx, func(f *ctxFields) { f.tenantID = id })
}

// WithUserID stores a user ID in the context.
func WithUserID(ctx context.Context, id string) context.Context {
	return withField(ctx, func(f *ctxFields) { f.userID = id })
}

// WithClientIP stores a client IP in the context.
func WithClientIP(ctx context.Context, ip string) context.Context {
	return withField(ctx, func(f *ctxFields) { f.clientIP = ip })
}

// WithSessionID stores a session ID in the context.
func WithSessionID(ctx context.Context, id string) context.Context {
	return withField(ctx, func(f *ctxFields) { f.sessionID = id })
}

// WithWorkflow stores workflow identifiers in the context. Empty values are
// ignored.
func WithWorkflow(ctx context.Context, childWorkflowID, runID, parentWorkflowID string) context.Context {
	if childWorkflowID == "" && runID == "" && parentWorkflowID == "" {
		return ctx
	}
	return withField(ctx, func(f *ctxFields) {
		if childWorkflowID != "" {
			f.childWorkflowID = childWorkflowID
		}
		if runID != "" {
			f.runID = runID
		}
		if parentWorkflowID != "" {
			f.parentWorkflowID = parentWorkflowID
		}
	})
}
