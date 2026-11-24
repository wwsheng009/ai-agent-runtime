package memorystore

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTrustDefaultsAndPromotionGuard(t *testing.T) {
	store := newScopedTestStore(t, "g5-trust", "")
	ctx := context.Background()
	manual, err := store.Append(AppendNoteOptions{Text: "manual policy note", Kind: "reference"})
	require.NoError(t, err)
	record, err := store.GetRecord(ctx, manual.ID)
	require.NoError(t, err)
	require.Equal(t, TrustUserExplicit, record.TrustLevel)
	require.NotZero(t, record.TrustedAt)

	episode, err := store.CreateEpisode(ctx, EpisodeInput{
		TaskID: "trust-task", SessionID: "trust-session", Problem: "trust episode",
		Outcome: EpisodeOutcomeSuccess, SourceEventIDs: []string{"trust-event"},
		EndedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	episodeRecord, err := store.GetRecord(ctx, episode.MemoryID)
	require.NoError(t, err)
	require.Equal(t, TrustAgentInference, episodeRecord.TrustLevel)

	db, err := store.ensureV2()
	require.NoError(t, err)
	now := time.Now().UTC()
	untrusted := Record{
		ID: "mem_untrusted", Scope: ScopeProject, ScopeKind: ScopeProject,
		ScopeKey: store.ProjectKey(), ProjectKey: store.ProjectKey(),
		Kind: "workflow", Status: StatusCandidate, Text: "untrusted workflow",
		NormalizedText: "untrusted workflow", SourceKind: "web", SourceType: "web",
		SourceTrust: "untrusted_external", Revision: 1, CreatedAt: now, UpdatedAt: now,
		Freshness: FreshnessFresh, LastVerifiedAt: now,
	}
	require.NoError(t, store.insertRecordTx(ctx, db, untrusted, "web", ""))
	untrustedRecord, err := store.GetRecord(ctx, untrusted.ID)
	require.NoError(t, err)
	require.Equal(t, TrustUntrustedExternal, untrustedRecord.TrustLevel)
	_, err = store.PromoteCandidate(ctx, untrusted.ID)
	require.ErrorContains(t, err, "untrusted external")
}

func TestEntityAutoLinkAndEntityOnlySearch(t *testing.T) {
	store := newScopedTestStore(t, "g5-entities", "")
	ctx := context.Background()
	record, err := store.Append(AppendNoteOptions{
		Text: "See E_INVALID_ARGUMENT in backend/internal/api/memory.go for v1.2.3",
	})
	require.NoError(t, err)
	entities, err := store.ListRecordEntities(ctx, record.ID)
	require.NoError(t, err)
	types := make(map[string]bool)
	for _, entity := range entities {
		types[entity.EntityType] = true
	}
	require.True(t, types["error_code"])
	require.True(t, types["path"])
	require.True(t, types["version"])

	aliasRecord, err := store.Append(AppendNoteOptions{Text: "record without the alias token"})
	require.NoError(t, err)
	_, err = store.LinkRecordEntity(ctx, aliasRecord.ID, EntityLink{
		CanonicalName: "AliasOnlyEntity", EntityType: "project",
		Aliases: []string{"AliasOnlyEntity"}, Relation: "mentions", Confidence: 0.9,
	})
	require.NoError(t, err)
	result, err := store.SearchRecords(ctx, RecordSearchQuery{Text: "AliasOnlyEntity", Limit: 10})
	require.NoError(t, err)
	require.Equal(t, "entity", result.Backend)
	require.Len(t, result.Items, 1)
	require.Equal(t, aliasRecord.ID, result.Items[0].Record.ID)
	require.Equal(t, "entity", result.Items[0].MatchSource)
	require.Greater(t, result.Items[0].Score, 0.0)
}

func TestFeedbackChangesScoreAndMarksStale(t *testing.T) {
	store := newScopedTestStore(t, "g5-feedback", "")
	ctx := context.Background()
	note, err := store.Append(AppendNoteOptions{Text: "feedback target memory"})
	require.NoError(t, err)
	before, err := store.GetRecord(ctx, note.ID)
	require.NoError(t, err)

	helpful, err := store.RecordFeedback(ctx, FeedbackInput{
		MemoryID: note.ID, SessionID: "feedback-session", Kind: FeedbackHelpful,
		Comment: "useful", Actor: "user:test",
	})
	require.NoError(t, err)
	require.Equal(t, FeedbackHelpful, helpful.Kind)
	afterHelpful, err := store.GetRecord(ctx, note.ID)
	require.NoError(t, err)
	require.Greater(t, afterHelpful.FeedbackScore, before.FeedbackScore)
	require.Equal(t, before.Revision, afterHelpful.Revision, "helpful feedback must not rewrite the record")

	incorrect, err := store.RecordFeedback(ctx, FeedbackInput{
		MemoryID: note.ID, SessionID: "feedback-session", Kind: FeedbackIncorrect,
		Comment: "wrong fact", Actor: "user:test",
	})
	require.NoError(t, err)
	require.Equal(t, FeedbackIncorrect, incorrect.Kind)
	stale, err := store.GetRecord(ctx, note.ID)
	require.NoError(t, err)
	require.Equal(t, FreshnessStale, stale.Freshness)
	require.Equal(t, "feedback:incorrect", stale.StaleReason)
	require.Greater(t, stale.Revision, afterHelpful.Revision)

	list, err := store.ListFeedback(ctx, note.ID, 10)
	require.NoError(t, err)
	require.Len(t, list, 2)
}
