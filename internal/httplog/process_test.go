package httplog

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/mustafakarakulak/gophlog"
)

const formType = "application/x-www-form-urlencoded"

var passwordOnly = LowerStrategies(map[string]gophlog.MaskingStrategy{"password": gophlog.HideAll})

// hidden is what HideAll turns s into.
func hidden(s string) string { return gophlog.MaskString(s, gophlog.HideAll) }

// TestProcessBodyMalformedFormIsNotLogged: a form body that url.ParseQuery (and
// so r.ParseForm) rejects must never be logged raw once masking is configured —
// the handler still reads the password out of it.
func TestProcessBodyMalformedFormIsNotLogged(t *testing.T) {
	for _, body := range []string{
		"password=hunter2&x=%zz",    // bad escape in another field
		"user=a;b&password=hunter2", // semicolon separator
		"pass%zzword=hunter2&a=1",   // bad escape in a key
		"password=hunter2&note=100%",
	} {
		got := ProcessBody(body, formType, passwordOnly, nil, 1024, "request_", nil)
		if got != BodyUnmaskable {
			t.Errorf("ProcessBody(%q) = %q; want %q", body, got, BodyUnmaskable)
		}
	}
	// Without a strategy there is nothing to mask: the body is logged as-is.
	extraOnly := LowerExtraFields([]string{"id"})
	if got := ProcessBody("a=1;b=2", formType, nil, extraOnly, 1024, "request_", map[string]any{}); got != "a=1;b=2" {
		t.Errorf("unmasked malformed form = %q; want it as-is", got)
	}
}

// TestProcessBodyFormThatIsAlsoJSON: a body can parse as a form with a masked
// field and as JSON at once. Masking either view leaves the other raw.
func TestProcessBodyFormThatIsAlsoJSON(t *testing.T) {
	for _, body := range []string{`["&password=hunter2"]`, `{"a":"&password=hunter2&"}`} {
		if got := ProcessBody(body, formType, passwordOnly, nil, 1024, "request_", nil); got != BodyUnmaskable {
			t.Errorf("ProcessBody(%q) = %q; want %q", body, got, BodyUnmaskable)
		}
	}
	// A JSON body mislabelled as a form, whose form view has no masked field,
	// is still masked as JSON.
	got := ProcessBody(`{"password":"hunter2"}`, formType, passwordOnly, nil, 1024, "request_", nil)
	if got != `{"password":"`+hidden("hunter2")+`"}` {
		t.Errorf("mislabelled JSON = %q", got)
	}
}

// TestProcessBodyKeepsNumbers: numbers survive masking exactly, instead of
// being rounded through float64 (12345678901234567891 -> 12345678901234567000).
func TestProcessBodyKeepsNumbers(t *testing.T) {
	body := `{"order_id":12345678901234567891,"amount":0.1000,"exp":1e3,"password":"x"}`
	extra := map[string]any{}
	got := ProcessBody(body, "application/json", passwordOnly, LowerExtraFields([]string{"order_id"}), 1024, "request_", extra)
	want := `{"amount":0.1000,"exp":1e3,"order_id":12345678901234567891,"password":"` + hidden("x") + `"}`
	if got != want {
		t.Errorf("ProcessBody = %s\nwant          %s", got, want)
	}
	if n, ok := extra["request_order_id"].(json.Number); !ok || n.String() != "12345678901234567891" {
		t.Errorf("extra.request_order_id = %#v; want the exact json.Number", extra["request_order_id"])
	}
	// A masked number is masked from its exact literal.
	pin := LowerStrategies(map[string]gophlog.MaskingStrategy{"pin": gophlog.ShowLast2})
	want = `{"pin":"` + gophlog.MaskString("12345678901234567891", gophlog.ShowLast2) + `"}`
	if got := ProcessBody(`{"pin":12345678901234567891}`, "application/json", pin, nil, 1024, "request_", nil); got != want {
		t.Errorf("masked number = %s", got)
	}
}

// TestDecodeJSONMatchesUnmarshal: switching to a json.Decoder must not widen
// what counts as JSON — trailing data is rejected exactly as json.Unmarshal
// rejects it, so such bodies keep their documented as-is handling.
func TestDecodeJSONMatchesUnmarshal(t *testing.T) {
	for _, in := range []string{
		`{"a":1}`, " {\"a\":1}\n", `{"a":1}x`, `{"a":1}{"b":2}`, "{\"a\":1}\n{\"b\":2}",
		`{"a":1}]`, `{"a":1},`, `123abc`, `1 2`, `"s"`, `null`, `[1,]`, "\xef\xbb\xbf{}", ``, ` `,
	} {
		var v any
		want := json.Unmarshal([]byte(in), &v) == nil
		if _, got := decodeJSON(in); got != want {
			t.Errorf("decodeJSON(%q) ok = %v; json.Unmarshal ok = %v", in, got, want)
		}
	}
}

// TestProcessBodyDoesNotEscapeHTML: the body is rendered like the log line
// around it, with <, > and & kept literal instead of \u003c-escaped.
func TestProcessBodyDoesNotEscapeHTML(t *testing.T) {
	got := ProcessBody(`{"q":"a<b&c>d","password":"x"}`, "application/json", passwordOnly, nil, 1024, "request_", nil)
	if want := `{"password":"` + hidden("x") + `","q":"a<b&c>d"}`; got != want {
		t.Errorf("ProcessBody = %s; want %s", got, want)
	}
}

