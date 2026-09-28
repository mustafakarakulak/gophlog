// Package middleware provides net/http server middleware that automatically
// logs every HTTP request/response using github.com/mustafakarakulak/gophlog.
//
// It captures the request/response bodies, duration, status, client IP, query
// parameters and workflow headers, applies field masking, and emits a single
// structured log line per request.
//
// A handler that panics is logged as a 500 with "panic: ..." in the error
// fields; the panic then continues with the same value. An inbound workflow
// header is dropped when it is longer than 1000 bytes, not valid UTF-8, or
// contains control, line/paragraph-separator or bidi embedding, override or
// isolate characters.
package middleware

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/mustafakarakulak/gophlog"
	"github.com/mustafakarakulak/gophlog/internal/httplog"
)

// Options configures the request-logging middleware.
type Options struct {
	// Logger is the logger to use. Defaults to gophlog.Default().
	Logger *gophlog.Logger

	// DisableRequestBody / DisableResponseBody turn off body capture, which is
	// on by default. They are phrased negatively so the zero-value Options
	// captures bodies, as documented.
	//
	// Some bodies are never captured and are logged as a marker instead:
	// "[body not logged: non-identity Content-Encoding]" for a Content-Encoding
	// other than identity, and "[body not logged: streaming]" for Server-Sent
	// Events, gRPC, Connect streaming, stream=watch and 101 Switching Protocols
	// responses.
	DisableRequestBody  bool
	DisableResponseBody bool

	// MaxBodySize caps captured bodies in bytes; a larger body is logged as
	// "[body not logged: exceeds MaxBodySize]". Default: 100 KiB.
	MaxBodySize int

	// MaskFieldStrategies masks named fields in JSON and form-urlencoded
	// request/response bodies (case-insensitive, applied recursively) and in
	// query parameters. With it set, a form body that cannot be masked
	// unambiguously is logged as "[body not logged: cannot be masked]".
	MaskFieldStrategies map[string]gophlog.MaskingStrategy

	// LogExtraFields lifts the named JSON fields out of the bodies and into the
	// searchable `extra` object (keyed request_<field> / response_<field>).
	LogExtraFields []string

	// SuccessLogLevel is used for 2xx/3xx. Default: INFO.
	SuccessLogLevel gophlog.Level
	// ErrorLogLevel is used for 4xx/5xx. Default: ERROR.
	ErrorLogLevel gophlog.Level

	// EventName overrides the event name. Default: "http_request".
	EventName string

	// ExcludePaths skips logging for matching paths (wildcards via trailing /*);
	// a pattern without a wildcard matches by prefix, case-insensitively. The
	// correlation ID, workflow IDs and client IP still go into the request
	// context of a skipped path.
	// Defaults to DefaultExcludePaths — common API-docs and probe endpoints.
	// Setting this field replaces that list rather than adding to it, so append
	// to DefaultExcludePaths to keep the defaults.
	ExcludePaths []string
	// IncludePaths, when set, limits logging to matching paths.
	IncludePaths []string

	// ExtraProvider adds per-request extra fields derived from the request.
	ExtraProvider func(*http.Request) map[string]string

	// DisableForwardedHeaders makes the logged client IP come only from the
	// connection's remote address, ignoring X-Forwarded-For / X-Real-IP. Set it
	// when the service is NOT behind a trusted proxy: those headers are
	// client-controlled and can otherwise be spoofed in audit logs.
	DisableForwardedHeaders bool
}

// DefaultExcludePaths is the path list Options.ExcludePaths falls back to: API
// documentation UIs and health/metrics probes, which are high-volume and carry
// no audit value. Patterns match by prefix, so "/health" also covers
// "/healthz" and "/health/ready".
//
// Assigning Options.ExcludePaths replaces this list; append to it to keep the
// defaults alongside your own patterns:
//
//	ExcludePaths: append(middleware.DefaultExcludePaths, "/internal/*")
//
// Treat it as read-only: it is shared by every Options that relies on the
// default.
var DefaultExcludePaths = []string{
	"/swagger",
	"/scalar",
	"/health",
	"/healthz",
	"/healthcheck",
	"/metrics",
}

func (o *Options) applyDefaults() {
	if o.Logger == nil {
		o.Logger = gophlog.Default()
	}
	if o.MaxBodySize == 0 {
		o.MaxBodySize = 100 * 1024
	}
	if o.SuccessLogLevel == "" {
		o.SuccessLogLevel = gophlog.INFO
	}
	if o.ErrorLogLevel == "" {
		o.ErrorLogLevel = gophlog.ERROR
	}
	if o.EventName == "" {
		o.EventName = "http_request"
	}
	if o.ExcludePaths == nil {
		o.ExcludePaths = DefaultExcludePaths
	}
}

