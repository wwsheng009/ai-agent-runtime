package commands

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/planmode"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

func newPlanCommandSession(mode runtimepolicy.Mode) *ChatSession {
	return &ChatSession{
		PermissionMode:          mode,
		RequestedPermissionMode: string(mode),
		EffectivePermissionMode: string(mode),
		RuntimeSession: &runtimechat.Session{
			ID: "plan-cmd-test",
			Metadata: runtimechat.SessionMetadata{
				Context: map[string]interface{}{},
			},
		},
	}
}

func TestHandlePlanCommand_EnterExitStatus(t *testing.T) {
	session := newPlanCommandSession(runtimepolicy.ModeDefault)

	statusOut := captureStdout(t, func() {
		if handlePlanCommand(session, "/plan") {
			t.Fatalf("status should not consume as chat message")
		}
	})
	if !strings.Contains(statusOut, "plan mode: inactive") {
		t.Fatalf("expected inactive status, got %q", statusOut)
	}

	enterOut := captureStdout(t, func() {
		if handlePlanCommand(session, "/plan enter docs/feature-plan.md") {
			t.Fatalf("enter should not consume as chat message")
		}
	})
	if !strings.Contains(enterOut, "已进入 plan mode") {
		t.Fatalf("expected enter confirmation, got %q", enterOut)
	}
	if session.PermissionMode != runtimepolicy.ModePlan {
		t.Fatalf("expected permission mode plan after enter, got %s", session.PermissionMode)
	}
	state := loadChatPlanMode(session)
	if !planmode.IsActive(state) {
		t.Fatalf("expected active plan mode state, got %#v", state)
	}
	if state.PlanPath != "docs/feature-plan.md" {
		t.Fatalf("expected custom plan path, got %q", state.PlanPath)
	}
	if state.PreviousMode != string(runtimepolicy.ModeDefault) {
		t.Fatalf("expected previous mode default, got %q", state.PreviousMode)
	}
	if len(state.WriteAllowPaths) == 0 || state.WriteAllowPaths[0] != "docs/feature-plan.md" {
		t.Fatalf("expected write allow paths to include plan path, got %#v", state.WriteAllowPaths)
	}

	activeStatus := captureStdout(t, func() {
		_ = handlePlanCommand(session, "/plan status")
	})
	for _, expected := range []string{"plan mode: active", "docs/feature-plan.md", "permission-mode: plan"} {
		if !strings.Contains(activeStatus, expected) {
			t.Fatalf("expected active status to contain %q, got %q", expected, activeStatus)
		}
	}

	requestOut := captureStdout(t, func() {
		_ = handlePlanCommand(session, "/plan request_changes need more risks")
	})
	if !strings.Contains(requestOut, "保持 plan mode") {
		t.Fatalf("expected request_changes to keep plan mode, got %q", requestOut)
	}
	state = loadChatPlanMode(session)
	if !planmode.IsActive(state) {
		t.Fatalf("request_changes should keep plan mode active, got %#v", state)
	}
	if state.ExitDecision != planmode.ExitRequestChanges {
		t.Fatalf("expected request_changes decision recorded, got %q", state.ExitDecision)
	}
	if state.Notes != "need more risks" {
		t.Fatalf("expected notes preserved, got %q", state.Notes)
	}
	if session.PermissionMode != runtimepolicy.ModePlan {
		t.Fatalf("expected permission mode still plan, got %s", session.PermissionMode)
	}

	approveOut := captureStdout(t, func() {
		_ = handlePlanCommand(session, "/plan approve ship it")
	})
	if !strings.Contains(approveOut, "已批准计划并退出 plan mode") {
		t.Fatalf("expected approve confirmation, got %q", approveOut)
	}
	state = loadChatPlanMode(session)
	if planmode.IsActive(state) {
		t.Fatalf("approve should exit plan mode, got %#v", state)
	}
	if state.ExitDecision != planmode.ExitApprove {
		t.Fatalf("expected approve decision, got %q", state.ExitDecision)
	}
	if session.PermissionMode != runtimepolicy.ModeDefault {
		t.Fatalf("expected restored default mode after approve, got %s", session.PermissionMode)
	}
}

