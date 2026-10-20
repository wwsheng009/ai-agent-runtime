package chat

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func newDedupTestStore(t *testing.T) *SQLiteRuntimeStore {
	t.Helper()
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{
		Path: filepath.Join(t.TempDir(), "runtime.sqlite"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func sampleToolSurface(tag string) []runtimetypes.ToolDefinition {
	return []runtimetypes.ToolDefinition{
		{Name: "read_" + tag, Description: strings.Repeat("d", 2048)},
		{Name: "write_" + tag, Description: strings.Repeat("w", 2048)},
	}
}

// TestToolSurfaceContentIsDeduplicatedAcrossSessions 锁定内容寻址的核心收益：
// N 个会话共享同一套工具面时，内容表只落一份。
func TestToolSurfaceContentIsDeduplicatedAcrossSessions(t *testing.T) {
	ctx := context.Background()
	store := newDedupTestStore(t)

	shared := sampleToolSurface("shared")
	const sessions = 12
	for i := 0; i < sessions; i++ {
		state := &RuntimeState{
			SessionID:            "sess-dedup-" + string(rune('a'+i)),
			Status:               SessionIdle,
			StableToolSurface:    shared,
			StableToolSurfaceSet: true,
			UpdatedAt:            time.Now().UTC(),
		}
		require.NoError(t, store.SaveState(ctx, state))
	}

	var contents, refs int
	require.NoError(t, store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM runtime_tool_surface_content`).Scan(&contents))
	require.NoError(t, store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM session_runtime_state WHERE stable_tool_surface_ref IS NOT NULL`).Scan(&refs))

	require.Equal(t, 1, contents, "same tool surface must be stored exactly once")
	require.Equal(t, sessions, refs, "every session must carry a ref")

	// 内联列必须为空：新行只写 ref。
	var inline int
	require.NoError(t, store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM session_runtime_state WHERE stable_tool_surface_json IS NOT NULL`).Scan(&inline))
	require.Zero(t, inline, "new rows must not inline the tool surface")
}

// TestLoadStateResolvesToolSurfaceFromRef 锁定读取侧：通过 ref 取回的内容
// 必须与写入时完全一致（名称、描述、顺序）。
func TestLoadStateResolvesToolSurfaceFromRef(t *testing.T) {
	ctx := context.Background()
	store := newDedupTestStore(t)

	original := sampleToolSurface("resolve")
	state := &RuntimeState{
		SessionID:            "sess-ref-resolve",
		Status:               SessionRunning,
		StableToolSurface:    original,
		StableToolSurfaceSet: true,
		UpdatedAt:            time.Now().UTC(),
	}
	require.NoError(t, store.SaveState(ctx, state))

	loaded, err := store.LoadState(ctx, state.SessionID)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.True(t, loaded.StableToolSurfaceSet)
	require.Len(t, loaded.StableToolSurface, len(original))
	for i := range original {
		require.Equal(t, original[i].Name, loaded.StableToolSurface[i].Name)
		require.Equal(t, original[i].Description, loaded.StableToolSurface[i].Description)
	}
}

// TestLoadStateFallsBackToInlineColumn 锁定向后兼容：v25 之前写入的行
// 只有内联列，必须仍然能读出来。
func TestLoadStateFallsBackToInlineColumn(t *testing.T) {
	ctx := context.Background()
	store := newDedupTestStore(t)

	// 直接写一行"旧格式"：stable_tool_surface_json 有值，ref 为空。
	payload := `[{"name":"legacy_tool","description":"legacy desc"}]`
	// store 是惰性打开的：先 ensure 建库，才能直接操作底层连接。
	require.NoError(t, store.ensure())
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO session_runtime_state (
			session_id, status, current_turn_id, current_checkpoint_id, suspended_turn_id,
			current_run_meta_json, ambient_run_meta_json, stable_tool_surface_json, frozen_turn_tools_json,
			pending_tool_json, pending_approval_json, pending_question_json,
			last_run_terminal_reason, head_offset, active_job_ids_json, updated_at
		) VALUES ('sess-legacy-inline','idle',NULL,NULL,NULL,NULL,NULL,?,NULL,NULL,NULL,NULL,NULL,0,'[]',?)
	`, payload, time.Now().UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)

	loaded, err := store.LoadState(ctx, "sess-legacy-inline")
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.True(t, loaded.StableToolSurfaceSet, "legacy inline row must still load")
	require.Len(t, loaded.StableToolSurface, 1)
	require.Equal(t, "legacy_tool", loaded.StableToolSurface[0].Name)
}

// TestInvalidateStableToolSurfacesClearsRef 锁定失效语义：
// 清理必须同时清空 ref，否则失效后仍会从内容表读回旧工具面。
func TestInvalidateStableToolSurfacesClearsRef(t *testing.T) {
	ctx := context.Background()
	store := newDedupTestStore(t)

	state := &RuntimeState{
		SessionID:            "sess-invalidate-ref",
		Status:               SessionIdle,
		StableToolSurface:    sampleToolSurface("inv"),
		StableToolSurfaceSet: true,
		UpdatedAt:            time.Now().UTC(),
	}
	require.NoError(t, store.SaveState(ctx, state))

	affected, err := store.InvalidateStableToolSurfaces(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, affected)

	loaded, err := store.LoadState(ctx, state.SessionID)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.False(t, loaded.StableToolSurfaceSet, "invalidate must clear the resolved surface")
	require.Empty(t, loaded.StableToolSurface)
}

// TestDifferentToolSurfacesStoreSeparately 反向保护：不同的工具面不能合并。
func TestDifferentToolSurfacesStoreSeparately(t *testing.T) {
	ctx := context.Background()
	store := newDedupTestStore(t)

	for _, tag := range []string{"alpha", "beta"} {
		state := &RuntimeState{
			SessionID:            "sess-distinct-" + tag,
			Status:               SessionIdle,
			StableToolSurface:    sampleToolSurface(tag),
			StableToolSurfaceSet: true,
			UpdatedAt:            time.Now().UTC(),
		}
		require.NoError(t, store.SaveState(ctx, state))
	}

	var contents int
	require.NoError(t, store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM runtime_tool_surface_content`).Scan(&contents))
	require.Equal(t, 2, contents, "distinct surfaces must not be merged")
}
