// Package httplog holds helpers shared by the middleware and httpclient
// subpackages: path/URL pattern matching, body capture and masking, credential
// header redaction and extra-field extraction. It is internal to the module.
package httplog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/mustafakarakulak/gophlog"
)

// Markers logged in place of a body that is deliberately not captured or not
// logged. Each is a fixed string, never body content, so masking can never be
// bypassed through a body the logger could not fully see or safely parse. The
// real body always reaches the handler / caller untouched.
const (
	// BodyTooLarge replaces a body that exceeds MaxBodySize.
	BodyTooLarge = "[body not logged: exceeds MaxBodySize]"
	// BodyStreaming replaces a streaming body (Server-Sent Events, gRPC, a
	// 101 Switching Protocols connection): reading it up front would block
	// until the stream ends, and replacing it would break the stream.
	BodyStreaming = "[body not logged: streaming]"
	// BodyEncoded replaces a body whose Content-Encoding is not identity
	// (gzip, br, ...): masking cannot see into the encoded bytes, and a short
	// "compressed" body is often a stored block carrying the plain text.
	BodyEncoded = "[body not logged: non-identity Content-Encoding]"
	// BodyUnmaskable replaces a body that masking was asked to cover but that
	// could not be parsed unambiguously — e.g. a malformed form-urlencoded body.
	BodyUnmaskable = "[body not logged: cannot be masked]"
)

// IsMarker reports whether body is one of the fixed markers above.
func IsMarker(body string) bool {
	switch body {
	case BodyTooLarge, BodyStreaming, BodyEncoded, BodyUnmaskable:
		return true
	}
	return false
}

// IsIdentityEncoding reports whether h leaves the body as-is: no
// Content-Encoding, an empty one, or only "identity" tokens. Anything else —
// including an encoding this package does not recognise — counts as encoded,
// fail-closed.
func IsIdentityEncoding(h http.Header) bool {
	for _, v := range h.Values("Content-Encoding") {
		for rest := v; rest != ""; {
			var token string
			token, rest, _ = strings.Cut(rest, ",")
			if t := strings.TrimSpace(token); t != "" && !strings.EqualFold(t, "identity") {
				return false
			}
		}
	}
	return true
}

// IsStreamingContentType reports whether contentType names a streaming body:
// one that stays open for as long as the peer wants, so reading it up front
// (to log it) would stall the exchange until the stream ends.
//
//   - text/event-stream: Server-Sent Events.
//   - application/grpc, application/grpc+proto, application/grpc-web, ...:
//     gRPC and gRPC-Web, whose streaming calls share the unary content type.
//   - application/connect+proto, application/connect+json: the Connect
//     protocol's streaming variants (unary Connect calls use application/proto
//     and application/json and are still captured).
//   - any media type with a stream=watch parameter: Kubernetes watch responses
//     (application/json;stream=watch).
func IsStreamingContentType(contentType string) bool {
	if contentType == "" {
		return false
	}
	mediaType, params, _ := strings.Cut(contentType, ";")
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if mediaType == "text/event-stream" ||
		strings.HasPrefix(mediaType, "application/grpc") ||
		strings.HasPrefix(mediaType, "application/connect+") {
		return true
	}
	for params != "" {
		var param string
		param, params, _ = strings.Cut(params, ";")
		name, value, _ := strings.Cut(param, "=")
		if strings.EqualFold(strings.TrimSpace(name), "stream") &&
			strings.EqualFold(strings.Trim(strings.TrimSpace(value), `"`), "watch") {
			return true
		}
	}
	return false
}

// credentialHeaders carry credentials and are never written verbatim — the
// curl dump replaces them with [REDACTED]. Names are lower-case for
// case-insensitive matching.
var credentialHeaders = map[string]struct{}{
	"authorization":             {},
	"proxy-authorization":       {},
	"cookie":                    {},
	"set-cookie":                {},
	"x-api-key":                 {},
	"api-key":                   {},
	"apikey":                    {}, // Kong, Supabase
	"x-auth-token":              {},
	"x-access-token":            {},
	"x-session-token":           {},
	"private-token":             {}, // GitLab
	"x-goog-api-key":            {}, // Google APIs
	"x-amz-security-token":      {}, // AWS STS session token
	"ocp-apim-subscription-key": {}, // Azure API Management
	"x-csrf-token":              {},
	"x-xsrf-token":              {},
}

