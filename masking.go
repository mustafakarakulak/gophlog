package logging

import (
	"strings"
)

// MaskingStrategy defines how a sensitive value is partially or fully masked.
//
// The behaviour mirrors the .NET Odeal.Logging MaskHelper exactly, including
// the implementation detail that "hidden" portions are capped at 8 asterisks.
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

// maskFixed mirrors the .NET MaskHelper.MaskString(value, visibleChars) helper.
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

// maskCreditCard mirrors the .NET MaskHelper.MaskStringCreditCard helper.
func maskCreditCard(value string) string {
	if value == "" {
		return value
	}
	replacer := strings.NewReplacer(" ", "", "-", "", "_", "")
	cleaned := replacer.Replace(value)
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

	first6 := string(cr[:6])
	last4 := string(cr[cn-4:])
	middleMask := strings.Repeat("*", cn-10)
	result := first6 + middleMask + last4

	// The original always reformats here because cleaned length >= 10.
	rr := []rune(result)
	rn := len(rr)
	var b strings.Builder
	index := 0

	if index < rn {
		end := index + 4
		if end > rn {
			end = rn
		}
		b.WriteString(string(rr[index:end]))
		index += 4
	}
	if index < rn {
		end := index + 2
		if end > rn {
			end = rn
		}
		b.WriteString(" " + string(rr[index:end]))
		index += 2
	}
	last4Start := rn - 4
	for index < last4Start {
		segLen := 4
		if last4Start-index < segLen {
			segLen = last4Start - index
		}
		b.WriteString(" " + string(rr[index:index+segLen]))
		index += segLen
	}
	if last4Start >= 0 && last4Start <= rn {
		b.WriteString(" " + string(rr[last4Start:]))
	}
	return b.String()
}

// maskScalar masks a scalar JSON value (string/number/bool). Non-scalar values
// are returned unchanged. The result is always a string, matching .NET where
// MaskHelper.Mask returns a string JSON element.
func maskScalar(value any, strategy MaskingStrategy) any {
	switch v := value.(type) {
	case nil:
		return nil
	case string:
		if v == "" {
			return v
		}
		return MaskString(v, strategy)
	case bool, float64, int, int64:
		return MaskString(scalarToString(v), strategy)
	default:
		// Objects / arrays are not scalar-maskable.
		return value
	}
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
				out[key] = maskScalarOrRecurse(val, strategy, lower)
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

// maskScalarOrRecurse masks scalars but recurses into containers so a strategy
// targeting a field that happens to hold an object still masks its leaves.
func maskScalarOrRecurse(val any, strategy MaskingStrategy, lower map[string]MaskingStrategy) any {
	if isContainer(val) {
		return applyMaskingLower(val, lower)
	}
	return maskScalar(val, strategy)
}

func isContainer(v any) bool {
	switch v.(type) {
	case map[string]any, []any:
		return true
	default:
		return false
	}
}
