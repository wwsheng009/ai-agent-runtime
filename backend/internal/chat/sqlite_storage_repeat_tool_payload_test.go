package chat

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// Live-fire regression (2026-10-17): a second turn whose tool result repeats an
// earlier one byte-for-byte (same query, same response pool) mis-anchored the
// backward identity walk on the stored first-turn rows, so the new turn's
// tool_calls assistant + tool result were silently dropped from the canonical
// transcript (1 row stored instead of 3). The walk now consumes substance
// quotas, so a canonical row backs at most one history message — identical
// content in a NEW turn is appended, not collapsed.
func TestUpdateAppendsTurnWhoseToolResultRepeatsStoredRow(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)
	session := NewSession("repeat-tool-turn-user")
	session.AddMessage(*types.NewUserMessage("search once"))
	assistant := *types.NewAssistantMessage("")
	assistant.ToolCalls = []types.ToolCall{{
		ID:   "call_first",
		Type: "function",
		Name: "web_search",
	}}
	session.AddMessage(assistant)
	session.AddMessage(*types.NewToolMessage("call_first", "identical tool payload"))

	require.NoError(t, store.Save(ctx, session))
	before := canonicalRowCount(t, store, session.ID)

	// Second turn: rebuilt identities (fresh ids), same tool payload verbatim.
	loaded, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	durable := append(loaded.GetMessages(),
		*types.NewUserMessage("search again"))
	second := *types.NewAssistantMessage("")
	second.ToolCalls = []types.ToolCall{{
		ID:   "call_second",
		Type: "function",
		Name: "web_search",
	}}
	durable = append(durable, second)
	durable = append(durable, *types.NewToolMessage("call_second", "identical tool payload"))
	durable = append(durable, *types.NewAssistantMessage("final answer"))
	loaded.ReplaceHistory(durable)
	require.NoError(t, store.Update(ctx, loaded))

	require.Equal(t, before+4, canonicalRowCount(t, store, session.ID),
		"a new turn repeating a stored tool payload must append all its rows")
	require.Equal(t, []string{"search again", "", "identical tool payload", "final answer"},
		renderedTail(t, store, session.ID, 4))
}

// The quota must not over-append either: re-persisting the same (reissued)
// window stays a no-op — the stored rows back the window exactly once.
func TestUpdateRepeatedToolPayloadWindowPersistsIdempotently(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)
	session := NewSession("repeat-tool-idempotent-user")
	session.AddMessage(*types.NewUserMessage("q1"))
	assistant := *types.NewAssistantMessage("")
	assistant.ToolCalls = []types.ToolCall{{
		ID:   "call_a",
		Type: "function",
		Name: "web_search",
	}}
	session.AddMessage(assistant)
	session.AddMessage(*types.NewToolMessage("call_a", "same payload"))

	require.NoError(t, store.Save(ctx, session))
	loaded, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	before := canonicalRowCount(t, store, session.ID)

	loaded.ReplaceHistory(reissueMessageIDs(loaded.GetMessages()))
	require.NoError(t, store.Update(ctx, loaded))
	require.Equal(t, before, canonicalRowCount(t, store, session.ID),
		"re-persisting an unchanged reissued window must not duplicate rows")
}
