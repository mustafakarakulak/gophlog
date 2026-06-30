package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// timestampLayout is the ISO-8601 UTC layout with millisecond precision and a
// trailing Z (e.g. 2006-01-02T15:04:05.000Z).
const timestampLayout = "2006-01-02T15:04:05.000Z"

// defaultStackTraceLimit caps captured stack traces at 3000 characters.
const defaultStackTraceLimit = 3000

// TraceExtractor pulls distributed-tracing identifiers from a context. It lets
// callers integrate OpenTelemetry (or any tracer) without the core package
// depending on it. Either return value may be empty.
type TraceExtractor func(ctx context.Context) (traceID, spanID string)

// Logger writes structured JSON log lines, one per entry, to its writer
// (os.Stdout by default — ideal for Kubernetes/FluentBit collection).
//
// A Logger is safe for concurrent use by multiple goroutines.
type Logger struct {
	mu         sync.Mutex
	w          io.Writer
	minLevel   atomic.Int32 // stores the minimum level's severity
	trace      TraceExtractor
	kube       *KubernetesInfo
	stackLimit int
	now        func() time.Time
}

// bufPool recycles the byte buffers used to render each log line, so steady-state
// logging avoids a fresh allocation per entry.
var bufPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

// maxPooledBuffer bounds the capacity of buffers returned to bufPool, so a single
// oversized log line cannot pin a large buffer in the pool indefinitely.
const maxPooledBuffer = 64 * 1024

// Option configures a Logger.
type Option func(*Logger)

// WithWriter sets the destination writer (default os.Stdout).
func WithWriter(w io.Writer) Option { return func(l *Logger) { l.w = w } }

// WithMinLevel drops entries whose level is below min (default TRACE).
func WithMinLevel(min Level) Option {
	return func(l *Logger) { l.minLevel.Store(int32(min.severity())) }
}

// WithTraceExtractor sets a function that resolves trace_id/span_id from the
// context (e.g. an OpenTelemetry adapter).
func WithTraceExtractor(fn TraceExtractor) Option { return func(l *Logger) { l.trace = fn } }

// WithKubernetes attaches static Kubernetes metadata to every log entry.
func WithKubernetes(info *KubernetesInfo) Option { return func(l *Logger) { l.kube = info } }

// WithKubernetesFromEnv populates Kubernetes metadata from the conventional
// POD_NAME / POD_NAMESPACE / NODE_NAME / CONTAINER_NAME environment variables.
func WithKubernetesFromEnv() Option {
	return func(l *Logger) {
		info := KubernetesInfo{
			PodName:       os.Getenv("POD_NAME"),
			Namespace:     os.Getenv("POD_NAMESPACE"),
			NodeName:      os.Getenv("NODE_NAME"),
			ContainerName: os.Getenv("CONTAINER_NAME"),
		}
		if info != (KubernetesInfo{}) {
			l.kube = &info
		}
	}
}

// WithStackTraceLimit overrides the maximum stack-trace length (default 3000).
func WithStackTraceLimit(max int) Option { return func(l *Logger) { l.stackLimit = max } }

// WithClock overrides the time source (useful for tests).
func WithClock(now func() time.Time) Option { return func(l *Logger) { l.now = now } }

// New creates a Logger with the supplied options.
func New(opts ...Option) *Logger {
	l := &Logger{
		w:          os.Stdout,
		stackLimit: defaultStackTraceLimit,
		now:        time.Now,
	}
	l.minLevel.Store(int32(TRACE.severity()))
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// Enabled reports whether the given level would be emitted.
func (l *Logger) Enabled(level Level) bool {
	return level.severity() >= int(l.minLevel.Load())
}

// SetMinLevel changes the minimum level at runtime. It is safe to call
// concurrently with logging.
func (l *Logger) SetMinLevel(min Level) {
	l.minLevel.Store(int32(min.severity()))
}

// --- Fluent entry points ---------------------------------------------------

// Trace starts a TRACE-level log entry.
func (l *Logger) Trace(message, event string) *Entry { return newEntry(l, TRACE, message, event) }

// Debug starts a DEBUG-level log entry.
func (l *Logger) Debug(message, event string) *Entry { return newEntry(l, DEBUG, message, event) }

// Info starts an INFO-level log entry.
func (l *Logger) Info(message, event string) *Entry { return newEntry(l, INFO, message, event) }

// Warn starts a WARN-level log entry.
func (l *Logger) Warn(message, event string) *Entry { return newEntry(l, WARN, message, event) }

// Error starts an ERROR-level log entry.
func (l *Logger) Error(message, event string) *Entry { return newEntry(l, ERROR, message, event) }

// Fatal starts a FATAL-level log entry.
func (l *Logger) Fatal(message, event string) *Entry { return newEntry(l, FATAL, message, event) }

// At starts a log entry at an arbitrary level.
func (l *Logger) At(level Level, message, event string) *Entry {
	return newEntry(l, level, message, event)
}

// --- Emission --------------------------------------------------------------

// emit builds the Event from an Entry and writes it as a single JSON line.
func (l *Logger) emit(e *Entry) {
	if !l.Enabled(e.level) {
		return
	}
	event := l.build(e)

	buf := bufPool.Get().(*bytes.Buffer)
	defer func() {
		if buf.Cap() <= maxPooledBuffer {
			buf.Reset()
			bufPool.Put(buf)
		}
	}()

	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false) // log payloads are machine-read; keep <,>,& literal
	if err := enc.Encode(event); err != nil {
		// Fall back to a minimal record so a bad payload/extra value never
		// silently drops the log line entirely. Encode already wrote a partial
		// object, so reset before re-encoding.
		buf.Reset()
		if encErr := enc.Encode(&Event{
			Timestamp:    event.Timestamp,
			Level:        event.Level,
			TraceID:      event.TraceID,
			Event:        event.Event,
			Message:      event.Message,
			ErrorType:    "LogSerializationError",
			ErrorMessage: err.Error(),
		}); encErr != nil {
			return
		}
	}

	l.mu.Lock()
	l.w.Write(buf.Bytes())
	l.mu.Unlock()
}

