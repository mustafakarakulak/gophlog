package gophlog

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"testing/slogtest"
	"time"
)

func TestSlogBasic(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)
	sl := slog.New(NewSlogHandler(log, nil))

	sl.Info("hello", "user_id", "u1", "count", 3)

	m := parseLine(t, &buf)
	if m["message"] != "hello" {
		t.Errorf("message = %v", m["message"])
	}
	if m["level"] != "INFO" {
		t.Errorf("level = %v", m["level"])
	}
	extra, ok := m["extra"].(map[string]any)
	if !ok {
		t.Fatalf("extra missing: %v", m["extra"])
	}
	if extra["user_id"] != "u1" {
		t.Errorf("extra.user_id = %v", extra["user_id"])
	}
	if extra["count"] != float64(3) {
		t.Errorf("extra.count = %v", extra["count"])
	}
}

func TestSlogLevelMapping(t *testing.T) {
	cases := map[slog.Level]string{
		slog.LevelDebug - 1: "TRACE",
		slog.LevelDebug:     "DEBUG",
		slog.LevelInfo:      "INFO",
		slog.LevelWarn:      "WARN",
		slog.LevelError:     "ERROR",
		slog.LevelError + 4: "FATAL",
	}
	for in, want := range cases {
		if got := fromSlogLevel(in); string(got) != want {
			t.Errorf("fromSlogLevel(%v) = %v; want %v", in, got, want)
		}
	}
}

func TestSlogGroupsAndAttrs(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)
	sl := slog.New(NewSlogHandler(log, nil))

	sl.With("svc", "api").WithGroup("db").Info("query", "rows", 5, "table", "users")

	m := parseLine(t, &buf)
	extra := m["extra"].(map[string]any)
	if extra["svc"] != "api" {
		t.Errorf("extra.svc = %v", extra["svc"])
	}
	db, ok := extra["db"].(map[string]any)
	if !ok {
		t.Fatalf("extra.db should be a nested object: %v", extra["db"])
	}
	if db["rows"] != float64(5) || db["table"] != "users" {
		t.Errorf("extra.db = %v", db)
	}
}

func TestSlogErrorValue(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)
	sl := slog.New(NewSlogHandler(log, nil))

	sl.Error("boom", "err", errors.New("kaboom"))

	m := parseLine(t, &buf)
	if m["level"] != "ERROR" {
		t.Errorf("level = %v", m["level"])
	}
	extra := m["extra"].(map[string]any)
	if extra["err"] != "kaboom" {
		t.Errorf("error should render as its message, got %v", extra["err"])
	}
}

func TestSlogEventKey(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)
	sl := slog.New(NewSlogHandler(log, nil))

	sl.Info("user created", "event", "user_created", "id", 7)

	m := parseLine(t, &buf)
	if m["event"] != "user_created" {
		t.Errorf("event = %v", m["event"])
	}
	extra := m["extra"].(map[string]any)
	if _, present := extra["event"]; present {
		t.Errorf("event attr should be lifted out of extra: %v", extra)
	}
	if extra["id"] != float64(7) {
		t.Errorf("extra.id = %v", extra["id"])
	}
}

func TestSlogEventKeyDisabled(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)
	sl := slog.New(NewSlogHandler(log, &SlogOptions{DisableEventKey: true}))

	sl.Info("x", "event", "stays")

	m := parseLine(t, &buf)
	if _, present := m["event"]; present {
		t.Errorf("event field should be empty when EventKey is disabled")
	}
	extra := m["extra"].(map[string]any)
	if extra["event"] != "stays" {
		t.Errorf("event attr should remain in extra: %v", extra)
	}
}

// TestSlogEventKeyDefaultSurvivesOtherOptions guards the zero-value footgun:
// setting an unrelated option must not silently switch off the event mapping.
func TestSlogEventKeyDefaultSurvivesOtherOptions(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)
	sl := slog.New(NewSlogHandler(log, &SlogOptions{AddSource: true}))

	sl.Info("x", "event", "mapped")

	m := parseLine(t, &buf)
	if m["event"] != "mapped" {
		t.Errorf("event = %v; want the default mapping to still apply", m["event"])
	}
}

func TestSlogHonorsRecordTime(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf) // fixed clock at 2026-01-11
	h := NewSlogHandler(log, nil)

	recTime := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	rec := slog.NewRecord(recTime, slog.LevelInfo, "t", 0)
	if err := h.Handle(context.Background(), rec); err != nil {
		t.Fatal(err)
	}

	m := parseLine(t, &buf)
	if m["timestamp"] != "2020-01-02T03:04:05.000Z" {
		t.Errorf("record time should be honored, got %v", m["timestamp"])
	}
}

// TestSlogHandlerConformance runs the standard testing/slogtest suite against
// the adapter, pinning the documented claim that it passes. Each line is mapped
// onto slogtest's shape: timestamp/level/message become the built-in keys and
// the extra object supplies the attributes.
func TestSlogHandlerConformance(t *testing.T) {
	var buf bytes.Buffer
	slogtest.Run(t,
		func(t *testing.T) slog.Handler {
			if strings.HasSuffix(t.Name(), "/zero-time") {
				// The documented exception (see NewSlogHandler): every line carries
				// a timestamp, so a zero Record.Time falls back to the logger clock
				// instead of being omitted. TestSlogZeroRecordTimeUsesClock pins it.
				t.Skip("this format always emits a timestamp")
			}
			buf.Reset()
			return NewSlogHandler(newTestLogger(&buf), nil)
		},
		func(t *testing.T) map[string]any {
			m := parseLine(t, &buf)
			got := map[string]any{
				slog.TimeKey:    m["timestamp"],
				slog.LevelKey:   m["level"],
				slog.MessageKey: m["message"],
			}
			if extra, ok := m["extra"].(map[string]any); ok {
				for k, v := range extra {
					got[k] = v
				}
			}
			return got
		},
	)
}

func TestSlogZeroRecordTimeUsesClock(t *testing.T) {
	var buf bytes.Buffer
	h := NewSlogHandler(newTestLogger(&buf), nil) // fixed clock at 2026-01-11

	if err := h.Handle(context.Background(), slog.NewRecord(time.Time{}, slog.LevelInfo, "t", 0)); err != nil {
		t.Fatal(err)
	}

	if m := parseLine(t, &buf); m["timestamp"] != "2026-01-11T00:15:34.123Z" {
		t.Errorf("a zero record time should fall back to the logger clock, got %v", m["timestamp"])
	}
}

func TestSlogEnabledRespectsMinLevel(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)
	log.SetMinLevel(WARN)
	sl := slog.New(NewSlogHandler(log, nil))

	sl.Info("dropped")
	if buf.Len() != 0 {
		t.Errorf("INFO should be dropped, got %q", buf.String())
	}

	sl.Warn("kept")
	if buf.Len() == 0 {
		t.Error("WARN should be emitted")
	}
}
