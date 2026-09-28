// Package httpclient provides an http.RoundTripper that automatically logs
// outbound HTTP requests/responses using github.com/mustafakarakulak/gophlog.
//
// It captures request/response bodies, duration and status, applies field
// masking, logs failures (including timeouts) and propagates the correlation ID
// downstream.
//
// Wherever the URL is logged (http_path, the message, the curl dump), its
// userinfo is redacted in full (user:pass@ becomes xxxxx:xxxxx@, a bare token
// xxxxx@) and its fragment is dropped.
package httpclient

import (
	"bytes"
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
	//
	// Some bodies are never read for the log and are logged as a marker
	// instead: "[body not logged: non-identity Content-Encoding]" for a
	// Content-Encoding other than identity, and "[body not logged: streaming]"
	// for Server-Sent Events, gRPC, Connect streaming, stream=watch and
	// 101 Switching Protocols responses, whose body is returned untouched.
	DisableRequestBody  bool
	DisableResponseBody bool

	// MaxBodySize caps captured bodies in bytes; a larger body is logged as
	// "[body not logged: exceeds MaxBodySize]". Default: 100 KiB.
	MaxBodySize int

	// MaskFieldStrategies masks named fields in JSON and form-urlencoded
	// request/response bodies and in query parameters, and headers of the
	// same name in the curl dump. With it set, a form body that cannot be
	// masked unambiguously is logged as "[body not logged: cannot be masked]".
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
	// trailing "*"). A request that is not logged still carries the correlation
	// ID downstream.
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

// New wraps base (or http.DefaultTransport) with request/response logging.
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
		// Not logged, but the correlation ID still propagates — as on the
		// level-gated fast path below, and as the middleware keeps it in the
		// context of an excluded path.
		return t.Base.RoundTrip(propagateCorrelation(req))
	}

	opts := t.opts
	ctx := req.Context()

	// Mask sensitive query parameters before the URL is logged.
	url := maskedURL(req.URL, t.lowerStrategies)

	// Per the http.RoundTripper contract we must not mutate the caller's
	// request; operate on a clone instead (header changes + body capture).
	outReq := req.Clone(ctx)

	// Propagate the correlation ID downstream.
	setCorrelationHeader(outReq)

	// Fast path: when no level this call could log at is enabled and no curl
	// dump is requested, skip capture and masking entirely — the entry would be
	// dropped at emit anyway. The correlation ID above still propagates.
	successEnabled := opts.Logger.Enabled(opts.SuccessLogLevel)
	errorEnabled := opts.Logger.Enabled(opts.ErrorLogLevel)
	if !successEnabled && !errorEnabled && !opts.LogCurl {
		return t.Base.RoundTrip(outReq)
	}

	var requestBody string
	if !opts.DisableRequestBody && req.Body != nil && req.Body != http.NoBody {
		switch {
		case !httplog.IsIdentityEncoding(outReq.Header):
			// Compressed bytes cannot be masked; never log them raw.
			requestBody = httplog.BodyEncoded
		case httplog.IsStreamingContentType(outReq.Header.Get("Content-Type")):
			// A client stream stays open while it waits for the response;
			// reading it up front would stall the call indefinitely.
			requestBody = httplog.BodyStreaming
		default:
			requestBody = captureRequestBody(req, outReq, opts.MaxBodySize)
		}
	}

	// Mask the request body once, before it can reach either the curl output or
	// the log line — the raw body may hold credentials. The extra map is only
	// needed when LogExtraFields can fill it.
	var extra map[string]any
	if len(t.extraWant) > 0 {
		extra = make(map[string]any)
	}
	maskedReq := httplog.ProcessBody(requestBody, outReq.Header.Get("Content-Type"),
		t.lowerStrategies, t.extraWant, opts.MaxBodySize, "request_", extra)

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
	if !opts.DisableResponseBody && resp.Body != nil && resp.Body != http.NoBody {
		switch {
		case status == http.StatusSwitchingProtocols || httplog.IsStreamingContentType(resp.Header.Get("Content-Type")):
			// Left exactly as the transport returned it: reading a stream up
			// front blocks until it ends (never, for an idle SSE or gRPC
			// stream), and a 101 body is the io.ReadWriteCloser of the
			// upgraded connection, which a replacement reader would break.
			responseBody = httplog.BodyStreaming
		case !httplog.IsIdentityEncoding(resp.Header):
			// The transport only decompresses transparently when it asked for
			// gzip itself; a caller-set Accept-Encoding leaves the body
			// encoded, and encoded bytes cannot be masked.
			responseBody = httplog.BodyEncoded
		default:
			captured, restored, truncated := httplog.CaptureBody(resp.Body, opts.MaxBodySize)
			if truncated {
				responseBody = httplog.BodyTooLarge
			} else {
				responseBody = string(captured)
			}
			resp.Body = restored
		}
	}

	maskedResp := httplog.ProcessBody(responseBody, resp.Header.Get("Content-Type"),
		t.lowerStrategies, t.extraWant, opts.MaxBodySize, "response_", extra)

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

