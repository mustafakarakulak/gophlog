package gophlog

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
)

// MaskingStrategy defines how a sensitive value is partially or fully masked.
//
// For the fully hidden ("hideall") strategy the masked portion is capped at 8
// asterisks so the output never reveals the secret's length.
type MaskingStrategy string

const (
	// HideAll masks the entire value. Example: "12345678932" -> "********"
	HideAll MaskingStrategy = "hideall"
	// MaskAll is an alias for HideAll.
	//
	// Deprecated: use HideAll, which matches the underlying tag value
	// ("hideall"). MaskAll is kept for compatibility and will not be removed,
	// but new code should prefer HideAll.
	MaskAll MaskingStrategy = "hideall"
	// ShowFirst1 shows only the first character. Example: "12345678932" -> "1********"
	ShowFirst1 MaskingStrategy = "showfirst1"
	// ShowLast1 shows only the last character. Example: "12345678932" -> "**********2"
	ShowLast1 MaskingStrategy = "showlast1"
	// ShowFirst2 shows the first 2 characters. Example: "12345678932" -> "12********"
	ShowFirst2 MaskingStrategy = "showfirst2"
	// ShowLast2 shows the last 2 characters. Example: "12345678932" -> "*********32"
	ShowLast2 MaskingStrategy = "showlast2"
	// ShowFirst1AndLast1 shows the first and last character. Example: "1********2"
	ShowFirst1AndLast1 MaskingStrategy = "showfirst1andlast1"
	// ShowFirst2AndLast2 shows the first 2 and last 2 characters. Example: "12*******32"
	ShowFirst2AndLast2 MaskingStrategy = "showfirst2andlast2"
	// CreditCard shows the first 6 and last 4 digits (BIN + last four).
	// Example: "5101521234564582" -> "510152 ****** 4582" (grouped).
	CreditCard MaskingStrategy = "creditcard"
)

// parseStrategy converts a struct-tag value to a MaskingStrategy.
// Returns (strategy, true) when recognised.
func parseStrategy(s string) (MaskingStrategy, bool) {
	switch MaskingStrategy(strings.ToLower(strings.TrimSpace(s))) {
	case HideAll:
		return HideAll, true
	case ShowFirst1:
		return ShowFirst1, true
	case ShowLast1:
		return ShowLast1, true
	case ShowFirst2:
		return ShowFirst2, true
	case ShowLast2:
		return ShowLast2, true
	case ShowFirst1AndLast1:
		return ShowFirst1AndLast1, true
	case ShowFirst2AndLast2:
		return ShowFirst2AndLast2, true
	case CreditCard:
		return CreditCard, true
	default:
		return "", false
	}
}

// MaskString applies a masking strategy to a raw string value.
//
// Every strategy is fail-closed: when the value is too short for the strategy to
// hide anything meaningful, the whole value is hidden instead of leaking in the
// clear. For example ShowLast2 hides a 2-character value entirely rather than
// revealing it.
func MaskString(value string, strategy MaskingStrategy) string {
	if value == "" {
		return value
	}
	r := []rune(value)
	n := len(r)

	switch strategy {
	case CreditCard:
		return maskCreditCard(value)
	case ShowFirst1:
		if n <= 1 {
			return maskFixed(r, 0)
		}
		return string(r[0]) + maskFixed(r[1:], 0)
	case ShowLast1:
		return maskFixed(r, 1)
	case ShowFirst2:
		if n <= 2 {
			return maskFixed(r, 0)
		}
		return string(r[:2]) + maskFixed(r[2:], 0)
	case ShowLast2:
		return maskFixed(r, 2)
	case ShowFirst1AndLast1:
		if n <= 2 {
			return maskFixed(r, 0)
		}
		return string(r[0]) + maskFixed(r[1:n-1], 0) + string(r[n-1])
	case ShowFirst2AndLast2:
		if n <= 4 {
			return maskFixed(r, 0)
		}
		return string(r[:2]) + maskFixed(r[2:n-2], 0) + string(r[n-2:])
	case HideAll:
		fallthrough
	default:
		return maskFixed(r, 0)
	}
}

