package background

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// ReadOutputTail is the supervision projection's business-payload reader: it
// must return the END of a job's output, not its beginning, so the parent sees
// the outcome instead of the banner.
func TestReadOutputTailReturnsEndOfRingBuffer(t *testing.T) {
	manager := NewManager(Config{})
	t.Cleanup(func() { _ = manager.Close() })

	job := &managedJob{
		ctx:    context.Background(),
		info:   Job{ID: "job-tail-live", Status: StatusRunning},
		output: newOutputBuffer(4096),
	}
	manager.mu.Lock()
	manager.jobs[job.info.ID] = job
	manager.mu.Unlock()

	_, err := job.output.Write([]byte("banner line\n"))
	require.NoError(t, err)
	_, err = job.output.Write([]byte("middle line\n"))
	require.NoError(t, err)
	_, err = job.output.Write([]byte("FINAL: 3 tests passed\n"))
	require.NoError(t, err)

	tail, ok := manager.ReadOutputTail(context.Background(), "job-tail-live", 24)
	require.True(t, ok)
	require.Contains(t, tail, "FINAL: 3 tests passed")
	require.NotContains(t, tail, "banner line", "the head must be dropped: only the tail explains the outcome")
}

// A recovered job has no in-memory buffer; its log file is the only source.
func TestReadOutputTailFallsBackToLogFile(t *testing.T) {
	manager := NewManager(Config{})
	t.Cleanup(func() { _ = manager.Close() })

	path := filepath.Join(t.TempDir(), "job.log")
	require.NoError(t, os.WriteFile(path, []byte("first\nsecond\nTHIRD\n"), 0o644))

	job := &managedJob{
		ctx:     context.Background(),
		info:    Job{ID: "job-tail-log", Status: StatusCompleted, LogPath: path},
		logPath: path,
	}
	manager.mu.Lock()
	manager.jobs[job.info.ID] = job
	manager.mu.Unlock()

	tail, ok := manager.ReadOutputTail(context.Background(), "job-tail-log", 12)
	require.True(t, ok)
	require.Contains(t, tail, "THIRD")
	require.NotContains(t, tail, "first")
}

// Unknown jobs and empty output must report ok=false so the digest renders no
// detail line instead of an empty "输出末尾：" header.
func TestReadOutputTailMissingOrEmpty(t *testing.T) {
	manager := NewManager(Config{})
	t.Cleanup(func() { _ = manager.Close() })

	_, ok := manager.ReadOutputTail(context.Background(), "job-does-not-exist", 0)
	require.False(t, ok)

	empty := &managedJob{
		ctx:    context.Background(),
		info:   Job{ID: "job-tail-empty", Status: StatusRunning},
		output: newOutputBuffer(1024),
	}
	manager.mu.Lock()
	manager.jobs[empty.info.ID] = empty
	manager.mu.Unlock()

	_, ok = manager.ReadOutputTail(context.Background(), "job-tail-empty", 0)
	require.False(t, ok)
}

// A tail cut in the middle of a multi-byte rune must not emit a replacement
// character at the start of the parent-visible text.
func TestReadOutputTailTrimsPartialLeadingRune(t *testing.T) {
	manager := NewManager(Config{})
	t.Cleanup(func() { _ = manager.Close() })

	job := &managedJob{
		ctx:    context.Background(),
		info:   Job{ID: "job-tail-rune", Status: StatusRunning},
		output: newOutputBuffer(4096),
	}
	manager.mu.Lock()
	manager.jobs[job.info.ID] = job
	manager.mu.Unlock()

	_, err := job.output.Write([]byte(strings.Repeat("汉", 20)))
	require.NoError(t, err)

	// 7 bytes starts inside the second 3-byte rune.
	tail, ok := manager.ReadOutputTail(context.Background(), "job-tail-rune", 7)
	require.True(t, ok)
	require.NotContains(t, string([]rune(tail)[:1]), string(rune(0xFFFD)))
	require.True(t, strings.HasPrefix(tail, "汉"))
}
