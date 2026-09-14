package chat

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// branchTestHistory builds a session-backed history so every message carries a
// stable message_id (as it does on the real API path).
func branchTestHistory(t *testing.T, messages []types.Message) []types.Message {
	t.Helper()
	session := NewSession("branch-unit-user")
	for _, msg := range messages {
		session.AddMessage(msg)
	}
	return session.GetMessages()
}

func messageIDList(messages []types.Message) []string {
	ids := make([]string, 0, len(messages))
	for _, msg := range messages {
		ids = append(ids, types.MessageID(msg))
	}
	return ids
}

func TestPlanBranchWholeSessionDefault(t *testing.T) {
	history := branchTestHistory(t, []types.Message{
		*types.NewUserMessage("first"),
		*types.NewAssistantMessage("a1"),
		*types.NewUserMessage("second"),
		*types.NewAssistantMessage("a2"),
	})

	plan, err := PlanBranch(history, BranchRequest{IncludeAnchor: true})
	require.NoError(t, err)
	require.Equal(t, "", plan.Anchor.SourceMessageID)
	require.Equal(t, 1, plan.Anchor.TurnIndex, "default anchor reports the last user turn")
	require.True(t, plan.Anchor.Included)
	require.Equal(t, messageIDList(history), messageIDList(plan.Prefix))
}

func TestPlanBranchEmptyHistoryDefault(t *testing.T) {
	plan, err := PlanBranch(nil, BranchRequest{IncludeAnchor: true})
	require.NoError(t, err)
	require.Equal(t, "", plan.Anchor.SourceMessageID)
	require.Equal(t, -1, plan.Anchor.TurnIndex)
	require.True(t, plan.Anchor.Included)
	require.Empty(t, plan.Prefix)
}

func TestPlanBranchTurnTailAnchor(t *testing.T) {
	history := branchTestHistory(t, []types.Message{
		*types.NewUserMessage("first"),
		*types.NewAssistantMessage("a1"),
		*types.NewUserMessage("second"),
		*types.NewAssistantMessage("a2"),
	})
	ids := messageIDList(history)

	for _, tc := range []struct {
		name          string
		includeAnchor bool
		wantPrefix    []string
	}{
		{name: "include anchor", includeAnchor: true, wantPrefix: ids[:2]},
		{name: "exclude anchor", includeAnchor: false, wantPrefix: ids[:1]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := PlanBranch(history, BranchRequest{AnchorMessageID: ids[1], IncludeAnchor: tc.includeAnchor})
			require.NoError(t, err)
			require.Equal(t, ids[1], plan.Anchor.SourceMessageID)
			require.Equal(t, 0, plan.Anchor.TurnIndex)
			require.Equal(t, tc.includeAnchor, plan.Anchor.Included)
			require.Equal(t, tc.wantPrefix, messageIDList(plan.Prefix))
		})
	}

	// Anchoring the last message of the history is a valid turn tail.
	plan, err := PlanBranch(history, BranchRequest{AnchorMessageID: ids[3], IncludeAnchor: true})
	require.NoError(t, err)
	require.Equal(t, 1, plan.Anchor.TurnIndex)
	require.Equal(t, ids, messageIDList(plan.Prefix))
}

func TestPlanBranchRejectsUnknownAnchor(t *testing.T) {
	history := branchTestHistory(t, []types.Message{
		*types.NewUserMessage("first"),
		*types.NewAssistantMessage("a1"),
	})

	_, err := PlanBranch(history, BranchRequest{AnchorMessageID: "msg_missing", IncludeAnchor: true})
	require.ErrorIs(t, err, ErrBranchAnchorNotFound)
}

