package team

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
)

// setupFencedTask creates a team, an idle teammate, and a ready task, then
// claims it, returning the claimed record (with fencing token) for assertions.
func setupFencedTask(t *testing.T, usePathClaims bool) (*SQLiteStore, string, string, *agentcontrol.TaskRecord) {
	t.Helper()
	ctx := context.Background()
	store := newTestStore(t)
	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)
	mateID, err := store.UpsertTeammate(ctx, Teammate{
		ID:        "mate-a",
		TeamID:    teamID,
		Name:      "mate-a",
		Profile:   "worker",
		SessionID: "session-a",
		State:     TeammateStateIdle,
	})
	require.NoError(t, err)
	taskID, err := store.CreateTask(ctx, Task{
		TeamID:     teamID,
		Title:      "fenced",
		Goal:       "run",
		Status:     TaskStatusReady,
		WritePaths: []string{"src/fenced.go"},
	})
	require.NoError(t, err)
	task, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, task)

	registry := NewAgentControlTaskRegistry(store)
	request := agentcontrol.TaskClaimRequest{
		ID:              taskID,
		Workflow:        agentcontrol.WorkflowSpawnTeam,
		TeamID:          teamID,
		Assignee:        mateID,
		LeaseUntil:      time.Now().UTC().Add(5 * time.Minute),
		ExpectedVersion: task.Version,
	}
	if usePathClaims {
		request.UsePathClaims = true
		request.WorkspaceRoot = "workspace"
		request.ReadPaths = task.ReadPaths
		request.WritePaths = task.WritePaths
	}
	record, claimed, err := registry.ClaimAgentControlTask(ctx, request)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotNil(t, record)
	require.NotEmpty(t, record.FencingToken, "claim must mint a fencing token")
	require.Equal(t, 1, record.Attempt, "first claim must be attempt 1")
	return store, teamID, taskID, record
}

func taskTeamID(t *testing.T, ctx context.Context, store *SQLiteStore, taskID string) string {
	t.Helper()
	task, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, task)
	return task.TeamID
}

// TestClaimRotatesFencingToken verifies that each claim mints a fresh fencing
// token and bumps the attempt counter (P4 items 1-2).
func TestClaimRotatesFencingToken(t *testing.T) {
	ctx := context.Background()
	store, _, taskID, first := setupFencedTask(t, false)

	// Release the first attempt, then claim again.
	registry := NewAgentControlTaskRegistry(store)
	_, err := registry.ReleaseAgentControlTask(ctx, agentcontrol.TaskReleaseRequest{
		ID:           taskID,
		Workflow:     agentcontrol.WorkflowSpawnTeam,
		Status:       string(TaskStatusReady),
		FencingToken: first.FencingToken,
	})
	require.NoError(t, err)

	task, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, task)
	second, claimed, err := registry.ClaimAgentControlTask(ctx, agentcontrol.TaskClaimRequest{
		ID:              taskID,
		Workflow:        agentcontrol.WorkflowSpawnTeam,
		TeamID:          task.TeamID,
		Assignee:        first.Assignee,
		LeaseUntil:      time.Now().UTC().Add(5 * time.Minute),
		ExpectedVersion: task.Version,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotNil(t, second)
	require.Equal(t, 2, second.Attempt, "second claim must bump the attempt counter")
	require.NotEqual(t, first.FencingToken, second.FencingToken, "fencing token must rotate per claim")
}

// TestTerminalRejectsStaleAttempt verifies that a late terminal result from a
// previous attempt is rejected and cannot overwrite the current attempt
// (P4 items 4-5, acceptance: old attempt result must not overwrite the new one).
func TestTerminalRejectsStaleAttempt(t *testing.T) {
	ctx := context.Background()
	store, _, taskID, first := setupFencedTask(t, false)

	// The task is reclaimed and re-assigned to a new attempt.
	registry := NewAgentControlTaskRegistry(store)
	_, err := registry.RetryAgentControlTask(ctx, agentcontrol.TaskRetryRequest{
		ID:       taskID,
		Workflow: agentcontrol.WorkflowSpawnTeam,
		Status:   string(TaskStatusReady),
	})
	require.NoError(t, err)
	second, claimed, err := registry.ClaimAgentControlTask(ctx, agentcontrol.TaskClaimRequest{
		ID:         taskID,
		Workflow:   agentcontrol.WorkflowSpawnTeam,
		TeamID:     taskTeamID(t, ctx, store, taskID),
		Assignee:   "mate-b",
		LeaseUntil: time.Now().UTC().Add(5 * time.Minute),
	})
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotNil(t, second)
	require.Equal(t, 2, second.Attempt)
	require.NotEqual(t, first.FencingToken, second.FencingToken)

	// Old attempt reports done with its stale token: must be rejected.
	_, err = registry.UpdateAgentControlTaskTerminal(ctx, agentcontrol.TaskTerminalUpdateRequest{
		ID:           taskID,
		Workflow:     agentcontrol.WorkflowSpawnTeam,
		Status:       string(TaskStatusDone),
		Summary:      "late result from attempt 1",
		FencingToken: first.FencingToken,
	})
	require.ErrorIs(t, err, agentcontrol.ErrTaskFencingMismatch)

	// The task must still be owned by the new attempt and unchanged.
	current, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, current)
	require.Equal(t, TaskStatusRunning, current.Status)
	require.Equal(t, 2, current.Attempt)
	require.Equal(t, second.FencingToken, current.FencingToken)
	require.Equal(t, "mate-b", *current.Assignee)

	// The new attempt's token still works.
	done, err := registry.UpdateAgentControlTaskTerminal(ctx, agentcontrol.TaskTerminalUpdateRequest{
		ID:           taskID,
		Workflow:     agentcontrol.WorkflowSpawnTeam,
		Status:       string(TaskStatusDone),
		Summary:      "fresh result from attempt 2",
		FencingToken: second.FencingToken,
	})
	require.NoError(t, err)
	require.NotNil(t, done)
	require.Equal(t, string(TaskStatusDone), done.Status)
}