// precomputed holds the lookups derived from Options at construction time.
// Options are static after New, so the per-request paths (query/body masking,
// extra-field collection) never rebuild them.
type precomputed struct {
	lowerStrategies map[string]gophlog.MaskingStrategy
	extraWant       map[string]string
}

// New returns middleware that logs requests using the given options.
func New(opts Options) func(http.Handler) http.Handler {
	opts.applyDefaults()
	pre := precomputed{
		lowerStrategies: httplog.LowerStrategies(opts.MaskFieldStrategies),
		extraWant:       httplog.LowerExtraFields(opts.LogExtraFields),
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handle(opts, pre, next, w, r)
		})
	}
}

// NewDefault returns middleware with the default options, which include
// request/response body capture. It is equivalent to New(Options{}).
func NewDefault() func(http.Handler) http.Handler {
	return New(Options{})
}

func handle(opts Options, pre precomputed, next http.Handler, w http.ResponseWriter, r *http.Request) {
	// Resolve / propagate correlation ID and workflow headers via context.
	// This runs before the path filters, so a skipped path keeps them too and
	// outbound calls from an excluded handler still carry the trace_id.
	// The inbound header is client-controlled, so an absent or malformed value
	// is replaced by a freshly generated ID rather than trusted into the logs.
	ctx := r.Context()
	correlationID := r.Header.Get(gophlog.CorrelationHeader)
	if !gophlog.IsValidCorrelationID(correlationID) {
		correlationID = gophlog.NewCorrelationID()
	}
	ctx = gophlog.WithCorrelationID(ctx, correlationID)
	ctx = gophlog.WithWorkflow(ctx,
		workflowHeader(r, gophlog.HeaderChildWorkflowID),
		workflowHeader(r, gophlog.HeaderRunID),
		workflowHeader(r, gophlog.HeaderParentWorkflowID),
	)
	if ip := clientIP(r, !opts.DisableForwardedHeaders); ip != "" {
		ctx = gophlog.WithClientIP(ctx, ip)
	}
	r = r.WithContext(ctx)

	path := r.URL.Path
	if httplog.ShouldExclude(path, opts.ExcludePaths) || !httplog.ShouldInclude(path, opts.IncludePaths) {
		next.ServeHTTP(w, r)
		return
	}

	// Fast path: when no level this request could log at is enabled, skip
	// capture and masking entirely — the entry would be dropped at emit anyway.
	// Context propagation above still happened, so handlers and outbound calls
	// keep their correlation ID.
	successEnabled := opts.Logger.Enabled(opts.SuccessLogLevel)
	errorEnabled := opts.Logger.Enabled(opts.ErrorLogLevel)
	if !successEnabled && !errorEnabled {
		next.ServeHTTP(w, r)
		return
	}

	// Capture request body without ever truncating what the handler receives.
	// bytes_in comes from the Content-Length header when declared, so it stays
	// correct for bodies larger than MaxBodySize and when capture is disabled;
	// a chunked (unknown-length) body is counted as it is actually read.
	reqBytes := r.ContentLength
	var counter *countingBody
	if reqBytes < 0 && r.Body != nil && r.Body != http.NoBody {
		counter = &countingBody{ReadCloser: r.Body}
		r.Body = counter
	}
	var requestBody string
	if !opts.DisableRequestBody && r.Body != nil && r.Body != http.NoBody {
		switch {
		case !httplog.IsIdentityEncoding(r.Header):
			// Compressed bytes cannot be masked; never log them raw.
			requestBody = httplog.BodyEncoded
		case httplog.IsStreamingContentType(r.Header.Get("Content-Type")):
			// A client stream stays open while it waits for the response;
			// reading it up front would stall the handler indefinitely.
			requestBody = httplog.BodyStreaming
		default:
			captured, restored, truncated := httplog.CaptureBody(r.Body, opts.MaxBodySize)
			r.Body = restored
			if truncated {
				requestBody = httplog.BodyTooLarge
			} else {
				requestBody = string(captured)
			}
		}
	}

	rec := &responseRecorder{
		ResponseWriter: w,
		status:         http.StatusOK,
		capture:        !opts.DisableResponseBody,
		max:            opts.MaxBodySize,
		successEnabled: successEnabled,
		errorEnabled:   errorEnabled,
	}

	begin := time.Now()
	// A panicking handler still gets its log line — status 500 and the panic
	// in the error fields — and the panic then carries on with the same value,
	// so net/http (or an outer recovery middleware) handles it as before.
	// handlerReturned keeps a panic raised while logging a completed request
	// from being reported as a handler panic.
	handlerReturned := false
	defer func() {
		if handlerReturned {
			return
		}
		if p := recover(); p != nil {
			func() {
				// A failure while logging must not replace the handler's panic.
				defer func() { _ = recover() }()
				logRequest(opts, pre, r, rec, requestBody, bytesIn(reqBytes, counter), begin, p)
			}()
			panic(p)
		}
	}()
	next.ServeHTTP(rec, r)
	handlerReturned = true
	logRequest(opts, pre, r, rec, requestBody, bytesIn(reqBytes, counter), begin, nil)
}

