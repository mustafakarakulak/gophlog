package httpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestTransportLogsAndMasks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(gophlog.CorrelationHeader) != "cid-123" {
			t.Errorf("correlation header not propagated: %q", r.Header.Get(gophlog.CorrelationHeader))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"paymentId":"PAY-1","status":"success"}`))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	log := gophlog.New(gophlog.WithWriter(&buf))
	client := NewClient(nil, Options{
		Logger:              log,
		MaskFieldStrategies: map[string]gophlog.MaskingStrategy{"creditCard": gophlog.CreditCard},
		EventName:           "payment_api_request",
	})

	ctx := gophlog.WithCorrelationID(context.Background(), "cid-123")
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/payments",
		strings.NewReader(`{"amount":100,"creditCard":"1111999988883333"}`))
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(string(body), "PAY-1") {
		t.Errorf("response body should pass through unchanged: %s", body)
	}

	m := parseLine(t, &buf)
	if m["trace_id"] != "cid-123" {
		t.Errorf("trace_id = %v", m["trace_id"])
	}
	if m["event"] != "payment_api_request" {
		t.Errorf("event = %v", m["event"])
	}
	if int(m["http_status"].(float64)) != 200 {
		t.Errorf("http_status = %v", m["http_status"])
	}
	reqBody := m["request_body"].(string)
	if strings.Contains(reqBody, "1111999988883333") {
		t.Errorf("creditCard should be masked: %s", reqBody)
	}
}

func TestTransportDoesNotMutateCallerRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	log := gophlog.New(gophlog.WithWriter(&buf))
	client := NewClient(nil, Options{Logger: log})

	ctx := gophlog.WithCorrelationID(context.Background(), "cid-xyz")
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	// The caller's request must remain untouched (RoundTripper contract).
	if req.Header.Get(gophlog.CorrelationHeader) != "" {
		t.Errorf("caller request was mutated: %q", req.Header.Get(gophlog.CorrelationHeader))
	}
}

func TestTransportRedirectReplaysBody(t *testing.T) {
	var bodies []string
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		http.Redirect(w, r, "/end", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/end", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		_, _ = w.Write([]byte(`{}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var buf bytes.Buffer
	log := gophlog.New(gophlog.WithWriter(&buf))
	client := NewClient(nil, Options{Logger: log})

	const payload = `{"amount":100}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/start", strings.NewReader(payload))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if len(bodies) != 2 {
		t.Fatalf("expected 2 hops, got %d", len(bodies))
	}
	for i, b := range bodies {
		if b != payload {
			t.Errorf("hop %d body = %q; want %q (body must replay across redirects)", i, b, payload)
		}
	}
}

func TestTransportLogCurl(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	var logBuf, curlBuf bytes.Buffer
	log := gophlog.New(gophlog.WithWriter(&logBuf))
	client := NewClient(nil, Options{
		Logger:     log,
		LogCurl:    true,
		CurlWriter: &curlBuf,
	})

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/x", strings.NewReader(`{"a":1}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	curl := curlBuf.String()
	if !strings.Contains(curl, "curl -X POST") {
		t.Errorf("curl command missing method: %q", curl)
	}
	if !strings.Contains(curl, `--data '{"a":1}'`) {
		t.Errorf("curl command missing body: %q", curl)
	}
}

func TestTransportMasksFormBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	log := gophlog.New(gophlog.WithWriter(&buf))
	client := NewClient(nil, Options{
		Logger:              log,
		MaskFieldStrategies: map[string]gophlog.MaskingStrategy{"password": gophlog.HideAll},
	})

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/login", strings.NewReader("user=alice&password=hunter2"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	reqBody := parseLine(t, &buf)["request_body"].(string)
	if strings.Contains(reqBody, "hunter2") {
		t.Errorf("form password should be masked: %s", reqBody)
	}
}

func TestTransportMasksURLQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	log := gophlog.New(gophlog.WithWriter(&buf))
	client := NewClient(nil, Options{
		Logger:              log,
		MaskFieldStrategies: map[string]gophlog.MaskingStrategy{"token": gophlog.HideAll},
	})

	resp, err := client.Get(srv.URL + "/x?token=supersecret&page=1")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	path := parseLine(t, &buf)["http_path"].(string)
	if strings.Contains(path, "supersecret") {
		t.Errorf("token should be masked in logged URL: %s", path)
	}
}

