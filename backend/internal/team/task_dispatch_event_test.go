package team

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBuildTaskLifecycleMailboxMessageUsesAgentControlEnvelope(t *testing.T) {
	message := BuildTaskLifecycleMailboxMessage(TeamEvent{
		Type:   "task.completed",
		TeamID: "team-1",
		Payload: map[string]interface{}{
			"task_id":  "task-1",
			"assignee": "mate-1",
			"summary":  "done",
		},
	})

	require.Equal(t, TaskLifecycleMailboxKind, message.Kind)
	require.Equal(t, "team-1", message.TeamID)
	require.Equal(t, "mate-1", message.ToAgent)
	require.NotNil(t, message.TaskID)
	require.Equal(t, "task-1", *message.TaskID)
	require.Equal(t, "done", message.Body)
	require.Equal(t, TaskLifecycleControlMessageType, message.Metadata["message_type"])
	require.Equal(t, TaskLifecycleControlAction, message.Metadata["control_action"])
	require.Equal(t, TaskAssignmentWorkflow, message.Metadata["workflow"])
	require.Equal(t, taskDispatchMailboxDeliveryAgentSubstr, message.Metadata["mailbox_delivery"])
	require.Equal(t, TaskLifecycleMailboxKind, message.Metadata["mailbox_kind"])
	require.Equal(t, "task.completed", message.Metadata["event_type"])
	require.Equal(t, "mate-1", message.Metadata["assignee"])
}

func TestBuildTaskAssignmentMailboxMessageIncludesDifficultyMetadata(t *testing.T) {
	message := BuildTaskAssignmentMailboxMessage(TaskTriggerRequest{
		SessionID:           "session-1",
		TeamID:              "team-1",
		AgentID:             "mate-1",
		TaskID:              "task-1",
		Difficulty:          TaskDifficultyExpert,
		DifficultyRationale: "Touches shared execution state.",
		Prompt:              "do the task",
	})

	require.Equal(t, TaskAssignmentMailboxKind, message.Kind)
	require.Equal(t, "team-1", message.TeamID)
	require.Equal(t, "mate-1", message.ToAgent)
	require.NotNil(t, message.TaskID)
	require.Equal(t, "task-1", *message.TaskID)
	require.Equal(t, TaskAssignmentControlMessageType, message.Metadata["message_type"])
	require.Equal(t, TaskAssignmentControlAction, message.Metadata["control_action"])
	require.Equal(t, TaskAssignmentWorkflow, message.Metadata["workflow"])
	require.Equal(t, TaskDifficultyExpert, message.Metadata["difficulty"])
	require.Equal(t, "Touches shared execution state.", message.Metadata["difficulty_rationale"])
}

func TestBuildTaskAssignmentMailboxMessageIncludesRouteMetadataAndPermissionMode(t *testing.T) {
	resolvedAt := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	message := BuildTaskAssignmentMailboxMessage(TaskTriggerRequest{
		SessionID:           "session-1",
		TeamID:              "team-1",
		AgentID:             "mate-1",
		TaskID:              "task-1",
		Difficulty:          TaskDifficultyHard,
		DifficultyRationale: "Touches shared execution state.",
		Prompt:              "do the task",
		Route: &TaskExecutionRoute{
			Difficulty:          TaskDifficultyExpert,
			DifficultySource:    "task",
			DifficultyRationale: "Route rationale wins.",
			Provider:            "remote-strong",
			Model:               "strong-model",
			ReasoningEffort:     "high",
			Source:              "difficulty_level",
			Warnings:            []string{"reasoning downgraded"},
			FallbackUsed:        true,
			FallbackReason:      "provider fallback",
			ResolvedAt:          resolvedAt,
			Attempt:             2,
		},
		RunMeta: &RunMeta{
			PermissionMode: "bypass_permissions",
		},
	})

	require.Equal(t, TaskAssignmentMailboxKind, message.Kind)
	require.Equal(t, TaskDifficultyExpert, message.Metadata["difficulty"])
	require.Equal(t, "task", message.Metadata["difficulty_source"])
	require.Equal(t, "Route rationale wins.", message.Metadata["difficulty_rationale"])
	require.Equal(t, "remote-strong", message.Metadata["route_provider"])
	require.Equal(t, "strong-model", message.Metadata["route_model"])
	require.Equal(t, "high", message.Metadata["route_reasoning_effort"])
	require.Equal(t, "difficulty_level", message.Metadata["route_source"])
	require.Equal(t, []string{"reasoning downgraded"}, message.Metadata["route_warnings"])
	require.Equal(t, true, message.Metadata["fallback"])
	require.Equal(t, true, message.Metadata["fallback_used"])
	require.Equal(t, "provider fallback", message.Metadata["fallback_reason"])
	require.Equal(t, resolvedAt.Format(time.RFC3339Nano), message.Metadata["route_resolved_at"])
	require.Equal(t, 2, message.Metadata["route_attempt"])
	require.Equal(t, "bypass_permissions", message.Metadata["permission_mode"])
}

