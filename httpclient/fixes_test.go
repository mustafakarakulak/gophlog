package httpclient

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mustafakarakulak/gophlog"
)

// Markers the transport logs in place of a body it deliberately leaves alone.
const (
	streamingMarker = "[body not logged: streaming]"
	encodedMarker   = "[body not logged: non-identity Content-Encoding]"
	unmaskedMarker  = "[body not logged: cannot be masked]"
)

// stuck is how long a test waits before calling a round trip hung. Before the
// streaming fix these calls blocked until the stream ended (or forever).
const stuck = 2 * time.Second

// storedGzip gzips s without compression: a stored deflate block, which carries
// s in plain text inside the "compressed" body.
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

// TestTransportStreamingResponseNotCaptured: a streaming response is handed
// back as soon as its header arrives, untouched, instead of being read up to
// MaxBodySize first — an idle SSE or gRPC stream used to hold Do until the
// context expired.
func TestTransportStreamingResponseNotCaptured(t *testing.T) {
	for _, ct := range []string{"text/event-stream", "application/grpc", "application/grpc+proto", "application/json;stream=watch"} {
		t.Run(ct, func(t *testing.T) {
			release := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", ct)
				_, _ = io.WriteString(w, "data: hello\n\n")
				w.(http.Flusher).Flush()
				<-release // keep the stream open and idle
			}))
			defer srv.Close()
			defer close(release)

			var logs bytes.Buffer
			client := NewClient(nil, Options{Logger: gophlog.New(gophlog.WithWriter(&logs))})
			ctx, cancel := context.WithTimeout(context.Background(), stuck)
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)

			begin := time.Now()
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			if elapsed := time.Since(begin); elapsed > stuck/2 {
				t.Fatalf("Do returned after %v; the stream was read before being handed back", elapsed)
			}
			line, err := bufio.NewReader(resp.Body).ReadString('\n')
			if err != nil || line != "data: hello\n" {
				t.Errorf("caller read %q, %v; want the first event", line, err)
			}
			if got := parseLine(t, &logs)["response_body"]; got != streamingMarker {
				t.Errorf("response_body = %v; want %q", got, streamingMarker)
			}
		})
	}
}

// TestTransportSwitchingProtocols: a 101 response body is the upgraded
// connection. It must come back as the transport's io.ReadWriteCloser, and
// reading it for the log used to block forever.
func TestTransportSwitchingProtocols(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, rw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.Close() }()
		go func() { <-release; _ = conn.Close() }()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")
		_ = rw.Flush()
		_, _ = io.Copy(conn, rw) // echo until the client hangs up
	}))
	defer srv.Close()
	defer close(release)

	var logs bytes.Buffer
	client := NewClient(nil, Options{Logger: gophlog.New(gophlog.WithWriter(&logs))})
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "echo")

	type result struct {
		resp *http.Response
		err  error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := client.Do(req)
		done <- result{resp, err}
	}()
	var res result
	select {
	case res = <-done:
	case <-time.After(stuck):
		t.Fatal("Do did not return for a 101 response")
	}
	if res.err != nil {
		t.Fatal(res.err)
	}
	defer func() { _ = res.resp.Body.Close() }()

	if res.resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d; want 101", res.resp.StatusCode)
	}
	conn, ok := res.resp.Body.(io.ReadWriteCloser)
	if !ok {
		t.Fatalf("101 body is %T; want the upgraded io.ReadWriteCloser", res.resp.Body)
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	echo := make([]byte, 4)
	if _, err := io.ReadFull(conn, echo); err != nil || string(echo) != "ping" {
		t.Errorf("echo = %q, %v; want ping", echo, err)
	}

	m := parseLine(t, &logs)
	if int(m["http_status"].(float64)) != http.StatusSwitchingProtocols {
		t.Errorf("http_status = %v; want 101", m["http_status"])
	}
	if m["response_body"] != streamingMarker {
		t.Errorf("response_body = %v; want %q", m["response_body"], streamingMarker)
	}
}

// blockingBase answers without touching the request body, the way an HTTP/2
// or full-duplex transport returns response headers while a client stream is
// still being written.
type blockingBase struct{ gotBody chan io.ReadCloser }

