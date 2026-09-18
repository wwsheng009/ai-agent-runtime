package commands

import (
	"context"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/planmode"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
)

// TestSyncRuntimeSessionFromChatPreservesDurablePlanMode pins the 2026-09-18
// remote finding: the CLI host's end-of-turn sync writes its pre-turn snapshot
// back to the session row and used to erase the plan-mode context that the
// actor persisted mid-turn through enter_plan_mode. Without the merge, the next
// turn reloads an inactive plan state, so plan-mode write gating silently
// disappears (observed live: plan_mode=active at 15:16:31Z, clobbered to
// bypass_permissions at 15:16:32Z).
func TestSyncRuntimeSessionFromChatPreservesDurablePlanMode(t *testing.T) {
	storage := runtimechat.NewInMemoryStorage()
	manager := runtimechat.NewSessionManager(storage, nil)
	defer manager.Stop()

	ctx := context.Background()
	runtimeSession, err := manager.Create(ctx, "plan-preserve-user")
	if err != nil {
		t.Fatalf("create runtime session: %v", err)
	}
	session := &ChatSession{
		Provider:       config.Provider{Protocol: "openai"},
		SessionManager: manager,
		SessionUserID:  "plan-preserve-user",
		RuntimeSession: runtimeSession,
	}

	// Actor-side write: active plan mode lands in the durable row mid-turn.
	stored, err := storage.Load(ctx, runtimeSession.ID)
	if err != nil {
		t.Fatalf("load stored session: %v", err)
	}
	planmode.Save(stored, planmode.Enter(string(runtimepolicy.ModeBypassPermissions), "docs/plan/preserved.md"))
	if err := storage.Update(ctx, stored); err != nil {
		t.Fatalf("persist plan mode: %v", err)
	}

	// Host-side write: the CLI snapshot predates the actor's plan entry.
	if err := syncRuntimeSessionFromChat(session); err != nil {
		t.Fatalf("sync runtime session from chat: %v", err)
	}

	reloaded, err := storage.Load(ctx, runtimeSession.ID)
	if err != nil {
		t.Fatalf("reload session: %v", err)
	}
	state := planmode.Load(reloaded)
	if !planmode.IsActive(state) {
		t.Fatalf("host sync erased durable plan mode: %+v", state)
	}
	if state.PlanPath != "docs/plan/preserved.md" {
		t.Fatalf("plan path changed across host sync: %q", state.PlanPath)
	}
}

// TestSyncRuntimeSessionFromChatReportsPlanAsEffectivePermissionMode pins the
// display half of the 2026-09-18 finding: while the plan lifecycle is active
// the persisted permission-mode keys must read "plan" instead of the CLI
// snapshot (--yolo's bypass_permissions), otherwise the session row and the web
// UI selectors contradict plan-mode enforcement. After exit the CLI mode must
// come back.
func TestSyncRuntimeSessionFromChatReportsPlanAsEffectivePermissionMode(t *testing.T) {
	storage := runtimechat.NewInMemoryStorage()
	manager := runtimechat.NewSessionManager(storage, nil)
	defer manager.Stop()

	ctx := context.Background()
	runtimeSession, err := manager.Create(ctx, "plan-display-user")
	if err != nil {
		t.Fatalf("create runtime session: %v", err)
	}
	session := &ChatSession{
		Provider:       config.Provider{Protocol: "openai"},
		SessionManager: manager,
		SessionUserID:  "plan-display-user",
		RuntimeSession: runtimeSession,
		PermissionMode: runtimepolicy.ModeBypassPermissions,
	}

	// Actor-side write: active plan mode lands in the durable row mid-turn.
	stored, err := storage.Load(ctx, runtimeSession.ID)
	if err != nil {
		t.Fatalf("load stored session: %v", err)
	}
	planmode.Save(stored, planmode.Enter(string(runtimepolicy.ModeBypassPermissions), "docs/plan/display.md"))
	if err := storage.Update(ctx, stored); err != nil {
		t.Fatalf("persist plan mode: %v", err)
	}

	if err := syncRuntimeSessionFromChat(session); err != nil {
		t.Fatalf("sync runtime session from chat: %v", err)
	}
	reloaded, err := storage.Load(ctx, runtimeSession.ID)
	if err != nil {
		t.Fatalf("reload session: %v", err)
	}
	if got := sessionmeta.String(reloaded.Metadata.Context, sessionmeta.PermissionMode); got != string(runtimepolicy.ModePlan) {
		t.Fatalf("active plan must persist permission_mode=plan, got %q", got)
	}
	if got := sessionmeta.String(reloaded.Metadata.Context, sessionmeta.EffectivePermissionMode); got != string(runtimepolicy.ModePlan) {
		t.Fatalf("active plan must persist effective_permission_mode=plan, got %q", got)
	}

	// Exit rewrites the row; the next host sync must restore the CLI mode.
	exited, err := planmode.Exit(planmode.Load(reloaded), planmode.ExitQuit, "display test exit")
	if err != nil {
		t.Fatalf("exit plan mode: %v", err)
	}
	planmode.Save(reloaded, exited)
	if err := storage.Update(ctx, reloaded); err != nil {
		t.Fatalf("persist plan exit: %v", err)
	}
	if err := syncRuntimeSessionFromChat(session); err != nil {
		t.Fatalf("sync runtime session after exit: %v", err)
	}
	afterExit, err := storage.Load(ctx, runtimeSession.ID)
	if err != nil {
		t.Fatalf("reload after exit: %v", err)
	}
	if got := sessionmeta.String(afterExit.Metadata.Context, sessionmeta.PermissionMode); got != string(runtimepolicy.ModeBypassPermissions) {
		t.Fatalf("exited plan must restore CLI permission mode, got %q", got)
	}
}

// TestRestoreChatRuntimeContext_StoredPlanDoesNotPoisonCLIMode guards the
// restore side of the same finding: the canonical key legitimately reads "plan"
// while the lifecycle is active, but "plan" must never be loaded into
// session.PermissionMode - that would keep the engine in plan after exit.
func TestRestoreChatRuntimeContext_StoredPlanDoesNotPoisonCLIMode(t *testing.T) {
	runtimeSession := runtimechat.NewSession("tester")
	sessionmeta.Set(runtimeSession.Metadata.Context, sessionmeta.PermissionMode, string(runtimepolicy.ModePlan), chatRuntimeContextPermissionMode)

	session := &ChatSession{PermissionMode: runtimepolicy.ModeBypassPermissions}
	restoreChatRuntimeContext(session, runtimeSession)
	if session.PermissionMode != runtimepolicy.ModeBypassPermissions {
		t.Fatalf("stored plan must not poison the CLI permission mode: got %s", session.PermissionMode)
	}
}
