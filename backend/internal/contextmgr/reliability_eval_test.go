package contextmgr

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
	"github.com/wwsheng009/ai-agent-runtime/internal/contextreconcile"
	"github.com/wwsheng009/ai-agent-runtime/internal/factledger"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestReliabilityEvalGoalChangeKeepsToolReplayPrefix(t *testing.T) {
	assistant := types.NewAssistantMessage("")
	assistant.ToolCalls = []types.ToolCall{
		{ID: "call-old", Name: "todos"},
		{ID: "call-active", Name: "read"},
	}
	oldResult := types.NewToolMessage("call-old", "old goal todo must not leak")
	oldResult.Metadata["goal_id"] = "goal-old"
	activeResult := types.NewToolMessage("call-active", "active result")
	activeResult.Metadata["goal_id"] = "goal-active"

	manager := NewManager(DefaultBudget(), nil)
	result := manager.Build(context.Background(), BuildInput{
		GoalID: "goal-active",
		History: []types.Message{
			*types.NewSystemMessage("system"),
			*assistant,
			*oldResult,
			*activeResult,
			*types.NewUserMessage("continue active goal"),
		},
	})

	require.Equal(t, 0, result.Metadata["goal_scoped_messages_filtered"])
	require.Equal(t, 0, result.Metadata["goal_scoped_tool_calls_filtered"])
	require.Contains(t, joinedMessageContent(result.Messages), "old goal todo must not leak")
	require.True(t, hasToolResult(result.Messages, "call-active"))
	require.True(t, hasToolResult(result.Messages, "call-old"))
	require.Equal(t, []string{"call-old", "call-active"}, assistantToolCallIDs(result.Messages))
}

func TestReliabilityEvalNoActiveGoalKeepsPreviouslySentGoalHistory(t *testing.T) {
	owned := types.NewAssistantMessage("todo from a previous goal")
	owned.Metadata["goal_id"] = "goal-old"

	manager := NewManager(DefaultBudget(), nil)
	result := manager.Build(context.Background(), BuildInput{
		History: []types.Message{*owned, *types.NewUserMessage("unscoped turn")},
	})

	require.Equal(t, 0, result.Metadata["goal_scoped_messages_filtered"])
	require.Contains(t, joinedMessageContent(result.Messages), owned.Content)
	require.Contains(t, joinedMessageContent(result.Messages), "unscoped turn")
}

