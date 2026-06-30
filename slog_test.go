package logging

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
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
	sl := slog.New(NewSlogHandler(log, &SlogOptions{EventKey: ""}))

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
