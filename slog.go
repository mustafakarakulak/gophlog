package logging

import (
	"context"
	"log/slog"
	"runtime"
)

// SlogOptions configures the slog.Handler adapter.
type SlogOptions struct {
	// EventKey names the attribute that, when present at the top level, sets the
	// log "event" field instead of being placed in the extra object. Set it to ""
	// to disable the mapping. Default: "event".
	EventKey string

	// AddSource includes the caller's file/line/function (taken from the slog
	// record) in the extra object under "source". The slog.Logger must be created
	// with AddSource enabled for the record to carry a program counter.
	AddSource bool
}

func (o *SlogOptions) eventKey() string {
	if o == nil {
		return "event"
	}
	return o.EventKey
}

// NewSlogHandler returns a slog.Handler that emits records through l, so code
// written against the standard log/slog API produces this library's structured
// JSON. A nil logger uses Default(); nil options use the defaults.
//
// Attributes land in the searchable extra object, with WithGroup nesting them in
// sub-objects; an attribute whose value is an error is rendered via Error()
// rather than serialized to an empty object. The handler passes the standard
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
	for _, f := range h.frames {
		for _, a := range f.attrs {
			addAttr(root, f.groups, a)
		}
	}
	r.Attrs(func(a slog.Attr) bool {
		addAttr(root, h.groups, a)
		return true
	})

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
	}

	e.Log()
	return nil
}

func (h *slogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	resolved := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		a.Value = a.Value.Resolve()
		resolved[i] = a
	}
	frames := make([]attrFrame, len(h.frames)+1)
	copy(frames, h.frames)
	frames[len(h.frames)] = attrFrame{groups: h.groups, attrs: resolved}

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
// recursing into groups and inlining group attributes with an empty key.
func addAttr(root map[string]any, groups []string, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return // zero attribute
	}
	if a.Value.Kind() == slog.KindGroup {
		members := a.Value.Group()
		if len(members) == 0 {
			return // empty group is omitted
		}
		next := groups
		if a.Key != "" {
			next = appendGroup(groups, a.Key)
		}
		for _, m := range members {
			addAttr(root, next, m)
		}
		return
	}
	if a.Key == "" {
		return
	}
	target := navigate(root, groups)
	target[a.Key] = slogValue(a.Value)
}

// navigate descends into root following the group path, creating nested maps as
// needed, and returns the leaf map where a value should be placed.
func navigate(root map[string]any, groups []string) map[string]any {
	m := root
	for _, g := range groups {
		child, ok := m[g].(map[string]any)
		if !ok {
			child = make(map[string]any)
			m[g] = child
		}
		m = child
	}
	return m
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
			return err.Error()
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
