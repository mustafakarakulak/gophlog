package logging

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// TestPayloadCycleGuard verifies a self-referential payload is bounded by the
// depth guard instead of recursing until the stack overflows.
func TestPayloadCycleGuard(t *testing.T) {
	type Node struct {
		Name string `json:"name"`
		Next *Node  `json:"next,omitempty"`
	}
	n := &Node{Name: "a"}
	n.Next = n // cycle

	var buf bytes.Buffer
	log := newTestLogger(&buf)

	done := make(chan struct{})
	go func() {
		log.Info("cyclic", "evt").WithPayload(n).Log()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("logging a cyclic payload did not return (possible infinite recursion)")
	}

	if !strings.Contains(buf.String(), "max depth exceeded") {
		t.Errorf("expected depth-guard marker in output: %s", buf.String())
	}
}

// TestSetMinLevel verifies the minimum level can be changed at runtime.
func TestSetMinLevel(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)

	log.SetMinLevel(WARN)
	if log.Enabled(INFO) {
		t.Fatal("INFO should be disabled after SetMinLevel(WARN)")
	}
	log.Info("dropped", "evt").Log()
	if buf.Len() != 0 {
		t.Errorf("INFO entry should be dropped, got: %s", buf.String())
	}

	log.Warn("kept", "evt").Log()
	if buf.Len() == 0 {
		t.Error("WARN entry should be emitted")
	}

	log.SetMinLevel(DEBUG)
	if !log.Enabled(INFO) {
		t.Error("INFO should be enabled after SetMinLevel(DEBUG)")
	}
}

// TestNoHTMLEscape verifies <, > and & are emitted literally rather than as
// \u00xx escapes, keeping log lines human-readable.
func TestNoHTMLEscape(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)

	log.Info("a<b>&c", "evt").Log()
	if !strings.Contains(buf.String(), "a<b>&c") {
		t.Errorf("HTML chars should not be escaped: %s", buf.String())
	}
}
