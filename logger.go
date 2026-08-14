package gophlog

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
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
//
// Derived (child) loggers created via With share the parent's writer, mutex,
// minimum level and other core configuration; they differ only in the fields
// bound to them.
type Logger struct {
	core  *loggerCore
	bound *boundFields // fields bound via With; nil on root loggers
}

// loggerCore holds the state shared between a root logger and every child
// derived from it, so all of them serialize writes through one mutex and react
// to one SetMinLevel.
type loggerCore struct {
	mu         sync.Mutex
	w          io.Writer
	minLevel   atomic.Int32 // stores the minimum level's severity
	trace      TraceExtractor
	kube       *KubernetesInfo
	stackLimit int
	now        func() time.Time
}

// lineBuffer bundles a render buffer with a JSON encoder bound to it, so both
// are recycled together and no encoder is allocated per log line.
type lineBuffer struct {
	buf bytes.Buffer
	enc *json.Encoder
}

// bufPool recycles the line buffers used to render each log line, so steady-state
// logging avoids a fresh allocation per entry.
var bufPool = sync.Pool{New: func() any {
	lb := &lineBuffer{}
	lb.enc = json.NewEncoder(&lb.buf)
	lb.enc.SetEscapeHTML(false) // log payloads are machine-read; keep <,>,& literal
	return lb
}}

// maxPooledBuffer bounds the capacity of buffers returned to bufPool, so a single
// oversized log line cannot pin a large buffer in the pool indefinitely.
const maxPooledBuffer = 64 * 1024

// Option configures a Logger.
type Option func(*Logger)

// WithWriter sets the destination writer (default os.Stdout).
func WithWriter(w io.Writer) Option { return func(l *Logger) { l.core.w = w } }

// WithMinLevel drops entries whose level is below min (default TRACE).
func WithMinLevel(min Level) Option {
	return func(l *Logger) { l.core.minLevel.Store(int32(min.severity())) }
}

// WithTraceExtractor sets a function that resolves trace_id/span_id from the
// context (e.g. an OpenTelemetry adapter).
func WithTraceExtractor(fn TraceExtractor) Option { return func(l *Logger) { l.core.trace = fn } }

// WithKubernetes attaches static Kubernetes metadata to every log entry.
func WithKubernetes(info *KubernetesInfo) Option { return func(l *Logger) { l.core.kube = info } }

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
			l.core.kube = &info
		}
	}
}

// WithStackTraceLimit overrides the maximum stack-trace length (default 3000).
func WithStackTraceLimit(max int) Option { return func(l *Logger) { l.core.stackLimit = max } }

// WithClock overrides the time source (useful for tests).
func WithClock(now func() time.Time) Option { return func(l *Logger) { l.core.now = now } }

