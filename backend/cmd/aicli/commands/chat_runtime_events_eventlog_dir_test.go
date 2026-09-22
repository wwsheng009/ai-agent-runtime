package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestChatRuntimeEventBridge_EventLogLazilyCreatesEventsDir 回归 `events dir
// missing`：会话 events/ 目录缺失时（旧进程早于
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

// TestChatRuntimeEventBridge_EventLogPrefersLegacyLayoutWhenPresent 回归旧布局
// 兼容：会话目录布局上线后，扁平 <session-id>.events/ 或更早的嵌套
// <sessionID>/ 中已存在 runtime-events.jsonl 的会话必须继续追加到原文件，
// 避免 /resume 事件链断连。
func TestChatRuntimeEventBridge_EventLogPrefersLegacyLayoutWhenPresent(t *testing.T) {
	logger := NewChatLogger("provider", "openai", "model", false, "")
	require.NoError(t, logger.SetLogDir(t.TempDir()))

	legacyPaths := logger.LegacyRuntimeEventsLogPaths()
	// 候选路径数量取决于 sessionID 是否还带旧格式字符：新会话 ID
	// （session_YYYYMMDDHHMMSS_<suffix>）解析后与目录名一致，只产生扁平
	// <session-id>.events/ 一个候选；旧 ID 才会额外追加嵌套布局候选。
	require.NotEmpty(t, legacyPaths)
	legacy := legacyPaths[0]
	require.NoError(t, os.MkdirAll(filepath.Dir(legacy), 0o755))
	require.NoError(t, os.WriteFile(legacy, []byte(`{"type":"assistant.reasoning"}`+"\n"), 0o644))

	bridge := newChatRuntimeEventBridge(&ChatSession{Logger: logger})
	bridge.appendEventLogLine([]byte(`{"type":"assistant.delta"}`))

	path, count, _, failures := bridge.eventLogStats()
	require.Zero(t, failures)
	require.Equal(t, uint64(1), count)
	require.Equal(t, legacy, path)
	raw, err := os.ReadFile(legacy)
	require.NoError(t, err)
	require.Contains(t, string(raw), "assistant.delta")

	newPath := filepath.Join(logger.RuntimeEventsDir(), "runtime-events.jsonl")
	_, statErr := os.Stat(newPath)
	require.True(t, os.IsNotExist(statErr), "legacy log present: new-layout file must not be created")
}