// IsCredentialHeader reports whether the header name (any case) carries
// credentials and must never be written verbatim.
func IsCredentialHeader(name string) bool {
	_, ok := credentialHeaders[strings.ToLower(name)]
	return ok
}

// CaptureBody reads at most limit+1 bytes from body for logging while
// preserving the full original stream for downstream consumers.
//
// It returns the captured bytes, a ReadCloser that still yields the COMPLETE
// original body, and whether the body exceeded limit (truncated). When
// truncated, memory use is bounded to ~limit bytes — the unread remainder is
// streamed lazily from the original body via io.MultiReader, so large
// uploads/downloads are not buffered in full. Closing the returned reader
// always closes the original body, so callers can simply replace the body and
// forget the original.
//
// When the body fails mid-read, nothing is captured (a partial, unparseable
// fragment could bypass masking) and the restored reader replays what was read
// followed by the SAME error — the consumer must see the failure, not a clean
// EOF over silently truncated data. captured is nil exactly in that case; a
// body that was read successfully but is empty yields a non-nil, empty slice.
func CaptureBody(body io.ReadCloser, limit int) (captured []byte, restored io.ReadCloser, truncated bool) {
	if limit < 0 {
		limit = 0
	}
	buf, err := io.ReadAll(io.LimitReader(body, int64(limit)+1))
	if err != nil {
		r := io.MultiReader(bytes.NewReader(buf), errReader{err: err})
		return nil, &readCloser{Reader: r, closer: body}, false
	}
	if len(buf) > limit {
		// More data may remain; stitch the peeked bytes back in front and keep
		// the original open so the remainder can still be streamed and closed.
		r := io.MultiReader(bytes.NewReader(buf), body)
		return buf[:limit], &readCloser{Reader: r, closer: body}, true
	}
	// Fully read: the original is drained and can be closed immediately.
	_ = body.Close()
	return buf, io.NopCloser(bytes.NewReader(buf)), false
}

// errReader replays a mid-stream read failure to the consumer of a captured
// body.
type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

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