func (b blockingBase) RoundTrip(r *http.Request) (*http.Response, error) {
	b.gotBody <- r.Body
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/grpc"}},
		Body: http.NoBody, Request: r}, nil
}

// TestTransportStreamingRequestNotCaptured: a client-streaming gRPC body stays
// open while it waits for the response, so it must go to the transport unread.
func TestTransportStreamingRequestNotCaptured(t *testing.T) {
	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()
	go func() { _, _ = pw.Write([]byte("\x00\x00\x00\x00\x01A")) }() // one frame, then idle

	base := blockingBase{gotBody: make(chan io.ReadCloser, 1)}
	var logs bytes.Buffer
	client := NewClient(&http.Client{Transport: base}, Options{Logger: gophlog.New(gophlog.WithWriter(&logs))})
	req, _ := http.NewRequest(http.MethodPost, "http://grpc.example/svc/Stream", pr)
	req.Header.Set("Content-Type", "application/grpc")

	done := make(chan error, 1)
	go func() {
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(stuck):
		t.Fatal("RoundTrip read the open request stream before sending it")
	}
	if body := <-base.gotBody; body != pr {
		t.Errorf("transport got body %T; want the caller's stream, untouched", body)
	}
	if got := parseLine(t, &logs)["request_body"]; got != streamingMarker {
		t.Errorf("request_body = %v; want %q", got, streamingMarker)
	}
}

