package commands

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

func TestHandleRetryCommandRestoresFailedPromptWithoutExecuting(t *testing.T) {
	executor := &fakeChatExecutor{}
	session := &ChatSession{ChatExecutor: executor}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	rememberChatTurnRecovery(session, "修复失败的构建", false)

	output := captureStdout(t, func() {
		handleRetryCommand(session, "/retry")
	})

	if executor.called {
		t.Fatal("expected /retry to restore a draft without executing the agent")
	}
	if got := coord.PromptInputSnapshot().Text; got != "修复失败的构建" {
		t.Fatalf("expected failed prompt to be restored, got %q", got)
	}
	if !strings.Contains(output, "当前未执行任何操作") {
		t.Fatalf("expected explicit no-execution feedback, got %q", output)
	}
}

func TestHandleRetryCommandInterruptedTurnWarnsAboutPartialToolEffects(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	rememberChatTurnRecovery(session, "继续修改文件", true)

	output := captureStdout(t, func() {
		handleRetryCommand(session, "/retry")
	})

	if got := coord.PromptInputSnapshot().Text; got != "继续修改文件" {
		t.Fatalf("expected interrupted prompt to be restored, got %q", got)
	}
	if !strings.Contains(output, "工具可能已部分执行") || !strings.Contains(output, "检查草稿") {
		t.Fatalf("expected side-effect warning, got %q", output)
	}
}

func TestHandleRetryCommandDoesNotOverwriteExistingDraft(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	coord.SetPromptInput("用户正在编辑的新草稿")
	rememberChatTurnRecovery(session, "旧失败消息", false)

	output := captureStdout(t, func() {
		handleRetryCommand(session, "/retry")
	})

	if got := coord.PromptInputSnapshot().Text; got != "用户正在编辑的新草稿" {
		t.Fatalf("expected existing draft to remain unchanged, got %q", got)
	}
	if !strings.Contains(output, "未覆盖现有内容") {
		t.Fatalf("expected overwrite refusal, got %q", output)
	}
}

func TestHandleRetryCommandRejectsForceExecution(t *testing.T) {
	executor := &fakeChatExecutor{}
	session := &ChatSession{ChatExecutor: executor}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	rememberChatTurnRecovery(session, "可能已执行工具的消息", true)

	output := captureStdout(t, func() {
		handleRetryCommand(session, "/retry --force")
	})

	if executor.called {
		t.Fatal("expected /retry --force not to execute the agent")
	}
	if got := coord.PromptInputSnapshot().Text; got != "" {
		t.Fatalf("expected rejected force retry not to change the draft, got %q", got)
	}
	if !strings.Contains(output, "不接受参数") {
		t.Fatalf("expected force retry to be rejected, got %q", output)
	}
}

func TestHandleRetryCommandRestoresQueueDraftWithoutSubmitting(t *testing.T) {
	queue := newChatInputQueue(bufio.NewReader(strings.NewReader("")))
	session := &ChatSession{InputQueue: queue}
	rememberChatTurnRecovery(session, "检查网络错误", false)

	handleRetryCommand(session, "/retry")

	if !queue.hasDraft() {
		t.Fatal("expected retry prompt to remain an editable queue draft")
	}
	if queue.hasReadySubmission() || queue.pendingCount() != 0 {
		t.Fatalf("expected restored draft not to be submitted, ready=%v pending=%d", queue.hasReadySubmission(), queue.pendingCount())
	}
}

func TestRenderChatTurnRecoveryHintExplainsSafeInterruptedRecovery(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	coord.SetWriter(&output)
	session.Interaction = coord
	rememberChatTurnRecovery(session, "可能运行过工具", true)

	renderChatTurnRecoveryHint(session)

	got := output.String()
	if !strings.Contains(got, "/retry") || !strings.Contains(got, "不会自动执行") || !strings.Contains(got, "可能已部分执行工具") {
		t.Fatalf("expected actionable safe recovery hint, got %q", got)
	}
}

func TestRenderChatTurnRecoveryHintExplainsLiveSessionLeaseConflict(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	coord.SetWriter(&output)
	session.Interaction = coord
	rememberChatTurnRecovery(session, "继续处理", false)
	lease := &runtimechat.SessionLease{
		SessionID: "session-1",
		OwnerID:   "aicli-actor:other-host:1234:session-1",
		OwnerKind: "aicli-actor",
		ExpiresAt: time.Now().Add(time.Minute),
	}
	renderChatTurnRecoveryHintForError(session, fmt.Errorf("wrapped: %w", &runtimechat.LeaseConflictError{Lease: lease}))

	got := output.String()
	if !strings.Contains(got, "仍被其他执行器占用") || !strings.Contains(got, "不会强制抢占") {
		t.Fatalf("expected lease-specific recovery hint, got %q", got)
	}
}

func TestHandleRetryCommandRequiresExplicitFailureRecord(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord

	output := captureStdout(t, func() {
		handleRetryCommand(session, "/retry")
	})

	if got := coord.PromptInputSnapshot().Text; got != "" {
		t.Fatalf("expected no draft without an explicit failure record, got %q", got)
	}
	if !strings.Contains(output, "没有可恢复") {
		t.Fatalf("expected empty recovery feedback, got %q", output)
	}
}

func TestHandleRetryCommandRejectsRecoveryFromAnotherSession(t *testing.T) {
	session := &ChatSession{RuntimeSession: &runtimechat.Session{ID: "session-old"}}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	rememberChatTurnRecovery(session, "旧会话失败消息", false)
	session.RuntimeSession = &runtimechat.Session{ID: "session-new"}

	output := captureStdout(t, func() {
		handleRetryCommand(session, "/retry")
	})

	if got := coord.PromptInputSnapshot().Text; got != "" {
		t.Fatalf("expected cross-session recovery to be rejected, got %q", got)
	}
	if chatTurnRecoverySnapshot(session) != nil {
		t.Fatal("expected stale cross-session recovery record to be cleared")
	}
	if !strings.Contains(output, "会话已切换") {
		t.Fatalf("expected session mismatch feedback, got %q", output)
	}
}

func TestFinishSuccessfulChatSendClearsRecoveryForEverySendPath(t *testing.T) {
	session := &ChatSession{}
	rememberChatTurnRecovery(session, "已失败的旧消息", false)

	finishSuccessfulChatSend(session, "", false)

	if chatTurnRecoverySnapshot(session) != nil {
		t.Fatal("expected successful send finalization to clear stale recovery")
	}
}
