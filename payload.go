package gophlog

import (
	"encoding"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"sync"
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

// jsonTag is the parsed `json` struct tag for one field.
type jsonTag struct {
	name      string // resolved JSON object key
	named     bool   // the tag supplied an explicit name
	omitEmpty bool   // the tag carried the omitempty option
	skip      bool   // the field must not appear in the output
}

// parseJSONTag resolves how a struct field is rendered, mirroring
// encoding/json: `json:"-"` and unexported fields are skipped, an explicit name
// overrides the field name, and the omitempty option is recorded so empty
// values can be dropped exactly as encoding/json would drop them.
//
// One unexported shape survives, as it does in encoding/json: an ANONYMOUS
// field of unexported struct (or pointer-to-struct) type. Its exported
// subfields are promoted when untagged, rendered as a nested object when the
// tag names it, and dropped with `json:"-"` — the tag parsing below handles
// all three exactly as for an exported field.
func parseJSONTag(f reflect.StructField) jsonTag {
	if f.PkgPath != "" { // unexported
		t := f.Type
		if t.Kind() == reflect.Ptr {
			t = t.Elem()
		}
		if !f.Anonymous || t.Kind() != reflect.Struct {
			return jsonTag{skip: true}
		}
	}
	tag := f.Tag.Get("json")
	if tag == "-" {
		return jsonTag{skip: true}
	}
	out := jsonTag{name: f.Name}
	if tag == "" {
		return out
	}
	name, opts, _ := strings.Cut(tag, ",")
	if name != "" {
		out.name = name
		out.named = true
	}
	for opts != "" {
		var opt string
		opt, opts, _ = strings.Cut(opts, ",")
		if opt == "omitempty" {
			out.omitEmpty = true
		}
	}
	return out
}

// isEmptyValue mirrors encoding/json's notion of an empty value, so the
// omitempty option drops exactly the same fields here as it does in
// json.Marshal.
func isEmptyValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Bool:
		return !v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	case reflect.Interface, reflect.Ptr:
		return v.IsNil()
	default:
		return false
	}
}

var (
	timeType         = reflect.TypeOf(time.Time{})
	jsonMarshalerTyp = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	textMarshalerTyp = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
)

// mapKeyString renders a map key the way encoding/json resolves it: string
// kinds by value first (named string types included, even when they implement
// TextMarshaler), then TextMarshaler, then integer kinds in decimal. Rendering
// by Kind rather than by concrete type is what keeps a `type K string` key from
// taking the json.Marshal fallback and ending up wrapped in literal quotes.
func mapKeyString(k reflect.Value) string {
	if k.Kind() == reflect.String {
		return k.String()
	}
	if k.Type().Implements(textMarshalerTyp) {
		if b, err := k.Interface().(encoding.TextMarshaler).MarshalText(); err == nil {
			return string(b)
		}
		return ""
	}
	switch k.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(k.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.FormatUint(k.Uint(), 10)
	default:
		// encoding/json rejects other key types outright; a logging library
		// renders a best-effort string instead of failing the log line.
		return scalarToString(k.Interface())
	}
}

// opaqueTypeCache memoizes, per type, whether values are passed through as
// opaque scalars. The Implements checks scan a type's method set, which is too
// expensive to repeat on every node of every payload walk.
var opaqueTypeCache sync.Map // reflect.Type -> bool

func isOpaqueType(t reflect.Type) bool {
	if v, ok := opaqueTypeCache.Load(t); ok {
		return v.(bool)
	}
	opaque := t == timeType || t.Implements(jsonMarshalerTyp) || reflect.PointerTo(t).Implements(jsonMarshalerTyp)
	opaqueTypeCache.Store(t, opaque)
	return opaque
}

// maxPayloadDepth bounds the reflection walk so a cyclic value (a pointer or
// slice that references itself) cannot recurse forever and overflow the stack.
const maxPayloadDepth = 64

// processPayload normalises an arbitrary value into JSON-ready data while
// honouring `mask:"strategy"` and `logextra:"true"` struct tags.
//
//   - mask tags partially/fully mask the field value in place.
//   - logextra tags MOVE the field out of the payload and into the returned
//     extra map (keyed by the field's JSON name), making it a first-class,
//     searchable field rather than part of the stringified payload.
//
// Apart from those two tags the result matches what encoding/json would produce
// for the same value: `json:"-"` and unexported fields are skipped, omitempty
// drops empty values, untagged embedded structs are promoted into the parent
// object and a nil embedded pointer contributes nothing.
//
// The returned extra map is nil when no logextra fields were found.
func processPayload(v any) (payload any, extra map[string]any) {
	if v == nil {
		return nil, nil
	}
	// The sink allocates its map lazily: most payload types carry no logextra
	// tags, so the common case never pays for an extra map that stays empty.
	var sink extraSink
	out := processValue(reflect.ValueOf(v), &sink, 0)
	return out, sink.m
}

// extraSink collects logextra fields during a payload walk, allocating the
// backing map only when the first field is inserted. A nil *extraSink discards
// logextra fields entirely (array/map elements, to avoid key collisions).
type extraSink struct{ m map[string]any }

func (s *extraSink) put(key string, val any) {
	if s.m == nil {
		s.m = make(map[string]any)
	}
	s.m[key] = val
}

