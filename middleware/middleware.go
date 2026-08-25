// Package middleware provides net/http server middleware that automatically
// logs every HTTP request/response using github.com/mustafakarakulak/gophlog.
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
	"net/url"
	"strings"
	"time"

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
	DisableRequestBody  bool
	DisableResponseBody bool

	// MaxBodySize caps captured bodies in bytes. Default: 100 KiB.
	MaxBodySize int

	// MaskFieldStrategies masks named JSON fields in request/response bodies
	// (case-insensitive, applied recursively).
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
	// a pattern without a wildcard matches by prefix, case-insensitively.
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
	path := r.URL.Path

	if httplog.ShouldExclude(path, opts.ExcludePaths) || !httplog.ShouldInclude(path, opts.IncludePaths) {
		next.ServeHTTP(w, r)
		return
	}

	// Resolve / propagate correlation ID and workflow headers via context.
	// The inbound header is client-controlled, so an absent or malformed value
	// is replaced by a freshly generated ID rather than trusted into the logs.
	ctx := r.Context()
	correlationID := r.Header.Get(gophlog.CorrelationHeader)
	if !gophlog.IsValidCorrelationID(correlationID) {
		correlationID = gophlog.NewCorrelationID()
	}
	ctx = gophlog.WithCorrelationID(ctx, correlationID)
	ctx = gophlog.WithWorkflow(ctx,
		r.Header.Get(gophlog.HeaderChildWorkflowID),
		r.Header.Get(gophlog.HeaderRunID),
		r.Header.Get(gophlog.HeaderParentWorkflowID),
	)
	if ip := clientIP(r, !opts.DisableForwardedHeaders); ip != "" {
		ctx = gophlog.WithClientIP(ctx, ip)
	}
	r = r.WithContext(ctx)

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
	// the captured length is the fallback for chunked (unknown-length) bodies.
	var requestBody string
	reqBytes := r.ContentLength
	if !opts.DisableRequestBody && r.Body != nil {
		captured, restored, truncated := httplog.CaptureBody(r.Body, opts.MaxBodySize)
		r.Body = restored
		if reqBytes < 0 {
			reqBytes = int64(len(captured))
		}
		if truncated {
			requestBody = bodyTooLarge
		} else {
			requestBody = string(captured)
		}
	}
	if reqBytes < 0 {
		reqBytes = 0
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
	next.ServeHTTP(rec, r)
	durationMs := float64(time.Since(begin).Microseconds()) / 1000.0

	responseBody := ""
	if !opts.DisableResponseBody {
		if rec.truncated {
			responseBody = bodyTooLarge
		} else {
			responseBody = rec.buf.String()
		}
	}

	status := rec.status
	level := opts.SuccessLogLevel
	enabled := successEnabled
	if status >= 400 {
		level = opts.ErrorLogLevel
		enabled = errorEnabled
	}
	// The level for the actual status is filtered out (e.g. minimum level ERROR
	// and the response was a 2xx): stop before any masking or query work.
	if !enabled {
		return
	}

	// The extra map is only needed when something can fill it; most setups
	// configure neither LogExtraFields nor an ExtraProvider.
	var extra map[string]any
	if len(pre.extraWant) > 0 || opts.ExtraProvider != nil {
		extra = make(map[string]any)
	}
	maskedReq := processBody(requestBody, r.Header.Get("Content-Type"), opts, pre, true, extra)
	maskedResp := processBody(responseBody, rec.Header().Get("Content-Type"), opts, pre, false, extra)

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
	entry.Log()
}

// bodyTooLarge is logged in place of a body that exceeds MaxBodySize. The full
// body is still delivered to the client/handler; only the logged copy is
// replaced, so masking is never bypassed by a partial, unparseable body.
const bodyTooLarge = "[body not logged: exceeds MaxBodySize]"

// processBody masks the FULL body and extracts extra fields, then truncates the
// masked result for gophlog. Masking happens before truncation so sensitive
// fields can never leak through a truncated, unparseable body. Form-urlencoded
// bodies are masked too; any other non-JSON body is logged as-is.
//
// The body is decoded exactly once; the final Marshal both compacts and
// re-serializes it, so no separate formatting pass is needed.
func processBody(body, contentType string, opts Options, pre precomputed, isRequest bool, extra map[string]any) string {
	if body == "" {
		return ""
	}
	// The oversize sentinel is a fixed marker, not body content — never cap it
	// (a tiny MaxBodySize would otherwise truncate the marker itself).
	if body == bodyTooLarge {
		return body
	}
	// Nothing to mask and nothing to lift into extra: the decode/encode round
	// trip below would only re-serialize the body, so log it as-is (capped).
	if len(pre.lowerStrategies) == 0 && len(pre.extraWant) == 0 {
		return httplog.CapBody(body, opts.MaxBodySize)
	}
	var decoded any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		if isFormContentType(contentType) {
			if masked, ok := httplog.MaskFormBodyLower(body, pre.lowerStrategies); ok {
				return httplog.CapBody(masked, opts.MaxBodySize)
			}
		}
		return httplog.CapBody(body, opts.MaxBodySize) // not JSON; log as-is
	}

	// Masking runs BEFORE extra extraction, so a field named in both
	// MaskFieldStrategies and LogExtraFields is lifted in its masked form —
	// the extra object must never carry a value the body already hides.
	// decoded is exclusively owned (fresh from json.Unmarshal), so it is masked
	// in place instead of paying MaskJSON's defensive deep copy.
	httplog.MaskDecodedInPlace(decoded, pre.lowerStrategies)

	if len(pre.extraWant) > 0 {
		prefix := "response_"
		if isRequest {
			prefix = "request_"
		}
		httplog.CollectExtraLower(decoded, pre.extraWant, prefix, extra)
	}

	out, err := json.Marshal(decoded)
	if err != nil {
		return httplog.CapBody(body, opts.MaxBodySize)
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
	// successEnabled / errorEnabled mirror the logger's level checks so the
	// capture decision can be made as soon as the status is known.
	successEnabled bool
	errorEnabled   bool
}

func (r *responseRecorder) WriteHeader(code int) {
	// Informational (1xx) responses may be written any number of times before
	// the final status — e.g. 103 Early Hints. Pass them through without
	// latching, so the real status still reaches both the client and the log.
	if code >= 100 && code <= 199 {
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
		if real := r.Header.Get("X-Real-IP"); real != "" {
			return real
		}
	}
	// RemoteAddr is host:port; SplitHostPort also unwraps bracketed IPv6
	// addresses ("[::1]:8080" -> "::1").
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func isFormContentType(contentType string) bool {
	return strings.Contains(strings.ToLower(contentType), "application/x-www-form-urlencoded")
}
