package runtimeapi

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// 2026-09-27 incident hardening: landing a worktree while its session still
// holds a turn races the child's remaining writes. The API host must refuse
// before any git command runs, and the refusal must be lifted once the session
// is idle again.
func TestSessionAgentControllerApplyWorktreeRefusesWhileSessionExecuting(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	manager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	handler.SetSessionManager(manager)
	defer handler.getSessionHub().StopAll()

	session, err := manager.Create(ctx, "user-1")
	require.NoError(t, err)
	session.SetContext(toolbroker.AgentSessionContextIsolation, "worktree")
	session.SetContext(toolbroker.AgentSessionContextWorktreePath, "/tmp/aicli-worktree-guard-test")
	session.SetContext(toolbroker.AgentSessionContextWorktreeBranch, "aicli/agent/guard-test")
	session.SetContext(toolbroker.AgentSessionContextWorktreeRepoRoot, "/tmp/aicli-worktree-guard-test")
	require.NoError(t, manager.Update(ctx, session))

	actor, err := handler.getSessionHub().GetOrCreate(session.ID)
	require.NoError(t, err)
	require.NoError(t, actor.UpdateStateForTest(ctx, func(state *chat.RuntimeState) error {
		state.Status = chat.SessionRunning
		return nil
	}))

	controller := &sessionAgentController{handler: handler}
	_, err = controller.ApplyWorktree(ctx, toolbroker.ApplyAgentWorktreeArgs{ID: session.ID})
	require.Error(t, err)
	require.Contains(t, err.Error(), "still executing")

	_, err = controller.DiscardWorktree(ctx, toolbroker.DiscardAgentWorktreeArgs{ID: session.ID})
	require.Error(t, err)
	require.Contains(t, err.Error(), "still executing")

	// Idle again: the guard no longer blocks (the apply itself may still fail
	// on the fake worktree path, but it must not fail on the running check).
	require.NoError(t, actor.UpdateStateForTest(ctx, func(state *chat.RuntimeState) error {
		state.Status = chat.SessionIdle
		return nil
	}))
	_, err = controller.ApplyWorktree(ctx, toolbroker.ApplyAgentWorktreeArgs{ID: session.ID})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "still executing")
	_, err = controller.DiscardWorktree(ctx, toolbroker.DiscardAgentWorktreeArgs{ID: session.ID})
	// A discard may legitimately succeed here (the fake worktree path is not a
	// registered git worktree); the point is that the running guard is gone.
	require.NotContains(t, fmt.Sprint(err), "still executing")
}
