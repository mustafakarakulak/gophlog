package gophlog

import (
	"context"
	"log/slog"
	"runtime"
)

// defaultSlogEventKey is the attribute name mapped onto the "event" field when
// SlogOptions does not override it.
const defaultSlogEventKey = "event"

// SlogOptions configures the slog.Handler adapter.
type SlogOptions struct {
	// EventKey names the attribute that, when present at the top level, sets the
	// log "event" field instead of being placed in the extra object.
	// Empty means the default, "event"; use DisableEventKey to turn the mapping
	// off.
	EventKey string

	// DisableEventKey turns off the event-attribute mapping entirely, so an
	// "event" attribute stays in the extra object like any other.
	DisableEventKey bool

	// AddSource includes the caller's file/line/function (taken from the slog
	// record) in the extra object under "source". The location comes from the
	// record's program counter, which slog.Logger always sets; a Record built
	// by hand with a zero PC carries none.
	AddSource bool
}

// eventKey resolves the attribute name mapped onto the "event" field, or "" when
// the mapping is disabled. An options struct that simply does not set EventKey
// keeps the default, so SlogOptions{AddSource: true} does not silently lose the
// event mapping.
func (o *SlogOptions) eventKey() string {
	switch {
	case o == nil:
		return defaultSlogEventKey
	case o.DisableEventKey:
		return ""
	case o.EventKey == "":
		return defaultSlogEventKey
	default:
		return o.EventKey
	}
}

// NewSlogHandler returns a slog.Handler that emits records through l, so code
// written against the standard log/slog API produces this library's structured
// JSON. A nil logger uses Default(); nil options use the defaults.
//
// Attributes land in the searchable extra object, with WithGroup nesting them in
// sub-objects; an attribute whose value is an error is rendered via Error()
// rather than serialized to an empty object, and struct values honour their
// `mask` tags, as in a payload. The handler passes the standard
// testing/slogtest suite except for the zero-Record.Time rule: this format
// always emits a timestamp, falling back to the logger clock when the record
// carries no time.
func NewSlogHandler(l *Logger, opts *SlogOptions) slog.Handler {
	if l == nil {
		l = Default()
	}
	addSource := opts != nil && opts.AddSource
	return &slogHandler{logger: l, eventKey: opts.eventKey(), addSource: addSource}
}

// NewSlogLogger is a convenience wrapper that returns a *slog.Logger backed by l.
func NewSlogLogger(l *Logger, opts *SlogOptions) *slog.Logger {
	return slog.New(NewSlogHandler(l, opts))
}

// attrFrame records a batch of attributes bound via WithAttrs together with the
// group path that was open at the time, so they nest correctly at emit time.
type attrFrame struct {
	groups []string
	attrs  []slog.Attr
	// plain reports that every attribute in the frame is plain (see
	// plainAttr), so the frame alone never makes build check the extras.
	plain bool
}

type slogHandler struct {
	logger    *Logger
	eventKey  string
	addSource bool
	groups    []string
	frames    []attrFrame
}

func (h *slogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return h.logger.Enabled(fromSlogLevel(level))
}

func (h *slogHandler) Handle(ctx context.Context, r slog.Record) error {
	level := fromSlogLevel(r.Level)
	if !h.logger.Enabled(level) {
		return nil
	}

	root := make(map[string]any)
	callerMaps, maskTags := false, false
	for _, f := range h.frames {
		maskTags = maskTags || !f.plain
		for _, a := range f.attrs {
			callerMaps = addAttr(root, f.groups, a) || callerMaps
		}
	}
	r.Attrs(func(a slog.Attr) bool {
		callerMaps = addAttr(root, h.groups, a) || callerMaps
		maskTags = maskTags || !plainAttr(a, len(h.groups))
		return true
	})
	if callerMaps {
		plainAttrMaps(root)
	}

	e := newEntry(h.logger, level, r.Message, "")
	e.ctx = ctx
	if !r.Time.IsZero() {
		e.ts = r.Time
	}

	if h.eventKey != "" {
		if ev, ok := root[h.eventKey].(string); ok {
			e.event = ev
			delete(root, h.eventKey)
		}
	}

	if h.addSource && r.PC != 0 {
		if src := sourceAttr(r.PC); src != nil {
			root["source"] = src
		}
	}

	if len(root) > 0 {
		e.extra = root
		e.extraTags = maskTags
	}

	e.Log()
	return nil
}

func (h *slogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	resolved := make([]slog.Attr, len(attrs))
	plain := true
	for i, a := range attrs {
		a.Value = a.Value.Resolve()
		resolved[i] = a
		plain = plain && plainAttr(a, len(h.groups))
	}
	frames := make([]attrFrame, len(h.frames)+1)
	copy(frames, h.frames)
	frames[len(h.frames)] = attrFrame{groups: h.groups, attrs: resolved, plain: plain}

	clone := *h
	clone.frames = frames
	return &clone
}

func (h *slogHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	groups := make([]string, len(h.groups)+1)
	copy(groups, h.groups)
	groups[len(h.groups)] = name

	clone := *h
	clone.groups = groups
	return &clone
}

