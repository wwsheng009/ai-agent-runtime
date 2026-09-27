package tools

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

// TestViewDedupPeekDoesNotDeadlockWithIdleSweep pins the 2026-09-27 P1: the
// peeker held its bucket mutex while sessionHasFullRead could trigger the
// amortized sweep, which locks every bucket — including the one already held.
// A sequential re-read of the same file then hung forever once the sweep counter
// lined up. The test forces that alignment instead of waiting ~512 reads.
func TestViewDedupPeekDoesNotDeadlockWithIdleSweep(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	ctx := dedupSessionContext(t)
	root := t.TempDir()
	path := filepath.Join(root, "deadlock.txt")
	if err := os.WriteFile(path, []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := newViewToolAt(t, root)
	first := executeViewParams(t, tool, ctx, map[string]interface{}{"file_path": "deadlock.txt"})
	if first.Metadata["dedup_hit"] == true {
		t.Fatal("fixture: first read must populate the cache, not hit it")
	}
	if toolctx.SessionID(ctx) == "" {
		t.Fatal("fixture: session id missing")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	// Line the amortized counter up so the next state access runs a sweep that
	// walks this session's own buckets.
	sessionReadStateSweepCounter.Store(sessionReadStateSweepInterval - 1)

	done := make(chan struct{})
	go func() {
		defer close(done)
		// offset=0 is the path that consults sessionHasFullRead inside the peek.
		_, _, _ = viewDedupPeek(ctx, path, info, 0, viewDefaultLimit)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("viewDedupPeek deadlocked against the idle sweep")
	}
}
