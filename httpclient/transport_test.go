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