func processValue(rv reflect.Value, extra *extraSink, depth int) any {
	if depth > maxPayloadDepth {
		return "[max depth exceeded]"
	}
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

	// Treat time.Time and custom json.Marshaler types as opaque scalars. A
	// value reached through an unexported embedded field cannot be extracted
	// as an interface; fall through and render its fields instead of panicking.
	if isOpaqueType(t) && rv.CanInterface() {
		return rv.Interface()
	}

	switch rv.Kind() {
	case reflect.Struct:
		return processStruct(rv, extra, depth)
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
			// Per-element logextra is discarded (nil extra) to avoid key
			// collisions across array items.
			arr[i] = processValue(rv.Index(i), nil, depth+1)
		}
		return arr
	case reflect.Map:
		if rv.IsNil() {
			return nil
		}
		out := make(map[string]any, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			out[mapKeyString(iter.Key())] = processValue(iter.Value(), nil, depth+1)
		}
		return out
	default:
		return rv.Interface()
	}
}

// fieldPlan is the precomputed handling for one struct field, cached per type so
// tags are parsed once rather than on every log call.
type fieldPlan struct {
	index     int
	name      string
	maskStrat MaskingStrategy
	hasMask   bool
	logextra  bool
	omitEmpty bool
	// promote marks an untagged embedded struct field whose own fields are
	// lifted into the parent object, as encoding/json does. An embedded field
	// carrying an explicit json name is a normal named field instead.
	promote bool
}

// structPlanInfo is the cached rendering plan for one struct type.
type structPlanInfo struct {
	plans []fieldPlan
	// directNames holds the JSON names claimed by the struct's own fields
	// (every plan except promoted embedded ones, logextra included). A field
	// promoted from an embedded struct may not use any of these names: the
	// shallower field always wins, exactly as encoding/json resolves the
	// conflict. Nil when the struct has no promoted fields, since only the
	// promotion path consults it.
	directNames map[string]struct{}
}

var fieldPlanCache sync.Map // reflect.Type -> *structPlanInfo

// structPlan returns the cached field plan for t, computing it on first use.
func structPlan(t reflect.Type) *structPlanInfo {
	if v, ok := fieldPlanCache.Load(t); ok {
		return v.(*structPlanInfo)
	}
	plans := make([]fieldPlan, 0, t.NumField())
	hasPromote := false
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		tag := parseJSONTag(field)
		if tag.skip {
			continue
		}
		fp := fieldPlan{
			index:     i,
			name:      tag.name,
			omitEmpty: tag.omitEmpty,
			promote:   field.Anonymous && !tag.named,
		}
		if maskTag := field.Tag.Get("mask"); maskTag != "" {
			if strategy, ok := parseStrategy(maskTag); ok {
				fp.maskStrat = strategy
				fp.hasMask = true
			}
		}
		fp.logextra = isTrueTag(field.Tag.Get("logextra"))
		hasPromote = hasPromote || fp.promote
		plans = append(plans, fp)
	}
	info := &structPlanInfo{plans: plans}
	if hasPromote {
		info.directNames = make(map[string]struct{}, len(plans))
		for _, fp := range plans {
			if !fp.promote {
				info.directNames[fp.name] = struct{}{}
			}
		}
	}
	fieldPlanCache.Store(t, info)
	return info
}

func processStruct(rv reflect.Value, extra *extraSink, depth int) any {
	sp := structPlan(rv.Type())
	out := make(map[string]any, len(sp.plans))

	// Fields lifted from embedded structs are collected here and merged after
	// the loop, so that a same-named field on the outer struct wins regardless
	// of declaration order, and a name promoted by two embedded siblings is
	// dropped entirely — both exactly as encoding/json resolves them.
	var promoted map[string]any
	var promotedDup map[string]struct{}

	for _, fp := range sp.plans {
		field := rv.Field(fp.index)

		// omitempty drops the field before any further work, exactly as
		// encoding/json would — including for logextra fields, so an empty
		// value never shows up as an empty extra either.
		if fp.omitEmpty && isEmptyValue(field) {
			continue
		}

		processed := processValue(field, extra, depth+1)

		if fp.hasMask {
			processed = maskScalarOrRecurse(processed, fp.maskStrat)
		}

		// logextra moves the field into the extra map. A nil extra (inside an
		// array/map element) means the field is discarded instead.
		if fp.logextra {
			if extra != nil {
				extra.put(fp.name, processed)
			}
			continue
		}

		if fp.promote {
			// Embedded struct: lift its fields into the parent object.
			if m, isMap := processed.(map[string]any); isMap {
				for k, val := range m {
					// The outer struct owns this name, even when its own field
					// was dropped by omitempty — name resolution in
					// encoding/json happens at the type level, not per value.
					if _, direct := sp.directNames[k]; direct {
						continue
					}
					if _, dup := promotedDup[k]; dup {
						continue
					}
					if _, seen := promoted[k]; seen {
						// A second embedded sibling promotes the same name:
						// neither field is rendered.
						delete(promoted, k)
						if promotedDup == nil {
							promotedDup = make(map[string]struct{})
						}
						promotedDup[k] = struct{}{}
						continue
					}
					if promoted == nil {
						promoted = make(map[string]any, len(m))
					}
					promoted[k] = val
				}
				continue
			}
			// A nil embedded pointer contributes no fields at all, matching
			// encoding/json — rather than a "TypeName": null entry.
			if processed == nil {
				continue
			}
		}

		out[fp.name] = processed
	}

	for k, val := range promoted {
		out[k] = val
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
