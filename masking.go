package logging

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
	// MaskAll masks the entire value. Example: "12345678932" -> "********"
	MaskAll MaskingStrategy = "hideall"
	// HideAll is an alias for MaskAll.
	HideAll MaskingStrategy = "hideall"
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
	case MaskAll:
		return MaskAll, true
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
	case MaskAll:
		fallthrough
	default:
		return maskFixed(r, 0)
	}
}

// maskFixed masks all but the last visibleChars runes of r. When visibleChars
// is <= 0 the whole value is hidden, capped at 8 asterisks.
func maskFixed(r []rune, visibleChars int) string {
	n := len(r)
	if n == 0 {
		return ""
	}
	if visibleChars <= 0 {
		count := n
		if count > 8 {
			count = 8
		}
		return strings.Repeat("*", count)
	}
	if n <= visibleChars {
		stars := n - 1
		if stars < 1 {
			stars = 1
		}
		return strings.Repeat("*", stars) + string(r[n-1])
	}
	return strings.Repeat("*", n-visibleChars) + string(r[n-visibleChars:])
}

// creditCardCleaner strips common card-number separators. It is built once
// because strings.NewReplacer constructs an internal lookup structure that is
// expensive to allocate on every call.
var creditCardCleaner = strings.NewReplacer(" ", "", "-", "", "_", "")

// maskCreditCard masks a card number, keeping the first 6 (BIN) and last 4
// digits visible and regrouping the result for readability.
func maskCreditCard(value string) string {
	if value == "" {
		return value
	}
	cleaned := creditCardCleaner.Replace(value)
	cr := []rune(cleaned)
	cn := len(cr)

	if cn <= 10 {
		if cn <= 4 {
			return strings.Repeat("*", cn)
		}
		first2 := string(cr[:2])
		last2 := string(cr[cn-2:])
		middle := strings.Repeat("*", cn-4)
		return first2 + middle + last2
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

func isContainer(v any) bool {
	switch v.(type) {
	case map[string]any, []any:
		return true
	default:
		return false
	}
}