func TestHandlePlanCommand_QuitRestoresPreviousMode(t *testing.T) {
	session := newPlanCommandSession(runtimepolicy.ModeAcceptEdits)

	captureStdout(t, func() {
		_ = handlePlanCommand(session, "/plan on")
	})
	if session.PermissionMode != runtimepolicy.ModePlan {
		t.Fatalf("expected plan mode after on, got %s", session.PermissionMode)
	}
	state := loadChatPlanMode(session)
	if state.PreviousMode != string(runtimepolicy.ModeAcceptEdits) {
		t.Fatalf("expected previous accept_edits, got %q", state.PreviousMode)
	}
	if state.PlanPath != planmode.DefaultPlanPath {
		t.Fatalf("expected default plan path, got %q", state.PlanPath)
	}

	quitOut := captureStdout(t, func() {
		_ = handlePlanCommand(session, "/plan quit not now")
	})
	if !strings.Contains(quitOut, "已退出 plan mode") {
		t.Fatalf("expected quit confirmation, got %q", quitOut)
	}
	state = loadChatPlanMode(session)
	if planmode.IsActive(state) {
		t.Fatalf("quit should leave plan mode inactive, got %#v", state)
	}
	if state.ExitDecision != planmode.ExitQuit {
		t.Fatalf("expected quit decision, got %q", state.ExitDecision)
	}
	if session.PermissionMode != runtimepolicy.ModeAcceptEdits {
		t.Fatalf("expected restored accept_edits after quit, got %s", session.PermissionMode)
	}
}

func TestHandlePlanCommand_BareMarkdownPathEnters(t *testing.T) {
	session := newPlanCommandSession(runtimepolicy.ModeDefault)
	out := captureStdout(t, func() {
		_ = handlePlanCommand(session, "/plan plans/iteration-b.md")
	})
	if !strings.Contains(out, "已进入 plan mode") {
		t.Fatalf("expected bare path enter, got %q", out)
	}
	state := loadChatPlanMode(session)
	if state.PlanPath != "plans/iteration-b.md" {
		t.Fatalf("expected bare path as plan path, got %q", state.PlanPath)
	}
	if !planmode.IsActive(state) {
		t.Fatalf("expected active state after bare path enter")
	}
}

func TestHandlePlanCommand_ExitWithoutActiveRequiresPlanMode(t *testing.T) {
	session := newPlanCommandSession(runtimepolicy.ModeDefault)
	out := captureStdout(t, func() {
		_ = handlePlanCommand(session, "/plan exit approve")
	})
	if !strings.Contains(out, "当前不在 plan mode") {
		t.Fatalf("expected not-in-plan error, got %q", out)
	}

	// Bare permission-mode=plan (via /mode) can still exit without durable state.
	session.PermissionMode = runtimepolicy.ModePlan
	out = captureStdout(t, func() {
		_ = handlePlanCommand(session, "/plan exit quit")
	})
	if !strings.Contains(out, "已退出 plan mode") {
		t.Fatalf("expected bare plan mode quit, got %q", out)
	}
	if session.PermissionMode != runtimepolicy.ModeDefault {
		t.Fatalf("expected default after bare plan quit, got %s", session.PermissionMode)
	}
}

func TestEnterChatPlanMode_NestedEnterKeepsOriginalPreviousMode(t *testing.T) {
	session := newPlanCommandSession(runtimepolicy.ModeAcceptEdits)
	if err := enterChatPlanMode(session, "plan.md"); err != nil {
		t.Fatalf("enter: %v", err)
	}
	if err := enterChatPlanMode(session, "docs/revised.md"); err != nil {
		t.Fatalf("nested enter: %v", err)
	}
	state := loadChatPlanMode(session)
	if state.PreviousMode != string(runtimepolicy.ModeAcceptEdits) {
		t.Fatalf("nested enter should keep original previous mode, got %q", state.PreviousMode)
	}
	if state.PlanPath != "docs/revised.md" {
		t.Fatalf("nested enter should refresh plan path, got %q", state.PlanPath)
	}
}

func TestApplyChatPlanModeToAgent_ActiveStateForcesPlanPaths(t *testing.T) {
	session := newPlanCommandSession(runtimepolicy.ModeDefault)
	if err := enterChatPlanMode(session, "plan.md"); err != nil {
		t.Fatalf("enter: %v", err)
	}

	apiAgent := agent.NewAgent(&agent.Config{Name: "plan-mode-test"}, nil)
	applyChatPlanModeToAgent(apiAgent, session, session.RuntimeSession)

	engine := apiAgent.GetPermissionEngine()
	if engine == nil {
		t.Fatal("expected permission engine")
	}
	if engine.Mode != runtimepolicy.ModePlan {
		t.Fatalf("expected engine mode plan, got %s", engine.Mode)
	}
	if len(engine.PlanWriteAllowPaths) == 0 || engine.PlanWriteAllowPaths[0] != "plan.md" {
		t.Fatalf("expected plan write allow paths, got %#v", engine.PlanWriteAllowPaths)
	}
}