func TestAppendTaskDispatchRequestedIncludesDifficultyMetadata(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)

	seq, err := AppendTaskDispatchRequested(ctx, store, TaskTriggerRequest{
		SessionID:           "session-1",
		TeamID:              teamID,
		AgentID:             "mate-1",
		TaskID:              "task-1",
		Difficulty:          TaskDifficultyHard,
		DifficultyRationale: "Touches dispatch audit.",
		Prompt:              "do the task",
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), seq)

	events, err := store.ListTeamEvents(ctx, TeamEventFilter{
		TeamID:    teamID,
		EventType: TaskDispatchRequestedEvent,
	})
	require.NoError(t, err)
	require.Len(t, events, 1)
	payload := events[0].Payload
	require.Equal(t, teamID, payload["team_id"])
	require.Equal(t, "mate-1", payload["assignee"])
	require.Equal(t, "task-1", payload["task_id"])
	require.Equal(t, TaskDifficultyHard, payload["difficulty"])
	require.Equal(t, "Touches dispatch audit.", payload["difficulty_rationale"])
}

func TestAppendTaskDispatchRequestedIncludesRouteMetadataAndPermissionMode(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)

	seq, err := AppendTaskDispatchRequested(ctx, store, TaskTriggerRequest{
		SessionID: "session-1",
		TeamID:    teamID,
		AgentID:   "mate-1",
		TaskID:    "task-1",
		Prompt:    "do the task",
		Route: &TaskExecutionRoute{
			Difficulty:      TaskDifficultyHard,
			Provider:        "remote-strong",
			Model:           "strong-model",
			ReasoningEffort: "high",
			Source:          "difficulty_level",
			Warnings:        []string{"fallback checked"},
			FallbackUsed:    false,
		},
		RunMeta: &RunMeta{
			PermissionMode: "bypass_permissions",
		},
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), seq)

	events, err := store.ListTeamEvents(ctx, TeamEventFilter{
		TeamID:    teamID,
		EventType: TaskDispatchRequestedEvent,
	})
	require.NoError(t, err)
	require.Len(t, events, 1)
	payload := events[0].Payload
	require.Equal(t, TaskDifficultyHard, payload["difficulty"])
	require.Equal(t, "remote-strong", payload["route_provider"])
	require.Equal(t, "strong-model", payload["route_model"])
	require.Equal(t, "high", payload["route_reasoning_effort"])
	require.Equal(t, "difficulty_level", payload["route_source"])
	require.Equal(t, []interface{}{"fallback checked"}, payload["route_warnings"])
	require.Equal(t, false, payload["fallback"])
	require.Equal(t, false, payload["fallback_used"])
	require.Equal(t, "bypass_permissions", payload["permission_mode"])
}