// New creates a Logger with the supplied options.
func New(opts ...Option) *Logger {
	l := &Logger{core: &loggerCore{
		w:          os.Stdout,
		stackLimit: defaultStackTraceLimit,
		now:        time.Now,
	}}
	l.core.minLevel.Store(int32(TRACE.severity()))
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// Enabled reports whether the given level would be emitted.
func (l *Logger) Enabled(level Level) bool {
	return l.enabledSeverity(level.severity())
}

// enabledSeverity is the hot-path level check: entries carry their severity as
// an int so the string-based Level lookup runs once per entry, not per check.
func (l *Logger) enabledSeverity(sev int) bool {
	return sev >= int(l.core.minLevel.Load())
}

// SetMinLevel changes the minimum level at runtime. It is safe to call
// concurrently with logging. The level is shared with every logger derived via
// With, so changing it on a child changes it for the whole family.
func (l *Logger) SetMinLevel(min Level) {
	l.core.minLevel.Store(int32(min.severity()))
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

// Fatal starts a FATAL-level log entry. Unlike the standard library's
// log.Fatal it does NOT terminate the process; the caller decides whether to
// exit after logging.
func (l *Logger) Fatal(message, event string) *Entry { return newEntry(l, FATAL, message, event) }

// At starts a log entry at an arbitrary level.
func (l *Logger) At(level Level, message, event string) *Entry {
	return newEntry(l, level, message, event)
}

// --- Emission --------------------------------------------------------------

// tsCache memoizes the formatted timestamp for one millisecond, since bursts of
// log entries frequently share it and time.Format allocates on every call.
type tsCache struct {
	unixMs    int64
	formatted string
}

var lastTS atomic.Pointer[tsCache]

// formatTimestamp renders ts in the ISO-8601 UTC layout, reusing the previous
// rendering when the millisecond has not changed.
func formatTimestamp(ts time.Time) string {
	utc := ts.UTC()
	ms := utc.UnixMilli()
	if c := lastTS.Load(); c != nil && c.unixMs == ms {
		return c.formatted
	}
	s := utc.Format(timestampLayout)
	lastTS.Store(&tsCache{unixMs: ms, formatted: s})
	return s
}

// emit builds the Event from an Entry and writes it as a single JSON line.
func (l *Logger) emit(e *Entry) {
	if !l.enabledSeverity(e.sev) {
		return
	}
	event := l.build(e)

	lb := bufPool.Get().(*lineBuffer)
	buf, enc := &lb.buf, lb.enc
	defer func() {
		if buf.Cap() <= maxPooledBuffer {
			buf.Reset()
			bufPool.Put(lb)
		}
	}()

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

	l.core.mu.Lock()
	l.core.w.Write(buf.Bytes())
	l.core.mu.Unlock()
}

// build assembles the final Event, resolving trace context and rendering the
// payload to its stringified form.
//
// Field precedence: an explicit Entry value wins over the context value, which
// wins over the value bound to the logger via With.
func (l *Logger) build(e *Entry) *Event {
	ctx := e.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	cf := fieldsFromCtx(ctx)
	b := l.bound
	if b == nil {
		b = &emptyBound
	}

	traceID := firstNonEmpty(e.traceID, cf.correlationID)
	spanID := firstNonEmpty(e.spanID, cf.spanID)
	if l.core.trace != nil && (traceID == "" || spanID == "") {
		tID, sID := l.core.trace(ctx)
		traceID = firstNonEmpty(traceID, tID)
		spanID = firstNonEmpty(spanID, sID)
	}
	if traceID == "" {
		traceID = NewCorrelationID()
	}

	ts := l.core.now()
	if !e.ts.IsZero() {
		ts = e.ts
	}

	logType := e.logType
	if logType == "" {
		logType = b.logType
	}

	ev := &Event{
		Timestamp: formatTimestamp(ts),
		Level:     e.level,
		LogType:   logType,
		Category:  firstNonEmpty(e.category, b.category),

		TraceID:   traceID,
		SpanID:    spanID,
		RequestID: firstNonEmpty(e.requestID, cf.requestID, b.requestID),

		TenantID:  firstNonEmpty(e.tenantID, cf.tenantID, b.tenantID),
		UserID:    firstNonEmpty(e.userID, cf.userID, b.userID),
		ClientIP:  firstNonEmpty(e.clientIP, cf.clientIP, b.clientIP),
		SessionID: firstNonEmpty(e.sessionID, cf.sessionID, b.sessionID),

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
		StackTrace:   truncate(e.stackTrace, l.core.stackLimit),

		ChildWorkflowID:  firstNonEmpty(e.childWorkflowID, cf.childWorkflowID),
		RunID:            firstNonEmpty(e.runID, cf.runID),
		ParentWorkflowID: firstNonEmpty(e.parentWorkflowID, cf.parentWorkflowID),

		Integration: e.integration,
		Queue:       e.queue,
		Job:         e.job,

		Kubernetes: l.core.kube,
	}
	if ev.Integration == nil {
		ev.Integration = b.integration
	}
	if ev.Queue == nil {
		ev.Queue = b.queue
	}
	if ev.Job == nil {
		ev.Job = b.job
	}

	// Resolve payload (struct tags -> masking -> stringify) and merge extras.
	payload, extra := l.renderPayload(e, b)
	ev.Payload = payload

	if len(extra) > 0 || len(e.extra) > 0 || len(b.extra) > 0 {
		merged := make(map[string]any, len(b.extra)+len(extra)+len(e.extra))
		for k, v := range b.extra { // bound extras are the weakest defaults
			merged[k] = v
		}
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
func (l *Logger) renderPayload(e *Entry, b *boundFields) (payload any, extra map[string]any) {
	if e.payload == nil {
		return nil, nil
	}
	normalized, extra := processPayload(e.payload)
	// Both maps are lower-cased on insert, so the lookup map never has to be
	// rebuilt at emit time. Entry strategies override bound ones per key.
	strategies := e.maskStrategies
	if len(b.maskStrategies) > 0 {
		if len(strategies) == 0 {
			strategies = b.maskStrategies
		} else {
			merged := make(map[string]MaskingStrategy, len(b.maskStrategies)+len(strategies))
			for k, s := range b.maskStrategies {
				merged[k] = s
			}
			for k, s := range strategies {
				merged[k] = s
			}
			strategies = merged
		}
	}
	if len(strategies) > 0 {
		normalized = applyMaskingLower(normalized, strategies)
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
	// Back up to a rune boundary so the cut never splits a multi-byte
	// character and produces an invalid string.
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "... [truncated]"
}