// TestRenewRejectsStaleFencingToken verifies that only the current attempt can
// renew the task lease (P4 item 4, acceptance: path claims and leases are not
// extended by stale attempts).
func TestRenewRejectsStaleFencingToken(t *testing.T) {
	ctx := context.Background()
	store, _, taskID, first := setupFencedTask(t, false)

	registry := NewAgentControlTaskRegistry(store)
	_, err := registry.RenewAgentControlTaskLease(ctx, agentcontrol.TaskLeaseRenewRequest{
		ID:           taskID,
		Workflow:     agentcontrol.WorkflowSpawnTeam,
		LeaseUntil:   time.Now().UTC().Add(5 * time.Minute),
		FencingToken: "stale-token",
	})
	require.ErrorIs(t, err, agentcontrol.ErrTaskFencingMismatch)

	current, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, current)
	require.Equal(t, first.FencingToken, current.FencingToken, "lease must stay owned by the original attempt")

	// The correct token renews successfully.
	renewed := time.Now().UTC().Add(9 * time.Minute)
	_, err = registry.RenewAgentControlTaskLease(ctx, agentcontrol.TaskLeaseRenewRequest{
		ID:           taskID,
		Workflow:     agentcontrol.WorkflowSpawnTeam,
		LeaseUntil:   renewed,
		FencingToken: first.FencingToken,
	})
	require.NoError(t, err)
	current, err = store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, current)
	require.NotNil(t, current.LeaseUntil)
	require.WithinDuration(t, renewed, *current.LeaseUntil, time.Second)
}

// TestTerminalStaleAttemptKeepsPathClaims verifies that rejecting a stale
// terminal result also keeps path claims owned by the current attempt
// (P4 item 4, acceptance: path claims are not released by stale attempts).
func TestTerminalStaleAttemptKeepsPathClaims(t *testing.T) {
	ctx := context.Background()
	store, teamID, taskID, _ := setupFencedTask(t, true)

	claimsBefore, err := store.ListPathClaims(ctx, teamID)
	require.NoError(t, err)
	require.NotEmpty(t, claimsBefore)

	registry := NewAgentControlTaskRegistry(store)
	_, err = registry.UpdateAgentControlTaskTerminal(ctx, agentcontrol.TaskTerminalUpdateRequest{
		ID:           taskID,
		Workflow:     agentcontrol.WorkflowSpawnTeam,
		Status:       string(TaskStatusDone),
		Summary:      "stale done",
		FencingToken: "stale-token",
	})
	require.ErrorIs(t, err, agentcontrol.ErrTaskFencingMismatch)

	current, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, current)
	require.NotNil(t, current.Assignee)
	claimsAfter, err := store.ListPathClaims(ctx, teamID)
	require.NoError(t, err)
	require.Equal(t, len(claimsBefore), len(claimsAfter), "stale terminal result must not release path claims")
	for _, claim := range claimsAfter {
		require.Equal(t, *current.Assignee, claim.OwnerAgentID, "path claims must stay owned by the current attempt")
	}
}

// TestHeartbeatLoopRenewsTaskLease verifies that the teammate runner heartbeat
// also renews the task lease with the current fencing token (P4 item 3).
func TestHeartbeatLoopRenewsTaskLease(t *testing.T) {
	ctx := context.Background()
	store, _, taskID, first := setupFencedTask(t, false)

	task, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, task)
	require.Equal(t, first.FencingToken, task.FencingToken)

	runner := &TeammateRunner{
		Mailbox:           NewMailboxService(store),
		HeartbeatInterval: 20 * time.Millisecond,
		LeaseTTL:          time.Minute,
	}
	stop := runner.startHeartbeatLoop(ctx, *task.Assignee, *task)
	time.Sleep(80 * time.Millisecond)
	stop()

	updated, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.NotNil(t, updated.LeaseUntil)
	require.True(t, updated.LeaseUntil.After(time.Now().UTC()), "heartbeat loop must renew the task lease")
	require.Equal(t, first.FencingToken, updated.FencingToken, "renewal must not rotate the fencing token")
}
