package logging

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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

const (
	ctxKeyCorrelationID ctxKey = iota
	ctxKeySpanID
	ctxKeyRequestID
	ctxKeyTenantID
	ctxKeyUserID
	ctxKeyClientIP
	ctxKeySessionID
	ctxKeyChildWorkflowID
	ctxKeyRunID
	ctxKeyParentWorkflowID
)

// NewCorrelationID returns a new random 32-character hex correlation ID
// (128 bits of entropy, rendered without dashes).
func NewCorrelationID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand should not fail; fall back to a fixed-length zero ID.
		return "00000000000000000000000000000000"
	}
	return hex.EncodeToString(b[:])
}

// WithCorrelationID stores the correlation ID (trace_id) in the context.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKeyCorrelationID, id)
}

// CorrelationID returns the correlation ID stored in the context, if any.
func CorrelationID(ctx context.Context) string { return strFromCtx(ctx, ctxKeyCorrelationID) }

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
	return context.WithValue(ctx, ctxKeySpanID, id)
}

// WithRequestID stores a request ID in the context.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKeyRequestID, id)
}

// WithTenantID stores a tenant ID in the context.
func WithTenantID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKeyTenantID, id)
}

// WithUserID stores a user ID in the context.
func WithUserID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKeyUserID, id)
}

// WithClientIP stores a client IP in the context.
func WithClientIP(ctx context.Context, ip string) context.Context {
	return context.WithValue(ctx, ctxKeyClientIP, ip)
}

// WithSessionID stores a session ID in the context.
func WithSessionID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKeySessionID, id)
}

// WithWorkflow stores workflow identifiers in the context. Empty values are
// ignored.
func WithWorkflow(ctx context.Context, childWorkflowID, runID, parentWorkflowID string) context.Context {
	if childWorkflowID != "" {
		ctx = context.WithValue(ctx, ctxKeyChildWorkflowID, childWorkflowID)
	}
	if runID != "" {
		ctx = context.WithValue(ctx, ctxKeyRunID, runID)
	}
	if parentWorkflowID != "" {
		ctx = context.WithValue(ctx, ctxKeyParentWorkflowID, parentWorkflowID)
	}
	return ctx
}

func strFromCtx(ctx context.Context, key ctxKey) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(key).(string); ok {
		return v
	}
	return ""
}
