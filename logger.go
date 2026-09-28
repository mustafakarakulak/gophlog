package gophlog

import (
	"bytes"
	"context"
	"encoding"
	"encoding/json"
	"io"
	"math"
	"os"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// timestampLayout is the ISO-8601 UTC layout with millisecond precision and a
// trailing Z (e.g. 2006-01-02T15:04:05.000Z).
const timestampLayout = "2006-01-02T15:04:05.000Z"

// defaultStackTraceLimit caps captured stack traces at 3000 characters. The
// "... [truncated]" marker is appended AFTER the cut, so a truncated value is
// up to 15 bytes longer than the limit (see truncate).
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
	mu          sync.Mutex
	w           io.Writer
	minLevel    atomic.Int32 // stores the minimum level's severity
	trace       TraceExtractor
	kube        *KubernetesInfo
	stackLimit  int
	now         func() time.Time
	autoTraceID bool
	onError     func(error)
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

// WithMinLevel drops entries whose level is below minLevel (default TRACE).
func WithMinLevel(minLevel Level) Option {
	return func(l *Logger) { l.core.minLevel.Store(int32(minLevel.severity())) }
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

// WithStackTraceLimit caps stack traces at limit bytes (default 3000); the
// "... [truncated]" marker is appended after the cut. A limit of zero or less
// disables the cap.
func WithStackTraceLimit(limit int) Option { return func(l *Logger) { l.core.stackLimit = limit } }

// WithAutoTraceID makes the logger synthesize a random trace_id for entries that
// resolve none from the entry, the context or the TraceExtractor.
//
// It is off by default: two unrelated entries would otherwise each get their own
// invented trace, which inflates trace_id cardinality and makes a "search by
// trace" in OpenSearch return traces that never existed. Prefer establishing one
// correlation ID per request (the HTTP middleware does this) and propagating it
// through the context.
func WithAutoTraceID() Option { return func(l *Logger) { l.core.autoTraceID = true } }

// WithOnError registers a callback invoked when the logger cannot do its job:
// the writer returned an error, or a payload/entry could not be serialized.
// It also hears about values the logger had to repair: a cycle cut with a
// marker or a mask tag naming no strategy, in a payload or extra, and a NaN or
// ±Inf. In IntegrationInfo bodies neither a cycle nor such a tag is reported.
// Without it such failures are silent.
//
// The callback runs on the logging goroutine and must not log through this
// library (that would recurse); use it to increment a metric or write to
// os.Stderr.
func WithOnError(fn func(error)) Option { return func(l *Logger) { l.core.onError = fn } }

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
func (l *Logger) SetMinLevel(minLevel Level) {
	l.core.minLevel.Store(int32(minLevel.severity()))
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

// reportError hands a logging failure to the configured error callback, if any.
func (l *Logger) reportError(err error) {
	if fn := l.core.onError; fn != nil {
		fn(err)
	}
}

// emit builds the Event from an Entry and writes it as a single JSON line.
func (l *Logger) emit(e *Entry) {
	if !l.enabledSeverity(e.severity()) {
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

	err := enc.Encode(event)
	if err != nil {
		l.reportError(err)
		// Encode already wrote a partial object, so reset before re-encoding.
		buf.Reset()
		// A non-finite float (NaN, ±Inf) is the usual culprit and must not cost
		// the rest of the entry: retry once with those rendered as strings.
		if retry, ok := finiteEvent(event); ok {
			if err = enc.Encode(retry); err != nil {
				l.reportError(err)
				buf.Reset()
			}
		}
	}
	if err != nil {
		// Fall back to a minimal record so a bad payload/extra value never
		// silently drops the log line entirely.
		if encErr := enc.Encode(&Event{
			Timestamp:    event.Timestamp,
			Level:        event.Level,
			TraceID:      event.TraceID,
			Event:        event.Event,
			Message:      event.Message,
			ErrorType:    "LogSerializationError",
			ErrorMessage: err.Error(),
		}); encErr != nil {
			l.reportError(encErr)
			return
		}
	}

	if werr := l.core.write(buf.Bytes()); werr != nil {
		l.reportError(werr)
	}
}

// write hands one rendered line to the writer under the core mutex. The unlock
// is deferred so a panicking writer cannot leave the mutex held and deadlock
// every later log call of the logger family; the panic itself still reaches
// the caller.
func (c *loggerCore) write(p []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.w.Write(p)
	return err
}

// finiteEvent returns a copy of ev that encodes despite non-finite floats, and
// whether ev had any. In the free-form extra object they are written as "NaN",
// "+Inf" or "-Inf". The typed duration fields are numeric in the index mapping,
// where such a string would get the whole document rejected, so a non-finite
// value there is omitted instead. ev is not modified: extra maps may be shared
// with the caller or a derived logger.
func finiteEvent(ev *Event) (*Event, bool) {
	cp := *ev
	changed := false
	if len(ev.Extra) > 0 {
		w := finiteWalk{budget: maxFiniteValues}
		extra := make(map[string]any, len(ev.Extra))
		for k, v := range ev.Extra {
			if fv, ok := w.value(reflect.ValueOf(v), 0); ok {
				v, changed = fv, true
			}
			extra[k] = v
		}
		cp.Extra = extra
	}

	if d := ev.DurationMs; d != nil {
		if _, bad := nonFiniteString(*d); bad {
			cp.DurationMs, changed = nil, true
		}
	}
	if in := ev.Integration; in != nil && in.ExternalDurationMs != nil {
		if _, bad := nonFiniteString(*in.ExternalDurationMs); bad {
			c := *in
			c.ExternalDurationMs = nil
			cp.Integration, changed = &c, true
		}
	}
	if !changed {
		return nil, false
	}
	return &cp, true
}

// finiteWalk bounds the reflection walk behind finiteEvent: depth caps the
// recursion and budget the number of values visited, so a cyclic or heavily
// shared value cannot hang the fallback. Whatever the walk does not reach
// keeps its non-finite floats and ends up in the minimal record.
type finiteWalk struct{ budget int }

const (
	maxFiniteDepth  = 64
	maxFiniteValues = 1 << 16
)

// value returns a copy of v with non-finite floats rendered as strings, and
// whether it found any. It descends only through shapes whose JSON form it can
// reproduce exactly (pointers, interfaces, slices, arrays and string-keyed
// maps); structs and types with their own marshalers are left alone.
func (w *finiteWalk) value(v reflect.Value, depth int) (any, bool) {
	w.budget--
	if w.budget < 0 || depth > maxFiniteDepth || !v.IsValid() || hasCustomJSON(v.Type()) {
		return nil, false
	}
	switch v.Kind() {
	case reflect.Float32, reflect.Float64:
		return nonFiniteString(v.Float())
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return nil, false
		}
		return w.value(v.Elem(), depth+1)
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			return nil, false
		}
		out := make([]any, v.Len())
		changed := false
		for i := range out {
			if fv, ok := w.value(v.Index(i), depth+1); ok {
				out[i], changed = fv, true
			} else {
				out[i] = v.Index(i).Interface()
			}
		}
		return out, changed
	case reflect.Map:
		if v.IsNil() || v.Type().Key().Kind() != reflect.String || hasCustomJSON(v.Type().Key()) {
			return nil, false
		}
		out := make(map[string]any, v.Len())
		changed := false
		for it := v.MapRange(); it.Next(); {
			if fv, ok := w.value(it.Value(), depth+1); ok {
				out[it.Key().String()], changed = fv, true
			} else {
				out[it.Key().String()] = it.Value().Interface()
			}
		}
		return out, changed
	}
	return nil, false
}

// hasCustomJSON reports whether encoding/json would render t (or an
// addressable t) through a MarshalJSON or MarshalText method.
func hasCustomJSON(t reflect.Type) bool {
	jm, tm := reflect.TypeFor[json.Marshaler](), reflect.TypeFor[encoding.TextMarshaler]()
	pt := reflect.PointerTo(t)
	return t.Implements(jm) || t.Implements(tm) || pt.Implements(jm) || pt.Implements(tm)
}

// nonFiniteString renders NaN and ±Inf the way strconv does ("NaN", "+Inf",
// "-Inf"); it reports false for a finite f.
func nonFiniteString(f float64) (string, bool) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return strconv.FormatFloat(f, 'g', -1, 64), true
	}
	return "", false
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
	if traceID == "" && l.core.autoTraceID {
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
	payload, extra, err := l.renderPayload(e, b)
	ev.Payload = payload
	if err != nil {
		// The payload could not be serialized. Record that on the entry instead
		// of emitting a silently empty payload, and surface it to the error
		// callback. An error the caller already attached is never overwritten.
		l.reportError(err)
		if ev.ErrorType == "" {
			ev.ErrorType = "LogSerializationError"
			ev.ErrorMessage = err.Error()
		}
	}

	// A single extras source is referenced directly instead of copied: the Event
	// never outlives the emit, entry and payload extras are owned by this call,
	// and bound extras are immutable once the derived logger exists.
	sources := 0
	var soleExtra map[string]any
	if len(b.extra) > 0 {
		sources++
		soleExtra = b.extra
	}
	if len(extra) > 0 {
		sources++
		soleExtra = extra
	}
	if len(e.extra) > 0 {
		sources++
		soleExtra = e.extra
	}
	switch {
	case sources == 1:
		ev.Extra = soleExtra
	case sources > 1:
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

	// Struct values among the extras (WithExtra, bound extras, slog
	// attributes) honour their mask tags as a payload does. Extras that cannot
	// carry a tag are neither copied nor walked, and when every value was a
	// plain one as it was added they are not even looked at. Fields lifted out
	// of the payload need no check: the payload walk already rendered them.
	if len(ev.Extra) > 0 && (e.extraTags || b.extraTags) {
		var warn error
		if ev.Extra, warn = maskTaggedExtra(ev.Extra); warn != nil {
			l.reportError(warn)
		}
	}

	return ev
}

// renderPayload normalises the payload (honouring struct tags), applies any
// field masking strategies, and returns the stringified JSON payload plus any
// extracted logextra fields.
//
// When the payload cannot be serialized the returned payload is a visible
// marker (never an empty string) and the error is returned so the caller can
// record it on the entry.
func (l *Logger) renderPayload(e *Entry, b *boundFields) (payload string, extra map[string]any, err error) {
	if e.payload == nil {
		return "", nil, nil
	}
	normalized, extra, warn := processPayloadWarn(e.payload)
	if warn != nil {
		// A cycle or an unknown mask tag: the payload still renders (cut with
		// a marker, or fully hidden), but the caller should hear about it.
		l.reportError(warn)
	}
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
		// processPayload built both trees exclusively for this entry, so masking
		// rewrites them in place instead of deep-copying every node.
		applyMaskingLowerInPlace(normalized, strategies)
		// logextra lifts fields OUT of the payload before the strategies above
		// run, so the extra map must be masked too — otherwise Mask("refId")
		// combined with a `logextra` tag on refId would leak the raw value
		// through the extra object.
		if len(extra) > 0 {
			applyMaskingLowerInPlace(extra, strategies)
		}
	}
	rendered, err := stringifyJSON(normalized)
	if err != nil {
		return renderFailure(err), extra, err
	}
	return rendered, extra, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// truncate cuts s at limit bytes and appends a truncation marker. The marker is
// additive — output may exceed limit by its length — so the cap is on the
// retained content, not the rendered string; CapBody in internal/httplog
// follows the same convention.
func truncate(s string, limit int) string {
	if limit <= 0 || len(s) <= limit {
		return s
	}
	// Back up to a rune boundary so the cut never splits a multi-byte
	// character and produces an invalid string.
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "... [truncated]"
}