func TestReliabilityEvalLongSessionCompactStateRetention(t *testing.T) {
	for _, turns := range []int{50, 100, 200} {
		turns := turns
		t.Run(fmt.Sprintf("turns_%d", turns), func(t *testing.T) {
			const (
				activeGoalID       = "goal-active"
				activeGoal         = "complete the reliability release gate"
				currentTodo        = "current todo: run release gate"
				userConstraint     = "run the embedded browser gate before release"
				completedAction    = "completed action: Go unit and integration tests passed"
				jobID              = "job-release-check"
				childAgentID       = "agent-review-17"
				oldSessionTodoMark = "OLD_SESSION_TODO"
			)
			store, err := artifact.NewStore(nil)
			require.NoError(t, err)
			defer func() { require.NoError(t, store.Close()) }()

			ctx := context.Background()
			sessionID := fmt.Sprintf("session-long-%d", turns)
			oldSessionID := fmt.Sprintf("session-old-%d", turns)
			ledger := factledger.New(store)
			constraint := factledger.Fact{
				FactID:        fmt.Sprintf("fact-constraint-%d", turns),
				SessionID:     sessionID,
				GoalID:        activeGoalID,
				Scope:         factledger.ScopeGoal,
				Kind:          "constraint",
				Subject:       "release",
				Predicate:     "requires",
				Value:         userConstraint,
				SourceEventID: "event:user-constraint",
				EvidenceRefs:  []string{"event:user-constraint"},
				Confidence:    1,
				ValidFrom:     time.Unix(1, 0).UTC(),
				UpdatedAt:     time.Unix(1, 0).UTC(),
			}
			_, err = ledger.Append(ctx, constraint)
			require.NoError(t, err)
			_, err = ledger.Append(ctx, constraint)
			require.NoError(t, err)
			goalFact := factledger.Fact{
				FactID:        fmt.Sprintf("fact-goal-%d", turns),
				SessionID:     sessionID,
				GoalID:        activeGoalID,
				Scope:         factledger.ScopeGoal,
				Kind:          "goal",
				Subject:       activeGoalID,
				Predicate:     "objective",
				Value:         activeGoal,
				SourceEventID: "event:active-goal",
				EvidenceRefs:  []string{"event:active-goal"},
				Confidence:    1,
			}
			_, err = ledger.Append(ctx, goalFact)
			require.NoError(t, err)
			completed := factledger.Fact{
				FactID:        fmt.Sprintf("fact-completed-%d", turns),
				SessionID:     sessionID,
				GoalID:        activeGoalID,
				Scope:         factledger.ScopeGoal,
				Kind:          "completed_action",
				Subject:       "backend-tests",
				Predicate:     "completed",
				Value:         completedAction,
				SourceEventID: "event:completed-tests",
				EvidenceRefs:  []string{"test:go-all"},
				Confidence:    1,
			}
			_, err = ledger.Append(ctx, completed)
			require.NoError(t, err)
			_, err = ledger.Append(ctx, completed)
			require.NoError(t, err)
			oldSessionTodo := factledger.Fact{
				FactID:        fmt.Sprintf("fact-old-session-todo-%d", turns),
				SessionID:     oldSessionID,
				GoalID:        activeGoalID,
				Scope:         factledger.ScopeGoal,
				Kind:          "remaining_work",
				Subject:       "obsolete",
				Predicate:     "plans",
				Value:         oldSessionTodoMark + ": obsolete goal todo",
				SourceEventID: "event:old-session",
				EvidenceRefs:  []string{"event:old-session"},
				Confidence:    1,
			}
			_, err = ledger.Append(ctx, oldSessionTodo)
			require.NoError(t, err)

			history := longSessionHistory(
				turns,
				sessionID,
				oldSessionID,
				activeGoalID,
				currentTodo,
				jobID,
				childAgentID,
				oldSessionTodoMark,
			)
			manager := NewManager(Budget{
				MaxPromptTokens:     4000,
				MaxMessages:         32,
				KeepRecentMessages:  8,
				MaxRecallResults:    1,
				MaxObservationItems: 2,
			}, store)
			input := BuildInput{
				TraceID:     fmt.Sprintf("trace-long-%d", turns),
				WorkspaceID: "workspace-reliability",
				SessionID:   sessionID,
				GoalID:      activeGoalID,
				TaskID:      "task-release",
				Goal:        activeGoal,
				History:     history,
				CountTokens: func(messages []types.Message) int { return len(messages) * 200 },
			}

			before := manager.Build(ctx, input)
			input.EnablePromptCompaction = true
			after := manager.Build(ctx, input)

			expectedFactIDs := []string{constraint.FactID, goalFact.FactID, completed.FactID}
			beforeFactIDs := factIDs(before.Messages)
			afterFactIDs := factIDs(after.Messages)
			require.ElementsMatch(t, expectedFactIDs, beforeFactIDs)
			require.ElementsMatch(t, beforeFactIDs, afterFactIDs,
				"compaction must not change the authoritative facts injected into the current prompt")
			require.NotContains(t, before.Metadata, "compacted_messages")

			compactedMessages, ok := after.Metadata["compacted_messages"].(int)
			require.True(t, ok, "long-session eval did not report compaction")
			require.GreaterOrEqual(t, compactedMessages, turns,
				"the configured 50/100/200-turn history must cross the compaction threshold")
			checkpointID, ok := after.Metadata["checkpoint_id"].(string)
			require.True(t, ok)
			require.NotEmpty(t, checkpointID, "compaction must persist a checkpoint")
			cold := after.Metadata["context_layer_metrics"].(map[string]interface{})["cold"].(map[string]interface{})
			require.Equal(t, true, cold["compacted"])
			require.Equal(t, true, cold["ledger_injected"])

			checkpoints, err := store.ListCheckpoints(ctx, sessionID, 10, 0)
			require.NoError(t, err)
			require.Len(t, checkpoints, 1)
			checkpoint := checkpoints[0]
			require.Equal(t, checkpointID, checkpoint.ID)
			require.Equal(t, ledgerCheckpointSegmentReason, checkpoint.Reason)
			require.Equal(t, compactedMessages, checkpoint.MessageCount)
			require.NotEmpty(t, checkpoint.HistoryHash)
			require.Len(t, checkpoint.Ledger, checkpoint.MessageCount)
			start, end, validRange := ledgerCheckpointRange(checkpoint)
			require.True(t, validRange)
			require.Equal(t, 0, start)
			require.Equal(t, compactedMessages, end)

			ledgerMessages := ledgerMessagesFromResult(after.Messages)
			require.Len(t, ledgerMessages, 1)
			require.Equal(t, checkpointID, ledgerMessages[0].Metadata.GetString("checkpoint_id", ""))
			checkpointText := checkpointLedgerContent(checkpoint)
			require.Contains(t, checkpointText, jobID)
			require.Contains(t, checkpointText, childAgentID)

			beforeText := joinedMessageContent(before.Messages)
			afterText := joinedMessageContent(after.Messages)
			for _, expected := range []string{activeGoalID, activeGoal, currentTodo, userConstraint, jobID, childAgentID} {
				require.Contains(t, beforeText, expected)
				require.Contains(t, afterText, expected)
			}
			require.Equal(t, 1, strings.Count(beforeText, completedAction))
			require.Equal(t, 1, strings.Count(afterText, completedAction),
				"a completed action must not be replayed as duplicate work after compaction")
			require.Equal(t, 1, strings.Count(afterText, currentTodo))
			require.Equal(t, 1, strings.Count(afterText, userConstraint))
			// Before compaction the exact historical prefix is intentionally
			// preserved, including old goal-owned messages. Explicit compaction is
			// the boundary allowed to remove that stale replay.
			require.Contains(t, beforeText, oldSessionTodoMark)
			// The compaction implementation decides what semantic details survive;
			// prefix stability only permits (rather than requires) removal here.
			require.Zero(t, after.Metadata["goal_scoped_messages_filtered"].(int))

			activeFacts, err := ledger.ListActive(ctx, factledger.Query{SessionID: sessionID, GoalID: activeGoalID})
			require.NoError(t, err)
			require.Equal(t, 1, countFactID(activeFacts, constraint.FactID))
			require.Equal(t, 1, countFactID(activeFacts, completed.FactID))
			require.Zero(t, countFactID(activeFacts, oldSessionTodo.FactID))
			oldSessionFacts, err := ledger.ListActive(ctx, factledger.Query{SessionID: oldSessionID, GoalID: activeGoalID})
			require.NoError(t, err)
			require.Equal(t, 1, countFactID(oldSessionFacts, oldSessionTodo.FactID))

			reconciled, report := contextreconcile.Reconcile(after.Messages, contextreconcile.Snapshot{
				SessionID: sessionID,
				Goal: &contextreconcile.GoalSnapshot{
					GoalID: activeGoalID, Objective: input.Goal, Status: "active",
				},
				Todos: []contextreconcile.TodoSnapshot{{Content: "run release gate", Status: "in_progress"}},
				Jobs:  []contextreconcile.JobSnapshot{{JobID: jobID, Status: "running"}},
			})
			// Compaction may retain stale semantic details and reconciliation may
			// append a correction. That is inside the explicit rewrite epoch and is
			// orthogonal to ordinary request-prefix immutability.
			if !report.CorrectionMade {
				require.Zero(t, report.DriftCount)
				require.Equal(t, after.Messages, reconciled)
			}
		})
	}
}

