package httpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	logging "github.com/mustafakarakulak/go-logging"
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
		if r.Header.Get(logging.CorrelationHeader) != "cid-123" {
			t.Errorf("correlation header not propagated: %q", r.Header.Get(logging.CorrelationHeader))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"paymentId":"PAY-1","status":"success"}`))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	log := logging.New(logging.WithWriter(&buf))
	client := NewClient(nil, Options{
		Logger:              log,
		LogRequestBody:      true,
		LogResponseBody:     true,
		MaskFieldStrategies: map[string]logging.MaskingStrategy{"creditCard": logging.CreditCard},
		EventName:           "payment_api_request",
	})

	ctx := logging.WithCorrelationID(context.Background(), "cid-123")
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/payments",
		strings.NewReader(`{"amount":100,"creditCard":"1111999988883333"}`))
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
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
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	log := logging.New(logging.WithWriter(&buf))
	client := NewClient(nil, Options{Logger: log})

	ctx := logging.WithCorrelationID(context.Background(), "cid-xyz")
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// The caller's request must remain untouched (RoundTripper contract).
	if req.Header.Get(logging.CorrelationHeader) != "" {
		t.Errorf("caller request was mutated: %q", req.Header.Get(logging.CorrelationHeader))
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
		w.Write([]byte(`{}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var buf bytes.Buffer
	log := logging.New(logging.WithWriter(&buf))
	client := NewClient(nil, Options{Logger: log, LogRequestBody: true})

	const payload = `{"amount":100}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/start", strings.NewReader(payload))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

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
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	var logBuf, curlBuf bytes.Buffer
	log := logging.New(logging.WithWriter(&logBuf))
	client := NewClient(nil, Options{
		Logger:         log,
		LogRequestBody: true,
		LogCurl:        true,
		CurlWriter:     &curlBuf,
	})

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/x", strings.NewReader(`{"a":1}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

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
		io.ReadAll(r.Body)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	log := logging.New(logging.WithWriter(&buf))
	client := NewClient(nil, Options{
		Logger:              log,
		LogRequestBody:      true,
		MaskFieldStrategies: map[string]logging.MaskingStrategy{"password": logging.HideAll},
	})

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/login", strings.NewReader("user=alice&password=hunter2"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	reqBody := parseLine(t, &buf)["request_body"].(string)
	if strings.Contains(reqBody, "hunter2") {
		t.Errorf("form password should be masked: %s", reqBody)
	}
}

func TestTransportMasksURLQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	log := logging.New(logging.WithWriter(&buf))
	client := NewClient(nil, Options{
		Logger:              log,
		MaskFieldStrategies: map[string]logging.MaskingStrategy{"token": logging.HideAll},
	})

	resp, err := client.Get(srv.URL + "/x?token=supersecret&page=1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	path := parseLine(t, &buf)["http_path"].(string)
	if strings.Contains(path, "supersecret") {
		t.Errorf("token should be masked in logged URL: %s", path)
	}
}

func TestTransportErrorPath(t *testing.T) {
	var buf bytes.Buffer
	log := logging.New(logging.WithWriter(&buf))
	// Point at a closed port so RoundTrip returns a transport error.
	client := NewClient(nil, Options{Logger: log, LogRequestBody: true})

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
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	log := logging.New(logging.WithWriter(&buf))
	client := NewClient(nil, Options{Logger: log, ExcludeURLs: []string{srv.URL + "/health"}})

	resp, err := client.Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if buf.Len() != 0 {
		t.Errorf("excluded URL should not be logged, got %q", buf.String())
	}
}
