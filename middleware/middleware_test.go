package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mustafakarakulak/gophlog"
)

func parseLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	line := strings.TrimSpace(buf.String())
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("invalid JSON %q: %v", line, err)
	}
	return m
}

func TestMiddlewareLogsRequest(t *testing.T) {
	var buf bytes.Buffer
	log := gophlog.New(gophlog.WithWriter(&buf))

	mw := New(Options{
		Logger:              log,
		MaskFieldStrategies: map[string]gophlog.MaskingStrategy{"password": gophlog.HideAll},
		EventName:           "http_request",
	})

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "secret") {
			t.Error("handler should still receive the original request body")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"created"}`))
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/invoices?page=1", strings.NewReader(`{"password":"secret","amount":100}`))
	req.Header.Set("X-Forwarded-For", "10.0.1.12, proxy")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d", rec.Code)
	}

	m := parseLine(t, &buf)
	if m["http_method"] != "POST" {
		t.Errorf("http_method = %v", m["http_method"])
	}
	if int(m["http_status"].(float64)) != 201 {
		t.Errorf("http_status = %v", m["http_status"])
	}
	if m["client_ip"] != "10.0.1.12" {
		t.Errorf("client_ip = %v", m["client_ip"])
	}
	qp := m["query_params"].(map[string]any)
	if qp["page"] != "1" {
		t.Errorf("query_params = %v", qp)
	}
	// masked request body
	reqBody := m["request_body"].(string)
	if strings.Contains(reqBody, "secret") {
		t.Errorf("password should be masked in request_body: %s", reqBody)
	}
	if m["event"] != "http_request" {
		t.Errorf("event = %v", m["event"])
	}
}

func TestMiddlewareMasksQueryParams(t *testing.T) {
	var buf bytes.Buffer
	log := gophlog.New(gophlog.WithWriter(&buf))
	mw := New(Options{
		Logger:              log,
		MaskFieldStrategies: map[string]gophlog.MaskingStrategy{"token": gophlog.HideAll},
	})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/reset?token=supersecret&page=2", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	m := parseLine(t, &buf)
	qp := m["query_params"].(map[string]any)
	if qp["token"] == "supersecret" {
		t.Errorf("token query param should be masked: %v", qp["token"])
	}
	if qp["page"] != "2" {
		t.Errorf("non-sensitive query param should be untouched: %v", qp["page"])
	}
	// The masked value must not leak through the path component either.
	if strings.Contains(m["http_path"].(string), "supersecret") {
		t.Errorf("token should be masked in http_path: %v", m["http_path"])
	}
}

func TestMiddlewareMasksFormBody(t *testing.T) {
	var buf bytes.Buffer
	log := gophlog.New(gophlog.WithWriter(&buf))
	mw := New(Options{
		Logger:              log,
		MaskFieldStrategies: map[string]gophlog.MaskingStrategy{"password": gophlog.HideAll},
	})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("username=alice&password=hunter2"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	m := parseLine(t, &buf)
	reqBody := m["request_body"].(string)
	if strings.Contains(reqBody, "hunter2") {
		t.Errorf("password should be masked in form body: %s", reqBody)
	}
	if !strings.Contains(reqBody, "username=alice") {
		t.Errorf("non-sensitive form field should be preserved: %s", reqBody)
	}
}

func TestMiddlewareDoesNotTruncateHandlerBody(t *testing.T) {
	var buf bytes.Buffer
	log := gophlog.New(gophlog.WithWriter(&buf))
	// Tiny MaxBodySize so the body is "too large" for gophlog.
	mw := New(Options{Logger: log, MaxBodySize: 16})

	bodyContent := strings.Repeat("A", 5000)
	var received string
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		received = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/x", strings.NewReader(bodyContent))
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if received != bodyContent {
		t.Fatalf("handler received truncated body: got %d bytes, want %d", len(received), len(bodyContent))
	}
	// The log must NOT contain a partial body fragment.
	m := parseLine(t, &buf)
	if rb, _ := m["request_body"].(string); strings.Contains(rb, "AAAA") {
		t.Errorf("oversized body should not be logged partially, got %q", rb)
	}
}

func TestMiddlewareExcludePaths(t *testing.T) {
	var buf bytes.Buffer
	log := gophlog.New(gophlog.WithWriter(&buf))
	mw := New(Options{Logger: log, ExcludePaths: []string{"/health"}})

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if buf.Len() != 0 {
		t.Errorf("excluded path should not be logged, got %q", buf.String())
	}
}

func TestClientIPVariants(t *testing.T) {
	mk := func(setup func(*http.Request)) string {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		setup(r)
		return ClientIP(r)
	}
	if got := mk(func(r *http.Request) { r.Header.Set("X-Forwarded-For", "203.0.113.1, 10.0.0.1") }); got != "203.0.113.1" {
		t.Errorf("XFF first hop = %q", got)
	}
	if got := mk(func(r *http.Request) { r.Header.Set("X-Real-IP", "198.51.100.7") }); got != "198.51.100.7" {
		t.Errorf("X-Real-IP = %q", got)
	}
	if got := mk(func(r *http.Request) { r.RemoteAddr = "192.0.2.5:54321" }); got != "192.0.2.5" {
		t.Errorf("RemoteAddr = %q", got)
	}
}

func TestNewDefaultMiddleware(t *testing.T) {
	var buf bytes.Buffer
	gophlog.SetDefault(gophlog.New(gophlog.WithWriter(&buf)))
	defer gophlog.SetDefault(gophlog.New())

	handler := NewDefault()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/x", nil))

	if buf.Len() == 0 {
		t.Error("NewDefault middleware should log via the default logger")
	}
}

func TestResponseRecorderFlushAndUnwrap(t *testing.T) {
	var buf bytes.Buffer
	log := gophlog.New(gophlog.WithWriter(&buf))
	mw := New(Options{Logger: log})

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Flusher and ResponseController (via Unwrap) must work through the recorder.
		if f, ok := w.(http.Flusher); ok {
			_, _ = w.Write([]byte("chunk"))
			f.Flush()
		} else {
			t.Error("recorder should expose http.Flusher")
		}
		rc := http.NewResponseController(w)
		_ = rc.Flush()
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/stream", nil))

	if buf.Len() == 0 {
		t.Error("request should still be logged")
	}
}

func TestMiddlewareLogExtraFields(t *testing.T) {
	var buf bytes.Buffer
	log := gophlog.New(gophlog.WithWriter(&buf))
	mw := New(Options{
		Logger:         log,
		LogExtraFields: []string{"externalId"},
	})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/x", strings.NewReader(`{"externalId":"EXT-9","amount":5}`))
	handler.ServeHTTP(httptest.NewRecorder(), req)

	m := parseLine(t, &buf)
	extra := m["extra"].(map[string]any)
	if extra["request_externalId"] != "EXT-9" {
		t.Errorf("extra = %v", extra)
	}
}

// TestMiddlewareRejectsUntrustedCorrelationID verifies a malformed inbound
// correlation header is replaced instead of being trusted into the logs.
func TestMiddlewareRejectsUntrustedCorrelationID(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   string // "" means: expect a generated ID instead
	}{
		{"valid is adopted", "b7f5e0b3b78b4b0fb2df8e5a9c3e22e5", "b7f5e0b3b78b4b0fb2df8e5a9c3e22e5"},
		{"oversized is replaced", strings.Repeat("a", 500), ""},
		{"structured value is replaced", `{"injected":true}`, ""},
		{"spaces are replaced", "not a valid id", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := gophlog.New(gophlog.WithWriter(&buf))
			mw := New(Options{Logger: log})
			handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
			req.Header.Set(gophlog.CorrelationHeader, c.header)
			handler.ServeHTTP(httptest.NewRecorder(), req)

			m := parseLine(t, &buf)
			got, _ := m["trace_id"].(string)
			if c.want != "" {
				if got != c.want {
					t.Errorf("trace_id = %q; want %q", got, c.want)
				}
				return
			}
			if got == c.header {
				t.Errorf("untrusted header was adopted as trace_id: %q", got)
			}
			if !gophlog.IsValidCorrelationID(got) {
				t.Errorf("replacement trace_id should be valid, got %q", got)
			}
		})
	}
}

// TestMiddlewareCapturesBodiesByDefault pins the documented default: a
// zero-value Options captures request and response bodies.
func TestMiddlewareCapturesBodiesByDefault(t *testing.T) {
	var buf bytes.Buffer
	log := gophlog.New(gophlog.WithWriter(&buf))
	mw := New(Options{Logger: log})

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"answer":42}`))
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/x", strings.NewReader(`{"ask":true}`))
	handler.ServeHTTP(httptest.NewRecorder(), req)

	m := parseLine(t, &buf)
	if rb, _ := m["request_body"].(string); !strings.Contains(rb, `"ask":true`) {
		t.Errorf("request_body = %q; want captured by default", rb)
	}
	if rb, _ := m["response_body"].(string); !strings.Contains(rb, `"answer":42`) {
		t.Errorf("response_body = %q; want captured by default", rb)
	}
}

