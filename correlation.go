package gophlog

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