// TestTransportEncodedBodiesNotLogged: an encoded body is never logged raw. A
// short gzip body is a stored block carrying the plain text, so it used to
// bypass masking in both directions.
func TestTransportEncodedBodiesNotLogged(t *testing.T) {
	reqGz := storedGzip(t, `{"password":"hunter2"}`)
	respGz := storedGzip(t, `{"password":"s3cret"}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ := io.ReadAll(r.Body)
		if !bytes.Equal(got, reqGz) {
			t.Errorf("server received %q; want the gzip body untouched", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(respGz)
	}))
	defer srv.Close()

	var logs bytes.Buffer
	client := NewClient(nil, Options{
		Logger:              gophlog.New(gophlog.WithWriter(&logs)),
		MaskFieldStrategies: map[string]gophlog.MaskingStrategy{"password": gophlog.HideAll},
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(reqGz))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("Accept-Encoding", "gzip") // caller-set: the transport will not decompress
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !bytes.Equal(got, respGz) {
		t.Errorf("caller received %q; want the gzip body untouched", got)
	}

	for _, secret := range []string{"hunter2", "s3cret"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("log leaked %q from an encoded body:\n%s", secret, logs.String())
		}
	}
	m := parseLine(t, &logs)
	if m["request_body"] != encodedMarker || m["response_body"] != encodedMarker {
		t.Errorf("request_body = %v, response_body = %v; want %q", m["request_body"], m["response_body"], encodedMarker)
	}
}

// TestTransportDecompressedResponseIsMasked is the control: when the transport
// negotiates gzip itself it decompresses transparently and drops the
// Content-Encoding header, so the body is captured and masked as usual.
func TestTransportDecompressedResponseIsMasked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			t.Errorf("transport did not offer gzip: %q", r.Header.Get("Accept-Encoding"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(storedGzip(t, `{"password":"s3cret","ok":true}`))
	}))
	defer srv.Close()

	var logs bytes.Buffer
	client := NewClient(nil, Options{
		Logger:              gophlog.New(gophlog.WithWriter(&logs)),
		MaskFieldStrategies: map[string]gophlog.MaskingStrategy{"password": gophlog.HideAll},
	})
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != `{"password":"s3cret","ok":true}` {
		t.Errorf("caller body = %q", body)
	}
	rb, _ := parseLine(t, &logs)["response_body"].(string)
	if strings.Contains(rb, "s3cret") || !strings.Contains(rb, `"ok":true`) {
		t.Errorf("response_body = %q; want the decompressed body, masked", rb)
	}
}

// TestTransportMalformedFormNotLogged: a form body url.ParseQuery rejects is
// replaced by a marker instead of being logged raw — the server still reads
// the password out of it.
func TestTransportMalformedFormNotLogged(t *testing.T) {
	for _, body := range []string{"password=hunter2&x=%zz", "user=a;b&password=hunter2"} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = r.ParseForm()
				if r.PostForm.Get("password") != "hunter2" {
					t.Errorf("server should still read the password, got %q", r.PostForm.Get("password"))
				}
			}))
			defer srv.Close()

			var logs, curl bytes.Buffer
			client := NewClient(nil, Options{
				Logger:              gophlog.New(gophlog.WithWriter(&logs)),
				MaskFieldStrategies: map[string]gophlog.MaskingStrategy{"password": gophlog.HideAll},
				LogCurl:             true,
				CurlWriter:          &curl,
			})
			req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()

			if strings.Contains(logs.String(), "hunter2") || strings.Contains(curl.String(), "hunter2") {
				t.Errorf("malformed form leaked the password:\nlog:  %s\ncurl: %s", logs.String(), curl.String())
			}
			if got := parseLine(t, &logs)["request_body"]; got != unmaskedMarker {
				t.Errorf("request_body = %v; want %q", got, unmaskedMarker)
			}
		})
	}
}

// TestTransportKeepsBigNumbers: masking no longer routes numbers through
// float64, which rounded 12345678901234567891 to 12345678901234567000.
func TestTransportKeepsBigNumbers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":98765432109876543210,"rate":0.1000}`))
	}))
	defer srv.Close()

	var logs bytes.Buffer
	client := NewClient(nil, Options{
		Logger:              gophlog.New(gophlog.WithWriter(&logs)),
		MaskFieldStrategies: map[string]gophlog.MaskingStrategy{"password": gophlog.HideAll},
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{"order_id":12345678901234567891,"password":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	m := parseLine(t, &logs)
	if rb, _ := m["request_body"].(string); !strings.Contains(rb, `"order_id":12345678901234567891`) {
		t.Errorf("request_body = %q; want the exact number", rb)
	}
	if rb, _ := m["response_body"].(string); rb != `{"id":98765432109876543210,"rate":0.1000}` {
		t.Errorf("response_body = %q; want the exact numbers", rb)
	}
}

// TestTransportEmptyBodyKeepsContentLength: an empty request body must not turn
// into a chunked one (some servers answer 411 Length Required), whether it is
// http.NoBody or an empty reader of undeclared length.
func TestTransportEmptyBodyKeepsContentLength(t *testing.T) {
	type seen struct {
		contentLength    int64
		transferEncoding []string
	}
	got := make(chan seen, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- seen{r.ContentLength, r.TransferEncoding}
	}))
	defer srv.Close()

	client := NewClient(nil, Options{Logger: gophlog.New(gophlog.WithWriter(io.Discard))})
	noBody, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(nil)) // Body = http.NoBody
	emptyReader, _ := http.NewRequest(http.MethodPost, srv.URL, nil)
	emptyReader.Body = io.NopCloser(strings.NewReader("")) // ContentLength 0 = unknown

	for name, req := range map[string]*http.Request{"NoBody": noBody, "empty reader": emptyReader} {
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		_ = resp.Body.Close()
		s := <-got
		if s.contentLength != 0 || len(s.transferEncoding) != 0 {
			t.Errorf("%s: server saw Content-Length %d, Transfer-Encoding %v; want 0 and none",
				name, s.contentLength, s.transferEncoding)
		}
	}
}

// TestTransportNoBodyRedirect: GetBody keeps working for an empty body across a
// 307, which replays the request body.
func TestTransportNoBodyRedirect(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/end", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/end", func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
			t.Errorf("replayed request: Content-Length %d, Transfer-Encoding %v", r.ContentLength, r.TransferEncoding)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := NewClient(nil, Options{Logger: gophlog.New(gophlog.WithWriter(io.Discard))})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/start", http.NoBody)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d; want 200 after the redirect", resp.StatusCode)
	}
}