// maskedURL returns u as a string for the log line and the curl dump: userinfo
// redacted, the fragment dropped and sensitive query parameters masked
// (strategies is the pre-lowered map). The original URL is never mutated.
func maskedURL(u *url.URL, strategies map[string]gophlog.MaskingStrategy) string {
	if u == nil {
		return ""
	}
	clone := *u
	// A URL can carry credentials in its userinfo section: a basic-auth
	// password, and just as often a token in the username slot
	// (https://<token>@github.com/..., https://<token>:x-oauth-basic@...).
	// net/http turns userinfo into an Authorization header, which the curl dump
	// already redacts — the URL string must not leak the same secret, so both
	// parts are redacted. "xxxxx" mirrors url.Redacted.
	if _, hasPassword := u.User.Password(); hasPassword {
		clone.User = url.UserPassword("xxxxx", "xxxxx")
	} else if u.User.Username() != "" {
		clone.User = url.User("xxxxx")
	}
	// The fragment never goes on the wire (net/http does not send it), but
	// OAuth implicit-flow redirects carry access tokens there: drop it, so the
	// log shows exactly what was requested.
	clone.Fragment, clone.RawFragment = "", ""
	if clone.RawQuery != "" && len(strategies) > 0 {
		q := clone.Query()
		httplog.MaskQueryValuesLower(q, strategies)
		clone.RawQuery = httplog.RenderQuery(q)
	}
	return clone.String()
}

// captureRequestBody captures req's body for the log line and installs the
// replacement on outReq (the clone that goes on the wire), returning what the
// log line carries.
func captureRequestBody(req, outReq *http.Request, limit int) string {
	captured, restored, truncated := httplog.CaptureBody(req.Body, limit)
	outReq.Body = restored
	switch {
	case truncated:
		return httplog.BodyTooLarge
	case captured == nil:
		// The read failed: restored replays the error to the transport. The
		// caller's GetBody stays, since a replay of the partial read would
		// silently resend a truncated body.
		return ""
	case len(captured) == 0 && outReq.ContentLength == 0:
		// Empty after all. As http.NoBody the request keeps its
		// "Content-Length: 0"; any other empty reader with an undeclared
		// length would go out chunked, which some servers reject with 411.
		outReq.Body = http.NoBody
		outReq.GetBody = func() (io.ReadCloser, error) { return http.NoBody, nil }
		return ""
	}
	body := append([]byte(nil), captured...)
	outReq.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	return string(captured)
}

// propagateCorrelation returns req, or — when its context holds a correlation
// ID the caller has not set as a header — a clone carrying it. The caller's
// request is never mutated (RoundTripper contract), and a request with nothing
// to add is not cloned.
func propagateCorrelation(req *http.Request) *http.Request {
	if gophlog.CorrelationID(req.Context()) == "" || req.Header.Get(gophlog.CorrelationHeader) != "" {
		return req
	}
	out := req.Clone(req.Context())
	setCorrelationHeader(out)
	return out
}

// setCorrelationHeader sets the correlation header on req — a clone the
// transport owns — from its context, unless the caller already set one.
func setCorrelationHeader(req *http.Request) {
	cid := gophlog.CorrelationID(req.Context())
	if cid == "" || req.Header.Get(gophlog.CorrelationHeader) != "" {
		return
	}
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	req.Header.Set(gophlog.CorrelationHeader, cid)
}

// headerValueForCurl renders a header value for curl output, hiding credential
// headers outright and masking any other header the caller named in
// MaskFieldStrategies (strategies is the pre-lowered map). Credential headers
// are always hidden: a curl command is copy-pasted into tickets far more often
// than a log line is.
func headerValueForCurl(name, value string, strategies map[string]gophlog.MaskingStrategy) string {
	if httplog.IsCredentialHeader(name) {
		return "[REDACTED]"
	}
	lower := strings.ToLower(name)
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
