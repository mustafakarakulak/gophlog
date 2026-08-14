// Package httpclient provides an http.RoundTripper that automatically logs
// outbound HTTP requests/responses using github.com/mustafakarakulak/gophlog.
//
// It captures request/response bodies, duration and status, applies field
// masking, logs failures (including timeouts) and propagates the correlation ID
// downstream.
package httpclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/mustafakarakulak/gophlog"
	"github.com/mustafakarakulak/gophlog/internal/httplog"
)

// Options configures the logging transport.
type Options struct {
	// Logger to use. Defaults to gophlog.Default().
	Logger *gophlog.Logger

	// DisableRequestBody / DisableResponseBody turn off body capture, which is
	// on by default. They are phrased negatively so the zero-value Options
	// captures bodies, as documented.
	DisableRequestBody  bool
	DisableResponseBody bool

	// MaxBodySize caps captured bodies in bytes. Default: 100 KiB.
	MaxBodySize int

	// MaskFieldStrategies masks named JSON fields in request/response bodies.
	MaskFieldStrategies map[string]gophlog.MaskingStrategy

	// LogExtraFields lifts named JSON fields into the searchable `extra` object.
	LogExtraFields []string

	// SuccessLogLevel is used for 2xx/3xx. Default: INFO.
	SuccessLogLevel gophlog.Level
	// ErrorLogLevel is used for 4xx/5xx and transport errors. Default: ERROR.
	ErrorLogLevel gophlog.Level

	// EventName overrides the event name. Default: "http_client_request".
	EventName string

	// ExcludeURLs / IncludeURLs filter which requests are logged (wildcards via
	// trailing "*").
	ExcludeURLs []string
	IncludeURLs []string

	// LogCurl prints an equivalent curl command for each request. Credential
	// headers (Authorization, Cookie, X-Api-Key, ...) are redacted and the body
	// is the masked one, so the command is not runnable as-is.
	LogCurl bool

	// CurlWriter receives the curl commands when LogCurl is enabled. It defaults
	// to os.Stderr so the structured JSON log stream on stdout stays clean and
	// parseable by log collectors. Set it to io.Discard to silence, or to a
	// buffer in tests.
	CurlWriter io.Writer
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
		o.EventName = "http_client_request"
	}
	if o.CurlWriter == nil {
		o.CurlWriter = os.Stderr
	}
}

// Transport is a logging http.RoundTripper.
type Transport struct {
	Base http.RoundTripper
	opts Options

	// lowerStrategies / extraWant are derived from opts once at construction —
	// options are static after New — so the per-request masking and extra
	// collection paths never rebuild their lookups.
	lowerStrategies map[string]gophlog.MaskingStrategy
	extraWant       map[string]string
}

// New wraps base (or http.DefaultTransport) with request/response gophlog.
func New(base http.RoundTripper, opts Options) *Transport {
	opts.applyDefaults()
	if base == nil {
		base = http.DefaultTransport
	}
	return &Transport{
		Base:            base,
		opts:            opts,
		lowerStrategies: httplog.LowerStrategies(opts.MaskFieldStrategies),
		extraWant:       httplog.LowerExtraFields(opts.LogExtraFields),
	}
}

