package middleware

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mustafakarakulak/gophlog"
)

// Markers the middleware logs in place of a body it deliberately leaves alone.
const (
	streamingMarker = "[body not logged: streaming]"
	encodedMarker   = "[body not logged: non-identity Content-Encoding]"
	unmaskedMarker  = "[body not logged: cannot be masked]"
)

var maskPassword = map[string]gophlog.MaskingStrategy{"password": gophlog.HideAll}

// servePanicking runs h and returns the value it panicked with (nil if none).
func servePanicking(h http.Handler, req *http.Request) (recovered any) {
	defer func() { recovered = recover() }()
	h.ServeHTTP(httptest.NewRecorder(), req)
	return nil
}

// TestMiddlewareLogsHandlerPanic: a panicking handler used to leave no log line
// at all. It is now logged as a 500 carrying the panic, and the panic carries
// on with the same value.
func TestMiddlewareLogsHandlerPanic(t *testing.T) {
	var buf bytes.Buffer
	mw := New(Options{Logger: gophlog.New(gophlog.WithWriter(&buf)), MaskFieldStrategies: maskPassword})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"partial":`))
		panic("boom")
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/pay", strings.NewReader(`{"password":"hunter2"}`))
	if got := servePanicking(handler, req); got != "boom" {
		t.Fatalf("recovered %v; want the handler's own panic value", got)
	}

	m := parseLine(t, &buf)
	if int(m["http_status"].(float64)) != http.StatusInternalServerError || m["level"] != "ERROR" {
		t.Errorf("http_status = %v, level = %v; want 500 at ERROR", m["http_status"], m["level"])
	}
	if m["error_message"] != "panic: boom" {
		t.Errorf("error_message = %v", m["error_message"])
	}
	if st, _ := m["stack_trace"].(string); !strings.Contains(st, "TestMiddlewareLogsHandlerPanic") {
		t.Errorf("stack_trace should point at the panicking handler:\n%s", st)
	}
	if rb, _ := m["request_body"].(string); rb == "" || strings.Contains(rb, "hunter2") {
		t.Errorf("request_body = %q; want it logged and masked", rb)
	}
	// The response was cut off mid-write; the fragment is not logged.
	if _, present := m["response_body"]; present {
		t.Errorf("response_body = %v; want none for an aborted response", m["response_body"])
	}
}

// TestMiddlewarePanicErrAbortHandler: http.ErrAbortHandler is net/http's
// signal to abort a response. It is logged (the request did happen) without a
// stack trace, and re-raised unchanged so net/http still recognises it.
func TestMiddlewarePanicErrAbortHandler(t *testing.T) {
	var buf bytes.Buffer
	handler := New(Options{Logger: gophlog.New(gophlog.WithWriter(&buf))})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { panic(http.ErrAbortHandler) }))

	got := servePanicking(handler, httptest.NewRequest(http.MethodGet, "/api/proxy", nil))
	if got != http.ErrAbortHandler {
		t.Fatalf("recovered %v; want http.ErrAbortHandler itself", got)
	}
	m := parseLine(t, &buf)
	if int(m["http_status"].(float64)) != http.StatusInternalServerError {
		t.Errorf("http_status = %v; want 500", m["http_status"])
	}
	if msg, _ := m["error_message"].(string); !strings.Contains(msg, http.ErrAbortHandler.Error()) {
		t.Errorf("error_message = %q", msg)
	}
	if _, present := m["stack_trace"]; present {
		t.Errorf("ErrAbortHandler should be logged without a stack trace")
	}
}

// TestMiddlewarePanicWhileLogging: a panic raised while logging never replaces
// the handler's panic, and a panic while logging a completed request is not
// reported as a handler panic.
func TestMiddlewarePanicWhileLogging(t *testing.T) {
	var buf bytes.Buffer
	mw := New(Options{
		Logger:        gophlog.New(gophlog.WithWriter(&buf)),
		ExtraProvider: func(*http.Request) map[string]string { panic("extra provider") },
	})

	crashing := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { panic("handler") }))
	if got := servePanicking(crashing, httptest.NewRequest(http.MethodGet, "/api/x", nil)); got != "handler" {
		t.Errorf("recovered %v; want the handler's panic", got)
	}

	buf.Reset()
	fine := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	if got := servePanicking(fine, httptest.NewRequest(http.MethodGet, "/api/x", nil)); got != "extra provider" {
		t.Errorf("recovered %v; want the logging panic to propagate as before", got)
	}
	if buf.Len() != 0 {
		t.Errorf("a panic while logging must not produce a handler-panic line: %s", buf.String())
	}
}

// TestMiddlewarePanicWithErrorValue: an error panic value stays in the chain.
func TestMiddlewarePanicWithErrorValue(t *testing.T) {
	sentinel := errors.New("db down")
	if err := panicError(sentinel); !errors.Is(err, sentinel) || err.Error() != "panic: db down" {
		t.Errorf("panicError(%v) = %v", sentinel, err)
	}
}

func storedGzip(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw, err := gzip.NewWriterLevel(&b, gzip.NoCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zw.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// TestMiddlewareEncodedBodiesNotLogged: an encoded body is never logged raw. A
// short gzip body is a stored block that carries the plain text, which used to
// bypass masking.
func TestMiddlewareEncodedBodiesNotLogged(t *testing.T) {
	reqGz := storedGzip(t, `{"password":"hunter2"}`)
	respGz := storedGzip(t, `{"password":"s3cret"}`)

	var buf bytes.Buffer
	handler := New(Options{Logger: gophlog.New(gophlog.WithWriter(&buf)), MaskFieldStrategies: maskPassword})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got, _ := io.ReadAll(r.Body)
			if !bytes.Equal(got, reqGz) {
				t.Errorf("handler received %q; want the gzip body untouched", got)
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Encoding", "gzip")
			_, _ = w.Write(respGz)
		}))

	req := httptest.NewRequest(http.MethodPost, "/api/x", bytes.NewReader(reqGz))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !bytes.Equal(rec.Body.Bytes(), respGz) {
		t.Errorf("client received %q; want the gzip body untouched", rec.Body.Bytes())
	}
	for _, secret := range []string{"hunter2", "s3cret"} {
		if strings.Contains(buf.String(), secret) {
			t.Errorf("log leaked %q from an encoded body:\n%s", secret, buf.String())
		}
	}
	m := parseLine(t, &buf)
	if m["request_body"] != encodedMarker || m["response_body"] != encodedMarker {
		t.Errorf("request_body = %v, response_body = %v; want %q", m["request_body"], m["response_body"], encodedMarker)
	}
}

// TestMiddlewareMalformedFormNotLogged: r.ParseForm still hands the handler the
// password out of a body url.ParseQuery rejects, so such a body is replaced by
// a marker instead of being logged raw.
func TestMiddlewareMalformedFormNotLogged(t *testing.T) {
	for _, body := range []string{"password=hunter2&x=%zz", "user=a;b&password=hunter2", `["&password=hunter2"]`} {
		t.Run(body, func(t *testing.T) {
			var buf bytes.Buffer
			handler := New(Options{Logger: gophlog.New(gophlog.WithWriter(&buf)), MaskFieldStrategies: maskPassword})(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_ = r.ParseForm()
					if !strings.HasPrefix(r.PostForm.Get("password"), "hunter2") {
						t.Errorf("handler should still read the password, got %q", r.PostForm.Get("password"))
					}
				}))
			req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			handler.ServeHTTP(httptest.NewRecorder(), req)

			if strings.Contains(buf.String(), "hunter2") {
				t.Errorf("malformed form leaked the password:\n%s", buf.String())
			}
			if got := parseLine(t, &buf)["request_body"]; got != unmaskedMarker {
				t.Errorf("request_body = %v; want %q", got, unmaskedMarker)
			}
		})
	}
}

// onlyUnderMaskedKey reports whether secret appears in the form view only as
// the value of a masked key; anywhere else it is legitimately loggable.
func onlyUnderMaskedKey(view url.Values, secret string) bool {
	for key, vals := range view {
		if strings.Contains(key, secret) {
			return false
		}
		if _, masked := maskPassword[strings.ToLower(key)]; masked {
			continue
		}
		for _, v := range vals {
			if strings.Contains(v, secret) {
				return false
			}
		}
	}
	return true
}

// FuzzMiddlewareBody: whatever surrounds it, a value the handler reads through
// r.ParseForm under a masked key must never reach the log line raw.
func FuzzMiddlewareBody(f *testing.F) {
	for _, seed := range [][2]string{
		{"user=a", "x=1"},
		{"user=a", "x=%zz"},
		{"user=a;b", "x=1"},
		{`["`, `"]`},
		{`{"a":"`, `"}`},
		{"", ""},
		{"note=100%", ""},
	} {
		f.Add(seed[0], seed[1], uint8(0), uint8(0))
	}
	keys := []string{"password", "PASSWORD", "Password", "pass%77ord", "%70assword"}
	contentTypes := []string{
		"application/x-www-form-urlencoded",
		"application/x-www-form-urlencoded; charset=utf-8",
		"Application/X-WWW-Form-Urlencoded",
	}
	logger := func(buf *bytes.Buffer) *gophlog.Logger { return gophlog.New(gophlog.WithWriter(buf)) }

	f.Fuzz(func(t *testing.T, pre, post string, keySel, ctSel uint8) {
		const secret = "S3CR3TVALUEZZZ"
		body := pre + "&" + keys[int(keySel)%len(keys)] + "=" + secret + "&" + post

		var buf bytes.Buffer
		var view url.Values
		handler := New(Options{Logger: logger(&buf), MaskFieldStrategies: maskPassword})(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = r.ParseForm() // the view keeps whatever pairs parsed
				view = r.PostForm
			}))
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(body))
		req.Header.Set("Content-Type", contentTypes[int(ctSel)%len(contentTypes)])
		handler.ServeHTTP(httptest.NewRecorder(), req)

		if !onlyUnderMaskedKey(view, secret) {
			return // pre/post put the token somewhere it is not masked
		}
		var m map[string]any
		if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
			t.Fatalf("invalid log line %q: %v", buf.String(), err)
		}
		if rb, _ := m["request_body"].(string); strings.Contains(rb, secret) {
			t.Fatalf("masked value leaked for body %q:\n%s", body, rb)
		}
	})
}

// TestMiddlewareKeepsBigNumbers: masking no longer routes numbers through
// float64, which rounded 12345678901234567891 to 12345678901234567000.
func TestMiddlewareKeepsBigNumbers(t *testing.T) {
	var buf bytes.Buffer
	handler := New(Options{Logger: gophlog.New(gophlog.WithWriter(&buf)), MaskFieldStrategies: maskPassword})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":98765432109876543210,"rate":0.1000}`))
		}))
	req := httptest.NewRequest(http.MethodPost, "/api/x", strings.NewReader(`{"order_id":12345678901234567891,"password":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	m := parseLine(t, &buf)
	if rb, _ := m["request_body"].(string); !strings.Contains(rb, `"order_id":12345678901234567891`) {
		t.Errorf("request_body = %q; want the exact number", rb)
	}
	if rb, _ := m["response_body"].(string); rb != `{"id":98765432109876543210,"rate":0.1000}` {
		t.Errorf("response_body = %q; want the exact numbers", rb)
	}
}

// TestMiddlewareBodyKeepsHTMLLiteral: a masked body is rendered like the log
// line around it, with <, > and & literal rather than \u003c-escaped.
func TestMiddlewareBodyKeepsHTMLLiteral(t *testing.T) {
	var buf bytes.Buffer
	handler := New(Options{Logger: gophlog.New(gophlog.WithWriter(&buf)), MaskFieldStrategies: maskPassword})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.ReadAll(r.Body) }))
	req := httptest.NewRequest(http.MethodPost, "/api/x", strings.NewReader(`{"q":"a<b&c>d","password":"x"}`))
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if strings.Contains(buf.String(), `\\u003c`) || strings.Contains(buf.String(), `\\u0026`) {
		t.Errorf("body was HTML-escaped:\n%s", buf.String())
	}
	if rb, _ := parseLine(t, &buf)["request_body"].(string); !strings.Contains(rb, `"q":"a<b&c>d"`) {
		t.Errorf("request_body = %q", rb)
	}
}

// TestMiddlewareExcludedPathKeepsContext: an excluded path is not logged, but
// its handler still gets the correlation ID, workflow IDs and client IP.
func TestMiddlewareExcludedPathKeepsContext(t *testing.T) {
	var buf bytes.Buffer
	for name, opts := range map[string]Options{
		"excluded":     {ExcludePaths: []string{"/health"}},
		"not included": {IncludePaths: []string{"/api/*"}},
	} {
		buf.Reset()
		opts.Logger = gophlog.New(gophlog.WithWriter(&buf))
		var ctxCID, ctxIP string
		handler := New(opts)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctxCID = gophlog.CorrelationID(r.Context())
			// Log from inside the handler to observe the workflow IDs and
			// client IP the middleware put into the context.
			var inner bytes.Buffer
			gophlog.New(gophlog.WithWriter(&inner)).Info("inside", "probe").Ctx(r.Context()).Log()
			var m map[string]any
			_ = json.Unmarshal(inner.Bytes(), &m)
			ctxIP, _ = m["client_ip"].(string)
			if m["run_id"] != "run-1" {
				t.Errorf("%s: run_id in context = %v; want run-1", name, m["run_id"])
			}
		}))
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		req.Header.Set(gophlog.CorrelationHeader, "cid-health")
		req.Header.Set(gophlog.HeaderRunID, "run-1")
		req.RemoteAddr = "192.0.2.9:1234"
		handler.ServeHTTP(httptest.NewRecorder(), req)

		if ctxCID != "cid-health" || ctxIP != "192.0.2.9" {
			t.Errorf("%s: context correlation ID = %q, client IP = %q", name, ctxCID, ctxIP)
		}
		if buf.Len() != 0 {
			t.Errorf("%s: skipped path was logged: %s", name, buf.String())
		}
	}
}

// serveWorkflowHeaders sends one request carrying value in all three workflow
// headers and returns the log line.
func serveWorkflowHeaders(t *testing.T, value string) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	handler := New(Options{Logger: gophlog.New(gophlog.WithWriter(&buf))})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	for _, h := range []string{gophlog.HeaderChildWorkflowID, gophlog.HeaderRunID, gophlog.HeaderParentWorkflowID} {
		req.Header[h] = []string{value} // set raw, bypassing any client-side sanitising
	}
	handler.ServeHTTP(httptest.NewRecorder(), req)
	return parseLine(t, &buf)
}

var workflowFields = []string{"child_workflow_id", "run_id", "parent_workflow_id"}

// TestMiddlewareAcceptsWorkflowHeaders: workflow IDs are application-defined,
// so '/', '@', spaces and non-ASCII text stay accepted as in v1.1.0.
func TestMiddlewareAcceptsWorkflowHeaders(t *testing.T) {
	for _, value := range []string{
		"orders/123", "user@tenant", "wf id with space", `{"k":"v"}`, "sipariş-42",
		"019baa68-80eb-7b0f-b2df-8e5a9c3e22e5", strings.Repeat("w", 1000),
	} {
		m := serveWorkflowHeaders(t, value)
		for _, field := range workflowFields {
			if m[field] != value {
				t.Errorf("%s = %v; want %q kept", field, m[field], value)
			}
		}
	}
}

// TestMiddlewareDropsInvalidWorkflowHeaders: a value that could bloat, forge or
// disguise a log line is dropped from both the context and the audit log.
func TestMiddlewareDropsInvalidWorkflowHeaders(t *testing.T) {
	for name, value := range map[string]string{
		"newline":             "wf-1\nlevel=ERROR",
		"escape":              "wf-1\x1b[31mred",
		"tab":                 "wf\t1",
		"too long":            strings.Repeat("w", 1001),
		"invalid UTF-8":       "wf-\xff\xfe",
		"bidi override":       "wf-\u202egnp.exe",
		"bidi isolate":        "wf-\u2066x\u2069",
		"paragraph separator": "wf-1\u2029forged",
	} {
		m := serveWorkflowHeaders(t, value)
		for _, field := range workflowFields {
			if v, present := m[field]; present {
				t.Errorf("%s: %s = %q; want the value dropped", name, field, v)
			}
		}
	}
}

// TestMiddlewareFlushLatchesStatus: a Flush commits the implicit 200, so a
// later WriteHeader(500) reaches neither the client nor the log.
func TestMiddlewareFlushLatchesStatus(t *testing.T) {
	var buf bytes.Buffer
	handler := New(Options{Logger: gophlog.New(gophlog.WithWriter(&buf))})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.(http.Flusher).Flush()
			w.WriteHeader(http.StatusInternalServerError)
		}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/x", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("client status = %d; want 200", rec.Code)
	}
	if got := int(parseLine(t, &buf)["http_status"].(float64)); got != http.StatusOK {
		t.Errorf("http_status = %d; want the 200 the client received", got)
	}
}

// TestMiddlewareBytesInChunked: for a chunked body (no Content-Length) bytes_in
// counts what was actually read, not the captured prefix capped at MaxBodySize.
func TestMiddlewareBytesInChunked(t *testing.T) {
	for _, disable := range []bool{false, true} {
		var buf bytes.Buffer
		handler := New(Options{Logger: gophlog.New(gophlog.WithWriter(&buf)), MaxBodySize: 10, DisableRequestBody: disable})(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.ReadAll(r.Body) }))
		req := httptest.NewRequest(http.MethodPost, "/api/x", io.NopCloser(strings.NewReader(strings.Repeat("x", 100))))
		req.ContentLength = -1
		handler.ServeHTTP(httptest.NewRecorder(), req)

		if got := int64(parseLine(t, &buf)["bytes_in"].(float64)); got != 100 {
			t.Errorf("DisableRequestBody=%v: bytes_in = %d; want 100", disable, got)
		}
	}
}

// TestMiddlewareStreamingResponse: a streaming response is not buffered (SSE
// event data would otherwise be logged raw), and a 101 is logged as the final
// status net/http treats it as.
func TestMiddlewareStreamingResponse(t *testing.T) {
	var buf bytes.Buffer
	mw := New(Options{Logger: gophlog.New(gophlog.WithWriter(&buf)), MaskFieldStrategies: maskPassword})

	sse := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"password\":\"hunter2\"}\n\n")
	}))
	rec := httptest.NewRecorder()
	sse.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/events", nil))
	if !strings.Contains(rec.Body.String(), "hunter2") {
		t.Fatalf("client should receive the stream untouched, got %q", rec.Body.String())
	}
	if m := parseLine(t, &buf); m["response_body"] != streamingMarker {
		t.Errorf("SSE response_body = %v; want %q", m["response_body"], streamingMarker)
	}

	buf.Reset()
	upgrade := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Upgrade", "echo")
		w.WriteHeader(http.StatusSwitchingProtocols)
	}))
	upgrade.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/ws", nil))
	m := parseLine(t, &buf)
	if got := int(m["http_status"].(float64)); got != http.StatusSwitchingProtocols {
		t.Errorf("http_status = %d; want 101", got)
	}
	if m["response_body"] != streamingMarker {
		t.Errorf("101 response_body = %v; want %q", m["response_body"], streamingMarker)
	}
}

// TestMiddlewareStreamingRequestNotBuffered: a client-streaming gRPC body stays
// open while the client waits for the response, so the handler must start
// without the middleware reading it first.
func TestMiddlewareStreamingRequestNotBuffered(t *testing.T) {
	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()
	go func() { _, _ = pw.Write([]byte("\x00\x00\x00\x00\x01A")) }() // one frame, then idle

	var buf bytes.Buffer
	handler := New(Options{Logger: gophlog.New(gophlog.WithWriter(&buf))})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			frame := make([]byte, 6)
			_, _ = io.ReadFull(r.Body, frame)
			w.Header().Set("Content-Type", "application/grpc")
		}))
	req := httptest.NewRequest(http.MethodPost, "/svc/Stream", pr)
	req.Header.Set("Content-Type", "application/grpc")
	req.ContentLength = -1

	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(httptest.NewRecorder(), req)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start: the middleware read the open request stream first")
	}
	m := parseLine(t, &buf)
	if m["request_body"] != streamingMarker {
		t.Errorf("request_body = %v; want %q", m["request_body"], streamingMarker)
	}
	if got := int64(m["bytes_in"].(float64)); got != 6 {
		t.Errorf("bytes_in = %d; want the 6 bytes the handler read", got)
	}
}
