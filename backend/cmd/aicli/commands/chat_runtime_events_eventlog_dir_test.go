package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestChatRuntimeEventBridge_EventLogLazilyCreatesEventsDir 回归 `events dir
// missing`：<session-id>.events 目录缺失时（旧进程早于
// ensureSessionArtifactLayout 补丁启动、目录被外部清理、旧布局迁移等），
// 事件日志 append 必须惰性 MkdirAll 自愈并写入成功，而不是每次以 ENOENT
// 静默计入 eventLogFailures —— 后者会让整会话 runtime-events.jsonl 缺失，
// /resume 与崩溃恢复无法重放重建渲染模型。
func TestChatRuntimeEventBridge_EventLogLazilyCreatesEventsDir(t *testing.T) {
	logger := NewChatLogger("provider", "openai", "model", false, "")
	require.NoError(t, logger.SetLogDir(t.TempDir()))

	eventsDir := logger.RuntimeEventsDir()
	require.NotEmpty(t, eventsDir)
	require.NoError(t, os.RemoveAll(eventsDir))
	_, statErr := os.Stat(eventsDir)
	require.True(t, os.IsNotExist(statErr), "precondition: .events dir must be absent")

	bridge := newChatRuntimeEventBridge(&ChatSession{Logger: logger})
	bridge.appendEventLogLine([]byte(`{"type":"assistant.reasoning"}`))

	info, err := os.Stat(eventsDir)
	require.NoError(t, err, "append must lazily recreate the .events dir")
	require.True(t, info.IsDir())

	path, count, _, failures := bridge.eventLogStats()
	require.Zero(t, failures, "no append failure may be recorded")
	require.Equal(t, uint64(1), count)
	require.Equal(t, filepath.Join(eventsDir, "runtime-events.jsonl"), path)

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(raw), "assistant.reasoning")

	// 目录已存在时继续追加不得重建或截断（append-only 语义）。
	bridge.appendEventLogLine([]byte(`{"type":"assistant.delta"}`))
	raw, err = os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(raw), "assistant.reasoning")
	require.Contains(t, string(raw), "assistant.delta")
	_, count, _, failures = bridge.eventLogStats()
	require.Equal(t, uint64(2), count)
	require.Zero(t, failures)
}