// maskFixed masks all but the last visibleChars runes of r.
//
// It is fail-closed: when visibleChars is <= 0, or the value is so short that
// keeping visibleChars runes would leave nothing hidden, the whole value is
// hidden — capped at 8 asterisks so the output never reveals the secret's
// length.
func maskFixed(r []rune, visibleChars int) string {
	n := len(r)
	if n == 0 {
		return ""
	}
	if visibleChars <= 0 || n <= visibleChars {
		count := n
		if count > 8 {
			count = 8
		}
		return strings.Repeat("*", count)
	}
	return strings.Repeat("*", n-visibleChars) + string(r[n-visibleChars:])
}

// creditCardCleaner strips common card-number separators. It is built once
// because strings.NewReplacer constructs an internal lookup structure that is
// expensive to allocate on every call.
var creditCardCleaner = strings.NewReplacer(" ", "", "-", "", "_", "")

// maskCreditCard masks a card number, keeping the first 6 (BIN) and last 4
// digits visible and regrouping the result for readability.
//
// Real card numbers are 12–19 digits. A shorter value cannot be reduced to
// "BIN + last four" without revealing most of it, so it is hidden entirely
// (fail-closed) rather than partially leaked.
func maskCreditCard(value string) string {
	if value == "" {
		return value
	}
	cleaned := creditCardCleaner.Replace(value)
	cr := []rune(cleaned)
	cn := len(cr)

	if cn < 12 {
		return maskFixed(cr, 0)
	}

	// Masked layout: first 6 (BIN) + stars + last 4, regrouped as
	// "dddd dd **** ... dddd". Positions 6..cn-4 are always stars, so the
	// grouped output can be written in one pass without building the unmasked
	// intermediate string.
	var b strings.Builder
	b.Grow(cn + cn/2)
	b.WriteString(string(cr[:4]))
	b.WriteByte(' ')
	b.WriteString(string(cr[4:6]))
	for index, last4Start := 6, cn-4; index < last4Start; {
		segLen := 4
		if last4Start-index < segLen {
			segLen = last4Start - index
		}
		b.WriteByte(' ')
		for j := 0; j < segLen; j++ {
			b.WriteByte('*')
		}
		index += segLen
	}
	b.WriteByte(' ')
	b.WriteString(string(cr[cn-4:]))
	return b.String()
}

// maskScalar masks a scalar value. The masked result is always a string, so a
// masked number is emitted as a JSON string rather than a number.
//
// Decoded-JSON containers (map[string]any / []any) are returned unchanged —
// callers deep-mask them via maskScalarOrRecurse. Every other type is masked
// fail-closed: named scalar types and non-standard numeric widths are rendered
// via reflection, opaque values (json.Marshaler implementations such as
// time.Time) are rendered to JSON first, and a value that cannot be rendered at
// all is fully hidden rather than logged in the clear.
func maskScalar(value any, strategy MaskingStrategy) any {
	switch v := value.(type) {
	case nil:
		return nil
	case string:
		if v == "" {
			return v
		}
		return MaskString(v, strategy)
	case bool, float64, float32, int, int64, json.Number:
		return MaskString(scalarToString(v), strategy)
	case map[string]any, []any:
		return value
	}

	// Named scalar types and the remaining numeric widths.
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Bool:
		return MaskString(strconv.FormatBool(rv.Bool()), strategy)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return MaskString(strconv.FormatInt(rv.Int(), 10), strategy)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return MaskString(strconv.FormatUint(rv.Uint(), 10), strategy)
	case reflect.Float32, reflect.Float64:
		return MaskString(strconv.FormatFloat(rv.Float(), 'f', -1, 64), strategy)
	case reflect.String:
		if rv.String() == "" {
			return ""
		}
		return MaskString(rv.String(), strategy)
	}

	// Opaque values (json.Marshaler, time.Time, ...): mask the rendered JSON
	// text, unquoting strings so quote characters never count as visible chars.
	b, err := json.Marshal(value)
	if err != nil {
		return "********"
	}
	s := string(b)
	if len(s) >= 2 && s[0] == '"' {
		var u string
		if json.Unmarshal(b, &u) == nil {
			s = u
		}
	}
	if s == "null" {
		return nil
	}
	if s == "" {
		return s
	}
	return MaskString(s, strategy)
}

