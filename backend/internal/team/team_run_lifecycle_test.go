package team

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newStartedTeamRunForLifecycleTest(t *testing.T, store *SQLiteStore) *TeamRun {
	t.Helper()
	ctx := context.Background()
	run, err := store.CreateTeamRunDraft(ctx, TeamRunCreateRequest{
		OwnerID:     "owner-lifecycle",
		WorkspaceID: "workspace-lifecycle",
		Goal:        "project the authoritative terminal Team state",
		Config: json.RawMessage(`{
			"members":[
				{"id":"worker","role":"builder","session_id":"session-worker","state":"idle"}
			],
			"tasks":[
				{"id":"task-1","title":"Work","goal":"complete the lifecycle test","assignee":"worker"}
			]
		}`),
		IdempotencyKey: "lifecycle-draft-" + uuid.NewString(),
	})
	require.NoError(t, err)

	started, err := store.StartTeamRun(ctx, TeamRunStartRequest{
		OwnerID:         run.OwnerID,
		WorkspaceID:     run.WorkspaceID,
		RunID:           run.ID,
		ExpectedVersion: run.Version,
		IdempotencyKey:  "lifecycle-start-" + uuid.NewString(),
		RequestID:       "lifecycle-request-" + uuid.NewString(),
	})
	require.NoError(t, err)
	require.NotNil(t, started)
	require.NotEmpty(t, started.LegacyTeamID)
	return started
}

func appendLifecycleTeamCompleted(t *testing.T, store *SQLiteStore, teamID string, status TeamStatus) int64 {
	t.Helper()
	seq, err := store.AppendTeamEvent(context.Background(), TeamEvent{
		TeamID: teamID,
		Type:   "team.completed",
		Payload: map[string]interface{}{
			"status": string(status),
		},
	})
	require.NoError(t, err)
	return seq
}

func TestProjectTeamRunTerminalStateMapsTerminalStatusesAndReplaysIdempotently(t *testing.T) {
	for _, status := range []TeamStatus{
		TeamStatusDone,
		TeamStatusFailed,
		TeamStatusPartiallyCompleted,
		TeamStatusCanceled,
	} {
		t.Run(string(status), func(t *testing.T) {
			ctx := context.Background()
			store := newTestStore(t)
			run := newStartedTeamRunForLifecycleTest(t, store)

			require.NoError(t, store.UpdateTeamStatus(ctx, run.LegacyTeamID, status))
			eventSeq := appendLifecycleTeamCompleted(t, store, run.LegacyTeamID, status)

			require.NoError(t, store.ProjectTeamRunTerminalState(ctx, run.LegacyTeamID))
			projected, err := store.GetTeamRun(ctx, run.OwnerID, run.ID)
			require.NoError(t, err)
			require.NotNil(t, projected)
			assert.Equal(t, string(status), projected.Status)
			assert.Equal(t, TeamRunPhaseTerminal, projected.Phase)
			assert.Equal(t, TeamRunSummaryPending, projected.SummaryState)
			assert.Equal(t, eventSeq, projected.EventSeq)
			require.NotNil(t, projected.TerminalAt)
			assert.False(t, projected.TerminalAt.IsZero())

			version := projected.Version
			require.NoError(t, store.ProjectTeamRunTerminalState(ctx, run.LegacyTeamID))
			replayed, err := store.GetTeamRun(ctx, run.OwnerID, run.ID)
			require.NoError(t, err)
			require.NotNil(t, replayed)
			assert.Equal(t, version, replayed.Version, "replaying the same terminal event must be idempotent")
			assert.Equal(t, projected.EventSeq, replayed.EventSeq)
			assert.Equal(t, projected.TerminalAt, replayed.TerminalAt)
		})
	}
}