// TestProcessBodyPassesMarkersThrough: markers are fixed text, never capped or
// masked, even under a MaxBodySize shorter than the marker.
func TestProcessBodyPassesMarkersThrough(t *testing.T) {
	for _, m := range []string{BodyTooLarge, BodyStreaming, BodyEncoded, BodyUnmaskable} {
		if got := ProcessBody(m, formType, passwordOnly, nil, 4, "request_", nil); got != m {
			t.Errorf("marker %q came back as %q", m, got)
		}
	}
}

func TestIsIdentityEncoding(t *testing.T) {
	cases := []struct {
		values []string
		want   bool
	}{
		{nil, true},
		{[]string{""}, true},
		{[]string{"identity"}, true},
		{[]string{"IDENTITY"}, true},
		{[]string{" identity , identity "}, true},
		{[]string{"gzip"}, false},
		{[]string{"identity, gzip"}, false},
		{[]string{"br"}, false},
		{[]string{"x-unknown"}, false},
		{[]string{"identity", "zstd"}, false},
	}
	for _, c := range cases {
		h := http.Header{}
		for _, v := range c.values {
			h.Add("Content-Encoding", v)
		}
		if got := IsIdentityEncoding(h); got != c.want {
			t.Errorf("IsIdentityEncoding(%q) = %v; want %v", c.values, got, c.want)
		}
	}
}

func TestIsStreamingContentType(t *testing.T) {
	streaming := []string{
		"text/event-stream", "Text/Event-Stream; charset=utf-8",
		"application/grpc", "application/grpc+proto", "application/grpc-web+proto", "application/grpc-web-text",
		"application/connect+proto", "application/connect+json",
		"application/json;stream=watch", `application/vnd.kubernetes.protobuf; stream="watch"`,
	}
	for _, ct := range streaming {
		if !IsStreamingContentType(ct) {
			t.Errorf("IsStreamingContentType(%q) = false; want true", ct)
		}
	}
	for _, ct := range []string{"", "application/json", "application/proto", "text/plain", "application/x-ndjson", "application/json; charset=utf-8"} {
		if IsStreamingContentType(ct) {
			t.Errorf("IsStreamingContentType(%q) = true; want false", ct)
		}
	}
}

func TestIsCredentialHeader(t *testing.T) {
	for _, h := range []string{
		"Authorization", "proxy-authorization", "Cookie", "Set-Cookie", "X-Api-Key", "Api-Key",
		"X-Auth-Token", "X-Access-Token", "X-Session-Token",
		"Private-Token", "X-Goog-Api-Key", "X-Amz-Security-Token", "Ocp-Apim-Subscription-Key",
		"Apikey", "APIKEY", "X-Csrf-Token", "X-XSRF-TOKEN",
	} {
		if !IsCredentialHeader(h) {
			t.Errorf("IsCredentialHeader(%q) = false; want true", h)
		}
	}
	for _, h := range []string{"Content-Type", "X-Request-Id", "Accept"} {
		if IsCredentialHeader(h) {
			t.Errorf("IsCredentialHeader(%q) = true; want false", h)
		}
	}
}

// TestCaptureBodyEmptyIsNonNil pins the contract the httpclient relies on to
// tell an empty body from a failed read.
func TestCaptureBodyEmptyIsNonNil(t *testing.T) {
	captured, _, truncated := CaptureBody(io.NopCloser(strings.NewReader("")), 100)
	if captured == nil || len(captured) != 0 || truncated {
		t.Errorf("empty body: captured=%#v truncated=%v; want non-nil empty", captured, truncated)
	}
}

// onlyUnderMaskedKey reports whether secret appears in the form view only as
// the value of a masked key; anywhere else it is legitimately loggable.
func onlyUnderMaskedKey(view url.Values, secret string, lower map[string]gophlog.MaskingStrategy) bool {
	for key, vals := range view {
		if strings.Contains(key, secret) {
			return false
		}
		if _, masked := lower[strings.ToLower(key)]; masked {
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

// FuzzFormMask: whatever surrounds it, a value the handler's r.ParseForm reads
// under a masked key must never reach the logged body.
func FuzzFormMask(f *testing.F) {
	for _, seed := range [][2]string{
		{"user=a", "x=1"},
		{"user=a", "x=%zz"},
		{"user=a;b", "x=1"},
		{`["`, `"]`},
		{`{"a":"`, `"}`},
		{"", ""},
		{"password=first", "PASSWORD=third"},
		{"a=%", "b=%2"},
	} {
		f.Add(seed[0], seed[1], uint8(0))
	}
	keys := []string{"password", "PASSWORD", "Password", "pass%77ord", "%70assword"}
	f.Fuzz(func(t *testing.T, pre, post string, keySel uint8) {
		const secret = "S3CR3TVALUEZZZ"
		key := keys[int(keySel)%len(keys)]
		body := pre + "&" + key + "=" + secret + "&" + post

		// The handler's view (r.ParseForm keeps the pairs it could parse).
		view, _ := url.ParseQuery(body)
		if !onlyUnderMaskedKey(view, secret, passwordOnly) {
			return // pre/post put the token somewhere it is not masked
		}
		out := ProcessBody(body, formType, passwordOnly, nil, 1<<20, "request_", nil)
		if strings.Contains(out, secret) {
			t.Fatalf("masked value leaked for body %q:\n%s", body, out)
		}
	})
}
