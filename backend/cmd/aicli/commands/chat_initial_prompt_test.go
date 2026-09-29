package commands

import (
	"bufio"
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/formatter"
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

func newInitialPromptLoopTestSession(executor *fakeChatExecutor) *ChatSession {
	return &ChatSession{
		Provider:     config.Provider{Protocol: "codex"},
		cancelCtx:    context.Background(),
		ChatExecutor: executor,
		Logger:       NewChatLogger("codex_ee", "codex", "test-model", false, "https://example.com"),
		Formatter:    formatter.NewMarkdownFormatter(false),
	}
}
