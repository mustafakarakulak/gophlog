// Package middleware provides net/http server middleware that automatically
// logs every HTTP request/response using github.com/mustafakarakulak/go-logging.
//
// It captures the request/response bodies, duration, status, client IP, query
// parameters and workflow headers, applies field masking, and emits a single
// structured log line per request.
package middleware

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"

	logging "github.com/mustafakarakulak/go-logging"
	"github.com/mustafakarakulak/go-logging/internal/httplog"
)

// Options configures the request-logging middleware.
type Options struct {
	// Logger is the logger to use. Defaults to logging.Default().
	Logger *logging.Logger

	// LogRequestBody / LogResponseBody toggle body capture. Default: true.
	LogRequestBody  bool
	LogResponseBody bool

	// MaxBodySize caps captured bodies in bytes. Default: 100 KiB.
	MaxBodySize int

	// MaskFieldStrategies masks named JSON fields in request/response bodies
	// (case-insensitive, applied recursively).
	MaskFieldStrategies map[string]logging.MaskingStrategy

	// LogExtraFields lifts the named JSON fields out of the bodies and into the
	// searchable `extra` object (keyed request_<field> / response_<field>).
	LogExtraFields []string

	// SuccessLogLevel is used for 2xx/3xx. Default: INFO.
	SuccessLogLevel logging.Level
	// ErrorLogLevel is used for 4xx/5xx. Default: ERROR.
	ErrorLogLevel logging.Level

	// EventName overrides the event name. Default: "http_request".
	EventName string

	// ExcludePaths skips logging for matching paths (wildcards via trailing /*).
	// Defaults to common swagger/health paths.
	ExcludePaths []string
	// IncludePaths, when set, limits logging to matching paths.
	IncludePaths []string

	// ExtraProvider adds per-request extra fields derived from the request.
	ExtraProvider func(*http.Request) map[string]string
}

func (o *Options) applyDefaults() {
	if o.Logger == nil {
		o.Logger = logging.Default()
	}
	if o.MaxBodySize == 0 {
		o.MaxBodySize = 100 * 1024
	}
	if o.SuccessLogLevel == "" {
		o.SuccessLogLevel = logging.INFO
	}
	if o.ErrorLogLevel == "" {
		o.ErrorLogLevel = logging.ERROR
	}
	if o.EventName == "" {
		o.EventName = "http_request"
	}
	if o.ExcludePaths == nil {
		o.ExcludePaths = []string{"/swagger", "/health", "/healthz", "/healthcheck", "/metrics"}
	}
}

// New returns middleware that logs requests using the given options.
func New(opts Options) func(http.Handler) http.Handler {
	opts.applyDefaults()
	// Default body capture to true unless explicitly configured via NewDefault.
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handle(opts, next, w, r)
		})
	}
}

// NewDefault returns middleware with body capture enabled and default options.
func NewDefault() func(http.Handler) http.Handler {
	return New(Options{LogRequestBody: true, LogResponseBody: true})
}

func handle(opts Options, next http.Handler, w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	if httplog.ShouldExclude(path, opts.ExcludePaths) || !httplog.ShouldInclude(path, opts.IncludePaths) {
		next.ServeHTTP(w, r)
		return
	}

	// Resolve / propagate correlation ID and workflow headers via context.
	ctx := r.Context()
	correlationID := r.Header.Get(logging.CorrelationHeader)
	if correlationID == "" {
		correlationID = logging.NewCorrelationID()
	}
	ctx = logging.WithCorrelationID(ctx, correlationID)
	ctx = logging.WithWorkflow(ctx,
		r.Header.Get(logging.HeaderChildWorkflowID),
		r.Header.Get(logging.HeaderRunID),
		r.Header.Get(logging.HeaderParentWorkflowID),
	)
	if ip := ClientIP(r); ip != "" {
		ctx = logging.WithClientIP(ctx, ip)
	}
	r = r.WithContext(ctx)

	// Capture request body without ever truncating what the handler receives.
	var requestBody string
	var reqBytes int64
	if opts.LogRequestBody && r.Body != nil {
		captured, restored, truncated := httplog.CaptureBody(r.Body, opts.MaxBodySize)
		r.Body = restored
		reqBytes = int64(len(captured))
		if truncated {
			requestBody = bodyTooLarge
		} else {
			requestBody = string(captured)
		}
	}

	rec := &responseRecorder{
		ResponseWriter: w,
		status:         http.StatusOK,
		capture:        opts.LogResponseBody,
		max:            opts.MaxBodySize,
	}

	begin := time.Now()
	next.ServeHTTP(rec, r)
	durationMs := float64(time.Since(begin).Microseconds()) / 1000.0

	responseBody := ""
	if opts.LogResponseBody {
		if rec.truncated {
			responseBody = bodyTooLarge
		} else {
			responseBody = rec.buf.String()
		}
	}

	status := rec.status
	level := opts.SuccessLogLevel
	if status >= 400 {
		level = opts.ErrorLogLevel
	}

	extra := map[string]any{}
	maskedReq := processBody(requestBody, r.Header.Get("Content-Type"), opts, true, extra)
	maskedResp := processBody(responseBody, rec.Header().Get("Content-Type"), opts, false, extra)

	if opts.ExtraProvider != nil {
		for k, v := range opts.ExtraProvider(r) {
			extra[k] = v
		}
	}

	// Mask sensitive query parameters (tokens, passwords) before they reach
	// either the query_params field or the path component of the log message.
	query := r.URL.Query()
	httplog.MaskQueryValues(query, opts.MaskFieldStrategies)

	method := r.Method
	fullPath := r.URL.Path
	if r.URL.RawQuery != "" {
		if len(opts.MaskFieldStrategies) > 0 {
			fullPath += "?" + httplog.RenderQuery(query)
		} else {
			fullPath += "?" + r.URL.RawQuery
		}
	}
	msg := httplog.Message(method, fullPath, status, durationMs)

	entry := opts.Logger.At(level, msg, opts.EventName).
		Ctx(r.Context()).
		WithHTTPResult(method, fullPath, status, durationMs)

	bytesOut := int64(rec.written)
	entry.WithBytes(reqBytes, bytesOut)

	if maskedReq != "" {
		entry.WithRequestBody(maskedReq)
	}
	if maskedResp != "" {
		entry.WithResponseBody(maskedResp)
	}
	if qp := httplog.JoinQuery(query); len(qp) > 0 {
		entry.WithQueryParams(qp)
	}
	if len(extra) > 0 {
		entry.WithExtra(extra)
	}
	entry.Log()
}