func TestTransportErrorPath(t *testing.T) {
	var buf bytes.Buffer
	log := gophlog.New(gophlog.WithWriter(&buf))
	// Point at a closed port so RoundTrip returns a transport error.
	client := NewClient(nil, Options{Logger: log})

	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:1/unreachable", nil)
	_, err := client.Do(req)
	if err == nil {
		t.Fatal("expected a transport error")
	}

	m := parseLine(t, &buf)
	if !strings.HasSuffix(m["event"].(string), "_exception") {
		t.Errorf("error event should be suffixed _exception: %v", m["event"])
	}
	if m["error_message"] == nil {
		t.Errorf("error_message should be set: %v", m)
	}
}

func TestTransportExcludeURLs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	log := gophlog.New(gophlog.WithWriter(&buf))
	client := NewClient(nil, Options{Logger: log, ExcludeURLs: []string{srv.URL + "/health"}})

	resp, err := client.Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if buf.Len() != 0 {
		t.Errorf("excluded URL should not be logged, got %q", buf.String())
	}
}

// TestCurlRedactsCredentialHeaders locks in that the curl output never carries
// credentials, and that the body it prints is the masked one.
func TestCurlRedactsCredentialHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	var curl, logs bytes.Buffer
	client := NewClient(nil, Options{
		Logger:     gophlog.New(gophlog.WithWriter(&logs)),
		LogCurl:    true,
		CurlWriter: &curl,
		MaskFieldStrategies: map[string]gophlog.MaskingStrategy{
			"password":     gophlog.HideAll,
			"X-Tenant-Key": gophlog.ShowLast2,
		},
	})

	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{"password":"hunter2"}`))
	req.Header.Set("Authorization", "Bearer super-secret-token")
	req.Header.Set("Cookie", "session=abc123")
	req.Header.Set("X-Api-Key", "key-xyz")
	req.Header.Set("X-Tenant-Key", "tenant-99")
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	out := curl.String()
	for _, secret := range []string{"super-secret-token", "abc123", "key-xyz", "hunter2"} {
		if strings.Contains(out, secret) {
			t.Errorf("curl output leaked %q:\n%s", secret, out)
		}
	}
	if strings.Count(out, "[REDACTED]") != 3 {
		t.Errorf("expected Authorization/Cookie/X-Api-Key redacted:\n%s", out)
	}
	// A header named in MaskFieldStrategies is masked, not fully redacted.
	if !strings.Contains(out, "X-Tenant-Key: *******99") {
		t.Errorf("named header should be masked by its strategy:\n%s", out)
	}
	// Non-sensitive headers pass through untouched.
	if !strings.Contains(out, "Content-Type: application/json") {
		t.Errorf("plain header should survive:\n%s", out)
	}
}

// TestCurlMasksQueryParams locks in that the curl output renders the masked
// URL, not the raw one: a query secret named in MaskFieldStrategies must never
// reach the curl side channel even though the log line already masks it.
func TestCurlMasksQueryParams(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	var curl, logs bytes.Buffer
	client := NewClient(nil, Options{
		Logger:     gophlog.New(gophlog.WithWriter(&logs)),
		LogCurl:    true,
		CurlWriter: &curl,
		MaskFieldStrategies: map[string]gophlog.MaskingStrategy{
			"api_key": gophlog.HideAll,
		},
	})

	resp, err := client.Get(srv.URL + "/v1/pay?api_key=SUPERSECRET123&page=2")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	out := curl.String()
	if strings.Contains(out, "SUPERSECRET123") {
		t.Errorf("curl output leaked the masked query secret:\n%s", out)
	}
	if !strings.Contains(out, "api_key=********") {
		t.Errorf("curl output should carry the masked query value:\n%s", out)
	}
	// The rest of the query survives untouched.
	if !strings.Contains(out, "page=2") {
		t.Errorf("non-sensitive query params should survive:\n%s", out)
	}
	if strings.Contains(logs.String(), "SUPERSECRET123") {
		t.Errorf("log line leaked the masked query secret:\n%s", logs.String())
	}
}

// TestCurlOutputIsDeterministic guards the sorted header rendering.
func TestCurlOutputIsDeterministic(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://example.com/x", nil)
	req.Header.Set("B-Header", "2")
	req.Header.Set("A-Header", "1")
	req.Header.Set("C-Header", "3")

	first := buildCurl(req, "https://example.com/x", "", nil)
	for i := 0; i < 20; i++ {
		if got := buildCurl(req, "https://example.com/x", "", nil); got != first {
			t.Fatalf("curl output not stable:\n%s\nvs\n%s", first, got)
		}
	}
	if strings.Index(first, "A-Header") > strings.Index(first, "B-Header") {
		t.Errorf("headers should be sorted:\n%s", first)
	}
}

// TestTransportLevelGate locks the fast path in: with logging filtered out the
// call still succeeds, the caller can read the body, the correlation ID still
// propagates, and nothing is logged. With minimum level ERROR, a 200 stays
// silent while a 500 is logged.
func TestTransportLevelGate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(gophlog.CorrelationHeader) == "" {
			t.Error("correlation ID should propagate even when logging is filtered")
		}
		if r.URL.Path == "/fail" {
			w.WriteHeader(http.StatusInternalServerError)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	var logs bytes.Buffer
	client := NewClient(nil, Options{
		Logger: gophlog.New(gophlog.WithWriter(&logs), gophlog.WithMinLevel(gophlog.ERROR)),
	})

	ctx := gophlog.WithCorrelationID(context.Background(), "cid-gate")
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/ok", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != `{"ok":true}` {
		t.Errorf("caller body = %q", body)
	}
	if logs.Len() != 0 {
		t.Fatalf("2xx should not log when only ERROR is enabled, got %q", logs.String())
	}

	req, _ = http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/fail", nil)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if !strings.Contains(logs.String(), `"http_status":500`) {
		t.Errorf("5xx should be logged: %q", logs.String())
	}
}

// TestTransportRedactsURLPassword locks in that basic-auth credentials in the
// URL userinfo section (https://user:password@host) never reach the log line
// or the curl dump — net/http turns them into an Authorization header, which
// is already redacted, so the URL must not leak the same secret.
func TestTransportRedactsURLPassword(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	var logs, curl bytes.Buffer
	client := NewClient(nil, Options{
		Logger:     gophlog.New(gophlog.WithWriter(&logs)),
		LogCurl:    true,
		CurlWriter: &curl,
	})

	u, _ := url.Parse(srv.URL)
	u.User = url.UserPassword("alice", "PW-HUNTER2")
	req, _ := http.NewRequest(http.MethodGet, u.String(), nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if strings.Contains(logs.String(), "PW-HUNTER2") {
		t.Errorf("log line leaked the URL password:\n%s", logs.String())
	}
	if strings.Contains(curl.String(), "PW-HUNTER2") {
		t.Errorf("curl output leaked the URL password:\n%s", curl.String())
	}
	// The username survives, redacted-password style (mirrors url.Redacted).
	if !strings.Contains(logs.String(), "alice:xxxxx@") {
		t.Errorf("log line should keep the redacted userinfo: %s", logs.String())
	}
}

// TestTransportExtraFieldsAreMasked: a field named in both LogExtraFields and
// MaskFieldStrategies is lifted into extra in its MASKED form.
func TestTransportExtraFieldsAreMasked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	var logs bytes.Buffer
	client := NewClient(nil, Options{
		Logger:              gophlog.New(gophlog.WithWriter(&logs)),
		LogExtraFields:      []string{"cardNumber"},
		MaskFieldStrategies: map[string]gophlog.MaskingStrategy{"cardNumber": gophlog.HideAll},
	})

	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{"cardNumber":"4111111111111111"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if strings.Contains(logs.String(), "4111111111111111") {
		t.Errorf("extra leaked the masked field:\n%s", logs.String())
	}
	m := parseLine(t, &logs)
	extra := m["extra"].(map[string]any)
	if extra["request_cardNumber"] != "********" {
		t.Errorf("extra.request_cardNumber should be masked: %v", extra["request_cardNumber"])
	}
}
