package logging

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// scalarToString renders a scalar JSON value as a string for masking.
func scalarToString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(x), 'f', -1, 32)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case json.Number:
		return x.String()
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

// jsonFieldName resolves the JSON object key for a struct field, honouring the
// `json` tag (including "-" to skip and omitempty options). Returns ("", false)
// when the field must be skipped.
func jsonFieldName(f reflect.StructField) (string, bool) {
	if f.PkgPath != "" { // unexported
		return "", false
	}
	tag := f.Tag.Get("json")
	if tag == "-" {
		return "", false
	}
	name := f.Name
	if tag != "" {
		parts := strings.Split(tag, ",")
		if parts[0] != "" {
			name = parts[0]
		}
	}
	return name, true
}

var (
	timeType         = reflect.TypeOf(time.Time{})
	jsonMarshalerTyp = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
)

// processPayload normalises an arbitrary value into JSON-ready data while
// honouring `mask:"strategy"` and `logextra:"true"` struct tags.
//
//   - mask tags partially/fully mask the field value in place.
//   - logextra tags MOVE the field out of the payload and into the returned
//     extra map (keyed by the field's JSON name), making it a first-class,
//     searchable field. This matches the documented Odeal.Logging behaviour.
//
// The returned extra map is nil when no logextra fields were found.
func processPayload(v any) (payload any, extra map[string]any) {
	if v == nil {
		return nil, nil
	}
	extra = map[string]any{}
	out := processValue(reflect.ValueOf(v), extra)
	if len(extra) == 0 {
		extra = nil
	}
	return out, extra
}

func processValue(rv reflect.Value, extra map[string]any) any {
	if !rv.IsValid() {
		return nil
	}

	// Unwrap pointers and interfaces.
	for rv.Kind() == reflect.Ptr || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}

	t := rv.Type()

	// Treat time.Time and custom json.Marshaler types as opaque scalars.
	if t == timeType || t.Implements(jsonMarshalerTyp) || reflect.PtrTo(t).Implements(jsonMarshalerTyp) {
		return rv.Interface()
	}

	switch rv.Kind() {
	case reflect.Struct:
		return processStruct(rv, extra)
	case reflect.Slice, reflect.Array:
		if rv.Kind() == reflect.Slice && rv.IsNil() {
			return nil
		}
		// []byte is conventionally JSON-encoded as a base64 string.
		if rv.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8 {
			return rv.Interface()
		}
		n := rv.Len()
		arr := make([]any, n)
		for i := 0; i < n; i++ {
			// Per-element logextra is discarded to avoid key collisions across
			// array items (mirrors the .NET array handling).
			arr[i] = processValue(rv.Index(i), map[string]any{})
		}
		return arr
	case reflect.Map:
		if rv.IsNil() {
			return nil
		}
		out := make(map[string]any, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			key := scalarToString(iter.Key().Interface())
			out[key] = processValue(iter.Value(), map[string]any{})
		}
		return out
	default:
		return rv.Interface()
	}
}

func processStruct(rv reflect.Value, extra map[string]any) any {
	t := rv.Type()
	out := make(map[string]any, t.NumField())

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)

		// Recurse anonymous (embedded) structs so their fields are promoted.
		name, ok := jsonFieldName(field)
		if !ok {
			continue
		}

		fieldVal := rv.Field(i)
		processed := processValue(fieldVal, extra)

		// Apply masking if requested.
		if maskTag := field.Tag.Get("mask"); maskTag != "" {
			if strategy, ok := parseStrategy(maskTag); ok {
				processed = maskScalarOrRecurse(processed, strategy, map[string]MaskingStrategy{})
			}
		}

		// logextra moves the field into the extra map.
		if isTrueTag(field.Tag.Get("logextra")) {
			extra[name] = processed
			continue
		}

		if field.Anonymous {
			// Promote embedded struct fields to the parent object.
			if m, isMap := processed.(map[string]any); isMap {
				for k, val := range m {
					out[k] = val
				}
				continue
			}
		}

		out[name] = processed
	}

	return out
}

func isTrueTag(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes", "extra":
		return true
	default:
		return false
	}
}
