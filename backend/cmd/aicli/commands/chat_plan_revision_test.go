package commands

// §4.4 自动修订回合（CLI 半程）的测试：判定口径 + 两条投影的接线。

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

func TestPlanRevisionAfterCommitEffectGating(t *testing.T) {
	interactive := newPlanCommandSession(runtimepolicy.ModeDefault)

	if got := planRevisionAfterCommitEffect(interactive, "request_changes", "补上回滚风险"); got != agent.PlanRevisionPrompt() {
		t.Fatalf("request_changes + notes should start a revision turn, got %q", got)
	}
	if got := planRevisionAfterCommitEffect(interactive, "request_changes", "   "); got != "" {
		t.Fatalf("empty notes must not start a blind revision, got %q", got)
	}
	if got := planRevisionAfterCommitEffect(interactive, "approve", "looks good"); got != "" {
		t.Fatalf("approve must not start a revision turn, got %q", got)
	}
	if got := planRevisionAfterCommitEffect(interactive, "quit", "drop it"); got != "" {
		t.Fatalf("quit must not start a revision turn, got %q", got)
	}

	// 脚本 / JSON 保持旧语义：意见留待下一次输入交付。
	scripted := newPlanCommandSession(runtimepolicy.ModeDefault)
	scripted.NoInteractive = true
	if got := planRevisionAfterCommitEffect(scripted, "request_changes", "补上回滚风险"); got != "" {
		t.Fatalf("non-interactive sessions must not auto-start a turn, got %q", got)
	}
	jsonSession := newPlanCommandSession(runtimepolicy.ModeDefault)
	jsonSession.JSONOutput = true
	if got := planRevisionAfterCommitEffect(jsonSession, "request_changes", "补上回滚风险"); got != "" {
		t.Fatalf("JSON output must not auto-start a turn, got %q", got)
	}
	if got := planRevisionAfterCommitEffect(nil, "request_changes", "notes"); got != "" {
		t.Fatalf("nil session must not start a revision turn, got %q", got)
	}
}

// TestStructuredPlanExitCarriesRevisionTurn 覆盖统一 TTY 投影：裁决单元照常返回，
// 修订轮作为 post-commit send 效果挂上（由 command.go 在单元落盘后提交）。
func TestStructuredPlanExitCarriesRevisionTurn(t *testing.T) {
	withPlanArtifactStore(t)
	session := newPlanCommandSession(runtimepolicy.ModeDefault)
	if err := enterChatPlanMode(session, "plan.md"); err != nil {
		t.Fatalf("enter: %v", err)
	}

	result := executeStructuredPlanModeExit(session, "request_changes", "补上回滚风险")
	if result.SendMessageAfterCommit != agent.PlanRevisionPrompt() {
		t.Fatalf("request_changes with notes should carry the revision turn, got %q", result.SendMessageAfterCommit)
	}
	if !strings.Contains(ui.RenderDocumentPlain(result.Document()), "已请求修改计划") {
		t.Fatalf("verdict cell lost the request_changes line:\n%s", ui.RenderDocumentPlain(result.Document()))
	}

	// 空意见：裁决照旧，但不带修订轮。
	empty := executeStructuredPlanModeExit(session, "request_changes", "")
	if empty.SendMessageAfterCommit != "" {
		t.Fatalf("empty notes must not carry a revision turn, got %q", empty.SendMessageAfterCommit)
	}

	// 批准：退出 plan，不带修订轮。
	approved := executeStructuredPlanModeExit(session, "approve", "looks good")
	if approved.SendMessageAfterCommit != "" {
		t.Fatalf("approve must not carry a revision turn, got %q", approved.SendMessageAfterCommit)
	}
}

// TestPlainPlanExitPrintsVerdictThenRevisionHint 覆盖纯文本 REPL：裁决行先打印，
// 修订轮随后提交；没有可用运行时（无 actor）时提示降级，裁决不回滚。
func TestPlainPlanExitPrintsVerdictThenRevisionHint(t *testing.T) {
	withPlanArtifactStore(t)
	session := newPlanCommandSession(runtimepolicy.ModeDefault)
	if err := enterChatPlanMode(session, "plan.md"); err != nil {
		t.Fatalf("enter: %v", err)
	}

	stdout, _ := captureStdoutStderr(t, func() {
		exitChatPlanModeCommand(session, "request_changes", "补上回滚风险")
	})
	if !strings.Contains(stdout, "已请求修改计划") {
		t.Fatalf("verdict line missing from plain output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "自动修订回合提交失败") || !strings.Contains(stdout, "下一次输入时送达模型") {
		t.Fatalf("degraded revision round must be reported, got:\n%s", stdout)
	}
	if state := loadChatPlanMode(session); !strings.Contains(state.PendingReviewNotes, "补上回滚风险") {
		t.Fatalf("decision must stay durable after a failed trigger, state=%+v", state)
	}
}