// TestMiddlewareDisableBodyCapture covers the opt-out.
func TestMiddlewareDisableBodyCapture(t *testing.T) {
	var buf bytes.Buffer
	log := gophlog.New(gophlog.WithWriter(&buf))
	mw := New(Options{Logger: log, DisableRequestBody: true, DisableResponseBody: true})

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"answer":42}`))
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/x", strings.NewReader(`{"ask":true}`))
	handler.ServeHTTP(httptest.NewRecorder(), req)

	m := parseLine(t, &buf)
	if _, present := m["request_body"]; present {
		t.Errorf("request_body should be absent: %v", m["request_body"])
	}
	if _, present := m["response_body"]; present {
		t.Errorf("response_body should be absent: %v", m["response_body"])
	}
}

// TestMiddlewareLevelGateSkipsAllWork locks the fast path in: when neither log
// level is enabled, nothing is logged, but the handler still sees the intact
// body, the propagated correlation ID and an untouched response.
func TestMiddlewareLevelGateSkipsAllWork(t *testing.T) {
	var buf bytes.Buffer
	log := gophlog.New(gophlog.WithWriter(&buf), gophlog.WithMinLevel(gophlog.FATAL))
	mw := New(Options{Logger: log})

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gophlog.CorrelationID(r.Context()) == "" {
			t.Error("correlation ID should propagate even when logging is filtered")
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"ask":true}` {
			t.Errorf("handler body = %q", body)
		}
		_, _ = w.Write([]byte(`{"answer":42}`))
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/x", strings.NewReader(`{"ask":true}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Body.String() != `{"answer":42}` {
		t.Errorf("client response = %q", rec.Body.String())
	}
	if buf.Len() != 0 {
		t.Errorf("nothing should be logged at a filtered level, got %q", buf.String())
	}
}

