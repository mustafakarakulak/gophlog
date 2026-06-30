// Package httplog holds helpers shared by the middleware and httpclient
// subpackages: path/URL pattern matching, JSON body formatting and extra-field
// extraction. It is internal to the module.
package httplog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"

	logging "github.com/mustafakarakulak/go-logging"
)

// CaptureBody reads at most max+1 bytes from body for logging while preserving
// the full original stream for downstream consumers.
//
// It returns the captured bytes, a ReadCloser that still yields the COMPLETE
// original body, and whether the body exceeded max (truncated). When truncated,
// memory use is bounded to ~max bytes — the unread remainder is streamed lazily
// from the original body via io.MultiReader, so large uploads/downloads are not
// buffered in full. Closing the returned reader always closes the original
// body, so callers can simply replace the body and forget the original.
func CaptureBody(body io.ReadCloser, max int) (captured []byte, restored io.ReadCloser, truncated bool) {
	if max < 0 {
		max = 0
	}
	buf, _ := io.ReadAll(io.LimitReader(body, int64(max)+1))
	if len(buf) > max {
		// More data may remain; stitch the peeked bytes back in front and keep
		// the original open so the remainder can still be streamed and closed.
		r := io.MultiReader(bytes.NewReader(buf), body)
		return buf[:max], &readCloser{Reader: r, closer: body}, true
	}
	// Fully read: the original is drained and can be closed immediately.
	_ = body.Close()
	return buf, io.NopCloser(bytes.NewReader(buf)), false
}

type readCloser struct {
	io.Reader
	closer io.Closer
}

func (rc *readCloser) Close() error { return rc.closer.Close() }

// MatchPattern reports whether value matches pattern. A trailing "/*" or "*"
// is treated as a prefix wildcard; otherwise an exact or prefix match is used.
func MatchPattern(value, pattern string) bool {
	if pattern == "" {
		return false
	}
	if strings.EqualFold(value, pattern) {
		return true
	}
	if strings.HasSuffix(pattern, "/*") {
		prefix := pattern[:len(pattern)-2]
		return strings.HasPrefix(strings.ToLower(value), strings.ToLower(prefix))
	}
	if strings.HasSuffix(pattern, "*") {
		prefix := pattern[:len(pattern)-1]
		return strings.HasPrefix(strings.ToLower(value), strings.ToLower(prefix))
	}
	return strings.HasPrefix(strings.ToLower(value), strings.ToLower(pattern))
}

// ShouldExclude reports whether value matches any exclusion pattern.
func ShouldExclude(value string, patterns []string) bool {
	for _, p := range patterns {
		if MatchPattern(value, p) {
			return true
		}
	}
	return false
}

// ShouldInclude reports whether value matches the inclusion patterns. An empty
// pattern list includes everything.
func ShouldInclude(value string, patterns []string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if MatchPattern(value, p) {
			return true
		}
	}
	return false
}

// FormatJSON compacts a JSON body for consistent logging. Non-JSON input is
// returned unchanged.
func FormatJSON(body string) string {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return body
	}
	var v any
	if err := json.Unmarshal([]byte(trimmed), &v); err != nil {
		return body
	}
	out, err := json.Marshal(v)
	if err != nil {
		return body
	}
	return string(out)
}

// CapBody truncates body to max bytes, appending a marker when truncated.
func CapBody(body string, max int) string {
	if max <= 0 || len(body) <= max {
		return body
	}
	return body[:max] + "... [truncated]"
}

// MaskQueryValues masks, in place, every value of q whose key matches a
// strategy (case-insensitive). Keys without a matching strategy are left
// untouched. It is used to keep secrets (tokens, passwords) in query strings
// from being logged in clear text.
func MaskQueryValues(q url.Values, strategies map[string]logging.MaskingStrategy) {
	if len(q) == 0 || len(strategies) == 0 {
		return
	}
	lower := make(map[string]logging.MaskingStrategy, len(strategies))
	for k, s := range strategies {
		lower[strings.ToLower(k)] = s
	}
	for key, vals := range q {
		if s, ok := lower[strings.ToLower(key)]; ok {
			for i := range vals {
				vals[i] = logging.MaskString(vals[i], s)
			}
		}
	}
}

// MaskFormBody parses a application/x-www-form-urlencoded body, masks the values
// whose keys match a strategy, and re-encodes it. The original body is returned
// unchanged when it does not parse as a form. This closes the gap where masking
// only covered JSON bodies.
func MaskFormBody(body string, strategies map[string]logging.MaskingStrategy) (string, bool) {
	values, err := url.ParseQuery(body)
	if err != nil || len(values) == 0 {
		return body, false
	}
	MaskQueryValues(values, strategies)
	return values.Encode(), true
}

// RenderQuery renders url.Values as a sorted "k=v&k=v" string for log display.
// Unlike url.Values.Encode it does not percent-escape values, so a masked value
// stays readable (e.g. "token=********" rather than "token=%2A%2A..."). It is
// for log output only, never for issuing a real request.
func RenderQuery(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	first := true
	for _, k := range keys {
		for _, v := range q[k] {
			if !first {
				b.WriteByte('&')
			}
			first = false
			b.WriteString(k)
			b.WriteByte('=')
			b.WriteString(v)
		}
	}
	return b.String()
}

// JoinQuery flattens url.Values into a single-valued map, joining repeated
// values with a comma, for the structured query_params log field.
func JoinQuery(q url.Values) map[string]string {
	if len(q) == 0 {
		return nil
	}
	out := make(map[string]string, len(q))
	for k, v := range q {
		out[k] = strings.Join(v, ",")
	}
	return out
}

// CollectExtra recursively searches decoded JSON for the named fields
// (case-insensitive) and copies their values into extra, keyed prefix+field.
func CollectExtra(decoded any, fields []string, prefix string, extra map[string]any) {
	if len(fields) == 0 {
		return
	}
	want := make(map[string]string, len(fields))
	for _, f := range fields {
		want[strings.ToLower(f)] = f
	}
	collect(decoded, want, prefix, extra)
}

func collect(v any, want map[string]string, prefix string, extra map[string]any) {
	switch node := v.(type) {
	case map[string]any:
		for key, val := range node {
			if orig, ok := want[strings.ToLower(key)]; ok {
				extra[prefix+orig] = val
			}
			collect(val, want, prefix, extra)
		}
	case []any:
		for _, item := range node {
			collect(item, want, prefix, extra)
		}
	}
}

// Message renders the standard "METHOD path - status (durationms)" log message.
func Message(method, path string, status int, durationMs float64) string {
	return fmt.Sprintf("%s %s - %d (%.2fms)", method, path, status, durationMs)
}
