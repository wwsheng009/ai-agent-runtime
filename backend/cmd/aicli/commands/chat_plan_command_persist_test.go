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

// TestPlanCommandApprovePersistsDurableExit pins the 2026-09-28 finding:
// /plan approve (same for quit/request_changes) computed the user verdict
// locally, but the sync that writes the CLI snapshot back restored the durable
// plan-mode context first. The stored row still carried the actor's
// active+pending_exit_request state, so the update re-persisted "active": the
// user's decision was silently swallowed while the command still archived the
// approve round and reported success.
func TestPlanCommandApprovePersistsDurableExit(t *testing.T) {
	withPlanArtifactStore(t)

	storage := runtimechat.NewInMemoryStorage()
	manager := runtimechat.NewSessionManager(storage, nil)
	defer manager.Stop()

	ctx := context.Background()
	runtimeSession, err := manager.Create(ctx, "plan-approve-user")
	if err != nil {
		t.Fatalf("create runtime session: %v", err)
	}
	session := &ChatSession{
		Provider:                config.Provider{Protocol: "openai"},
		SessionManager:          manager,
		SessionUserID:           "plan-approve-user",
		RuntimeSession:          runtimeSession,
		PermissionMode:          runtimepolicy.ModePlan,
		RequestedPermissionMode: string(runtimepolicy.ModePlan),
		EffectivePermissionMode: string(runtimepolicy.ModePlan),
	}

	// CLI-side snapshot: the active plan state restored on resume.
	active := planmode.Enter(string(runtimepolicy.ModeAcceptEdits), "docs/plan/approve.md")
	planmode.Save(session.RuntimeSession, active)

	// Actor-side write: the model requested an exit mid-turn, so the durable
	// row carries active + pending_exit_request while the CLI snapshot does not.
	stored, err := storage.Load(ctx, runtimeSession.ID)
	if err != nil {
		t.Fatalf("load stored session: %v", err)
	}
	planmode.Save(stored, planmode.RequestExitFrom(active, planmode.ExitSourceModel, "pending model exit request"))
	if err := storage.Update(ctx, stored); err != nil {
		t.Fatalf("persist pending exit request: %v", err)
	}

	result, err := exitChatPlanModeWithResult(session, "approve", "ship it")
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if result.SyncErr != nil {
		t.Fatalf("approve sync: %v", result.SyncErr)
	}
	if planmode.IsActive(result.State) || result.State.ExitDecision != planmode.ExitApprove {
		t.Fatalf("local state must carry the user verdict, got %+v", result.State)
	}
	if session.PermissionMode != runtimepolicy.ModeAcceptEdits {
		t.Fatalf("CLI permission mode after approve = %s, want accept_edits", session.PermissionMode)
	}

	reloaded, err := storage.Load(ctx, runtimeSession.ID)
	if err != nil {
		t.Fatalf("reload session: %v", err)
	}
	state := planmode.Load(reloaded)
	if planmode.IsActive(state) {
		t.Fatalf("durable plan mode still active after approve: %+v", state)
	}
	if state.ExitDecision != planmode.ExitApprove {
		t.Fatalf("durable exit decision = %q, want %q", state.ExitDecision, planmode.ExitApprove)
	}
	if state.LastExitSource != planmode.ExitSourceUser {
		t.Fatalf("durable exit source = %q, want %q", state.LastExitSource, planmode.ExitSourceUser)
	}
	if got := sessionmeta.String(reloaded.Metadata.Context, sessionmeta.PermissionMode); got != string(runtimepolicy.ModeAcceptEdits) {
		t.Fatalf("durable permission_mode = %q, want %q", got, runtimepolicy.ModeAcceptEdits)
	}
	if got := sessionmeta.String(reloaded.Metadata.Context, sessionmeta.EffectivePermissionMode); got != string(runtimepolicy.ModeAcceptEdits) {
		t.Fatalf("durable effective_permission_mode = %q, want %q", got, runtimepolicy.ModeAcceptEdits)
	}

	// A later routine end-of-turn sync must keep the verdict: the restore now
	// copies the exited durable state instead of resurrecting the pending
	// request the user just answered.
	if err := syncRuntimeSessionFromChat(session); err != nil {
		t.Fatalf("routine sync after approve: %v", err)
	}
	finalRow, err := storage.Load(ctx, runtimeSession.ID)
	if err != nil {
		t.Fatalf("reload after routine sync: %v", err)
	}
	if final := planmode.Load(finalRow); planmode.IsActive(final) {
		t.Fatalf("routine sync resurrected plan mode after approve: %+v", final)
	}
}