func TestPlanBranchRejectsNonTurnTailAnchor(t *testing.T) {
	history := branchTestHistory(t, []types.Message{
		*types.NewSystemMessage("system"),
		*types.NewUserMessage("first"),
		*types.NewAssistantMessage("a1"),
		*types.NewToolMessage("tool_1", "result"),
		*types.NewAssistantMessage("a2"),
	})
	ids := messageIDList(history)

	for _, tc := range []struct {
		name     string
		anchorID string
	}{
		{name: "user message anchor", anchorID: ids[1]},
		{name: "assistant mid turn", anchorID: ids[2]},
		{name: "before first user turn", anchorID: ids[0]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := PlanBranch(history, BranchRequest{AnchorMessageID: tc.anchorID, IncludeAnchor: true})
			require.ErrorIs(t, err, ErrBranchAnchorNotTurnTail)
		})
	}

	// The final assistant message closing turn 0 is the only accepted anchor.
	plan, err := PlanBranch(history, BranchRequest{AnchorMessageID: ids[4], IncludeAnchor: true})
	require.NoError(t, err)
	require.Equal(t, 0, plan.Anchor.TurnIndex)
	require.Equal(t, ids, messageIDList(plan.Prefix))

	// Sanity: the sentinel classification is not a generic error.
	require.False(t, errors.Is(ErrBranchAnchorNotTurnTail, ErrBranchAnchorNotFound))
}

func TestPlanBranchPrefixIsIndependentCopy(t *testing.T) {
	history := branchTestHistory(t, []types.Message{
		*types.NewUserMessage("first"),
		*types.NewAssistantMessage("a1"),
		*types.NewUserMessage("second"),
		*types.NewAssistantMessage("a2"),
	})
	ids := messageIDList(history)

	plan, err := PlanBranch(history, BranchRequest{AnchorMessageID: ids[1], IncludeAnchor: true})
	require.NoError(t, err)
	require.Len(t, plan.Prefix, 2)

	plan.Prefix[0].Content = "mutated"
	plan.Prefix[0].Metadata.Set(types.MetadataKeyMessageID, "msg_mutated")

	require.Equal(t, "first", history[0].Content, "prefix must be a deep copy of the source history")
	require.Equal(t, ids[0], types.MessageID(history[0]))
}

func TestApplyForkLineageRecordsParentAndRoot(t *testing.T) {
	parent := NewSession("branch-unit-user")
	child := NewSession("branch-unit-user")
	createdAt := time.Date(2026, 9, 14, 10, 30, 0, 0, time.UTC)
	parent.UpdateTitle("Source Title")

	child.ApplyForkLineage(parent, "msg_anchor", createdAt)

	require.Equal(t, parent.ID, contextStringValue(child.Metadata.Context, ContextForkParentSessionID))
	require.Equal(t, parent.ID, contextStringValue(child.Metadata.Context, ContextForkRootSessionID))
	require.Equal(t, "msg_anchor", contextStringValue(child.Metadata.Context, ContextForkSourceMessageID))
	require.Equal(t, "Source Title", contextStringValue(child.Metadata.Context, ContextForkOriginTitle))
	require.Equal(t, createdAt.Format(time.RFC3339), contextStringValue(child.Metadata.Context, ContextForkCreatedAt))
	require.Equal(t, parent.ID, ForkRootSessionID(child))

	// Chained forks keep the original root.
	grandchild := NewSession("branch-unit-user")
	grandchild.ApplyForkLineage(child, "", createdAt)
	require.Equal(t, child.ID, contextStringValue(grandchild.Metadata.Context, ContextForkParentSessionID))
	require.Equal(t, parent.ID, contextStringValue(grandchild.Metadata.Context, ContextForkRootSessionID))
	require.Equal(t, "", contextStringValue(grandchild.Metadata.Context, ContextForkSourceMessageID),
		"a whole-session fork omits fork_source_message_id")
	// child has no title of its own, so its display title falls back to its id.
	require.Equal(t, child.ID, contextStringValue(grandchild.Metadata.Context, ContextForkOriginTitle))

	// An untitled source also falls back to its id for fork_origin_title.
	untitled := NewSession("branch-unit-user")
	fallback := NewSession("branch-unit-user")
	fallback.ApplyForkLineage(untitled, "", createdAt)
	require.Empty(t, untitled.Metadata.Title, "fixture source must stay untitled")
	require.Equal(t, untitled.ID, contextStringValue(fallback.Metadata.Context, ContextForkOriginTitle))

	// The lineage never reuses the agent-control parent key.
	_, isAgentChild := child.Metadata.Context["agent_parent_session_id"]
	require.False(t, isAgentChild)
}
