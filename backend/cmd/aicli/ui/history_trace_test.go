package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHistoryTraceAppendsToConfiguredFile pins the sink contract: the hook is a
// file path, never stderr (the live alt-screen frame is exactly what it
// observes), and each line carries the reduction prefix plus the decision.
func TestHistoryTraceAppendsToConfiguredFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history-trace.log")
	t.Setenv(historyTraceEnv, path)
	t.Cleanup(historyTrace.close)

	state := UIControllerState{}
	state.LayoutGeneration = 7
	traceHistoryReduction(state, "ack token=%d frame=%d", 3, 11)
	traceHistoryReduction(state, "fail token=%d err=%v", 4, "boom")

	lines := readHistoryTraceLines(t, path)
	if len(lines) != 2 {
		t.Fatalf("trace lines = %d, want 2: %q", len(lines), lines)
	}
	const prefix = "[hist] gen=7 next=0 unknown=false recon=false pending=0 | "
	if !strings.HasPrefix(lines[0], prefix+"ack token=3 frame=11") {
		t.Fatalf("ack trace line = %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], prefix+"fail token=4 err=boom") {
		t.Fatalf("fail trace line = %q", lines[1])
	}
}

// TestHistoryTraceFollowsPathChangesAndStopsWhenDisabled proves a live session
// can redirect or stop the trace without restarting, and that a disabled hook
// appends into neither the old nor the new file.
func TestHistoryTraceFollowsPathChangesAndStopsWhenDisabled(t *testing.T) {
	first := filepath.Join(t.TempDir(), "first.log")
	second := filepath.Join(t.TempDir(), "second.log")
	t.Cleanup(historyTrace.close)

	t.Setenv(historyTraceEnv, first)
	traceHistoryReduction(UIControllerState{}, "first")
	t.Setenv(historyTraceEnv, second)
	traceHistoryReduction(UIControllerState{}, "second")
	t.Setenv(historyTraceEnv, "")
	traceHistoryReduction(UIControllerState{}, "after-disable")

	firstLines := readHistoryTraceLines(t, first)
	if len(firstLines) != 1 || !strings.HasSuffix(firstLines[0], "first") {
		t.Fatalf("first sink = %q, want exactly the pre-redirect decision", firstLines)
	}
	secondLines := readHistoryTraceLines(t, second)
	if len(secondLines) != 1 || !strings.HasSuffix(secondLines[0], "second") {
		t.Fatalf("second sink = %q, want exactly the redirected decision", secondLines)
	}
	for _, line := range append(append([]string{}, firstLines...), secondLines...) {
		if strings.Contains(line, "after-disable") {
			t.Fatalf("disabled hook kept writing: %q", line)
		}
	}
}

// TestHistoryTraceRejectsBooleanToggleWithoutCreatingFile pins the migration
// away from the old boolean spelling: AIR_TRACE_HISTORY=1 must not silently
// become a relative file named "1" in the working directory.
func TestHistoryTraceRejectsBooleanToggleWithoutCreatingFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Cleanup(historyTrace.close)
	t.Setenv(historyTraceEnv, "1")

	traceHistoryReduction(UIControllerState{}, "ack token=1")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read working directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("boolean hook value created %v", entries)
	}
}

func readHistoryTraceLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read trace %s: %v", path, err)
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}
