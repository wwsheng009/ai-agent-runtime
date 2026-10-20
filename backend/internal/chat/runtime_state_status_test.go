package chat

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// newStatusTestStore 构造一个真实的 SQLite runtime store（临时目录）。
func newStatusTestStore(t *testing.T) *SQLiteRuntimeStore {
	t.Helper()
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{
		Path: filepath.Join(t.TempDir(), "runtime.sqlite"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// TestLoadStateStatusMatchesScalarFields 锁定标量投影与完整 LoadState 的一致性：
// status / turn / checkpoint / suspended / head_offset / updated_at 必须完全一致。
//
// 这是 LoadStateStatus 的唯一正确性契约 —— 它被用来替代 LoadState 做
// 「只看 status」的判定，任何标量漂移都会让对账结论出错。
func TestLoadStateStatusMatchesScalarFields(t *testing.T) {
	ctx := context.Background()
	store := newStatusTestStore(t)

	original := &RuntimeState{
		SessionID:             "sess-status-parity",
		Status:                SessionWaitingApproval,
		CurrentTurnID:         "turn-abc",
		CurrentCheckpointID:   "chk-1",
		SuspendedTurnID:       "turn-susp",
		LastRunTerminalReason: "approval_pending",
		HeadOffset:            42,
		UpdatedAt:             time.Now().UTC().Truncate(time.Microsecond),
	}
	require.NoError(t, store.SaveState(ctx, original))

	full, err := store.LoadState(ctx, original.SessionID)
	require.NoError(t, err)
	require.NotNil(t, full)

	scalar, err := store.LoadStateStatus(ctx, original.SessionID)
	require.NoError(t, err)
	require.NotNil(t, scalar)

	require.Equal(t, full.SessionID, scalar.SessionID)
	require.Equal(t, full.Status, scalar.Status)
	require.Equal(t, full.CurrentTurnID, scalar.CurrentTurnID)
	require.Equal(t, full.CurrentCheckpointID, scalar.CurrentCheckpointID)
	require.Equal(t, full.SuspendedTurnID, scalar.SuspendedTurnID)
	require.Equal(t, full.LastRunTerminalReason, scalar.LastRunTerminalReason)
	require.Equal(t, full.HeadOffset, scalar.HeadOffset)
	require.True(t, full.UpdatedAt.Equal(scalar.UpdatedAt),
		"UpdatedAt mismatch: full=%v scalar=%v", full.UpdatedAt, scalar.UpdatedAt)

	// 标量投影不得携带 JSON 形态字段。
	require.Nil(t, scalar.CurrentRunMeta, "scalar projection must not decode run meta")
	require.Nil(t, scalar.AmbientRunMeta, "scalar projection must not decode ambient run meta")
	require.False(t, scalar.StableToolSurfaceSet, "scalar projection must not decode tool surface")
	require.False(t, scalar.FrozenTurnToolsSet, "scalar projection must not decode frozen tools")
	require.Nil(t, scalar.PendingTool)
	require.Nil(t, scalar.PendingApproval)
	require.Nil(t, scalar.PendingQuestion)
}

// TestLoadStateStatusMissingSessionReturnsNil 缺失会话必须返回 (nil, nil)，
// 与 LoadState 一致（调用方用 nil 判定"没有持久状态"）。
func TestLoadStateStatusMissingSessionReturnsNil(t *testing.T) {
	ctx := context.Background()
	store := newStatusTestStore(t)

	state, err := store.LoadStateStatus(ctx, "session_does_not_exist")
	require.NoError(t, err)
	require.Nil(t, state)
}

// TestLoadStateStatusEmptySessionID 空 ID 直接返回 nil，不触碰数据库。
func TestLoadStateStatusEmptySessionID(t *testing.T) {
	ctx := context.Background()
	store := newStatusTestStore(t)

	state, err := store.LoadStateStatus(ctx, "   ")
	require.NoError(t, err)
	require.Nil(t, state)
}

// TestLoadStateStatusDoesNotDecodeToolSurface 是性能不变量哨兵：
// 写入一个超大的 stable tool surface 后，标量读取的分配量应显著小于完整读取。
//
// 不断言具体字节（随 Go 版本波动），只断言"标量投影明显更省"。
func TestLoadStateStatusDoesNotDecodeToolSurface(t *testing.T) {
	ctx := context.Background()
	store := newStatusTestStore(t)

	// 构造足够大的工具面：每个工具带 4 KB 描述。
	const toolCount = 40
	tools := make([]runtimetypes.ToolDefinition, 0, toolCount)
	for i := 0; i < toolCount; i++ {
		tools = append(tools, runtimetypes.ToolDefinition{
			Name:        "tool_" + strings.Repeat("x", 8) + string(rune('a'+i)),
			Description: strings.Repeat("d", 4096),
		})
	}
	state := &RuntimeState{
		SessionID:            "sess-big-tools",
		Status:               SessionRunning,
		StableToolSurface:    tools,
		StableToolSurfaceSet: true,
		UpdatedAt:            time.Now().UTC(),
	}
	require.NoError(t, store.SaveState(ctx, state))

	measure := func(fn func() error) uint64 {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		for i := 0; i < 20; i++ {
			require.NoError(t, fn())
		}
		runtime.ReadMemStats(&after)
		return after.TotalAlloc - before.TotalAlloc
	}

	fullAlloc := measure(func() error {
		_, err := store.LoadState(ctx, state.SessionID)
		return err
	})
	scalarAlloc := measure(func() error {
		_, err := store.LoadStateStatus(ctx, state.SessionID)
		return err
	})

	require.Less(t, scalarAlloc, fullAlloc,
		"scalar projection must allocate less than full LoadState (scalar=%d full=%d)", scalarAlloc, fullAlloc)
	t.Logf("alloc over 20 reads: full=%dB scalar=%dB (ratio %.2f)",
		fullAlloc, scalarAlloc, float64(scalarAlloc)/float64(fullAlloc))
}
