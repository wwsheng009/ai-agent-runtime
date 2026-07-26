package chat

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestBuildBacktrackTombstoneCollectsRemovedIDs(t *testing.T) {
	u1 := types.NewUserMessage("first")
	a1 := types.NewAssistantMessage("a1")
	u2 := types.NewUserMessage("second")
	a2 := types.NewAssistantMessage("a2")
	messages := []types.Message{*u1, *a1, *u2, *a2}
	_ = types.EnsureHistoryMessageIdentities(messages)

	plan, err := planBacktrack(messages, BacktrackRequest{UserTurnIndex: intPtr(1)})
	require.NoError(t, err)

	removed := messages[plan.PrefixLen:]
	tomb := buildBacktrackTombstone("sess-1", plan, removed, len(messages))
	require.NotNil(t, tomb)
	require.Equal(t, "sess-1", tomb.SessionID)
	require.Equal(t, BacktrackReasonUserTurn, tomb.Reason)
	require.Equal(t, BacktrackModeConversation, tomb.Mode)
	require.Equal(t, 2, tomb.TruncatedToMessageCount)
	require.Equal(t, 2, tomb.RemovedMessageCount)
	require.Equal(t, 1, tomb.RemovedUserTurns)
	require.Equal(t, 4, tomb.PriorMessageCount)
	require.Equal(t, types.MessageID(messages[2]), tomb.MessageID)
	require.Contains(t, tomb.RemovedMessageIDs, types.MessageID(messages[2]))
	require.Contains(t, tomb.RemovedMessageIDs, types.MessageID(messages[3]))
	require.Contains(t, tomb.RemovedTurnIDs, types.TurnID(messages[2]))
	require.True(t, stringsHasPrefix(tomb.ID, "bt_"))
	require.False(t, tomb.CreatedAt.IsZero())
}

func TestBuildBacktrackTombstoneSkipsCodeOnlyAndNoop(t *testing.T) {
	messages := []types.Message{
		*types.NewUserMessage("first"),
		*types.NewAssistantMessage("a1"),
	}
	plan, err := planBacktrack(messages, BacktrackRequest{
		UserTurnIndex: intPtr(0),
		Mode:          BacktrackModeCode,
	})
	require.NoError(t, err)
	require.Nil(t, buildBacktrackTombstone("s", plan, nil, 2))

	// IncludeAnchor on the last user turn with no trailing messages: nothing removed.
	lastOnly := []types.Message{
		*types.NewUserMessage("only"),
	}
	planKeep, err := planBacktrack(lastOnly, BacktrackRequest{
		UserTurnIndex: intPtr(0),
		IncludeAnchor: true,
	})
	require.NoError(t, err)
	require.Equal(t, 0, planKeep.RemovedMessageCount)
	require.Nil(t, buildBacktrackTombstone("s", planKeep, nil, 1))
}

func TestAppendBacktrackTombstoneRingBufferAndRoundTrip(t *testing.T) {
	session := NewSession("user-1")
	for i := 0; i < MaxBacktrackAuditLogEntries+3; i++ {
		ok := AppendBacktrackTombstone(session, &BacktrackTombstone{
			ID:                      "bt_manual_" + itoa(i),
			CreatedAt:               time.Unix(int64(i+1), 0).UTC(),
			Mode:                    BacktrackModeConversation,
			UserTurnIndex:           i,
			MessageIndex:            i * 2,
			TruncatedToMessageCount: i,
			RemovedMessageCount:     1,
			RemovedUserTurns:        1,
			PriorMessageCount:       i + 1,
		})
		require.True(t, ok)
	}

	entries := ListBacktrackTombstones(session)
	require.Len(t, entries, MaxBacktrackAuditLogEntries)
	require.Equal(t, "bt_manual_3", entries[0].ID)
	require.Equal(t, "bt_manual_22", entries[len(entries)-1].ID)

	// JSON round-trip via Metadata.Context (as persistence would).
	raw, err := json.Marshal(session.Metadata.Context[ContextBacktrackAuditLog])
	require.NoError(t, err)
	var decoded interface{}
	require.NoError(t, json.Unmarshal(raw, &decoded))
	session2 := NewSession("user-1")
	session2.SetContext(ContextBacktrackAuditLog, decoded)
	restored := ListBacktrackTombstones(session2)
	require.Len(t, restored, MaxBacktrackAuditLogEntries)
	require.Equal(t, entries[0].ID, restored[0].ID)
	require.Equal(t, entries[len(entries)-1].UserTurnIndex, restored[len(restored)-1].UserTurnIndex)

	latest := LatestBacktrackTombstone(session2)
	require.NotNil(t, latest)
	require.Equal(t, "bt_manual_22", latest.ID)
}