// addAttr inserts a (resolved) slog attribute into root at the given group path,
// recursing into groups and inlining group attributes with an empty key. It
// reports whether it stored a caller's map (see attrMap), in which case the
// assembled tree must go through plainAttrMaps before it leaves Handle.
func addAttr(root map[string]any, groups []string, a slog.Attr) (callerMap bool) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return false // zero attribute
	}
	if a.Value.Kind() == slog.KindGroup {
		members := a.Value.Group()
		if len(members) == 0 {
			return false // empty group is omitted
		}
		next := groups
		if a.Key != "" {
			next = appendGroup(groups, a.Key)
		}
		for _, m := range members {
			callerMap = addAttr(root, next, m) || callerMap
		}
		return callerMap
	}
	if a.Key == "" {
		return false
	}
	target := navigate(root, groups)
	v := slogValue(a.Value)
	if m, ok := v.(map[string]any); ok {
		target[a.Key] = attrMap(m)
		return true
	}
	target[a.Key] = v
	return false
}

// plainAttr reports whether attribute a, placed depth groups deep, adds only
// plain values (see plainExtraValue) to the extras, so it cannot make them
// hold a mask tag. The value is inspected as given: a LogValuer is not
// resolved a second time just for this and counts as not plain, as does
// anything nested deeper than the mask-tag walk goes.
func plainAttr(a slog.Attr, depth int) bool {
	if depth > maxPayloadDepth {
		return false
	}
	switch v := a.Value; v.Kind() {
	case slog.KindAny:
		x := v.Any()
		if _, isErr := x.(error); isErr {
			return true // written as its Error() string
		}
		return plainExtraValue(x)
	case slog.KindGroup:
		if a.Key != "" {
			depth++
		}
		for _, m := range v.Group() {
			if !plainAttr(m, depth) {
				return false
			}
		}
		return true
	case slog.KindLogValuer:
		return false
	}
	return true
}

// attrMap tags a map[string]any attribute value while a record is assembled.
// The map belongs to the caller — and, bound through WithAttrs, is shared by
// every concurrent Handle call — so it must never be written into; the
// distinct type is what tells navigate it is not one of its own groups. The
// conversion is free, so a record without map-valued attributes pays nothing.
type attrMap map[string]any

// navigate descends into root following the group path, creating nested maps as
// needed, and returns the leaf map where a value should be placed.
//
// A group whose key already holds a caller's map takes the key over with a
// copy of that map's fields, so the group's attributes land beside them while
// the caller's map stays untouched. Nested maps in the copy stay tagged, so a
// deeper group copies them in turn. Any other value is replaced, as a later
// attribute with the same key would replace it.
func navigate(root map[string]any, groups []string) map[string]any {
	m := root
	for _, g := range groups {
		child, ok := m[g].(map[string]any)
		if !ok {
			prev, _ := m[g].(attrMap)
			child = make(map[string]any, len(prev))
			for k, v := range prev {
				if nested, isMap := v.(map[string]any); isMap {
					v = attrMap(nested)
				}
				child[k] = v
			}
			m[g] = child
		}
		m = child
	}
	return m
}

// plainAttrMaps turns the attrMap tags under m back into plain map[string]any
// values, so the rest of the logger sees ordinary maps. It descends only into
// the groups navigate built — every caller's map is still tagged at this
// point — so it never writes into a caller's map.
func plainAttrMaps(m map[string]any) {
	for k, v := range m {
		switch x := v.(type) {
		case attrMap:
			m[k] = map[string]any(x)
		case map[string]any:
			plainAttrMaps(x)
		}
	}
}

func appendGroup(groups []string, name string) []string {
	out := make([]string, len(groups)+1)
	copy(out, groups)
	out[len(groups)] = name
	return out
}

// slogValue converts a resolved slog.Value into a JSON-friendly Go value. Errors
// are rendered via Error() so they do not serialize to an empty object.
func slogValue(v slog.Value) any {
	if v.Kind() == slog.KindAny {
		if err, ok := v.Any().(error); ok {
			return errorString(err)
		}
	}
	return v.Any()
}

// sourceAttr resolves a program counter into a structured source location.
func sourceAttr(pc uintptr) map[string]any {
	frame, _ := runtime.CallersFrames([]uintptr{pc}).Next()
	if frame.File == "" && frame.Function == "" {
		return nil
	}
	return map[string]any{
		"function": frame.Function,
		"file":     frame.File,
		"line":     frame.Line,
	}
}

// fromSlogLevel maps a slog.Level onto this package's Level, widening the four
// standard slog levels to the six levels exposed here.
func fromSlogLevel(l slog.Level) Level {
	switch {
	case l < slog.LevelDebug:
		return TRACE
	case l < slog.LevelInfo:
		return DEBUG
	case l < slog.LevelWarn:
		return INFO
	case l < slog.LevelError:
		return WARN
	case l < slog.LevelError+4:
		return ERROR
	default:
		return FATAL
	}
}
