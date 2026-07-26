package commands

import (
	"strings"
	"testing"
	"time"
)

func TestChatStartupTiming_MarkOrderAndSummary(t *testing.T) {
	timing := &chatStartupTiming{
		enabled: true,
		start:   time.Now().Add(-50 * time.Millisecond),
		last:    time.Now().Add(-50 * time.Millisecond),
	}
	timing.mark("begin")
	time.Sleep(5 * time.Millisecond)
	timing.mark("ready")
	if len(timing.marks) != 2 {
		t.Fatalf("expected 2 marks, got %d", len(timing.marks))
	}
	if timing.marks[0].name != "begin" || timing.marks[1].name != "ready" {
		t.Fatalf("unexpected mark names: %#v", timing.marks)
	}
	if timing.marks[1].elapsed < timing.marks[0].elapsed {
		t.Fatalf("elapsed should be monotonic: %#v", timing.marks)
	}

	parts := make([]string, 0, len(timing.marks))
	for _, mark := range timing.marks {
		parts = append(parts, mark.name)
	}
	if got := strings.Join(parts, ","); got != "begin,ready" {
		t.Fatalf("unexpected mark sequence %q", got)
	}
}

func TestChatStartupTimingEnabled(t *testing.T) {
	t.Setenv("AICLI_STARTUP_TIMING", "1")
	if !chatStartupTimingEnabled() {
		t.Fatal("expected AICLI_STARTUP_TIMING=1 to enable timing")
	}
	t.Setenv("AICLI_STARTUP_TIMING", "0")
	if chatStartupTimingEnabled() {
		t.Fatal("expected AICLI_STARTUP_TIMING=0 to disable timing")
	}
}