// NewClient returns an *http.Client whose transport logs requests. If client is
// nil a new client is created; otherwise its Transport is wrapped in place.
func NewClient(client *http.Client, opts Options) *http.Client {
	if client == nil {
		client = &http.Client{}
	}
	client.Transport = New(client.Transport, opts)
	return client
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	rawURL := ""
	if req.URL != nil {
		rawURL = req.URL.String()
	}

	if httplog.ShouldExclude(rawURL, t.opts.ExcludeURLs) || !httplog.ShouldInclude(rawURL, t.opts.IncludeURLs) {
		return t.Base.RoundTrip(req)
	}

	opts := t.opts
	ctx := req.Context()

	// Mask sensitive query parameters before the URL is logged.
	url := maskedURL(req.URL, t.lowerStrategies)

	// Per the http.RoundTripper contract we must not mutate the caller's
	// request; operate on a clone instead (header changes + body capture).
	outReq := req.Clone(ctx)

	// Propagate the correlation ID downstream.
	if cid := gophlog.CorrelationID(ctx); cid != "" && outReq.Header.Get(gophlog.CorrelationHeader) == "" {
		outReq.Header.Set(gophlog.CorrelationHeader, cid)
	}

	// Fast path: when no level this call could log at is enabled and no curl
	// dump is requested, skip capture and masking entirely — the entry would be
	// dropped at emit anyway. The correlation ID above still propagates.
	successEnabled := opts.Logger.Enabled(opts.SuccessLogLevel)
	errorEnabled := opts.Logger.Enabled(opts.ErrorLogLevel)
	if !successEnabled && !errorEnabled && !opts.LogCurl {
		return t.Base.RoundTrip(outReq)
	}

	var requestBody string
	if !opts.DisableRequestBody && req.Body != nil {
		captured, restored, truncated := httplog.CaptureBody(req.Body, opts.MaxBodySize)
		if !truncated {
			body := append([]byte(nil), captured...)
			outReq.GetBody = func() (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(body)), nil
			}
			requestBody = string(captured)
		} else {
			requestBody = bodyTooLarge
		}
		outReq.Body = restored
	}

	// Mask the request body once, before it can reach either the curl output or
	// the log line — the raw body may hold credentials. The extra map is only
	// needed when LogExtraFields can fill it.
	var extra map[string]any
	if len(t.extraWant) > 0 {
		extra = make(map[string]any)
	}
	maskedReq := t.processBody(requestBody, outReq.Header.Get("Content-Type"), true, extra)

	if opts.LogCurl {
		// CurlWriter is a diagnostic side channel (os.Stderr by default); a write
		// failure there must not disturb the request being proxied.
		_, _ = fmt.Fprintln(opts.CurlWriter, buildCurl(outReq, url, maskedReq, t.lowerStrategies))
	}

	method := outReq.Method
	begin := time.Now()
	resp, err := t.Base.RoundTrip(outReq)
	durationMs := float64(time.Since(begin).Microseconds()) / 1000.0

	if err != nil {
		if !errorEnabled {
			return nil, err
		}
		eventName := opts.EventName + "_exception"
		msg := httplog.Message(method, url, 0, durationMs)
		entry := opts.Logger.At(opts.ErrorLogLevel, msg, eventName).
			Ctx(ctx).
			WithHTTPResult(method, url, 0, durationMs).
			WithError(err)
		if maskedReq != "" {
			entry.WithRequestBody(maskedReq)
		}
		if len(extra) > 0 {
			entry.WithExtra(extra)
		}
		entry.Log()
		return nil, err
	}

	status := resp.StatusCode
	level := opts.SuccessLogLevel
	enabled := successEnabled
	if status >= 400 {
		level = opts.ErrorLogLevel
		enabled = errorEnabled
	}
	// The level for the actual status is filtered out (e.g. minimum level ERROR
	// and the call succeeded): hand the response back without touching its body.
	if !enabled {
		return resp, nil
	}

	var responseBody string
	if !opts.DisableResponseBody && resp.Body != nil {
		captured, restored, truncated := httplog.CaptureBody(resp.Body, opts.MaxBodySize)
		if truncated {
			responseBody = bodyTooLarge
		} else {
			responseBody = string(captured)
		}
		resp.Body = restored
	}

	maskedResp := t.processBody(responseBody, resp.Header.Get("Content-Type"), false, extra)

	msg := httplog.Message(method, url, status, durationMs)
	entry := opts.Logger.At(level, msg, opts.EventName).
		Ctx(ctx).
		WithHTTPResult(method, url, status, durationMs)
	if maskedReq != "" {
		entry.WithRequestBody(maskedReq)
	}
	if maskedResp != "" {
		entry.WithResponseBody(maskedResp)
	}
	if len(extra) > 0 {
		entry.WithExtra(extra)
	}
	entry.Log()

	return resp, nil
}

// bodyTooLarge is logged in place of a body that exceeds MaxBodySize. The full
// body is still delivered to the caller; only the logged copy is replaced, so
// masking is never bypassed by a partial, unparseable body.
const bodyTooLarge = "[body not logged: exceeds MaxBodySize]"

// maskedURL returns u as a string with sensitive query parameters masked
// (strategies is the pre-lowered map) and any userinfo password redacted. The
// original URL is never mutated.
func maskedURL(u *url.URL, strategies map[string]gophlog.MaskingStrategy) string {
	if u == nil {
		return ""
	}
	// A URL can carry basic-auth credentials in its userinfo section
	// (https://user:password@host/...); net/http turns those into an
	// Authorization header, which the curl dump already redacts — the URL
	// string must not leak the same secret. "xxxxx" mirrors url.Redacted.
	if _, hasPassword := u.User.Password(); hasPassword {
		clone := *u
		clone.User = url.UserPassword(u.User.Username(), "xxxxx")
		u = &clone
	}
	if u.RawQuery == "" || len(strategies) == 0 {
		return u.String()
	}
	q := u.Query()
	httplog.MaskQueryValuesLower(q, strategies)
	clone := *u
	clone.RawQuery = httplog.RenderQuery(q)
	return clone.String()
}