func TestAppendTaskDispatchStartedIncludesRouteMetadataAndPermissionMode(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)

	seq, err := AppendTaskDispatchStarted(ctx, store, TaskTriggerRequest{
		SessionID: "session-1",
		TeamID:    teamID,
		AgentID:   "mate-1",
		TaskID:    "task-1",
		Prompt:    "do the task",
		Route: &TaskExecutionRoute{
			Difficulty:      TaskDifficultyExpert,
			Provider:        "remote-strong",
			Model:           "strong-model",
			ReasoningEffort: "high",
			Source:          "difficulty_level",
			Warnings:        []string{"fallback checked"},
			FallbackUsed:    true,
			FallbackReason:  "provider fallback",
			Attempt:         2,
		},
		RunMeta: &RunMeta{
			PermissionMode: "bypass_permissions",
		},
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), seq)

	events, err := store.ListTeamEvents(ctx, TeamEventFilter{
		TeamID:    teamID,
		EventType: TaskDispatchStartedEvent,
	})
	require.NoError(t, err)
	require.Len(t, events, 1)
	payload := events[0].Payload
	require.Equal(t, TaskDifficultyExpert, payload["difficulty"])
	require.Equal(t, "remote-strong", payload["route_provider"])
	require.Equal(t, "strong-model", payload["route_model"])
	require.Equal(t, "high", payload["route_reasoning_effort"])
	require.Equal(t, "difficulty_level", payload["route_source"])
	require.Equal(t, []interface{}{"fallback checked"}, payload["route_warnings"])
	require.Equal(t, true, payload["fallback"])
	require.Equal(t, true, payload["fallback_used"])
	require.Equal(t, "provider fallback", payload["fallback_reason"])
	require.Equal(t, float64(2), payload["route_attempt"])
	require.Equal(t, "bypass_permissions", payload["permission_mode"])
}

func TestAppendTaskRouteResolvedRedactsSensitiveRouteError(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)

	_, err = AppendTaskRouteResolved(ctx, store, TaskRouteAudit{
		TeamID:    teamID,
		AgentID:   "mate-1",
		TaskID:    "task-1",
		SessionID: "session-1",
		Route: &TaskExecutionRoute{
			Difficulty: TaskDifficultyExpert,
			Source:     "difficulty_level",
			Error:      "provider failed Authorization: Bearer sk-secret-token api_key=abc123 https://user:pass@example.test/v1",
		},
		Error: "provider failed Authorization: Bearer sk-secret-token api_key=abc123 https://user:pass@example.test/v1",
	})
	require.NoError(t, err)

	events, err := store.ListTeamEvents(ctx, TeamEventFilter{
		TeamID:    teamID,
		EventType: TaskRouteResolvedEvent,
	})
	require.NoError(t, err)
	require.Len(t, events, 1)
	routeError, _ := events[0].Payload["route_error"].(string)
	require.NotEmpty(t, routeError)
	require.NotContains(t, routeError, "sk-secret-token")
	require.NotContains(t, routeError, "abc123")
	require.NotContains(t, routeError, "user:pass")
	require.Contains(t, routeError, "[REDACTED]")
}

func TestAppendTaskDispatchCompletedPreservesDifficultyMetadata(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)

	seq, err := AppendTaskDispatchCompleted(ctx, store, TaskTriggerRequest{
		SessionID:           "session-1",
		TeamID:              teamID,
		AgentID:             "mate-1",
		TaskID:              "task-1",
		Difficulty:          TaskDifficultyExpert,
		DifficultyRationale: "Needs expert audit.",
		Prompt:              "do the task",
	}, &SessionResult{
		Success: true,
		TraceID: "trace-1",
		Steps:   3,
	}, nil)
	require.NoError(t, err)
	require.Equal(t, int64(1), seq)

	events, err := store.ListTeamEvents(ctx, TeamEventFilter{
		TeamID:    teamID,
		EventType: TaskDispatchCompletedEvent,
	})
	require.NoError(t, err)
	require.Len(t, events, 1)
	payload := events[0].Payload
	require.Equal(t, true, payload["success"])
	require.Equal(t, "trace-1", payload["trace_id"])
	require.Equal(t, float64(3), payload["steps"])
	require.Equal(t, TaskDifficultyExpert, payload["difficulty"])
	require.Equal(t, "Needs expert audit.", payload["difficulty_rationale"])
}
