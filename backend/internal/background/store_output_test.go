package background

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAppendOutputEventStoresLogReferenceAndHydrates(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "job.log")
	chunk := "hello 中文\n"
	require.NoError(t, os.WriteFile(logPath, []byte(chunk), 0o644))

	store, err := NewSQLiteStore(&StoreConfig{Path: filepath.Join(dir, "background.sqlite")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, store.SaveJob(ctx, Job{
		ID:        "job-output-log",
		SessionID: "session-output",
		Kind:      "shell",
		Status:    StatusRunning,
		CreatedAt: time.Now().UTC(),
		LogPath:   logPath,
	}))
	require.NoError(t, store.AppendEvent(ctx, "job-output-log", "output", map[string]interface{}{
		"offset":      0,
		"next_offset": len(chunk),
		"size":        len(chunk),
		"stream":      "stdout",
		"chunk":       chunk,
	}))

	var raw string
	require.NoError(t, store.db.QueryRowContext(ctx, `
		SELECT payload_json FROM background_job_events
		WHERE job_id = ? AND type = 'output'
	`, "job-output-log").Scan(&raw))
	require.NotContains(t, raw, `"chunk"`, "log-backed output must not duplicate the body in SQLite")
	require.Contains(t, raw, `"output_ref":"log"`)

	events, err := store.ListEvents(ctx, "job-output-log", 0, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, chunk, events[0].Payload["chunk"], "ListEvents must hydrate the chunk from the log")
	require.NotContains(t, events[0].Payload, "output_ref")
}

func TestAppendOutputEventKeepsInlineChunkWithoutLog(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := NewSQLiteStore(&StoreConfig{Path: filepath.Join(dir, "background.sqlite")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, store.SaveJob(ctx, Job{
		ID:        "job-output-inline",
		SessionID: "session-inline",
		Kind:      "shell",
		Status:    StatusRunning,
		CreatedAt: time.Now().UTC(),
	}))
	chunk := "inline output"
	require.NoError(t, store.AppendEvent(ctx, "job-output-inline", "output", map[string]interface{}{
		"offset":      0,
		"next_offset": len(chunk),
		"size":        len(chunk),
		"stream":      "stdout",
		"chunk":       chunk,
	}))

	events, err := store.ListEvents(ctx, "job-output-inline", 0, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, chunk, events[0].Payload["chunk"])
}
