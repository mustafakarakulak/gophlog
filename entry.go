package gophlog

import (
	"context"
	"fmt"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Entry is a fluent builder for a single log record. Build it with one of the
// Logger level methods, chain the With* methods, and finish with Log().
//
// An Entry is not safe for concurrent use; build and Log it from one goroutine.
type Entry struct {
	logger  *Logger
	ctx     context.Context
	level   Level
	sev     int // level.severity(); -1 until severity() resolves a non-canonical name
	message string
	event   string
	ts      time.Time // optional timestamp override; zero means use the logger clock

	payload        any
	maskStrategies map[string]MaskingStrategy
	logType        LogType
	category       string
	extra          map[string]any
	// extraTags is set once an extra value that is not a plain value (see
	// plainExtraValue) is added: only then can the extras hold a mask tag.
	extraTags bool

	errorType    string
	errorMessage string
	stackTrace   string

	traceID   string
	spanID    string
	requestID string

	tenantID  string
	userID    string
	clientIP  string
	sessionID string

	httpMethod   string
	httpPath     string
	queryParams  map[string]string
	httpStatus   *int
	durationMs   *float64
	bytesIn      *int64
	bytesOut     *int64
	requestBody  string
	responseBody string

	childWorkflowID  string
	runID            string
	parentWorkflowID string

	integration *IntegrationInfo
	queue       *QueueInfo
	job         *JobInfo
}

func newEntry(l *Logger, level Level, message, event string) *Entry {
	return &Entry{logger: l, level: level, sev: level.canonicalSeverity(), message: message, event: event}
}

// severity returns the entry's severity. A level spelled other than
// canonically (e.g. "error") is resolved here on first use rather than in
// newEntry, whose inlining keeps the Entry off the heap; resolving also
// rewrites a known name to its canonical spelling for the emitted line.
func (e *Entry) severity() int {
	if e.sev < 0 {
		e.level, e.sev = e.level.resolve()
	}
	return e.sev
}

// Ctx attaches a context so trace/correlation IDs and propagated metadata are
// resolved at Log() time.
func (e *Entry) Ctx(ctx context.Context) *Entry {
	e.ctx = ctx
	return e
}

// WithPayload sets the payload. If the value is a struct (or pointer/slice/map
// thereof), `mask` and `logextra` struct tags are honoured automatically.
func (e *Entry) WithPayload(payload any) *Entry {
	e.payload = payload
	return e
}

// WithPayloadMasked sets the payload and applies field-specific masking
// strategies (keyed by field name, case-insensitive).
func (e *Entry) WithPayloadMasked(payload any, strategies map[string]MaskingStrategy) *Entry {
	e.payload = payload
	for k, v := range strategies {
		e.addMask(k, v)
	}
	return e
}

// Mask masks a single payload field using the given strategy.
func (e *Entry) Mask(field string, strategy MaskingStrategy) *Entry {
	e.addMask(field, strategy)
	return e
}

// MaskMany masks multiple payload fields.
func (e *Entry) MaskMany(strategies map[string]MaskingStrategy) *Entry {
	for k, v := range strategies {
		e.addMask(k, v)
	}
	return e
}

func (e *Entry) addMask(field string, strategy MaskingStrategy) {
	if e.maskStrategies == nil {
		e.maskStrategies = make(map[string]MaskingStrategy)
	}
	// Keys are lower-cased on insert so the case-insensitive lookup map never
	// has to be rebuilt at emit time.
	e.maskStrategies[strings.ToLower(field)] = strategy
}

// WithLogType sets the log type (app/audit/security).
func (e *Entry) WithLogType(t LogType) *Entry {
	e.logType = t
	return e
}

// WithCategory sets the log category.
func (e *Entry) WithCategory(category string) *Entry {
	e.category = category
	return e
}

// WithError attaches error type, message and a captured stack trace.
//
// Nothing is recorded when the entry's level would not be emitted, so a
// disabled-level error log never pays for the type lookup, the Error() call or
// the (relatively expensive) stack trace. The level is checked when WithError
// is called: if the minimum level is lowered before Log, the entry is written
// without its error fields.
func (e *Entry) WithError(err error) *Entry {
	if err == nil || !e.logger.enabledSeverity(e.severity()) {
		return e
	}
	e.errorType = errorTypeName(err)
	e.errorMessage = errorString(err)
	// skip=2 → start the trace at the caller of WithError (the user's code),
	// not at runtime internals.
	e.stackTrace = captureStack(2)
	return e
}

// WithStackTrace sets an explicit stack trace, overriding any captured one.
func (e *Entry) WithStackTrace(stack string) *Entry {
	e.stackTrace = stack
	return e
}

// WithExtra merges a map of searchable extra fields. Struct values honour their
// `mask` tags, as in a payload; name-based strategies (Mask, MaskMany) do not
// apply to these values.
func (e *Entry) WithExtra(extra map[string]any) *Entry {
	if len(extra) == 0 {
		return e
	}
	if e.extra == nil {
		e.extra = make(map[string]any, len(extra))
	}
	for k, v := range extra {
		e.extra[k] = v
		e.extraTags = e.extraTags || !plainExtraValue(v)
	}
	return e
}

// WithExtraField sets a single searchable extra field. Masking works as for
// WithExtra.
func (e *Entry) WithExtraField(key string, value any) *Entry {
	if e.extra == nil {
		e.extra = make(map[string]any, 1)
	}
	e.extra[key] = value
	e.extraTags = e.extraTags || !plainExtraValue(value)
	return e
}

// WithTenant sets the tenant ID.
func (e *Entry) WithTenant(id string) *Entry { e.tenantID = id; return e }

// WithUser sets the user ID.
func (e *Entry) WithUser(id string) *Entry { e.userID = id; return e }

// WithClientIP sets the client IP.
func (e *Entry) WithClientIP(ip string) *Entry { e.clientIP = ip; return e }

// WithSession sets the session ID.
func (e *Entry) WithSession(id string) *Entry { e.sessionID = id; return e }

// WithTraceID overrides the trace ID.
func (e *Entry) WithTraceID(id string) *Entry { e.traceID = id; return e }

// WithSpanID overrides the span ID.
func (e *Entry) WithSpanID(id string) *Entry { e.spanID = id; return e }

// WithRequestID sets the request ID.
func (e *Entry) WithRequestID(id string) *Entry { e.requestID = id; return e }

// WithHTTP sets the HTTP method and path.
func (e *Entry) WithHTTP(method, path string) *Entry {
	e.httpMethod = method
	e.httpPath = path
	return e
}

// WithHTTPResult sets HTTP method, path, status and duration in one call.
func (e *Entry) WithHTTPResult(method, path string, status int, durationMs float64) *Entry {
	e.httpMethod = method
	e.httpPath = path
	e.httpStatus = &status
	e.durationMs = &durationMs
	return e
}

// WithStatus sets the HTTP status code.
func (e *Entry) WithStatus(status int) *Entry { e.httpStatus = &status; return e }

// WithDuration sets the operation duration in milliseconds.
func (e *Entry) WithDuration(ms float64) *Entry { e.durationMs = &ms; return e }

// WithQueryParams sets the query parameters.
func (e *Entry) WithQueryParams(params map[string]string) *Entry {
	e.queryParams = params
	return e
}

// WithBytes sets the inbound/outbound byte counts.
func (e *Entry) WithBytes(bytesIn, bytesOut int64) *Entry {
	e.bytesIn = &bytesIn
	e.bytesOut = &bytesOut
	return e
}

// WithRequestBody sets the (already-rendered) request body string.
func (e *Entry) WithRequestBody(body string) *Entry { e.requestBody = body; return e }

// WithResponseBody sets the (already-rendered) response body string.
func (e *Entry) WithResponseBody(body string) *Entry { e.responseBody = body; return e }

// WithIntegration sets a full IntegrationInfo.
func (e *Entry) WithIntegration(info *IntegrationInfo) *Entry { e.integration = info; return e }

// WithIntegrationResult sets integration info from individual fields.
func (e *Entry) WithIntegrationResult(target string, status IntegrationStatus, durationMs float64, retryCount int) *Entry {
	e.integration = &IntegrationInfo{
		Target:             target,
		Status:             status,
		ExternalDurationMs: &durationMs,
		RetryCount:         &retryCount,
	}
	return e
}

// WithQueue sets a full QueueInfo.
func (e *Entry) WithQueue(info *QueueInfo) *Entry { e.queue = info; return e }

// WithQueueMessage sets queue info from individual fields.
func (e *Entry) WithQueueMessage(queueName, messageID string, retryCount int, ack bool) *Entry {
	e.queue = &QueueInfo{
		QueueName:  queueName,
		MessageID:  messageID,
		RetryCount: &retryCount,
		Ack:        &ack,
	}
	return e
}

// WithJob sets a full JobInfo.
func (e *Entry) WithJob(info *JobInfo) *Entry { e.job = info; return e }

// WithJobInfo sets job info from individual fields.
func (e *Entry) WithJobInfo(name, schedule, runID string) *Entry {
	e.job = &JobInfo{Name: name, Schedule: schedule, RunID: runID}
	return e
}

// WithWorkflow sets workflow identifiers (child workflow, run and parent
// workflow IDs). Empty values are ignored.
func (e *Entry) WithWorkflow(childWorkflowID, runID, parentWorkflowID string) *Entry {
	if childWorkflowID != "" {
		e.childWorkflowID = childWorkflowID
	}
	if runID != "" {
		e.runID = runID
	}
	if parentWorkflowID != "" {
		e.parentWorkflowID = parentWorkflowID
	}
	return e
}

// Log emits the entry. It is a no-op if the level is below the logger minimum.
func (e *Entry) Log() {
	e.logger.emit(e)
}

// errorTypeName returns a short type name for an error value. Named/custom error
// types report their own type name (e.g. "ValidationError"), while the standard
// library's anonymous wrappers (errors.New, fmt.Errorf, errors.Join) report a
// plain "error" instead of their unexported internals.
func errorTypeName(err error) string {
	t := reflect.TypeOf(err)
	for t != nil && t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t == nil {
		return "error"
	}
	switch t.PkgPath() {
	case "errors":
		if n := t.Name(); n == "errorString" || n == "joinError" {
			return "error"
		}
	case "fmt":
		if n := t.Name(); n == "wrapError" || n == "wrapErrors" {
			return "error"
		}
	}
	name := t.Name()
	if name == "" {
		return "error"
	}
	return name
}

// errorString returns err.Error() without letting a faulty Error method crash
// the caller. It mirrors fmt: when the method panics on a nil pointer receiver
// (a typed-nil error) the result is "<nil>", any other panic is rendered as a
// "%!v(PANIC=Error method: ...)" marker.
func errorString(err error) (s string) {
	defer func() {
		if r := recover(); r != nil {
			if v := reflect.ValueOf(err); v.Kind() == reflect.Pointer && v.IsNil() {
				s = "<nil>"
				return
			}
			s = "%!v(PANIC=Error method: " + fmt.Sprint(r) + ")"
		}
	}()
	return err.Error()
}

// captureStack renders the calling goroutine's stack, skipping `skip` frames.
func captureStack(skip int) string {
	const depth = 32
	var pcs [depth]uintptr
	n := runtime.Callers(skip+1, pcs[:])
	if n == 0 {
		return ""
	}
	frames := runtime.CallersFrames(pcs[:n])
	var b strings.Builder
	for {
		frame, more := frames.Next()
		b.WriteString("   at ")
		b.WriteString(frame.Function)
		b.WriteString("(")
		b.WriteString(frame.File)
		b.WriteString(":")
		b.WriteString(strconv.Itoa(frame.Line))
		b.WriteString(")\n")
		if !more {
			break
		}
	}
	return b.String()
}
