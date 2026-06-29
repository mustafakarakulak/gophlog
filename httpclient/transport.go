// Package httpclient provides an http.RoundTripper that automatically logs
// outbound HTTP requests/responses using github.com/mustafakarakulak/go-logging.
//
// It is the Go equivalent of the .NET HttpClientLoggingHandler: it captures
// request/response bodies, duration and status, applies field masking, logs
// failures (including timeouts) and propagates the correlation ID downstream.
package httpclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	logging "github.com/mustafakarakulak/go-logging"
	"github.com/mustafakarakulak/go-logging/internal/httplog"
)

// Options configures the logging transport.
type Options struct {
	// Logger to use. Defaults to logging.Default().
	Logger *logging.Logger

	// LogRequestBody / LogResponseBody toggle body capture. Default: true.
	LogRequestBody  bool
	LogResponseBody bool

	// MaxBodySize caps captured bodies in bytes. Default: 100 KiB.
	MaxBodySize int

	// MaskFieldStrategies masks named JSON fields in request/response bodies.
	MaskFieldStrategies map[string]logging.MaskingStrategy

	// LogExtraFields lifts named JSON fields into the searchable `extra` object.
	LogExtraFields []string

	// SuccessLogLevel is used for 2xx/3xx. Default: INFO.
	SuccessLogLevel logging.Level
	// ErrorLogLevel is used for 4xx/5xx and transport errors. Default: ERROR.
	ErrorLogLevel logging.Level

	// EventName overrides the event name. Default: "http_client_request".
	EventName string

	// ExcludeURLs / IncludeURLs filter which requests are logged (wildcards via
	// trailing "*").
	ExcludeURLs []string
	IncludeURLs []string

	// LogCurl prints an equivalent curl command for each request.
	LogCurl bool

	// CurlWriter receives the curl commands when LogCurl is enabled. It defaults
	// to os.Stderr so the structured JSON log stream on stdout stays clean and
	// parseable by log collectors. Set it to io.Discard to silence, or to a
	// buffer in tests.
	CurlWriter io.Writer
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
}

// New wraps base (or http.DefaultTransport) with request/response logging.
func New(base http.RoundTripper, opts Options) *Transport {
	opts.applyDefaults()
	if base == nil {
		base = http.DefaultTransport
	}
	return &Transport{Base: base, opts: opts}
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
	url := ""
	if req.URL != nil {
		url = req.URL.String()
	}

	if httplog.ShouldExclude(url, t.opts.ExcludeURLs) || !httplog.ShouldInclude(url, t.opts.IncludeURLs) {
		return t.Base.RoundTrip(req)
	}

	opts := t.opts
	ctx := req.Context()

	// Per the http.RoundTripper contract we must not mutate the caller's
	// request; operate on a clone instead (header changes + body capture).
	outReq := req.Clone(ctx)

	// Propagate the correlation ID downstream.
	if cid := logging.CorrelationID(ctx); cid != "" && outReq.Header.Get(logging.CorrelationHeader) == "" {
		outReq.Header.Set(logging.CorrelationHeader, cid)
	}

	var requestBody string
	if opts.LogRequestBody && req.Body != nil {
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

	if opts.LogCurl {
		fmt.Fprintln(opts.CurlWriter, buildCurl(outReq, requestBody))
	}

	method := outReq.Method
	begin := time.Now()
	resp, err := t.Base.RoundTrip(outReq)
	durationMs := float64(time.Since(begin).Microseconds()) / 1000.0

	if err != nil {
		extra := map[string]any{}
		maskedReq := processBody(requestBody, opts, true, extra)
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

	var responseBody string
	if opts.LogResponseBody && resp.Body != nil {
		captured, restored, truncated := httplog.CaptureBody(resp.Body, opts.MaxBodySize)
		if truncated {
			responseBody = bodyTooLarge
		} else {
			responseBody = string(captured)
		}
		resp.Body = restored
	}

	status := resp.StatusCode
	level := opts.SuccessLogLevel
	if status >= 400 {
		level = opts.ErrorLogLevel
	}

	extra := map[string]any{}
	maskedReq := processBody(requestBody, opts, true, extra)
	maskedResp := processBody(responseBody, opts, false, extra)

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

// processBody masks the FULL body and extracts extra fields, then truncates the
// masked result for logging (mask-before-truncate prevents leaks).
func processBody(body string, opts Options, isRequest bool, extra map[string]any) string {
	if body == "" {
		return ""
	}
	formatted := httplog.FormatJSON(body)
	var decoded any
	if err := json.Unmarshal([]byte(formatted), &decoded); err != nil {
		return httplog.CapBody(formatted, opts.MaxBodySize)
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

func buildCurl(req *http.Request, body string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "curl -X %s '%s'", req.Method, req.URL)
	for key, values := range req.Header {
		for _, v := range values {
			fmt.Fprintf(&b, " \\\n  -H '%s: %s'", key, v)
		}
	}
	if body != "" {
		escaped := strings.ReplaceAll(body, "'", `'\''`)
		fmt.Fprintf(&b, " \\\n  --data '%s'", escaped)
	}
	return b.String()
}
