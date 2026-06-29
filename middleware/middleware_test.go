package middleware

import (
	"bytes"
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

func TestMiddlewareLogsRequest(t *testing.T) {
	var buf bytes.Buffer
	log := logging.New(logging.WithWriter(&buf))

	mw := New(Options{
		Logger:              log,
		LogRequestBody:      true,
		LogResponseBody:     true,
		MaskFieldStrategies: map[string]logging.MaskingStrategy{"password": logging.HideAll},
		EventName:           "http_request",
	})

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "secret") {
			t.Error("handler should still receive the original request body")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"status":"created"}`))
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

func TestMiddlewareDoesNotTruncateHandlerBody(t *testing.T) {
	var buf bytes.Buffer
	log := logging.New(logging.WithWriter(&buf))
	// Tiny MaxBodySize so the body is "too large" for logging.
	mw := New(Options{Logger: log, LogRequestBody: true, MaxBodySize: 16})

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
	log := logging.New(logging.WithWriter(&buf))
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

func TestMiddlewareLogExtraFields(t *testing.T) {
	var buf bytes.Buffer
	log := logging.New(logging.WithWriter(&buf))
	mw := New(Options{
		Logger:         log,
		LogRequestBody: true,
		LogExtraFields: []string{"externalId"},
	})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
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