// CapBody truncates body to limit bytes, appending a marker when truncated.
// The cut is backed up to a rune boundary so a multi-byte character is never
// split. The marker is additive: output may exceed limit by its length, the cap
// applies to the retained content.
func CapBody(body string, limit int) string {
	if limit <= 0 || len(body) <= limit {
		return body
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(body[cut]) {
		cut--
	}
	return body[:cut] + "... [truncated]"
}

// LowerStrategies returns a copy of strategies keyed by lower-cased field name,
// or nil for an empty input. The middleware and httpclient constructors call it
// once — their options are static after construction — so the per-request
// masking paths look keys up directly instead of rebuilding the map every time.
func LowerStrategies(strategies map[string]gophlog.MaskingStrategy) map[string]gophlog.MaskingStrategy {
	if len(strategies) == 0 {
		return nil
	}
	lower := make(map[string]gophlog.MaskingStrategy, len(strategies))
	for k, s := range strategies {
		lower[strings.ToLower(k)] = s
	}
	return lower
}

// MaskQueryValues masks, in place, every value of q whose key matches a
// strategy (case-insensitive). Keys without a matching strategy are left
// untouched. It is used to keep secrets (tokens, passwords) in query strings
// from being logged in clear text.
func MaskQueryValues(q url.Values, strategies map[string]gophlog.MaskingStrategy) {
	if len(q) == 0 || len(strategies) == 0 {
		return
	}
	MaskQueryValuesLower(q, LowerStrategies(strategies))
}

// MaskQueryValuesLower is MaskQueryValues taking a pre-lowered strategy map
// (from LowerStrategies), so per-request callers skip rebuilding the lookup.
func MaskQueryValuesLower(q url.Values, lower map[string]gophlog.MaskingStrategy) {
	if len(q) == 0 || len(lower) == 0 {
		return
	}
	for key, vals := range q {
		if s, ok := lower[strings.ToLower(key)]; ok {
			for i := range vals {
				vals[i] = gophlog.MaskString(vals[i], s)
			}
		}
	}
}

// ProcessBody prepares a captured body for the log line: it masks the FULL
// body, lifts the LogExtraFields (extraWant, from LowerExtraFields) into extra
// keyed prefix+field, and only then caps the result at limit bytes. Masking
// before truncation keeps sensitive fields from leaking through a truncated,
// unparseable body. lower is the pre-lowered map from LowerStrategies.
//
// A JSON body is decoded exactly once. Numbers stay json.Number, so integers
// beyond 2^53 and the exact literal of a decimal survive the round trip, and
// <, > and & stay literal, as in the rest of the log line. Trailing data after
// the value (NDJSON, garbage) makes the body "not JSON", as with
// json.Unmarshal.
//
// A form-urlencoded body (by Content-Type) is masked as the handler's
// r.ParseForm sees it. When masking is configured but the form view cannot be
// masked unambiguously — the body does not parse as a form, or it parses both
// as a form with a masked field and as JSON — BodyUnmaskable is logged
// instead. Any other body is logged as-is, capped. Markers pass through.
func ProcessBody(body, contentType string, lower map[string]gophlog.MaskingStrategy, extraWant map[string]string, limit int, prefix string, extra map[string]any) string {
	if body == "" {
		return ""
	}
	// A marker is fixed text, not body content — never mask or cap it (a tiny
	// MaxBodySize would otherwise truncate the marker itself).
	if IsMarker(body) {
		return body
	}
	// Nothing to mask and nothing to lift into extra: the decode/encode round
	// trip below would only re-serialize the body, so log it as-is (capped).
	if len(lower) == 0 && len(extraWant) == 0 {
		return CapBody(body, limit)
	}

	// formValues holds a form with no masked field; it is logged only when the
	// body is not JSON either.
	var formValues url.Values
	if isFormContentType(contentType) {
		values, err := url.ParseQuery(body)
		switch {
		case err != nil:
			// url.ParseQuery (and so r.ParseForm) returns the pairs it could
			// parse alongside the error, but a segment it rejects — "x=%zz",
			// "a=1;password=..." — may well be read differently by another
			// parser. With masking asked for, the only safe view is none.
			if len(lower) > 0 {
				return BodyUnmaskable
			}
		case hasStrategyKey(values, lower):
			// A body can be a valid form and valid JSON at once
			// (["&password=..."]); masking one view would leave the other raw.
			if json.Valid([]byte(body)) {
				return BodyUnmaskable
			}
			MaskQueryValuesLower(values, lower)
			return CapBody(values.Encode(), limit)
		case len(values) > 0:
			formValues = values
		}
	}

	decoded, ok := decodeJSON(body)
	if !ok {
		if formValues != nil {
			return CapBody(formValues.Encode(), limit)
		}
		return CapBody(body, limit) // not JSON; log as-is
	}

	// Masking runs BEFORE extra extraction, so a field named in both
	// MaskFieldStrategies and LogExtraFields is lifted in its masked form —
	// the extra object must never carry a value the body already hides.
	// decoded is exclusively owned (fresh from the decoder), so it is masked in
	// place instead of paying MaskJSON's defensive deep copy.
	MaskDecodedInPlace(decoded, lower)
	CollectExtraLower(decoded, extraWant, prefix, extra)

	out, err := encodeJSON(decoded)
	if err != nil {
		// Unreachable for decoder output, but once masking was asked for the
		// raw body must never be the fallback.
		return BodyUnmaskable
	}
	return CapBody(out, limit)
}

// decodeJSON decodes body as a single JSON value, keeping numbers as
// json.Number. Like json.Unmarshal, it rejects trailing data after the value.
func decodeJSON(body string) (any, bool) {
	dec := json.NewDecoder(strings.NewReader(body))
	dec.UseNumber()
	var decoded any
	if err := dec.Decode(&decoded); err != nil {
		return nil, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, false
	}
	return decoded, true
}

// encodeJSON renders v compactly without HTML escaping, matching the log
// line's own encoder so a body reads the same as the envelope around it.
func encodeJSON(v any) (string, error) {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

// hasStrategyKey reports whether any key of q has a masking strategy.
func hasStrategyKey(q url.Values, lower map[string]gophlog.MaskingStrategy) bool {
	if len(lower) == 0 {
		return false
	}
	for key := range q {
		if _, ok := lower[strings.ToLower(key)]; ok {
			return true
		}
	}
	return false
}

func isFormContentType(contentType string) bool {
	return strings.Contains(strings.ToLower(contentType), "application/x-www-form-urlencoded")
}

// MaskDecodedInPlace masks, in place, a value freshly produced by
// json.Unmarshal or a json.Decoder (map[string]any / []any / scalar), using a
// pre-lowered strategy map from LowerStrategies. It produces exactly what
// gophlog.MaskJSON produces for that domain while skipping MaskJSON's
// defensive deep copy — the middleware and httpclient own their decoded bodies
// outright, so nothing else can observe the mutation. It must never run on data
// a caller may still hold.
func MaskDecodedInPlace(decoded any, lower map[string]gophlog.MaskingStrategy) {
	if len(lower) == 0 {
		return
	}
	switch node := decoded.(type) {
	case map[string]any:
		for key, val := range node {
			if strategy, ok := lower[strings.ToLower(key)]; ok {
				node[key] = maskDecodedValue(val, strategy)
			} else {
				MaskDecodedInPlace(val, lower)
			}
		}
	case []any:
		for _, item := range node {
			MaskDecodedInPlace(item, lower)
		}
	}
}

// maskDecodedValue masks one matched value. Containers are masked leaf-by-leaf
// in place, mirroring gophlog's rule that an explicitly-targeted container may
// never leak values through field names the strategy map does not know about.
// json.Unmarshal only ever yields nil/bool/float64/string leaves (plus
// json.Number under UseNumber); anything else is hidden outright, fail-closed.
func maskDecodedValue(val any, strategy gophlog.MaskingStrategy) any {
	switch v := val.(type) {
	case map[string]any:
		for key, item := range v {
			v[key] = maskDecodedValue(item, strategy)
		}
		return v
	case []any:
		for i, item := range v {
			v[i] = maskDecodedValue(item, strategy)
		}
		return v
	case nil:
		return nil
	case string:
		if v == "" {
			return v
		}
		return gophlog.MaskString(v, strategy)
	case bool:
		return gophlog.MaskString(strconv.FormatBool(v), strategy)
	case float64:
		return gophlog.MaskString(strconv.FormatFloat(v, 'f', -1, 64), strategy)
	case json.Number:
		return gophlog.MaskString(v.String(), strategy)
	default:
		return "********"
	}
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

// LowerExtraFields builds the case-insensitive lookup CollectExtra derives from
// its field list (lower-cased name -> original name), or nil for an empty list.
// Constructors with a static field list build it once instead of per body.
func LowerExtraFields(fields []string) map[string]string {
	if len(fields) == 0 {
		return nil
	}
	want := make(map[string]string, len(fields))
	for _, f := range fields {
		want[strings.ToLower(f)] = f
	}
	return want
}

// CollectExtra recursively searches decoded JSON for the named fields
// (case-insensitive) and copies their values into extra, keyed prefix+field.
func CollectExtra(decoded any, fields []string, prefix string, extra map[string]any) {
	CollectExtraLower(decoded, LowerExtraFields(fields), prefix, extra)
}

// CollectExtraLower is CollectExtra taking a pre-built lookup from
// LowerExtraFields.
func CollectExtraLower(decoded any, want map[string]string, prefix string, extra map[string]any) {
	if len(want) == 0 {
		return
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