// TestMiddlewareLevelGatePerStatus covers the mixed configuration (minimum
// level ERROR): a 2xx must not log, a 5xx on the same middleware must.
func TestMiddlewareLevelGatePerStatus(t *testing.T) {
	var buf bytes.Buffer
	log := gophlog.New(gophlog.WithWriter(&buf), gophlog.WithMinLevel(gophlog.ERROR))
	mw := New(Options{Logger: log})

	okHandler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"fine":true}`))
	}))
	okHandler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/ok", nil))
	if buf.Len() != 0 {
		t.Fatalf("2xx should not log when only ERROR is enabled, got %q", buf.String())
	}

	failHandler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"boom":true}`))
	}))
	failHandler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/fail", nil))

	m := parseLine(t, &buf)
	if int(m["http_status"].(float64)) != 500 {
		t.Errorf("http_status = %v", m["http_status"])
	}
	// The 5xx response body must still be captured despite the gate.
	if m["response_body"] != `{"boom":true}` {
		t.Errorf("response_body = %v", m["response_body"])
	}
}

// TestMiddlewareExtraFieldsAreMasked: a field named in both LogExtraFields and
// MaskFieldStrategies is lifted into extra in its MASKED form.
func TestMiddlewareExtraFieldsAreMasked(t *testing.T) {
	var buf bytes.Buffer
	log := gophlog.New(gophlog.WithWriter(&buf))
	mw := New(Options{
		Logger:              log,
		LogExtraFields:      []string{"cardNumber"},
		MaskFieldStrategies: map[string]gophlog.MaskingStrategy{"cardNumber": gophlog.HideAll},
	})

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/pay", strings.NewReader(`{"cardNumber":"4111111111111111"}`))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if strings.Contains(buf.String(), "4111111111111111") {
		t.Errorf("extra leaked the masked field:\n%s", buf.String())
	}
	m := parseLine(t, &buf)
	extra := m["extra"].(map[string]any)
	if extra["request_cardNumber"] != "********" {
		t.Errorf("extra.request_cardNumber should be masked: %v", extra["request_cardNumber"])
	}
}

// TestEarlyHintsPassThrough: a 1xx informational header (e.g. 103 Early Hints)
// must not latch the recorder — the final status still reaches the client and
// the log.
func TestEarlyHintsPassThrough(t *testing.T) {
	var buf bytes.Buffer
	log := gophlog.New(gophlog.WithWriter(&buf))
	mw := New(Options{Logger: log, IncludePaths: []string{"/*"}})

	srv := httptest.NewServer(mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusEarlyHints)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"missing":true}`))
	})))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/x")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("client status = %d; want 404", resp.StatusCode)
	}
	m := parseLine(t, &buf)
	if int(m["http_status"].(float64)) != 404 {
		t.Errorf("http_status = %v; want 404", m["http_status"])
	}
}

// TestBytesInUsesContentLength: bytes_in must reflect the request's real size,
// not the captured prefix, when the body exceeds MaxBodySize.
func TestBytesInUsesContentLength(t *testing.T) {
	var buf bytes.Buffer
	log := gophlog.New(gophlog.WithWriter(&buf))
	mw := New(Options{Logger: log, MaxBodySize: 10})

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	body := strings.Repeat("x", 100)
	req := httptest.NewRequest(http.MethodPost, "/api/x", strings.NewReader(body))
	handler.ServeHTTP(httptest.NewRecorder(), req)

	m := parseLine(t, &buf)
	if int64(m["bytes_in"].(float64)) != 100 {
		t.Errorf("bytes_in = %v; want 100", m["bytes_in"])
	}
	if m["request_body"] != "[body not logged: exceeds MaxBodySize]" {
		t.Errorf("request_body = %v", m["request_body"])
	}
}
