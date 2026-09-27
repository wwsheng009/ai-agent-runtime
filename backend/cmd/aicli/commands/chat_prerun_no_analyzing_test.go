package commands

// P3 尾部（docs/plan/aicli-chat-submit-run-epoch-wedge-hardening.md）：
// e2e 断言「没有活动 run 就不得出现 Analyzing 帧」。
//
// 事故现场的反面契约：提交在预跑阶段失败（actor 构建失败、从未 BeginRun）时，
// 界面既不能进入等待态，也不能渲染 "Analyzing"（Waiting/Thinking/Planning 三种
// 状态在状态行上都显示为 "Analyzing"）。本用例用真实 SessionManager + UI surface
// 走完整 sendMessage 路径，只把 host 构造成预跑必然失败。

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

func TestPreRunFailureNeverRendersAnalyzingFrame(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	manager, userID, dir, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	runtimeSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	// 预跑失败注入：host 存在但没有 SessionHub——chatActorForSession 在
	// GetOrCreate 之前就失败，本回合永远不会 BeginRun。
	host := &localChatRuntimeHost{}

	session := &ChatSession{
		ProviderName:     "test-provider",
		PermissionMode:   runtimepolicy.ModeDefault,
		Model:            "test-model",
		SessionManager:   manager,
		RuntimeSession:   runtimeSession,
		SessionUserID:    userID,
		SessionDir:       dir,
		LocalRuntimeHost: host,
		ChatExecutor:     newAICLIActorChatExecutor(),
		cancelCtx:        context.Background(),
	}
	coord := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coord
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	session.Surface = surface

	frameText := func() string {
		var text strings.Builder
		for _, row := range surface.ComposedFrameForTest() {
			for _, cell := range row {
				if !cell.Cont {
					text.WriteString(cell.Text)
				}
			}
			text.WriteByte('\n')
		}
		return text.String()
	}

	var response string
	var submitErr error
	var frameAfterFailure string
	captureSurfaceStdout(t, func() {
		coord.SetWriter(os.Stdout)
		coord.SetSurface(surface)
		coord.PrintPrompt()
		coord.SetPromptInput("hello")
		coord.ResetPromptState()
		renderSubmittedUserInputEcho(session, "hello")

		response, submitErr = sendMessage(session, "hello")
		coord.waitUIActorIdle()
		frameAfterFailure = frameText()
	})

	if submitErr == nil {
		t.Fatalf("pre-run failure must surface an error (response=%q)", response)
	}
	if coord.WaitingArmed() {
		t.Fatal("no run was opened: waiting state must stay unarmed")
	}
	if bridge := session.RuntimeEventBridge; bridge != nil && bridge.RunEpoch() != 0 {
		t.Fatalf("pre-run failure must not open a run epoch (epoch=%d)", bridge.RunEpoch())
	}
	// 正面控制：帧必须真的捕获到了本回合的提交行，否则下面的负向断言可能是
	// "捕获为空"造成的空洞通过。
	if !strings.Contains(frameAfterFailure, "hello") {
		t.Fatalf("sanity: captured frame must contain the submitted row:\n%s", frameAfterFailure)
	}
	if strings.Contains(frameAfterFailure, "Analyzing") {
		t.Fatalf("no Analyzing frame may be rendered when no run opened:\n%s", frameAfterFailure)
	}
}