func TestProjectTeamRunTerminalStateMapsSummaryReadiness(t *testing.T) {
	tests := []struct {
		name          string
		summaryType   string
		payload       map[string]interface{}
		expectedState string
	}{
		{
			name:          "pending without summary event",
			expectedState: TeamRunSummaryPending,
		},
		{
			name:          "ready summary",
			summaryType:   "team.summary",
			payload:       map[string]interface{}{"summary": "all work completed"},
			expectedState: TeamRunSummaryReady,
		},
		{
			name:          "failed summary event",
			summaryType:   "team.summary.failed",
			payload:       map[string]interface{}{"error_type": "lead_session_error"},
			expectedState: TeamRunSummaryFailed,
		},
		{
			name:          "summary carrying an error",
			summaryType:   "team.summary",
			payload:       map[string]interface{}{"summary": "fallback summary", "error_type": "prompt_preflight"},
			expectedState: TeamRunSummaryFailed,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store := newTestStore(t)
			run := newStartedTeamRunForLifecycleTest(t, store)
			require.NoError(t, store.UpdateTeamStatus(ctx, run.LegacyTeamID, TeamStatusDone))
			appendLifecycleTeamCompleted(t, store, run.LegacyTeamID, TeamStatusDone)

			if test.summaryType != "" {
				_, err := store.AppendTeamEvent(ctx, TeamEvent{
					TeamID:  run.LegacyTeamID,
					Type:    test.summaryType,
					Payload: test.payload,
				})
				require.NoError(t, err)
			}

			require.NoError(t, store.ProjectTeamRunTerminalState(ctx, run.LegacyTeamID))
			projected, err := store.GetTeamRun(ctx, run.OwnerID, run.ID)
			require.NoError(t, err)
			require.NotNil(t, projected)
			assert.Equal(t, test.expectedState, projected.SummaryState)
		})
	}
}

func TestProjectTeamRunTerminalStateAdvancesForDelayedSummaryAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	run := newStartedTeamRunForLifecycleTest(t, store)
	require.NoError(t, store.UpdateTeamStatus(ctx, run.LegacyTeamID, TeamStatusDone))
	appendLifecycleTeamCompleted(t, store, run.LegacyTeamID, TeamStatusDone)

	_, err := store.AppendTeamEvent(ctx, TeamEvent{
		TeamID:  run.LegacyTeamID,
		Type:    "team.summary.failed",
		Payload: map[string]interface{}{"error_type": "temporary_summary_failure"},
	})
	require.NoError(t, err)
	require.NoError(t, store.ProjectTeamRunTerminalState(ctx, run.LegacyTeamID))

	failed, err := store.GetTeamRun(ctx, run.OwnerID, run.ID)
	require.NoError(t, err)
	require.NotNil(t, failed)
	require.Equal(t, TeamRunSummaryFailed, failed.SummaryState)
	failedVersion := failed.Version
	terminalAt := failed.TerminalAt

	summarySeq, err := store.AppendTeamEvent(ctx, TeamEvent{
		TeamID: run.LegacyTeamID,
		Type:   "team.summary",
		Payload: map[string]interface{}{
			"summary": "summary became available on retry",
		},
	})
	require.NoError(t, err)
	require.NoError(t, store.ProjectTeamRunTerminalState(ctx, run.LegacyTeamID))

	ready, err := store.GetTeamRun(ctx, run.OwnerID, run.ID)
	require.NoError(t, err)
	require.NotNil(t, ready)
	assert.Equal(t, TeamRunSummaryReady, ready.SummaryState)
	assert.Equal(t, summarySeq, ready.EventSeq)
	assert.Equal(t, failedVersion+1, ready.Version)
	assert.Equal(t, terminalAt, ready.TerminalAt, "summary projection must preserve the original terminal timestamp")

	require.NoError(t, store.ProjectTeamRunTerminalState(ctx, run.LegacyTeamID))
	replayed, err := store.GetTeamRun(ctx, run.OwnerID, run.ID)
	require.NoError(t, err)
	require.NotNil(t, replayed)
	assert.Equal(t, ready.Version, replayed.Version)
	assert.Equal(t, ready.SummaryState, replayed.SummaryState)
}

