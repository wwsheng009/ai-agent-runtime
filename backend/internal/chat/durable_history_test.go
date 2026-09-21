package chat

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// A replayed auto-continuation prompt is request-scoped by contract: storing it
// would show up as a second copy of the user's own message.
func TestPruneRequestScopedHistoryDropsTransientPrompt(t *testing.T) {
	history := []types.Message{
		*types.NewUserMessage("检查为什么写入侧会有重复项"),
		transientPromptMessage("检查为什么写入侧会有重复项"),
		*types.NewAssistantMessage("先看写入链路"),
	}

	durable := PruneRequestScopedHistory(history)
	require.Len(t, durable, 2)
	require.Equal(t, "user", durable[0].Role)
	require.Empty(t, durable[1].Metadata[types.MetadataKeyTransientPrompt])
	require.Equal(t, "先看写入链路", durable[1].Content)
}

// Context layers are rebuilt for every provider request with fresh identities.
// The first copy stays (prompt assembly reuses a layer that is already stored);
// the surplus copies must not be appended as new canonical rows.
func TestPruneRequestScopedHistoryCollapsesRebuiltContextLayer(t *testing.T) {
	history := []types.Message{
		*types.NewUserMessage("检查为什么写入侧会有重复项"),
		stagedContextMessage("fact_ledger", "<fact_ledger>facts-v1</fact_ledger>"),
		*types.NewAssistantMessage("先看写入链路"),
		stagedContextMessage("fact_ledger", "<fact_ledger>facts-v1</fact_ledger>"),
		stagedContextMessage("fact_ledger", "<fact_ledger>facts-v1</fact_ledger>"),
		*types.NewAssistantMessage("已定位写入侧"),
	}

	durable := PruneRequestScopedHistory(history)
	require.Len(t, durable, 4)
	require.Equal(t, "<fact_ledger>facts-v1</fact_ledger>", durable[1].Content)
	require.Equal(t, "先看写入链路", durable[2].Content)

	// A changed layer carries new information and is kept, while a repeated copy
	// of an already stored layer is still collapsed.
	changed := append([]types.Message{}, durable...)
	changed = append(changed,
		stagedContextMessage("fact_ledger", "<fact_ledger>facts-v2</fact_ledger>"),
		stagedContextMessage("fact_ledger", "<fact_ledger>facts-v1</fact_ledger>"),
	)
	pruned := PruneRequestScopedHistory(changed)
	require.Len(t, pruned, 5)
	require.Equal(t, "<fact_ledger>facts-v2</fact_ledger>", pruned[4].Content)
}

// The dedupe is scoped to request-scoped layers: repeating conversational turns
// are legitimate history and must never be collapsed.
func TestPruneRequestScopedHistoryKeepsRepeatedTurns(t *testing.T) {
	history := []types.Message{
		*types.NewUserMessage("继续"),
		*types.NewAssistantMessage("继续处理"),
		*types.NewUserMessage("继续"),
		*types.NewAssistantMessage("继续处理"),
	}
	require.Nil(t, PruneRequestScopedHistory(history), "a durable transcript must report no change")
}

func TestSessionPruneRequestScopedHistory(t *testing.T) {
	session := NewSession("prune-user")
	session.AddMessage(*types.NewUserMessage("检查为什么写入侧会有重复项"))
	session.AddMessage(stagedContextMessage("recall", "<recall>old</recall>"))
	session.AddMessage(stagedContextMessage("recall", "<recall>old</recall>"))

	require.Equal(t, 1, session.PruneRequestScopedHistory())
	require.Len(t, session.GetMessages(), 2)
	require.Zero(t, session.PruneRequestScopedHistory(), "pruning is idempotent")
}

// A rebuilt transcript arrives without metadata (compaction, streaming
// assembly, request prefixes). ReplaceHistory must carry the durable identities
// forward, otherwise the storage layer sees a brand-new turn on every
// checkpoint and appends a duplicate copy.
func TestReplaceHistoryKeepsAssistantIdentitiesForRebuiltTranscript(t *testing.T) {
	session := NewSession("rebuild-user")
	session.AddMessage(*types.NewUserMessage("检查为什么写入侧会有重复项"))
	session.AddMessage(*types.NewAssistantMessage("先看写入链路"))
	assistantID := types.MessageID(session.History[1])
	assistantTurnID := types.TurnID(session.History[1])
	require.NotEmpty(t, assistantID)

	session.ReplaceHistory(rebuildMessagesWithoutIdentities(session.GetMessages()))

	require.Equal(t, assistantID, types.MessageID(session.History[1]))
	require.Equal(t, assistantTurnID, types.TurnID(session.History[1]))
	require.NotEmpty(t, types.MessageID(session.History[0]), "the user turn still gets a durable identity")
	require.NotEqual(t, types.MessageID(session.History[0]), types.MessageID(session.History[1]))
}