func TestSessionActorBacktrackWritesTombstoneAndEventPayload(t *testing.T) {
	ctx := context.Background()
	first := types.NewUserMessage("first")
	second := types.NewUserMessage("second")
	third := types.NewUserMessage("third")
	seed := []types.Message{
		*first,
		*types.NewAssistantMessage("a1"),
		*second,
		*types.NewAssistantMessage("a2"),
		*third,
		*types.NewAssistantMessage("a3"),
	}
	_ = types.EnsureHistoryMessageIdentities(seed)

	actor, storage, session := newBacktrackTestActor(t, seed)

	// Preview must not write a tombstone.
	preview, err := actor.PreviewBacktrack(ctx, BacktrackRequest{UserTurnIndex: intPtr(1)})
	require.NoError(t, err)
	require.Nil(t, preview.Tombstone)
	preloaded, err := storage.Load(ctx, session.ID)
	require.NoError(t, err)
	require.Empty(t, ListBacktrackTombstones(preloaded))

	result, _, err := actor.Backtrack(ctx, BacktrackRequest{
		UserTurnIndex: intPtr(1),
		Mode:          BacktrackModeConversation,
	})
	require.NoError(t, err)
	require.NotNil(t, result.Tombstone)
	require.Equal(t, 2, result.Tombstone.TruncatedToMessageCount)
	require.Equal(t, 4, result.Tombstone.RemovedMessageCount)
	require.Equal(t, 2, result.Tombstone.RemovedUserTurns)
	require.Equal(t, types.MessageID(seed[2]), result.Tombstone.MessageID)
	require.Contains(t, result.Tombstone.RemovedMessageIDs, types.MessageID(seed[2]))
	require.Contains(t, result.Tombstone.RemovedTurnIDs, types.TurnID(seed[2]))

	updated, err := storage.Load(ctx, session.ID)
	require.NoError(t, err)
	entries := ListBacktrackTombstones(updated)
	require.Len(t, entries, 1)
	require.Equal(t, result.Tombstone.ID, entries[0].ID)
	require.Equal(t, 6, entries[0].PriorMessageCount)

	audit, err := actor.ListBacktrackAudit(ctx)
	require.NoError(t, err)
	require.Len(t, audit, 1)
	require.Equal(t, result.Tombstone.ID, audit[0].ID)

	// Finished event payload includes tombstone summary.
	require.NotNil(t, actor.eventStore)
	events, err := actor.eventStore.ListEvents(ctx, session.ID, 0, 50)
	require.NoError(t, err)
	var finished map[string]interface{}
	for _, event := range events {
		if event.Type == EventBacktrackFinished {
			finished = event.Payload
			break
		}
	}
	require.NotNil(t, finished)
	require.Equal(t, result.Tombstone.ID, finished["tombstone_id"])
	tombPayload, ok := finished["tombstone"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, result.Tombstone.ID, tombPayload["id"])
	require.EqualValues(t, 4, tombPayload["removed_message_count"])
}

func TestSessionActorBacktrackCodeOnlySkipsTombstone(t *testing.T) {
	ctx := context.Background()
	actor, storage, session, _ := newBacktrackTestActorWithCheckpoints(t, []types.Message{
		*types.NewUserMessage("first"),
		*types.NewAssistantMessage("a1"),
		*types.NewUserMessage("second"),
	})

	result, _, err := actor.Backtrack(ctx, BacktrackRequest{
		UserTurnIndex: intPtr(1),
		Mode:          BacktrackModeCode,
	})
	require.NoError(t, err)
	require.Nil(t, result.Tombstone)

	updated, err := storage.Load(ctx, session.ID)
	require.NoError(t, err)
	require.Empty(t, ListBacktrackTombstones(updated))
	// History remains intact for code-only.
	require.Len(t, updated.GetMessages(), 3)
}

func stringsHasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