func joinedMessageContent(messages []types.Message) string {
	parts := make([]string, 0, len(messages))
	for _, message := range messages {
		parts = append(parts, message.Content)
	}
	return strings.Join(parts, "\n")
}

func hasToolResult(messages []types.Message, callID string) bool {
	for _, message := range messages {
		if message.Role == "tool" && message.ToolCallID == callID {
			return true
		}
	}
	return false
}

func assistantToolCallIDs(messages []types.Message) []string {
	var ids []string
	for _, message := range messages {
		if message.Role != "assistant" {
			continue
		}
		for _, call := range message.ToolCalls {
			ids = append(ids, call.ID)
		}
	}
	return ids
}

func longSessionHistory(
	turns int,
	sessionID string,
	oldSessionID string,
	activeGoalID string,
	currentTodo string,
	jobID string,
	childAgentID string,
	oldSessionTodoMark string,
) []types.Message {
	history := []types.Message{*types.NewSystemMessage("system reliability policy")}
	jobReference := types.NewAssistantMessage("background job reference: " + jobID + " status=running")
	jobReference.Metadata["session_id"] = sessionID
	jobReference.Metadata["goal_id"] = activeGoalID
	jobReference.Metadata["job_id"] = jobID
	childReference := types.NewAssistantMessage("child agent reference: " + childAgentID + " status=running")
	childReference.Metadata["session_id"] = sessionID
	childReference.Metadata["goal_id"] = activeGoalID
	childReference.Metadata["child_agent_id"] = childAgentID
	history = append(history, *jobReference, *childReference)

	for turn := 0; turn < turns; turn++ {
		user := types.NewUserMessage(fmt.Sprintf("active turn %03d", turn))
		user.Metadata["session_id"] = sessionID
		user.Metadata["goal_id"] = activeGoalID
		assistant := types.NewAssistantMessage(fmt.Sprintf("active result %03d", turn))
		assistant.Metadata["session_id"] = sessionID
		assistant.Metadata["goal_id"] = activeGoalID
		history = append(history, *user, *assistant)
		if turn%10 == 0 {
			stale := types.NewAssistantMessage(fmt.Sprintf("%s_%03d", oldSessionTodoMark, turn))
			stale.Metadata["session_id"] = oldSessionID
			stale.Metadata["goal_id"] = "goal-old"
			history = append(history, *stale)
		}
	}
	todoState := types.NewAssistantMessage(currentTodo)
	todoState.Metadata["context_stage"] = "todo_state"
	todoState.Metadata["session_id"] = sessionID
	todoState.Metadata["goal_id"] = activeGoalID
	todoState.Metadata["todos"] = []map[string]interface{}{{
		"content": "run release gate", "status": "in_progress",
	}}
	history = append(history, *todoState)
	lastStale := types.NewAssistantMessage(oldSessionTodoMark + "_FINAL")
	lastStale.Metadata["session_id"] = oldSessionID
	lastStale.Metadata["goal_id"] = "goal-old"
	finalUser := types.NewUserMessage("finish the active release goal")
	finalUser.Metadata["session_id"] = sessionID
	finalUser.Metadata["goal_id"] = activeGoalID
	history = append(history, *lastStale, *finalUser)
	return history
}

func checkpointLedgerContent(checkpoint artifact.Checkpoint) string {
	parts := make([]string, 0, len(checkpoint.Ledger))
	for _, entry := range checkpoint.Ledger {
		parts = append(parts, fmt.Sprintf("%v", entry.Content["summary"]))
	}
	return strings.Join(parts, "\n")
}

func factIDs(messages []types.Message) []string {
	for _, message := range messages {
		if message.Metadata.GetString("context_stage", "") != "fact_ledger" {
			continue
		}
		ids, _ := message.Metadata["fact_ids"].([]string)
		return ids
	}
	return nil
}

func countFactID(facts []factledger.Fact, factID string) int {
	count := 0
	for _, fact := range facts {
		if fact.FactID == factID {
			count++
		}
	}
	return count
}