// MaskJSON walks a decoded JSON value (map[string]any / []any / scalar) and
// masks any field whose name matches one of the provided strategies
// (case-insensitive), recursing into nested objects and arrays. It is the
// exported entry point used by the middleware and httpclient subpackages.
func MaskJSON(value any, strategies map[string]MaskingStrategy) any {
	return applyMaskingToJSON(value, strategies)
}

// applyMaskingToJSON walks a decoded JSON value (map / slice / scalar) and
// masks any field whose name matches one of the provided strategies
// (case-insensitive), recursing into nested objects and arrays.
func applyMaskingToJSON(value any, strategies map[string]MaskingStrategy) any {
	if len(strategies) == 0 {
		return value
	}
	// Build a lower-cased lookup once.
	lower := make(map[string]MaskingStrategy, len(strategies))
	for k, s := range strategies {
		lower[strings.ToLower(k)] = s
	}
	return applyMaskingLower(value, lower)
}

func applyMaskingLower(value any, lower map[string]MaskingStrategy) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, val := range v {
			if strategy, ok := lower[strings.ToLower(key)]; ok {
				out[key] = maskScalarOrRecurse(val, strategy)
			} else if isContainer(val) {
				out[key] = applyMaskingLower(val, lower)
			} else {
				out[key] = val
			}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = applyMaskingLower(item, lower)
		}
		return out
	default:
		return value
	}
}

// applyMaskingLowerInPlace is applyMaskingLower for trees the caller owns
// exclusively — freshly built by processPayload, where every map/slice node was
// just allocated and nothing else can observe it. Masked values are written
// back in place, so an unmatched subtree costs nothing instead of a full
// reallocation. It must never run on caller-supplied data; the exported
// MaskJSON keeps the copying walk for exactly that reason.
func applyMaskingLowerInPlace(value any, lower map[string]MaskingStrategy) {
	switch v := value.(type) {
	case map[string]any:
		for key, val := range v {
			if strategy, ok := lower[strings.ToLower(key)]; ok {
				v[key] = maskScalarOrRecurseInPlace(val, strategy)
			} else if isContainer(val) {
				applyMaskingLowerInPlace(val, lower)
			}
		}
	case []any:
		for _, item := range v {
			applyMaskingLowerInPlace(item, lower)
		}
	}
}

// maskScalarOrRecurse masks scalars; when the strategy targets a field that
// holds an object or array, every scalar leaf underneath it is masked with the
// same strategy, so an explicitly-targeted container can never leak values
// through field names the strategy map does not know about.
func maskScalarOrRecurse(val any, strategy MaskingStrategy) any {
	switch v := val.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = maskScalarOrRecurse(item, strategy)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = maskScalarOrRecurse(item, strategy)
		}
		return out
	default:
		return maskScalar(val, strategy)
	}
}

// maskScalarOrRecurseInPlace is maskScalarOrRecurse for exclusively-owned
// trees: targeted containers are masked leaf-by-leaf in place instead of being
// copied node by node.
func maskScalarOrRecurseInPlace(val any, strategy MaskingStrategy) any {
	switch v := val.(type) {
	case map[string]any:
		for key, item := range v {
			v[key] = maskScalarOrRecurseInPlace(item, strategy)
		}
		return v
	case []any:
		for i, item := range v {
			v[i] = maskScalarOrRecurseInPlace(item, strategy)
		}
		return v
	default:
		return maskScalar(val, strategy)
	}
}

func isContainer(v any) bool {
	switch v.(type) {
	case map[string]any, []any:
		return true
	default:
		return false
	}
}