// Regression test for the duplicated /history rows: a long turn persists a
// rebuilt transcript every checkpoint. Neither the turns that are already stored
// nor the request-scoped context layer may be appended a second time.
func TestUpdateKeepsTranscriptStableAcrossRebuiltCheckpoints(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)
	session := NewSession("dup-check-user")
	session.AddMessage(*types.NewUserMessage("检查为什么写入侧会有重复项"))
	session.AddMessage(*types.NewAssistantMessage("先看写入链路"))
	require.NoError(t, store.Save(ctx, session))
	require.Equal(t, 2, canonicalRowCount(t, store, session.ID))

	ledger := stagedContextMessage("fact_ledger", "<fact_ledger>facts-v1</fact_ledger>")
	loaded, err := store.Load(ctx, session.ID)
	require.NoError(t, err)

	// Checkpoint 1: the transcript was rebuilt without the stored identities,
	// the new turn was appended and the context layer was injected again.
	rebuilt := append(rebuildMessagesWithoutIdentities(loaded.GetMessages()),
		*types.NewUserMessage("继续"),
		*types.NewAssistantMessage("已定位"),
		ledger,
	)
	loaded.ReplaceHistory(rebuilt)
	require.Zero(t, loaded.PruneRequestScopedHistory(), "the first copy of a context layer is durable")
	require.NoError(t, store.Update(ctx, loaded))
	require.Equal(t, 5, canonicalRowCount(t, store, session.ID))

	// Checkpoint 2: same shape, one more turn, the identical layer again. Only
	// the genuinely new turn may be appended.
	reloaded, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	rebuilt = append(rebuildMessagesWithoutIdentities(reloaded.GetMessages()),
		*types.NewUserMessage("继续检查"),
		*types.NewAssistantMessage("在看"),
		ledger,
	)
	reloaded.ReplaceHistory(rebuilt)
	require.Equal(t, 1, reloaded.PruneRequestScopedHistory(),
		"the rebuilt copy of an unchanged context layer is surplus")
	require.NoError(t, store.Update(ctx, reloaded))

	require.Equal(t, 7, canonicalRowCount(t, store, session.ID),
		"a checkpoint must append the new turn once, not the stored tail again")
	require.Equal(t,
		[]string{
			"检查为什么写入侧会有重复项",
			"先看写入链路",
			"继续",
			"已定位",
			"<fact_ledger>facts-v1</fact_ledger>",
			"继续检查",
			"在看",
		},
		renderedTail(t, store, session.ID, 7),
		"every turn and every context layer must be stored exactly once")
}

func transientPromptMessage(content string) types.Message {
	message := *types.NewUserMessage(content)
	message.Metadata.Set(types.MetadataKeyTransientPrompt, true)
	return message
}

func stagedContextMessage(stage, content string) types.Message {
	message := *types.NewSystemMessage(content)
	message.Metadata.Set(types.MetadataKeyContextStage, stage)
	return message
}

// rebuildMessagesWithoutIdentities mimics an in-memory copy rebuilt by the ReAct
// loop or a compaction pass: same renderable content, no identity metadata.
func rebuildMessagesWithoutIdentities(messages []types.Message) []types.Message {
	rebuilt := make([]types.Message, 0, len(messages))
	for index := range messages {
		clone := *messages[index].Clone()
		if clone.Metadata != nil {
			delete(clone.Metadata, types.MetadataKeyMessageID)
			delete(clone.Metadata, types.MetadataKeyTurnID)
		}
		rebuilt = append(rebuilt, clone)
	}
	return rebuilt
}

// Regression test for the duplicated user prompts of compacted sessions. Real
// transcript rows 4089/4091/4092 of session_20260920192824_cQCsPhUr stored the
// same prompt three times under three turn ids: a pre-turn compaction rebuilds
// the transcript without identities, re-emits the prompt of the turn that is
// still being answered, and the storage layer appended the fresh copy as a new
// canonical row. The rebuilt copy must anchor on the stored open turn instead.
func TestUpdateDoesNotAppendRebuiltPromptOfOpenTurn(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)
	session := NewSession("open-turn-user")
	session.AddMessage(*types.NewUserMessage("L4的兜底大小是多少"))
	correction := *types.NewAssistantMessage("Authoritative context correction: {\"run\":{\"status\":\"running\"}}")
	correction.Metadata.Set(types.MetadataKeyContextStage, "correction")
	session.AddMessage(correction)
	require.NoError(t, store.Save(ctx, session))
	require.Equal(t, 2, canonicalRowCount(t, store, session.ID))

	loaded, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	rebuilt := append(rebuildMessagesWithoutIdentities(loaded.GetMessages()),
		*types.NewUserMessage("L4的兜底大小是多少"),
	)
	loaded.ReplaceHistory(rebuilt)
	require.NoError(t, store.Update(ctx, loaded))

	require.Equal(t, 2, canonicalRowCount(t, store, session.ID),
		"re-emitting the prompt of the open turn must not append a second canonical row")
	require.Equal(t,
		[]string{
			"L4的兜底大小是多少",
			"Authoritative context correction: {\"run\":{\"status\":\"running\"}}",
		},
		renderedTail(t, store, session.ID, 2),
		"the request-scoped correction layer does not answer the turn it follows")
}

// The open-turn anchor must not swallow a genuine repetition: once the first
// prompt was answered, the same text sent again is a new turn and is stored.
func TestUpdateAppendsRepeatedPromptAfterReply(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)
	session := NewSession("repeat-user")
	session.AddMessage(*types.NewUserMessage("继续"))
	session.AddMessage(*types.NewAssistantMessage("继续处理"))
	require.NoError(t, store.Save(ctx, session))

	loaded, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	rebuilt := append(rebuildMessagesWithoutIdentities(loaded.GetMessages()),
		*types.NewUserMessage("继续"),
	)
	loaded.ReplaceHistory(rebuilt)
	require.NoError(t, store.Update(ctx, loaded))

	require.Equal(t, 3, canonicalRowCount(t, store, session.ID),
		"a repeat of the same text after a reply is a new turn")
}