func TestProjectTeamRunTerminalStateIgnoresNonTerminalAndUnrelatedRuns(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	terminalRun := newStartedTeamRunForLifecycleTest(t, store)
	activeRun := newStartedTeamRunForLifecycleTest(t, store)

	before, err := store.GetTeamRun(ctx, activeRun.OwnerID, activeRun.ID)
	require.NoError(t, err)
	require.NotNil(t, before)

	require.NoError(t, store.ProjectTeamRunTerminalState(ctx, activeRun.LegacyTeamID))
	unchanged, err := store.GetTeamRun(ctx, activeRun.OwnerID, activeRun.ID)
	require.NoError(t, err)
	require.NotNil(t, unchanged)
	assert.Equal(t, before.Status, unchanged.Status)
	assert.Equal(t, before.Phase, unchanged.Phase)
	assert.Equal(t, before.SummaryState, unchanged.SummaryState)
	assert.Equal(t, before.Version, unchanged.Version)
	assert.Nil(t, unchanged.TerminalAt)

	require.NoError(t, store.UpdateTeamStatus(ctx, terminalRun.LegacyTeamID, TeamStatusDone))
	appendLifecycleTeamCompleted(t, store, terminalRun.LegacyTeamID, TeamStatusDone)
	require.NoError(t, store.ProjectTeamRunTerminalState(ctx, terminalRun.LegacyTeamID))

	projectedTerminal, err := store.GetTeamRun(ctx, terminalRun.OwnerID, terminalRun.ID)
	require.NoError(t, err)
	require.NotNil(t, projectedTerminal)
	assert.Equal(t, string(TeamStatusDone), projectedTerminal.Status)
	assert.Equal(t, TeamRunPhaseTerminal, projectedTerminal.Phase)

	other, err := store.GetTeamRun(ctx, activeRun.OwnerID, activeRun.ID)
	require.NoError(t, err)
	require.NotNil(t, other)
	assert.Equal(t, TeamRunStatusActive, other.Status)
	assert.Equal(t, TeamRunPhaseRunning, other.Phase)
	assert.Nil(t, other.TerminalAt)
}

func TestProjectTeamRunTerminalStateUpdatesOnlySelectedRunWhenLegacyLinkIsDuplicated(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	firstRun := newStartedTeamRunForLifecycleTest(t, store)
	otherRun := newStartedTeamRunForLifecycleTest(t, store)

	_, err := store.db.ExecContext(ctx, `
		UPDATE team_runs SET legacy_team_id = ? WHERE id = ?
	`, firstRun.LegacyTeamID, otherRun.ID)
	require.NoError(t, err)

	var selectedRunID string
	require.NoError(t, store.db.QueryRowContext(ctx, `
		SELECT id FROM team_runs
		WHERE legacy_team_id = ?
		ORDER BY created_at ASC, id ASC
		LIMIT 1
	`, firstRun.LegacyTeamID).Scan(&selectedRunID))
	require.NoError(t, store.UpdateTeamStatus(ctx, firstRun.LegacyTeamID, TeamStatusDone))
	appendLifecycleTeamCompleted(t, store, firstRun.LegacyTeamID, TeamStatusDone)

	require.NoError(t, store.ProjectTeamRunTerminalState(ctx, firstRun.LegacyTeamID))

	first, err := store.GetTeamRun(ctx, firstRun.OwnerID, firstRun.ID)
	require.NoError(t, err)
	require.NotNil(t, first)
	other, err := store.GetTeamRun(ctx, otherRun.OwnerID, otherRun.ID)
	require.NoError(t, err)
	require.NotNil(t, other)

	if selectedRunID == firstRun.ID {
		assert.Equal(t, TeamRunPhaseTerminal, first.Phase)
		assert.Equal(t, TeamRunPhaseRunning, other.Phase)
		assert.Nil(t, other.TerminalAt)
	} else {
		assert.Equal(t, TeamRunPhaseRunning, first.Phase)
		assert.Nil(t, first.TerminalAt)
		assert.Equal(t, TeamRunPhaseTerminal, other.Phase)
	}
}