// bodyTooLarge is logged in place of a body that exceeds MaxBodySize. The full
// body is still delivered to the client/handler; only the logged copy is
// replaced, so masking is never bypassed by a partial, unparseable body.
const bodyTooLarge = "[body not logged: exceeds MaxBodySize]"

// processBody masks the FULL body and extracts extra fields, then truncates the
// masked result for logging. Masking happens before truncation so sensitive
// fields can never leak through a truncated, unparseable body. Form-urlencoded
// bodies are masked too; any other non-JSON body is logged as-is.
func processBody(body, contentType string, opts Options, isRequest bool, extra map[string]any) string {
	if body == "" {
		return ""
	}
	formatted := httplog.FormatJSON(body)
	var decoded any
	if err := json.Unmarshal([]byte(formatted), &decoded); err != nil {
		if isFormContentType(contentType) {
			if masked, ok := httplog.MaskFormBody(body, opts.MaskFieldStrategies); ok {
				return httplog.CapBody(masked, opts.MaxBodySize)
			}
		}
		return httplog.CapBody(formatted, opts.MaxBodySize) // not JSON; log as-is
	}

	if len(opts.LogExtraFields) > 0 {
		prefix := "response_"
		if isRequest {
			prefix = "request_"
		}
		httplog.CollectExtra(decoded, opts.LogExtraFields, prefix, extra)
	}

	if len(opts.MaskFieldStrategies) > 0 {
		decoded = logging.MaskJSON(decoded, opts.MaskFieldStrategies)
	}

	out, err := json.Marshal(decoded)
	if err != nil {
		return httplog.CapBody(formatted, opts.MaxBodySize)
	}
	return httplog.CapBody(string(out), opts.MaxBodySize)
}

type responseRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	capture     bool
	max         int
	buf         bytes.Buffer
	written     int
	truncated   bool
}

func (r *responseRecorder) WriteHeader(code int) {
	if r.wroteHeader {
		return
	}
	r.status = code
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(b)
	r.written += n
	if r.capture {
		if r.buf.Len()+len(b) > r.max {
			// Stop buffering and mark truncated; the full body still reaches
			// the client, but the partial copy is not logged (avoids leaking
			// unmasked data through an unparseable fragment).
			r.truncated = true
			r.capture = false
		} else {
			r.buf.Write(b)
		}
	}
	return n, err
}

// Flush implements http.Flusher when the underlying writer supports it.
func (r *responseRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap exposes the underlying ResponseWriter so http.ResponseController (and
// thus Flush/Hijack/SetWriteDeadline) keeps working through the recorder.
func (r *responseRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// Hijack implements http.Hijacker when the underlying writer supports it,
// preserving WebSocket/SSE upgrades behind the middleware.
func (r *responseRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := r.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

// ClientIP extracts the client IP from X-Forwarded-For, X-Real-IP, or the
// connection's remote address.
func ClientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		parts := strings.Split(fwd, ",")
		if len(parts) > 0 {
			if ip := strings.TrimSpace(parts[0]); ip != "" {
				return ip
			}
		}
	}
	if real := r.Header.Get("X-Real-IP"); real != "" {
		return real
	}
	addr := r.RemoteAddr
	if i := strings.LastIndex(addr, ":"); i > 0 {
		return addr[:i]
	}
	return addr
}

func isFormContentType(contentType string) bool {
	return strings.Contains(strings.ToLower(contentType), "application/x-www-form-urlencoded")
}
