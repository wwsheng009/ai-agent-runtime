package team

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReconcileTerminalTeamStateDoesNotDuplicateSummarySideEffects(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{
		Status:        TeamStatusActive,
		LeadSessionID: "lead-session",
	})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, Task{
		ID:      "task-1",
		TeamID:  teamID,
		Title:   "done task",
		Status:  TaskStatusDone,
		Summary: "finished",
	})
	require.NoError(t, err)

	planner := &LeadPlanner{
		Sessions: &staticSessionClient{
			result: &SessionResult{
				Success: true,
				Output:  "final team summary",
			},
		},
		Store: store,
	}
	mailbox := NewMailboxService(store)

	first, err := ReconcileTerminalTeamState(ctx, TerminalTeamServices{
		Store:   store,
		Planner: planner,
		Mailbox: mailbox,
	}, teamID)
	require.NoError(t, err)
	require.NotNil(t, first)
	assert.True(t, first.Terminal)
	assert.Equal(t, TeamStatusDone, first.Status)
	assert.Equal(t, "final team summary", first.Summary)

	second, err := ReconcileTerminalTeamState(ctx, TerminalTeamServices{
		Store:   store,
		Planner: planner,
		Mailbox: mailbox,
	}, teamID)
	require.NoError(t, err)
	require.NotNil(t, second)
	assert.True(t, second.Terminal)
	assert.Equal(t, TeamStatusDone, second.Status)
	assert.Empty(t, second.Summary)

	events, err := store.ListTeamEvents(ctx, TeamEventFilter{TeamID: teamID})
	require.NoError(t, err)

	eventCounts := map[string]int{}
	for _, event := range events {
		eventCounts[event.Type]++
	}
	assert.Equal(t, 1, eventCounts["team.completed"])
	assert.Equal(t, 1, eventCounts["team.summary"])

	messages, err := store.ListMail(ctx, MailFilter{
		TeamID: teamID,
		Kind:   "done",
	})
	require.NoError(t, err)
	require.Len(t, messages, 1)
	assert.Equal(t, "lead", messages[0].FromAgent)
	assert.Equal(t, "*", messages[0].ToAgent)
	assert.Equal(t, "final team summary", messages[0].Body)
}

func TestReconcileTerminalTeamStateDoesNotDowngradeTerminalDoneTeam(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{
		Status:        TeamStatusDone,
		LeadSessionID: "lead-session",
	})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, Task{
		ID:      "task-1",
		TeamID:  teamID,
		Title:   "late failed task",
		Status:  TaskStatusFailed,
		Summary: "context canceled",
	})
	require.NoError(t, err)

	result, err := ReconcileTerminalTeamState(ctx, TerminalTeamServices{
		Store: store,
	}, teamID)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Terminal)
	assert.False(t, result.Transition)
	assert.Equal(t, TeamStatusDone, result.Status)

	record, err := store.GetTeam(ctx, teamID)
	require.NoError(t, err)
	require.NotNil(t, record)
	assert.Equal(t, TeamStatusDone, record.Status)

	events, err := store.ListTeamEvents(ctx, TeamEventFilter{TeamID: teamID})
	require.NoError(t, err)
	assert.Empty(t, events)
}

func TestReconcileTerminalTeamStatePublishesFallbackSummaryFailureMetadata(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{
		Status:        TeamStatusActive,
		LeadSessionID: "lead-session",
	})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, Task{
		ID:      "task-1",
		TeamID:  teamID,
		Title:   "done task",
		Status:  TaskStatusDone,
		Summary: "finished",
	})
	require.NoError(t, err)

	planner := &LeadPlanner{
		Sessions: &staticSessionClient{
			result: &SessionResult{
				Success:   false,
				Error:     "prompt preflight budget exceeded",
				TraceID:   "trace-terminal-summary-preflight",
				ErrorType: "prompt_preflight",
				ErrorMetadata: map[string]interface{}{
					"failure_reason_code":         "prompt_still_exceeds_budget_after_compaction",
					"replacement_history_applied": true,
				},
			},
			err: assert.AnError,
		},
		Store: store,
	}
	mailbox := NewMailboxService(store)

	result, err := ReconcileTerminalTeamState(ctx, TerminalTeamServices{
		Store:   store,
		Planner: planner,
		Mailbox: mailbox,
	}, teamID)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.True(t, result.Terminal)
	assert.True(t, result.Transition)
	assert.Equal(t, TeamStatusDone, result.Status)
	assert.Equal(t, FinalSummarySourceFallback, result.SummarySource)
	assert.True(t, result.SummaryUsedFallback)
	assert.Equal(t, FinalSummaryFallbackLeadSessionError, result.SummaryFallbackReason)
	assert.Equal(t, "trace-terminal-summary-preflight", result.SummaryTraceID)
	assert.Equal(t, "prompt_preflight", result.SummaryErrorType)
	assert.Equal(t, "prompt_still_exceeds_budget_after_compaction", result.SummaryErrorMetadata["failure_reason_code"])
	assert.Equal(t, true, result.SummaryErrorMetadata["replacement_history_applied"])
	assert.Contains(t, result.Summary, "Team "+teamID+" summary:")
	assert.Contains(t, result.Summary, "finished")

	events, err := store.ListTeamEvents(ctx, TeamEventFilter{TeamID: teamID})
	require.NoError(t, err)
	require.Len(t, events, 3)
	assert.Equal(t, "team.completed", events[0].Type)
	assert.Equal(t, "team.summary.failed", events[1].Type)
	assert.Equal(t, "team.summary", events[2].Type)
	assert.Equal(t, "prompt_preflight", events[1].Payload["error_type"])
	assert.Equal(t, FinalSummaryFallbackLeadSessionError, events[1].Payload["fallback_reason"])
	assert.Equal(t, "trace-terminal-summary-preflight", events[1].Payload["trace_id"])
	assert.Equal(t, "prompt_still_exceeds_budget_after_compaction", events[1].Payload["failure_reason_code"])
	assert.Equal(t, FinalSummarySourceFallback, events[2].Payload["summary_source"])
	assert.Equal(t, true, events[2].Payload["used_fallback"])
	assert.Equal(t, FinalSummaryFallbackLeadSessionError, events[2].Payload["fallback_reason"])
	assert.Equal(t, "prompt_preflight", events[2].Payload["error_type"])
	assert.Equal(t, "trace-terminal-summary-preflight", events[2].Payload["trace_id"])

	messages, err := store.ListMail(ctx, MailFilter{
		TeamID: teamID,
		Kind:   "done",
	})
	require.NoError(t, err)
	require.Len(t, messages, 1)
	assert.Equal(t, result.Summary, messages[0].Body)
}