// build assembles the final Event, resolving trace context and rendering the
// payload to its stringified form.
func (l *Logger) build(e *Entry) *Event {
	ctx := e.ctx
	if ctx == nil {
		ctx = context.Background()
	}

	traceID := firstNonEmpty(e.traceID, CorrelationID(ctx))
	spanID := firstNonEmpty(e.spanID, strFromCtx(ctx, ctxKeySpanID))
	if l.trace != nil && (traceID == "" || spanID == "") {
		tID, sID := l.trace(ctx)
		traceID = firstNonEmpty(traceID, tID)
		spanID = firstNonEmpty(spanID, sID)
	}
	if traceID == "" {
		traceID = NewCorrelationID()
	}

	ts := l.now()
	if !e.ts.IsZero() {
		ts = e.ts
	}

	ev := &Event{
		Timestamp: ts.UTC().Format(timestampLayout),
		Level:     e.level,
		LogType:   e.logType,
		Category:  e.category,

		TraceID:   traceID,
		SpanID:    spanID,
		RequestID: firstNonEmpty(e.requestID, strFromCtx(ctx, ctxKeyRequestID)),

		TenantID:  firstNonEmpty(e.tenantID, strFromCtx(ctx, ctxKeyTenantID)),
		UserID:    firstNonEmpty(e.userID, strFromCtx(ctx, ctxKeyUserID)),
		ClientIP:  firstNonEmpty(e.clientIP, strFromCtx(ctx, ctxKeyClientIP)),
		SessionID: firstNonEmpty(e.sessionID, strFromCtx(ctx, ctxKeySessionID)),

		HTTPMethod:  e.httpMethod,
		HTTPPath:    e.httpPath,
		QueryParams: e.queryParams,
		HTTPStatus:  e.httpStatus,
		DurationMs:  e.durationMs,
		BytesIn:     e.bytesIn,
		BytesOut:    e.bytesOut,

		RequestBody:  e.requestBody,
		ResponseBody: e.responseBody,

		Event:   e.event,
		Message: e.message,

		ErrorType:    e.errorType,
		ErrorMessage: e.errorMessage,
		StackTrace:   truncate(e.stackTrace, l.stackLimit),

		ChildWorkflowID:  firstNonEmpty(e.childWorkflowID, strFromCtx(ctx, ctxKeyChildWorkflowID)),
		RunID:            firstNonEmpty(e.runID, strFromCtx(ctx, ctxKeyRunID)),
		ParentWorkflowID: firstNonEmpty(e.parentWorkflowID, strFromCtx(ctx, ctxKeyParentWorkflowID)),

		Integration: e.integration,
		Queue:       e.queue,
		Job:         e.job,

		Kubernetes: l.kube,
	}

	// Resolve payload (struct tags -> masking -> stringify) and merge extras.
	payload, extra := l.renderPayload(e)
	ev.Payload = payload

	if len(extra) > 0 || len(e.extra) > 0 {
		merged := make(map[string]any, len(extra)+len(e.extra))
		for k, v := range extra {
			merged[k] = v
		}
		for k, v := range e.extra { // explicit WithExtra wins
			merged[k] = v
		}
		ev.Extra = merged
	}

	return ev
}

// renderPayload normalises the payload (honouring struct tags), applies any
// field masking strategies, and returns the stringified JSON payload plus any
// extracted logextra fields.
func (l *Logger) renderPayload(e *Entry) (payload any, extra map[string]any) {
	if e.payload == nil {
		return nil, nil
	}
	normalized, extra := processPayload(e.payload)
	if len(e.maskStrategies) > 0 {
		normalized = applyMaskingToJSON(normalized, e.maskStrategies)
	}
	return stringifyJSON(normalized), extra
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "... [truncated]"
}
