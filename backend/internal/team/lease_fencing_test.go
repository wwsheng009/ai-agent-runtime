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
	_, err = store.UpsertTeammate(ctx, Teammate{
		ID:        "mate-b",
		TeamID:    teamID,
		Name:      "mate-b",
		Profile:   "worker",
		SessionID: "session-b",
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
	_, err := registry.ReleaseAgentControlTask(ctx, agentcontrol.TaskReleaseRequest{
		ID:           taskID,
		Workflow:     agentcontrol.WorkflowSpawnTeam,
		Status:       string(TaskStatusReady),
		FencingToken: first.FencingToken,
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

func TestReleaseRequiresCurrentTokenButCancellationCanRevoke(t *testing.T) {
	ctx := context.Background()
	store, _, taskID, claim := setupFencedTask(t, false)
	registry := NewAgentControlTaskRegistry(store)

	_, err := registry.ReleaseAgentControlTask(ctx, agentcontrol.TaskReleaseRequest{
		ID:       taskID,
		Workflow: agentcontrol.WorkflowSpawnTeam,
		Status:   string(TaskStatusReady),
	})
	require.ErrorIs(t, err, agentcontrol.ErrTaskFencingMismatch,
		"an ordinary release without the active attempt token must not revoke its claim")

	current, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, current)
	require.Equal(t, TaskStatusRunning, current.Status)
	require.Equal(t, claim.FencingToken, current.FencingToken)

	cancelled, err := registry.ReleaseAgentControlTask(ctx, agentcontrol.TaskReleaseRequest{
		ID:       taskID,
		Workflow: agentcontrol.WorkflowSpawnTeam,
		Status:   string(TaskStatusCancelled),
		Summary:  "cancelled by control plane",
	})
	require.NoError(t, err, "control-plane cancellation intentionally revokes the current claim")
	require.NotNil(t, cancelled)
	require.Equal(t, string(TaskStatusCancelled), cancelled.Status)
}

func TestFencedLifecycleDoesNotMutateAnotherTeamsTeammate(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	taskTeamID, err := store.CreateTeam(ctx, Team{Status: TeamStatusActive})
	require.NoError(t, err)
	otherTeamID, err := store.CreateTeam(ctx, Team{Status: TeamStatusActive})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{
		ID:     "foreign-mate",
		TeamID: otherTeamID,
		State:  TeammateStateBusy,
	})
	require.NoError(t, err)

	registry := NewAgentControlTaskRegistry(store)
	createClaimedTask := func(id, token string) {
		t.Helper()
		assignee := "foreign-mate"
		_, createErr := store.CreateTask(ctx, Task{
			ID:       id,
			TeamID:   taskTeamID,
			Status:   TaskStatusRunning,
			Assignee: &assignee,
		})
		require.NoError(t, createErr)
		_, updateErr := store.db.ExecContext(ctx, `
			UPDATE agent_control_task_records
			SET fencing_token = ?
			WHERE workflow = ? AND task_id = ?
		`, token, agentcontrol.WorkflowSpawnTeam, id)
		require.NoError(t, updateErr)
	}
	assertForeignMateStillBusy := func() {
		t.Helper()
		mate, getErr := store.GetTeammate(ctx, "foreign-mate")
		require.NoError(t, getErr)
		require.NotNil(t, mate)
		require.Equal(t, TeammateStateBusy, mate.State)
	}

	createClaimedTask("task-cross-team-release", "release-token")
	_, err = registry.ReleaseAgentControlTask(ctx, agentcontrol.TaskReleaseRequest{
		ID:           "task-cross-team-release",
		Workflow:     agentcontrol.WorkflowSpawnTeam,
		Status:       string(TaskStatusReady),
		FencingToken: "release-token",
	})
	require.NoError(t, err)
	assertForeignMateStillBusy()

	createClaimedTask("task-cross-team-retry", "retry-token")
	_, err = registry.RetryAgentControlTask(ctx, agentcontrol.TaskRetryRequest{
		ID:           "task-cross-team-retry",
		Workflow:     agentcontrol.WorkflowSpawnTeam,
		Status:       string(TaskStatusReady),
		FencingToken: "retry-token",
	})
	require.NoError(t, err)
	assertForeignMateStillBusy()

	createClaimedTask("task-cross-team-block", "block-token")
	_, err = registry.BlockAgentControlTask(ctx, agentcontrol.TaskBlockRequest{
		ID:           "task-cross-team-block",
		Workflow:     agentcontrol.WorkflowSpawnTeam,
		Summary:      "blocked",
		FencingToken: "block-token",
	})
	require.NoError(t, err)
	assertForeignMateStillBusy()
}

func TestFencedLifecycleRollsBackTaskWhenTeammateUpdateFails(t *testing.T) {
	for _, action := range []string{"block", "terminal"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			store, _, taskID, claim := setupFencedTask(t, false)
			_, err := store.db.ExecContext(ctx, `
				CREATE TRIGGER fail_teammate_state_update
				BEFORE UPDATE OF state ON teammates
				WHEN NEW.id = 'mate-a'
				BEGIN
					SELECT RAISE(ABORT, 'injected teammate state failure');
				END;
			`)
			require.NoError(t, err)

			registry := NewAgentControlTaskRegistry(store)
			switch action {
			case "block":
				_, err = registry.BlockAgentControlTask(ctx, agentcontrol.TaskBlockRequest{
					ID:           taskID,
					Workflow:     agentcontrol.WorkflowSpawnTeam,
					Summary:      "blocked",
					FencingToken: claim.FencingToken,
				})
			case "terminal":
				_, err = registry.UpdateAgentControlTaskTerminal(ctx, agentcontrol.TaskTerminalUpdateRequest{
					ID:           taskID,
					Workflow:     agentcontrol.WorkflowSpawnTeam,
					Status:       string(TaskStatusDone),
					Summary:      "completed",
					FencingToken: claim.FencingToken,
				})
			}
			require.ErrorContains(t, err, "injected teammate state failure")

			task, err := store.GetTask(ctx, taskID)
			require.NoError(t, err)
			require.NotNil(t, task)
			require.Equal(t, TaskStatusRunning, task.Status, "task transition must roll back with the teammate update")
			require.Equal(t, claim.FencingToken, task.FencingToken, "the active attempt must remain current")

			mate, err := store.GetTeammate(ctx, "mate-a")
			require.NoError(t, err)
			require.NotNil(t, mate)
			require.Equal(t, TeammateStateBusy, mate.State)
		})
	}
}