// TestTransportFilteredURLsPropagateCorrelationID: a request that is not logged
// (ExcludeURLs / IncludeURLs) still carries the correlation ID downstream, like
// the level-gated fast path and like an excluded path in the middleware.
func TestTransportFilteredURLsPropagateCorrelationID(t *testing.T) {
	gotCID := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCID <- r.Header.Get(gophlog.CorrelationHeader)
	}))
	defer srv.Close()

	for name, opts := range map[string]Options{
		"excluded":     {ExcludeURLs: []string{srv.URL + "/health"}},
		"not included": {IncludeURLs: []string{"https://elsewhere.example/*"}},
	} {
		var logs bytes.Buffer
		opts.Logger = gophlog.New(gophlog.WithWriter(&logs))
		client := NewClient(nil, opts)

		ctx := gophlog.WithCorrelationID(context.Background(), "cid-filtered")
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/health", nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()

		if got := <-gotCID; got != "cid-filtered" {
			t.Errorf("%s: server saw correlation ID %q; want cid-filtered", name, got)
		}
		if logs.Len() != 0 {
			t.Errorf("%s: filtered request was logged: %s", name, logs.String())
		}
		if req.Header.Get(gophlog.CorrelationHeader) != "" {
			t.Errorf("%s: the caller's request was mutated", name)
		}
	}
}

// TestTransportRedactsTokenUserinfo: a token in the username slot of the URL
// (https://<token>@github.com/...) is redacted like a password.
func TestTransportRedactsTokenUserinfo(t *testing.T) {
	var logs, curl bytes.Buffer
	client := NewClient(&http.Client{Transport: blockingBase{gotBody: make(chan io.ReadCloser, 1)}}, Options{
		Logger:     gophlog.New(gophlog.WithWriter(&logs)),
		LogCurl:    true,
		CurlWriter: &curl,
	})
	resp, err := client.Get("https://ghp_ABCDEF123456@github.com/org/repo.git")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	for _, out := range []string{logs.String(), curl.String()} {
		if strings.Contains(out, "ghp_ABCDEF123456") {
			t.Errorf("userinfo token leaked:\n%s", out)
		}
		if !strings.Contains(out, "https://xxxxx@github.com/org/repo.git") {
			t.Errorf("want the redacted URL in:\n%s", out)
		}
	}
}

// TestTransportDropsURLFragment: the fragment never goes on the wire, but OAuth
// implicit-flow URLs carry access tokens there, so it is left out of the log.
func TestTransportDropsURLFragment(t *testing.T) {
	var logs, curl bytes.Buffer
	client := NewClient(&http.Client{Transport: blockingBase{gotBody: make(chan io.ReadCloser, 1)}}, Options{
		Logger:     gophlog.New(gophlog.WithWriter(&logs)),
		LogCurl:    true,
		CurlWriter: &curl,
	})
	resp, err := client.Get("https://api.example.com/cb?state=1#access_token=frag-tok&token_type=bearer")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	for _, out := range []string{logs.String(), curl.String()} {
		if strings.Contains(out, "frag-tok") || strings.Contains(out, "#") {
			t.Errorf("fragment leaked:\n%s", out)
		}
	}
	if got := parseLine(t, &logs)["http_path"]; got != "https://api.example.com/cb?state=1" {
		t.Errorf("http_path = %v", got)
	}
}

// TestCurlRedactsVendorCredentialHeaders covers the credential headers beyond
// Authorization / Cookie / X-Api-Key, in any case.
func TestCurlRedactsVendorCredentialHeaders(t *testing.T) {
	headers := map[string]string{
		"PRIVATE-TOKEN":             "glpat-secret1",
		"x-goog-api-key":            "AIza-secret2",
		"X-Amz-Security-Token":      "amz-secret3",
		"Ocp-Apim-Subscription-Key": "az-secret4",
		"apikey":                    "kong-secret5",
		"X-CSRF-Token":              "csrf-secret6",
		"X-Xsrf-Token":              "xsrf-secret7",
	}
	req, _ := http.NewRequest(http.MethodGet, "https://api.example.com/x", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	out := buildCurl(req, "https://api.example.com/x", "", nil)
	for _, v := range headers {
		if strings.Contains(out, v) {
			t.Errorf("curl output leaked %q:\n%s", v, out)
		}
	}
	if n := strings.Count(out, "[REDACTED]"); n != len(headers) {
		t.Errorf("redacted %d headers; want %d:\n%s", n, len(headers), out)
	}
}
