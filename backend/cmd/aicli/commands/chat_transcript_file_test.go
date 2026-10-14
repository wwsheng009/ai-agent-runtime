package commands

import (
	"testing"

	"github.com/stretchr/testify/require"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// --transcript-file：把"已提交的语义条目"投影为无 ANSI 纯文本（供脚本/E2E
// 断言）。断言经 transcriptSinkOverride 在进程内截获：进程级 journal 写者由
// 并行测试共享，文件级断言会被其它测试干扰。

func newTranscriptCaptureBridge() (*chatRuntimeEventBridge, *[]string) {
	session := &ChatSession{transcriptFile: "unused-in-test"}
	bridge := newChatRuntimeEventBridge(session)
	lines := &[]string{}
	bridge.transcriptSinkOverride = func(text string) { *lines = append(*lines, text) }
	return bridge, lines
}

func TestTranscriptFileProjectsSemanticItems(t *testing.T) {
	bridge, lines := newTranscriptCaptureBridge()
	require.Equal(t, "unused-in-test", bridge.transcriptFile, "桥接器必须从会话带上 transcript 目标")

	bridge.appendInjectionLog(eventLogInjection{UserInput: "hello\nworld"})
	bridge.appendInjectionLog(eventLogInjection{Command: "Goal 已设置\nGoal: active"})
	bridge.appendInjectionLog(eventLogInjection{Error: "错误: boom"})
	bridge.appendInjectionLog(eventLogInjection{Assistant: "direct answer"})
	bridge.appendInjectionLog(eventLogInjection{HistoryReset: true}) // 重放边界：不产生可读条目

	bridge.appendEventLog(runtimeevents.Event{
		Type: "assistant_message", Payload: map[string]interface{}{"content": "final answer"},
	})
	bridge.appendEventLog(runtimeevents.Event{
		Type: "tool.requested", ToolName: "view",
		Payload: map[string]interface{}{"arg_preview": "files=[2]"},
	})
	bridge.appendEventLog(runtimeevents.Event{
		Type: "tool.completed", ToolName: "view",
		Payload: map[string]interface{}{"outcome": "success", "ok": true},
	})
	// delta 增量不入 transcript（噪音/可重建）。
	bridge.appendEventLog(deltaMergeTestEvent(1, "wip"))
	bridge.flushStreamDeltaLog()

	want := []string{
		"> hello",
		"> world",
		"Goal 已设置\nGoal: active",
		"错误: boom",
		"direct answer",
		"final answer",
		"[tool] view requested: files=[2]",
		"[tool] view -> success",
	}
	require.Equal(t, want, *lines)
	for _, line := range *lines {
		require.NotContains(t, line, "\x1b", "transcript 不得包含 ANSI 控制序列")
	}
}

func TestParseChatCommandOptionsLoadsTranscriptFile(t *testing.T) {
	cmd := NewChatCommand(func() *config.Config { return nil })
	opts, err := parseChatCommandOptions(cmd, &config.Config{})
	require.NoError(t, err)
	require.Empty(t, opts.TranscriptFile, "未指定时保持关闭")

	cmd = NewChatCommand(func() *config.Config { return nil })
	require.NoError(t, cmd.ParseFlags([]string{"--transcript-file", `C:\tmp\goal-e2e\transcript.txt`}))
	opts, err = parseChatCommandOptions(cmd, &config.Config{})
	require.NoError(t, err)
	require.Equal(t, `C:\tmp\goal-e2e\transcript.txt`, opts.TranscriptFile)
}