// logRequest emits the log line for a request whose handler has finished.
// panicVal is the value a panicking handler raised, or nil.
func logRequest(opts Options, pre precomputed, r *http.Request, rec *responseRecorder, requestBody string, reqBytes int64, begin time.Time, panicVal any) {
	durationMs := float64(time.Since(begin).Microseconds()) / 1000.0

	status := rec.status
	if panicVal != nil {
		// net/http aborts the connection, so the client got no complete
		// response; the request amounted to an internal server error.
		status = http.StatusInternalServerError
	}
	level := opts.SuccessLogLevel
	enabled := rec.successEnabled
	if status >= 400 {
		level = opts.ErrorLogLevel
		enabled = rec.errorEnabled
	}
	// The level for the actual status is filtered out (e.g. minimum level ERROR
	// and the response was a 2xx): stop before any masking or query work.
	if !enabled {
		return
	}

	// A panicking handler's response was cut off mid-write: the partial body
	// could be an unparseable fragment that bypasses masking, so none is logged.
	responseBody := ""
	if !opts.DisableResponseBody && panicVal == nil {
		responseBody = rec.loggedBody()
	}

	// The extra map is only needed when something can fill it; most setups
	// configure neither LogExtraFields nor an ExtraProvider.
	var extra map[string]any
	if len(pre.extraWant) > 0 || opts.ExtraProvider != nil {
		extra = make(map[string]any)
	}
	maskedReq := httplog.ProcessBody(requestBody, r.Header.Get("Content-Type"),
		pre.lowerStrategies, pre.extraWant, opts.MaxBodySize, "request_", extra)
	maskedResp := httplog.ProcessBody(responseBody, rec.Header().Get("Content-Type"),
		pre.lowerStrategies, pre.extraWant, opts.MaxBodySize, "response_", extra)

	if opts.ExtraProvider != nil {
		for k, v := range opts.ExtraProvider(r) {
			extra[k] = v
		}
	}

	// Mask sensitive query parameters (tokens, passwords) before they reach
	// either the query_params field or the path component of the log message.
	// Parsing is skipped entirely when the request carries no query string.
	var query url.Values
	if r.URL.RawQuery != "" {
		query = r.URL.Query()
		httplog.MaskQueryValuesLower(query, pre.lowerStrategies)
	}

	method := r.Method
	fullPath := r.URL.Path
	if r.URL.RawQuery != "" {
		if len(pre.lowerStrategies) > 0 {
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
	if panicVal != nil {
		entry.WithError(panicError(panicVal))
		if panicVal == http.ErrAbortHandler {
			// ErrAbortHandler is net/http's signal to abort a response
			// (httputil.ReverseProxy raises it when the upstream copy fails),
			// not a bug. The request is still logged, but like net/http itself
			// without a stack trace.
			entry.WithStackTrace("")
		}
	}
	entry.Log()
}

// panicError renders a recovered panic value for the log line's error fields.
// An error value stays wrapped, so errors.Is / errors.As still see it.
func panicError(p any) error {
	if err, ok := p.(error); ok {
		return fmt.Errorf("panic: %w", err)
	}
	return fmt.Errorf("panic: %v", p)
}

// maxWorkflowIDLen caps an inbound workflow header in bytes. It matches
// Temporal's default ID length limit (limit.maxIDLength = 1000), so any
// workflow ID an orchestrator accepts still fits, while an oversized header
// can no longer bloat the audit log.
const maxWorkflowIDLen = 1000

// workflowHeader returns the named workflow header if isValidWorkflowID
// accepts it, or "" otherwise. These headers are client-controlled and end up
// in the audit log; unlike the correlation ID there is nothing to regenerate,
// so an invalid value is dropped.
func workflowHeader(r *http.Request, name string) string {
	if v := r.Header.Get(name); isValidWorkflowID(v) {
		return v
	}
	return ""
}

// isValidWorkflowID reports whether id is safe to adopt from a workflow
// header. Workflow IDs are application-defined and legitimately carry '/',
// '@' or spaces ("orders/123"), so unlike IsValidCorrelationID the character
// set stays open; only what can bloat, forge or disguise a log line is
// rejected: more than maxWorkflowIDLen bytes, invalid UTF-8, control
// characters, the line and paragraph separators (U+2028, U+2029) and the bidi
// embedding, override and isolate characters (U+202A–U+202E, U+2066–U+2069).
func isValidWorkflowID(id string) bool {
	if id == "" || len(id) > maxWorkflowIDLen || !utf8.ValidString(id) {
		return false
	}
	for _, c := range id {
		switch {
		case unicode.IsControl(c),
			c == '\u2028', c == '\u2029',
			c >= '\u202a' && c <= '\u202e',
			c >= '\u2066' && c <= '\u2069':
			return false
		}
	}
	return true
}

// countingBody counts the bytes read from a request body whose length was not
// declared up front (chunked), so bytes_in reports what was actually received
// rather than the captured prefix. The count is atomic because a handler may
// hand the body to a goroutine that outlives ServeHTTP (http.TimeoutHandler
// does).
type countingBody struct {
	io.ReadCloser
	n atomic.Int64
}

func (c *countingBody) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// bytesIn resolves bytes_in: the declared Content-Length, or the bytes counted
// off a chunked body.
func bytesIn(declared int64, counter *countingBody) int64 {
	if counter != nil {
		return counter.n.Load()
	}
	if declared < 0 {
		return 0
	}
	return declared
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
	// streaming marks a response that is never buffered: a 101 Switching
	// Protocols upgrade or a streaming Content-Type.
	streaming bool
	// successEnabled / errorEnabled mirror the logger's level checks so the
	// capture decision can be made as soon as the status is known.
	successEnabled bool
	errorEnabled   bool
}

func (r *responseRecorder) WriteHeader(code int) {
	// Informational (1xx) responses may be written any number of times before
	// the final status — e.g. 103 Early Hints. Pass them through without
	// latching, so the real status still reaches both the client and the log.
	// 101 Switching Protocols is the exception: net/http treats it as final,
	// and what follows it is the upgraded connection, not a body.
	if code >= 100 && code <= 199 && code != http.StatusSwitchingProtocols {
		r.ResponseWriter.WriteHeader(code)
		return
	}
	if r.wroteHeader {
		return
	}
	r.status = code
	r.wroteHeader = true
	// The status decides which level this request logs at; if that level is
	// filtered out, the buffered body would be discarded at the end anyway, so
	// stop capturing before the first Write.
	if code >= 400 {
		r.capture = r.capture && r.errorEnabled
	} else {
		r.capture = r.capture && r.successEnabled
	}
	if r.capture {
		// A stream never ends on the logger's schedule and encoded bytes
		// cannot be masked, so neither is buffered; loggedBody reports why.
		h := r.Header()
		if code == http.StatusSwitchingProtocols || httplog.IsStreamingContentType(h.Get("Content-Type")) {
			r.streaming = true
			r.capture = false
		} else if !httplog.IsIdentityEncoding(h) {
			r.capture = false
		}
	}
	r.ResponseWriter.WriteHeader(code)
}

// loggedBody returns what the log line carries as the response body: the
// captured bytes, or a marker when they were deliberately not captured. The
// Content-Encoding is checked again here, fail-closed, so a header set after
// WriteHeader still keeps an encoded body out of the log.
func (r *responseRecorder) loggedBody() string {
	switch {
	case r.streaming:
		return httplog.BodyStreaming
	case r.written > 0 && !httplog.IsIdentityEncoding(r.Header()):
		return httplog.BodyEncoded
	case r.truncated:
		return httplog.BodyTooLarge
	}
	return r.buf.String()
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
// Flushing commits the status line — net/http ignores a WriteHeader that
// comes after it — so the implicit 200 is latched first, keeping the logged
// status in line with what the client received.
func (r *responseRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		if !r.wroteHeader {
			r.WriteHeader(http.StatusOK)
		}
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
// connection's remote address. Note that the forwarded headers are
// client-controlled; when the service is not behind a trusted proxy, use the
// middleware's DisableForwardedHeaders option instead of trusting them.
func ClientIP(r *http.Request) string {
	return clientIP(r, true)
}

func clientIP(r *http.Request, trustForwarded bool) string {
	if trustForwarded {
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			first, _, _ := strings.Cut(fwd, ",")
			if ip := strings.TrimSpace(first); ip != "" {
				return ip
			}
		}
		if realIP := r.Header.Get("X-Real-IP"); realIP != "" {
			return realIP
		}
	}
	// RemoteAddr is host:port; SplitHostPort also unwraps bracketed IPv6
	// addresses ("[::1]:8080" -> "::1").
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
