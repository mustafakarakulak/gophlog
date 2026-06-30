package logging

import (
	"context"
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
	message string
	event   string
	ts      time.Time // optional timestamp override; zero means use the logger clock

	payload        any
	maskStrategies map[string]MaskingStrategy
	logType        LogType
	category       string
	extra          map[string]any

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
	return &Entry{logger: l, level: level, message: message, event: event}
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
	e.maskStrategies[field] = strategy
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
// The stack is only captured when the entry's level would actually be emitted,
// so a disabled-level error log never pays for the (relatively expensive) trace.
func (e *Entry) WithError(err error) *Entry {
	if err == nil {
		return e
	}
	e.errorType = errorTypeName(err)
	e.errorMessage = err.Error()
	if e.logger.Enabled(e.level) {
		// skip=2 → start the trace at the caller of WithError (the user's code),
		// not at runtime internals.
		e.stackTrace = captureStack(2)
	}
	return e
}

// WithStackTrace sets an explicit stack trace, overriding any captured one.
func (e *Entry) WithStackTrace(stack string) *Entry {
	e.stackTrace = stack
	return e
}

// WithExtra merges a map of searchable extra fields.
func (e *Entry) WithExtra(extra map[string]any) *Entry {
	if len(extra) == 0 {
		return e
	}
	if e.extra == nil {
		e.extra = make(map[string]any, len(extra))
	}
	for k, v := range extra {
		e.extra[k] = v
	}
	return e
}

// WithExtraField sets a single searchable extra field.
func (e *Entry) WithExtraField(key string, value any) *Entry {
	if e.extra == nil {
		e.extra = make(map[string]any, 1)
	}
	e.extra[key] = value
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
	switch t.PkgPath() + "." + t.Name() {
	case "errors.errorString", "fmt.wrapError", "fmt.wrapErrors", "errors.joinError":
		return "error"
	}
	name := t.Name()
	if name == "" {
		return "error"
	}
	return name
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
