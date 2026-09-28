package gophlog

import (
	"encoding"
	"encoding/json"
	"fmt"
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
	quoted    bool   // the tag carried the string option
	skip      bool   // the field must not appear in the output
}

// parseJSONTag resolves how a struct field is rendered, mirroring
// encoding/json: `json:"-"` and unexported fields are skipped, an explicit name
// overrides the field name, and the omitempty and string options are recorded
// so they can be applied exactly as encoding/json applies them.
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
		switch opt {
		case "omitempty":
			out.omitEmpty = true
		case "string":
			out.quoted = true
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
	jsonMarshalerTyp = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	textMarshalerTyp = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
)

// mapKeyString renders a map key the way encoding/json resolves it: TextMarshaler
// or string kind first depending on the toolchain (see mapKeyPrefersText), then
// integer kinds in decimal. Rendering by Kind rather than by concrete type is
// what keeps a `type K string` key from taking the json.Marshal fallback and
// ending up wrapped in literal quotes.
func mapKeyString(k reflect.Value) string {
	if mapKeyPrefersText(k) {
		// A nil pointer key has no value to call MarshalText on; encoding/json
		// renders it as the empty string.
		if (k.Kind() == reflect.Ptr || k.Kind() == reflect.Interface) && k.IsNil() {
			return ""
		}
		if tm, ok := k.Interface().(encoding.TextMarshaler); ok {
			if b, err := tm.MarshalText(); err == nil {
				return string(b)
			}
		}
		return ""
	}
	if k.Kind() == reflect.String {
		return k.String()
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

// opaqueMode records whether encoding/json renders a type through its own
// marshaler instead of walking it.
type opaqueMode uint8

const (
	opaqueNever  opaqueMode = iota // rendered by kind: fields and elements are walked
	opaqueAlways                   // the type implements json.Marshaler or encoding.TextMarshaler
	opaqueAddr                     // only its pointer does: opaque when the value is addressable
)

// opaqueTypeCache memoizes opaqueOf per type. The Implements checks scan a
// type's method set, which is too expensive to repeat on every node of every
// payload walk.
var opaqueTypeCache sync.Map // reflect.Type -> opaqueMode

// opaqueOf mirrors encoding/json's marshaler lookup: a type implementing
// json.Marshaler or encoding.TextMarshaler renders itself (time.Time,
// netip.Addr, a secret wrapper), and so does a type whose pointer implements
// one — but only when the value is addressable, since only then can the
// pointer method be called.
func opaqueOf(t reflect.Type) opaqueMode {
	if v, ok := opaqueTypeCache.Load(t); ok {
		return v.(opaqueMode)
	}
	mode := opaqueNever
	switch {
	case t.Implements(jsonMarshalerTyp) || t.Implements(textMarshalerTyp):
		mode = opaqueAlways
	case t.Kind() != reflect.Ptr:
		if pt := reflect.PointerTo(t); pt.Implements(jsonMarshalerTyp) || pt.Implements(textMarshalerTyp) {
			mode = opaqueAddr
		}
	}
	opaqueTypeCache.Store(t, mode)
	return mode
}

// opaqueValue returns the value encoding/json would hand to rv's marshaler —
// rv itself, or its address for a pointer-receiver method — and false when rv
// is rendered by walking it instead. A value reached through an unexported
// embedded field cannot be extracted as an interface; it is walked rather than
// panicking.
func opaqueValue(rv reflect.Value) (any, bool) {
	switch opaqueOf(rv.Type()) {
	case opaqueAlways:
		if rv.CanInterface() {
			return rv.Interface(), true
		}
	case opaqueAddr:
		if rv.CanAddr() {
			if p := rv.Addr(); p.CanInterface() {
				return p.Interface(), true
			}
		}
	}
	return nil, false
}

// maxPayloadDepth bounds the reflection walk so a pathologically deep value
// cannot exhaust the stack. A value that contains itself is cut separately,
// where it first repeats (see pathGuard).
const maxPayloadDepth = 64

const (
	// depthMarker is rendered in place of a value nested deeper than a walk
	// allows.
	depthMarker = "[max depth exceeded]"
	// cycleMarker is rendered in place of a value that contains itself, where
	// it first repeats. It keeps the depth marker's wording — a cyclic value
	// is one whose depth is unbounded — so searches for that marker still
	// find it.
	cycleMarker = "[cycle: max depth exceeded]"
)

// visitKey identifies a pointer, map or slice on the walk path. The type is
// part of the key because a struct and its first field share an address; a
// slice also records its length, so a sub-slice of the same backing array is
// not mistaken for its parent.
type visitKey struct {
	ptr uintptr
	n   int
	typ reflect.Type
}

// pathInline is how many path entries a pathGuard holds without allocating.
const pathInline = 8

// pathGuard records the pointers, maps and slices on the current walk path,
// as encoding/json does, so a value that contains itself is cut where it first
// repeats instead of being walked until the depth limit — which, for a value
// that branches back into itself, takes exponentially long. A value merely
// shared between branches is never on the path twice, so it is still rendered
// in full each time, exactly as json.Marshal renders it.
type pathGuard struct {
	n    int                   // entries on the path
	near [pathInline]visitKey  // the first pathInline entries, scanned linearly
	far  map[visitKey]struct{} // the rest; allocated only for very deep paths
}

// enter pushes k onto the path. It reports false, pushing nothing, when k is
// already on it: the value contains itself.
func (g *pathGuard) enter(k visitKey) bool {
	for _, e := range g.near[:min(g.n, pathInline)] {
		if e == k {
			return false
		}
	}
	if g.n < pathInline {
		g.near[g.n] = k
	} else {
		if _, dup := g.far[k]; dup {
			return false
		}
		if g.far == nil {
			g.far = make(map[visitKey]struct{})
		}
		g.far[k] = struct{}{}
	}
	g.n++
	return true
}

// leave pops k, which must be the entry pushed last.
func (g *pathGuard) leave(k visitKey) {
	g.n--
	if g.n >= pathInline {
		delete(g.far, k)
	}
}

// processPayload normalises an arbitrary value into JSON-ready data while
// honouring `mask:"strategy"` and `logextra:"true"` struct tags.
//
//   - mask tags partially/fully mask the field value in place. A tag that
//     names no known strategy hides the value entirely (fail-closed).
//   - logextra tags MOVE the field out of the payload and into the returned
//     extra map (keyed by the field's JSON name), making it a first-class,
//     searchable field rather than part of the stringified payload.
//
// Apart from those two tags the result matches what encoding/json would produce
// for the same value: `json:"-"` and unexported fields are skipped, omitempty
// drops empty values, the string option quotes scalars, marshalers render
// themselves, embedded structs are promoted into the parent object under
// encoding/json's name-resolution rules, and a nil embedded pointer
// contributes nothing. A value that contains itself is cut with cycleMarker
// where it first repeats, instead of failing as it would in json.Marshal.
//
// The returned extra map is nil when no logextra fields were found.
func processPayload(v any) (payload any, extra map[string]any) {
	payload, extra, _ = processPayloadWarn(v)
	return payload, extra
}

// processPayloadWarn is processPayload that also returns the first
// recoverable problem the walk met (see payloadWalker.warn).
func processPayloadWarn(v any) (payload any, extra map[string]any, warn error) {
	if v == nil {
		return nil, nil, nil
	}
	// The sink allocates its map lazily: most payload types carry no logextra
	// tags, so the common case never pays for an extra map that stays empty.
	var sink extraSink
	var w payloadWalker
	out := w.value(reflect.ValueOf(v), &sink, 0)
	return out, sink.m, w.warn
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

// payloadWalker carries the state of one walk over a logged value.
type payloadWalker struct {
	path pathGuard
	// inline keeps logextra fields in place instead of lifting them out. It
	// is set when the value walked is itself an extra field (or an
	// integration body), where there is no payload to lift a field out of.
	inline bool
	// warn is the first recoverable problem the walk met: a cycle, or a mask
	// tag naming no known strategy. The value is still rendered (with a
	// marker, or fully hidden); the logger reports warn to OnError.
	warn error
}

func (w *payloadWalker) note(err error) {
	if w.warn == nil {
		w.warn = err
	}
}

// cycle notes that a value of type t contains itself and returns the marker
// rendered where it repeats.
func (w *payloadWalker) cycle(t reflect.Type) any {
	if w.warn == nil {
		w.warn = fmt.Errorf("gophlog: logged value contains a cycle via %s; rendered as %q", t, cycleMarker)
	}
	return cycleMarker
}

func (w *payloadWalker) value(rv reflect.Value, extra *extraSink, depth int) any {
	if depth > maxPayloadDepth {
		return depthMarker
	}
	if !rv.IsValid() {
		return nil
	}

	// Unwrap interfaces. Pointers are followed one at a time, so each can be
	// recorded on the walk path.
	for rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}
	if rv.Kind() == reflect.Ptr {
		return w.pointer(rv, extra, depth)
	}

	// Types that render themselves (json.Marshaler and encoding.TextMarshaler
	// implementations such as time.Time) are opaque scalars, exactly as
	// encoding/json treats them.
	if v, ok := opaqueValue(rv); ok {
		return v
	}

	switch rv.Kind() {
	case reflect.Struct:
		return w.structValue(rv, extra, depth)
	case reflect.Slice, reflect.Array:
		if rv.Kind() == reflect.Slice && rv.IsNil() {
			return nil
		}
		// []byte is conventionally JSON-encoded as a base64 string.
		if rv.Kind() == reflect.Slice && rv.Type().Elem().Kind() == reflect.Uint8 {
			return rv.Interface()
		}
		return w.list(rv, depth)
	case reflect.Map:
		if rv.IsNil() {
			return nil
		}
		return w.mapValue(rv, depth)
	default:
		return rv.Interface()
	}
}

// pointer follows a pointer, recording it on the walk path first so a value
// that points back at one of its ancestors is cut instead of walked again.
func (w *payloadWalker) pointer(rv reflect.Value, extra *extraSink, depth int) any {
	if rv.IsNil() {
		return nil
	}
	k := visitKey{ptr: rv.Pointer(), typ: rv.Type()}
	if !w.path.enter(k) {
		return w.cycle(rv.Type())
	}
	out := w.value(rv.Elem(), extra, depth)
	w.path.leave(k)
	return out
}

// list renders a slice or array. A non-empty slice is recorded on the walk
// path first, so one that contains itself is cut.
func (w *payloadWalker) list(rv reflect.Value, depth int) any {
	n := rv.Len()
	tracked := rv.Kind() == reflect.Slice && n > 0
	var k visitKey
	if tracked {
		k = visitKey{ptr: rv.Pointer(), n: n, typ: rv.Type()}
		if !w.path.enter(k) {
			return w.cycle(rv.Type())
		}
	}
	arr := make([]any, n)
	for i := 0; i < n; i++ {
		// Per-element logextra is discarded (nil extra) to avoid key
		// collisions across array items.
		arr[i] = w.value(rv.Index(i), nil, depth+1)
	}
	if tracked {
		w.path.leave(k)
	}
	return arr
}

// mapValue renders a non-nil map, recording it on the walk path first so one
// that contains itself is cut.
func (w *payloadWalker) mapValue(rv reflect.Value, depth int) any {
	if rv.Len() == 0 {
		return make(map[string]any)
	}
	k := visitKey{ptr: rv.Pointer(), typ: rv.Type()}
	if !w.path.enter(k) {
		return w.cycle(rv.Type())
	}
	out := make(map[string]any, rv.Len())
	iter := rv.MapRange()
	for iter.Next() {
		out[mapKeyString(iter.Key())] = w.value(iter.Value(), nil, depth+1)
	}
	w.path.leave(k)
	return out
}

// fieldPlan is the precomputed handling for one struct field, cached per type so
// tags are parsed once rather than on every log call.
type fieldPlan struct {
	index     int
	name      string
	maskStrat MaskingStrategy
	hasMask   bool
	// maskErr is set when the mask tag names no known strategy: the field is
	// then hidden entirely (HideAll) and maskErr reported to OnError.
	maskErr   error
	logextra  bool
	omitEmpty bool
	// quoted applies the `json:",string"` option to a scalar field.
	quoted bool
	// hidden marks a field that loses its JSON name to another field of the
	// same name (see dominantOwners), so encoding/json never renders it.
	hidden bool
	// promote marks an untagged embedded struct field whose own fields are
	// lifted into the parent object, as encoding/json does. An embedded field
	// carrying an explicit json name is a normal named field instead.
	promote bool
}

// structPlanInfo is the cached rendering plan for one struct type.
type structPlanInfo struct {
	plans []fieldPlan
	// owner maps each JSON name that survives encoding/json's resolution to
	// the index of the struct's own field it is rendered through (see
	// dominantOwners). A name promoted from an embedded struct is rendered
	// only when that embedded field owns it, so a shallower field, a tagged
	// field at the same depth, or a tie that drops the name altogether all
	// resolve exactly as in encoding/json — regardless of declaration order,
	// and even when omitempty drops the winning field's value. Nil when the
	// struct has no promoted fields, since only the promotion path consults it.
	owner map[string]int
}

var fieldPlanCache sync.Map // reflect.Type -> *structPlanInfo

// structPlan returns the cached field plan for t, computing it on first use.
func structPlan(t reflect.Type) *structPlanInfo {
	if v, ok := fieldPlanCache.Load(t); ok {
		return v.(*structPlanInfo)
	}
	owner := dominantOwners(t)
	plans := make([]fieldPlan, 0, t.NumField())
	hasPromote := false
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		tag := parseJSONTag(field)
		if tag.skip {
			continue
		}
		ft := field.Type
		if ft.Name() == "" && ft.Kind() == reflect.Ptr {
			ft = ft.Elem()
		}
		fp := fieldPlan{
			index:     i,
			name:      tag.name,
			omitEmpty: tag.omitEmpty,
			promote:   field.Anonymous && !tag.named && ft.Kind() == reflect.Struct,
		}
		if !fp.promote {
			idx, ok := owner[fp.name]
			fp.hidden = !ok || idx != i
		}
		if tag.quoted {
			// encoding/json honours the string option on scalar fields only.
			switch ft.Kind() {
			case reflect.Bool, reflect.String,
				reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
				reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
				reflect.Float32, reflect.Float64:
				fp.quoted = true
			}
		}
		if maskTag, tagged := field.Tag.Lookup("mask"); tagged {
			strategy, ok := parseStrategy(maskTag)
			if !ok {
				// Fail closed: a tag naming no known strategy (a typo such
				// as "hide" or "true") hides the value entirely, exactly as
				// an unknown runtime strategy does, rather than leaving it in
				// the clear.
				strategy = HideAll
				fp.maskErr = fmt.Errorf("gophlog: unknown mask strategy %q on field %s.%s; hiding the value entirely", maskTag, t, field.Name)
			}
			fp.maskStrat = strategy
			fp.hasMask = true
		}
		fp.logextra = isTrueTag(field.Tag.Get("logextra"))
		hasPromote = hasPromote || fp.promote
		plans = append(plans, fp)
	}
	info := &structPlanInfo{plans: plans}
	if hasPromote {
		info.owner = owner
	}
	fieldPlanCache.Store(t, info)
	return info
}

// dominantOwners resolves the JSON names of struct type t the way
// encoding/json's typeFields does once embedded structs are flattened: the
// shallowest field wins a name, a tagged field beats an untagged one at the
// same depth, and any remaining tie drops the name altogether. It returns, for
// every name that survives, the index of t's own field through which the
// winning field is reached.
func dominantOwners(t reflect.Type) map[string]int {
	type candidate struct {
		owner  int // index of t's own field the candidate is reached through
		depth  int
		tagged bool
	}
	type embedded struct {
		typ   reflect.Type
		owner int
	}
	best := make(map[string]candidate)
	tied := make(map[string]bool)
	// Candidates arrive in breadth-first order, so never shallower than best.
	consider := func(name string, c candidate) {
		b, seen := best[name]
		switch {
		case !seen || c.depth < b.depth || c.depth == b.depth && c.tagged && !b.tagged:
			best[name] = c
			delete(tied, name)
		case c.depth == b.depth && c.tagged == b.tagged:
			tied[name] = true
		}
	}

	visited := make(map[reflect.Type]bool)
	level := []embedded{{typ: t, owner: -1}}
	var count map[reflect.Type]int // how often each type occurs at this level
	for depth := 1; len(level) > 0; depth++ {
		var next []embedded
		nextCount := make(map[reflect.Type]int)
		for _, e := range level {
			if visited[e.typ] {
				continue
			}
			visited[e.typ] = true
			for i := 0; i < e.typ.NumField(); i++ {
				sf := e.typ.Field(i)
				tag := parseJSONTag(sf)
				if tag.skip {
					continue
				}
				owner := e.owner
				if owner < 0 {
					owner = i
				}
				ft := sf.Type
				if ft.Name() == "" && ft.Kind() == reflect.Ptr {
					ft = ft.Elem()
				}
				if tag.named || !sf.Anonymous || ft.Kind() != reflect.Struct {
					c := candidate{owner: owner, depth: depth, tagged: tag.named}
					consider(tag.name, c)
					if count[e.typ] > 1 {
						// The same embedded type reached twice at one depth
						// conflicts with itself.
						consider(tag.name, c)
					}
					continue
				}
				nextCount[ft]++
				if nextCount[ft] == 1 {
					next = append(next, embedded{typ: ft, owner: owner})
				}
			}
		}
		level, count = next, nextCount
	}

	owners := make(map[string]int, len(best))
	for name, c := range best {
		if !tied[name] {
			owners[name] = c.owner
		}
	}
	return owners
}

func (w *payloadWalker) structValue(rv reflect.Value, extra *extraSink, depth int) any {
	sp := structPlan(rv.Type())
	out := make(map[string]any, len(sp.plans))

	for i := range sp.plans {
		fp := &sp.plans[i]
		// A field that loses its name to another field is never rendered
		// by encoding/json either.
		if fp.hidden {
			continue
		}
		field := rv.Field(fp.index)

		// omitempty drops the field before any further work, exactly as
		// encoding/json would — including for logextra fields, so an empty
		// value never shows up as an empty extra either.
		if fp.omitEmpty && isEmptyValue(field) {
			continue
		}

		processed := w.value(field, extra, depth+1)

		switch {
		case fp.hasMask:
			if fp.maskErr != nil {
				w.note(fp.maskErr)
			}
			// processed was built by this walk, so it is masked in place.
			processed = maskScalarOrRecurseInPlace(processed, fp.maskStrat)
		case fp.quoted:
			processed = quoteScalar(processed)
		}

		// logextra moves the field into the extra map. A nil extra (inside an
		// array/map element) means the field is discarded instead; an inline
		// walk keeps it in place.
		if fp.logextra && !w.inline {
			if extra != nil {
				extra.put(fp.name, processed)
			}
			continue
		}

		if fp.promote {
			// Embedded struct: lift into the parent object the names this
			// embedded field owns.
			if m, isMap := processed.(map[string]any); isMap {
				for k, val := range m {
					if owner, ok := sp.owner[k]; ok && owner == fp.index {
						out[k] = val
					}
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

	return out
}

// quoteScalar applies the `json:",string"` option: the value becomes a JSON
// string holding its own JSON encoding, as in encoding/json. A nil pointer
// stays null, and a type that renders itself ignores the option, as it does
// in encoding/json.
func quoteScalar(v any) any {
	if v == nil || opaqueOf(reflect.TypeOf(v)) == opaqueAlways {
		return v
	}
	s, err := encodeJSON(v)
	if err != nil {
		// Leave the value for the final render to report (a NaN, say).
		return v
	}
	return s
}

func isTrueTag(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes", "extra":
		return true
	default:
		return false
	}
}

// Mask-tag reachability of a type, as computed by maskTagsOf.
const (
	maskTagsNone  uint8 = iota // no mask tag is reachable: encoding/json may render it as-is
	maskTagsMaybe              // one is reachable only through interface values: inspect the value
	maskTagsSome               // a mask-tagged field is reachable from the type alone
)

var maskTagsCache sync.Map // reflect.Type -> uint8

// maskTagsOf reports, per type and cached, whether a value of type t can hold
// a struct field carrying a mask tag. It lets values outside the payload —
// extra fields, slog attributes, integration bodies — honour mask tags, while
// every value that cannot carry one (strings, numbers, map[string]string, a
// struct without tags, ...) keeps going straight to encoding/json untouched.
func maskTagsOf(t reflect.Type) uint8 {
	if v, ok := maskTagsCache.Load(t); ok {
		return v.(uint8)
	}
	r := scanMaskTags(t, make(map[reflect.Type]struct{}))
	maskTagsCache.Store(t, r)
	return r
}

// scanMaskTags searches the types reachable from t the way the payload walk
// descends into them. seen holds the types already searched, so a recursive
// type terminates; only the root's answer is complete, which is why only
// maskTagsOf's result is cached.
func scanMaskTags(t reflect.Type, seen map[reflect.Type]struct{}) uint8 {
	if _, done := seen[t]; done {
		return maskTagsNone
	}
	seen[t] = struct{}{}
	if opaqueOf(t) == opaqueAlways {
		// It renders itself; its fields are never looked at.
		return maskTagsNone
	}
	switch t.Kind() {
	case reflect.Interface:
		return maskTagsMaybe
	case reflect.Ptr, reflect.Slice, reflect.Array, reflect.Map:
		return scanMaskTags(t.Elem(), seen)
	case reflect.Struct:
		found := maskTagsNone
		for _, fp := range structPlan(t).plans {
			if fp.hasMask {
				return maskTagsSome
			}
			if r := scanMaskTags(t.Field(fp.index).Type, seen); r > found {
				found = r
			}
		}
		return found
	default:
		return maskTagsNone
	}
}

// needsMaskTagWalk reports whether v may hold a mask-tagged struct field, so
// that rendering it with encoding/json alone could print in the clear a value
// its tag says to mask. The common extra values are answered without
// reflection. A value nested deeper than the payload walk goes is reported as
// needing the walk — fail-closed, since the walk bounds it and cuts cycles.
func needsMaskTagWalk(v any, depth int) bool {
	if plainExtraValue(v) {
		return false
	}
	switch x := v.(type) {
	case map[string]any:
		if depth > maxPayloadDepth {
			return true
		}
		for _, e := range x {
			if needsMaskTagWalk(e, depth+1) {
				return true
			}
		}
		return false
	case []any:
		if depth > maxPayloadDepth {
			return true
		}
		for _, e := range x {
			if needsMaskTagWalk(e, depth+1) {
				return true
			}
		}
		return false
	}
	return valueNeedsMaskTagWalk(reflect.ValueOf(v), depth)
}

// plainExtraValue reports whether v is one of the common extra values that can
// never hold a mask-tagged field, whatever the caller does with it later. The
// entry, the child-logger builder and the slog handler test each extra value
// with it as they add it, so an entry whose extras are all plain skips the
// mask-tag check without looking at them again.
func plainExtraValue(v any) bool {
	switch v.(type) {
	case nil, string, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64,
		float32, float64, json.Number, time.Time, time.Duration, []byte, []string, map[string]string:
		return true
	}
	return false
}

// valueNeedsMaskTagWalk is needsMaskTagWalk for a reflected value.
func valueNeedsMaskTagWalk(rv reflect.Value, depth int) bool {
	switch maskTagsOf(rv.Type()) {
	case maskTagsNone:
		return false
	case maskTagsSome:
		return true
	}
	// maskTagsMaybe: a mask tag can only arrive through an interface value,
	// so look at the values actually held.
	if depth > maxPayloadDepth {
		return true
	}
	switch rv.Kind() {
	case reflect.Interface:
		if rv.IsNil() {
			return false
		}
		if e := rv.Elem(); e.CanInterface() {
			return needsMaskTagWalk(e.Interface(), depth+1)
		}
		return valueNeedsMaskTagWalk(rv.Elem(), depth+1)
	case reflect.Ptr:
		return !rv.IsNil() && valueNeedsMaskTagWalk(rv.Elem(), depth+1)
	case reflect.Struct:
		for _, fp := range structPlan(rv.Type()).plans {
			if valueNeedsMaskTagWalk(rv.Field(fp.index), depth+1) {
				return true
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < rv.Len(); i++ {
			if valueNeedsMaskTagWalk(rv.Index(i), depth+1) {
				return true
			}
		}
	case reflect.Map:
		iter := rv.MapRange()
		for iter.Next() {
			if valueNeedsMaskTagWalk(iter.Value(), depth+1) {
				return true
			}
		}
	}
	return false
}

// maskTaggedExtra applies struct mask tags to the values of an extra map —
// entry, bound and slog attributes alike — the way a payload honours them.
// Only the tags apply: key-based strategies (Mask, WithPayloadMasked) remain
// payload-only. When no value can carry a mask tag, m itself is returned and
// nothing is copied or walked. Otherwise a copy is returned; m is never
// modified, since bound extras are shared between entries. The error is the
// walk's first recoverable problem, for OnError.
func maskTaggedExtra(m map[string]any) (map[string]any, error) {
	for _, v := range m {
		if needsMaskTagWalk(v, 0) {
			return walkTaggedExtra(m)
		}
	}
	return m, nil
}

func walkTaggedExtra(m map[string]any) (map[string]any, error) {
	w := payloadWalker{inline: true}
	out := make(map[string]any, len(m))
	for k, v := range m {
		if needsMaskTagWalk(v, 0) {
			v = w.value(reflect.ValueOf(v), nil, 0)
		}
		out[k] = v
	}
	return out, w.warn
}

// maskTaggedValue is maskTaggedExtra for a single value (an integration
// body). It runs while the body is being marshalled, where there is no logger
// to report a problem to; the value is still masked and cut the same way.
func maskTaggedValue(v any) any {
	if !needsMaskTagWalk(v, 0) {
		return v
	}
	w := payloadWalker{inline: true}
	return w.value(reflect.ValueOf(v), nil, 0)
}