// processBody masks the FULL body and extracts extra fields, then truncates the
// masked result for logging (mask-before-truncate prevents leaks). Form-urlencoded
// bodies are masked too; any other non-JSON body is logged as-is.
//
// The body is decoded exactly once; the final Marshal both compacts and
// re-serializes it, so no separate formatting pass is needed.
func (t *Transport) processBody(body, contentType string, isRequest bool, extra map[string]any) string {
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
	if len(t.lowerStrategies) == 0 && len(t.extraWant) == 0 {
		return httplog.CapBody(body, t.opts.MaxBodySize)
	}
	var decoded any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		if isFormContentType(contentType) {
			if masked, ok := httplog.MaskFormBodyLower(body, t.lowerStrategies); ok {
				return httplog.CapBody(masked, t.opts.MaxBodySize)
			}
		}
		return httplog.CapBody(body, t.opts.MaxBodySize)
	}
	// Masking runs BEFORE extra extraction, so a field named in both
	// MaskFieldStrategies and LogExtraFields is lifted in its masked form —
	// the extra object must never carry a value the body already hides.
	// decoded is exclusively owned (fresh from json.Unmarshal), so it is masked
	// in place instead of paying MaskJSON's defensive deep copy.
	httplog.MaskDecodedInPlace(decoded, t.lowerStrategies)
	if len(t.extraWant) > 0 {
		prefix := "response_"
		if isRequest {
			prefix = "request_"
		}
		httplog.CollectExtraLower(decoded, t.extraWant, prefix, extra)
	}
	out, err := json.Marshal(decoded)
	if err != nil {
		return httplog.CapBody(body, t.opts.MaxBodySize)
	}
	return httplog.CapBody(string(out), t.opts.MaxBodySize)
}

func isFormContentType(contentType string) bool {
	return strings.Contains(strings.ToLower(contentType), "application/x-www-form-urlencoded")
}

// redactedHeaders are always hidden in curl output: they carry credentials, and
// a curl command is copy-pasted and pasted into tickets far more often than a log
// line is. Names are lower-case for case-insensitive matching.
var redactedHeaders = map[string]struct{}{
	"authorization":       {},
	"proxy-authorization": {},
	"cookie":              {},
	"set-cookie":          {},
	"x-api-key":           {},
	"api-key":             {},
	"x-auth-token":        {},
	"x-access-token":      {},
	"x-session-token":     {},
}

// headerValueForCurl renders a header value for curl output, hiding credential
// headers outright and masking any other header the caller named in
// MaskFieldStrategies (strategies is the pre-lowered map).
func headerValueForCurl(name, value string, strategies map[string]gophlog.MaskingStrategy) string {
	lower := strings.ToLower(name)
	if _, secret := redactedHeaders[lower]; secret {
		return "[REDACTED]"
	}
	if strategy, ok := strategies[lower]; ok {
		return gophlog.MaskString(value, strategy)
	}
	return value
}

// buildCurl renders an equivalent curl command for req. Credential headers are
// redacted, so the output is not runnable as-is against an authenticated
// endpoint — that is deliberate. maskedURL is the pre-masked URL string (never
// req.URL, whose raw query may hold secrets named in MaskFieldStrategies);
// strategies is the pre-lowered map.
func buildCurl(req *http.Request, maskedURL, body string, strategies map[string]gophlog.MaskingStrategy) string {
	var b strings.Builder
	fmt.Fprintf(&b, "curl -X %s '%s'", req.Method, maskedURL)

	// Sorted so the same request always renders the same command.
	names := make([]string, 0, len(req.Header))
	for key := range req.Header {
		names = append(names, key)
	}
	sort.Strings(names)
	for _, key := range names {
		for _, v := range req.Header[key] {
			fmt.Fprintf(&b, " \\\n  -H '%s: %s'", key, headerValueForCurl(key, v, strategies))
		}
	}

	if body != "" {
		escaped := strings.ReplaceAll(body, "'", `'\''`)
		fmt.Fprintf(&b, " \\\n  --data '%s'", escaped)
	}
	return b.String()
}
