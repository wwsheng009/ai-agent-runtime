package commands

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/formatter"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func TestRunChatLoopInteractiveInitialPromptSubmitsOnceAndStaysInteractive(t *testing.T) {
	queue := newChatInputQueue(bufio.NewReader(strings.NewReader("")))
	queue.lines <- chatQueuedInput{Text: "follow up\n", Source: "stdin"}
	queue.lines <- chatQueuedInput{Text: "/exit\n", Source: "stdin"}

	executor := &fakeChatExecutor{output: "ok"}
	session := newInitialPromptLoopTestSession(executor)
	session.InputQueue = queue
	var output bytes.Buffer
	session.Interaction = newTestChatInteractionCoordinator(t, session)
	session.Interaction.SetWriter(&output)
	defer session.Interaction.Shutdown()

	// go test 的 stdin/stdout 是管道：管道/PTY 分支会把测试注入的输入队列置空，
	// 队列输入永远不会被消费。本用例锁定队列消费契约，先关闭该分支
	// （接缝说明见 chatPipeLineEditorPreferredFn）。
	prevPreferred := chatPipeLineEditorPreferredFn
	chatPipeLineEditorPreferredFn = func() bool { return false }
	t.Cleanup(func() { chatPipeLineEditorPreferredFn = prevPreferred })

	runChatLoop(session, false, "inspect project")

	want := []string{"inspect project", "follow up"}
	if !reflect.DeepEqual(executor.prompts, want) {
		t.Fatalf("submitted prompts = %#v, want %#v", executor.prompts, want)
	}
}

func TestRunChatLoopNoInteractiveInitialPromptExitsAfterOneSend(t *testing.T) {
	executor := &fakeChatExecutor{output: ""}
	session := newInitialPromptLoopTestSession(executor)
	session.NoInteractive = true

	runChatLoop(session, true, "inspect project")

	want := []string{"inspect project"}
	if !reflect.DeepEqual(executor.prompts, want) {
		t.Fatalf("submitted prompts = %#v, want %#v", executor.prompts, want)
	}
}

// TestRunChatLoopRetriedPromptRendersOneUserBlock 固化用户实际遇到的现场：
// 第一条 prompt 所在回合失败/中断（没有落 assistant 消息）后，用户重发同一
// 文本。canonical 历史按末条 user 去重只保留一条消息，因此终端/Scene 也只能
// 有一个 user 块；否则 resume 重放时第一条 prompt 会重复。
func TestRunChatLoopRetriedPromptRendersOneUserBlock(t *testing.T) {
	queue := newChatInputQueue(bufio.NewReader(strings.NewReader("")))
	queue.lines <- chatQueuedInput{Text: "retry me\n", Source: "stdin"}
	queue.lines <- chatQueuedInput{Text: "retry me\n", Source: "stdin"}
	queue.lines <- chatQueuedInput{Text: "/exit\n", Source: "stdin"}

	first := true
	executor := &fakeChatExecutor{
		onCall: func(_ context.Context, _ *ChatSession, _ string) (string, error) {
			if first {
				first = false
				return "", errors.New("simulated failed turn")
			}
			return "ok", nil
		},
	}
	session := newInitialPromptLoopTestSession(executor)
	session.InputQueue = queue
	var output bytes.Buffer
	session.Interaction = newTestChatInteractionCoordinator(t, session)
	session.Interaction.SetWriter(&output)
	defer session.Interaction.Shutdown()

	prevPreferred := chatPipeLineEditorPreferredFn
	chatPipeLineEditorPreferredFn = func() bool { return false }
	t.Cleanup(func() { chatPipeLineEditorPreferredFn = prevPreferred })

	runChatLoop(session, false, "")

	want := []string{"retry me", "retry me"}
	if !reflect.DeepEqual(executor.prompts, want) {
		t.Fatalf("submitted prompts = %#v, want %#v", executor.prompts, want)
	}
	if got := strings.Count(output.String(), ui.FormatUserMessage("retry me")); got != 1 {
		t.Fatalf("retried pending prompt rendered %d user blocks, want 1\n%s", got, output.String())
	}
	userMessages := 0
	for _, message := range session.Messages {
		if message.Role == "user" && message.Content == "retry me" {
			userMessages++
		}
	}
	if userMessages != 1 {
		t.Fatalf("canonical history kept %d retry messages, want 1: %#v", userMessages, session.Messages)
	}
}

func TestRunChatLoopExitsWhenExternalExitRequested(t *testing.T) {
	// 网关「关窗口」在节点进程内部置位退出请求；聊天循环必须在消费下一条
	// 输入（这里是启动消息）之前观察到它，否则关闭动作会被当成一次正常发送。
	executor := &fakeChatExecutor{output: "should not run"}
	session := newInitialPromptLoopTestSession(executor)
	session.exitRequested.Store(true)

	runChatLoop(session, true, "inspect project")

	if len(executor.prompts) != 0 {
		t.Fatalf("exit request must be observed before submitting input, got %#v", executor.prompts)
	}
}

func newInitialPromptLoopTestSession(executor *fakeChatExecutor) *ChatSession {
	return &ChatSession{
		Provider:     config.Provider{Protocol: "codex"},
		cancelCtx:    context.Background(),
		ChatExecutor: executor,
		Logger:       NewChatLogger("codex_ee", "codex", "test-model", false, "https://example.com"),
		Formatter:    formatter.NewMarkdownFormatter(false),
	}
}
