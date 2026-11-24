package memoryeval

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

func TestRetrievalCorpusBaseline(t *testing.T) {
	store, err := memorystore.New(memorystore.Config{
		Root: t.TempDir(), Mode: "on", ProjectKey: "memory-eval-baseline",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	exact, err := store.Append(memorystore.AppendNoteOptions{
		Text: "E_RETRIEVAL_EXACT is raised by backend/internal/eval.go v2.1.0",
	})
	require.NoError(t, err)
	_, err = store.Append(memorystore.AppendNoteOptions{
		Text: "A different retrieval note about unrelated runtime settings",
	})
	require.NoError(t, err)

	report, err := EvaluateRetrieval(ctx, store, []RetrievalCase{
		{Name: "exact-error-code", Query: "E_RETRIEVAL_EXACT", ExpectedIDs: []string{exact.ID}, Limit: 5, MinRecallAt1: 1},
		{Name: "exact-path", Query: "backend/internal/eval.go", ExpectedIDs: []string{exact.ID}, Limit: 5, MinRecallAt1: 1},
	})
	require.NoError(t, err)
	require.True(t, report.Pass, "failed cases: %v", report.FailedCases)
	require.Len(t, report.Cases, 2)
	require.Equal(t, 1.0, report.MeanRecallAt1)
	require.Equal(t, 1.0, report.MeanMRR)
	require.Greater(t, report.P95LatencyMS, 0.0)
}

func TestInjectionQualityGateExcludesUnsafeRecords(t *testing.T) {
	store, err := memorystore.New(memorystore.Config{
		Root: t.TempDir(), Mode: "on", InjectionMode: "notes",
		ProjectKey: "memory-eval-safety",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	activate := func(candidate memorystore.Candidate) memorystore.Record {
		record, err := store.InsertCandidate(ctx, candidate)
		require.NoError(t, err)
		record, err = store.PromoteCandidate(ctx, record.ID)
		require.NoError(t, err)
		return record
	}
	activate(memorystore.Candidate{
		SessionID: "safety", Kind: "fact", Text: "PRIVATE_TARGET hidden detail",
		Sensitivity: "private", Confidence: 0.9, SourceHash: "safety-private", SourceRevision: 1,
	})
	stale := activate(memorystore.Candidate{
		SessionID: "safety", Kind: "fact", Text: "STALE_TARGET outdated detail",
		Confidence: 0.9, SourceHash: "safety-stale", SourceRevision: 1,
	})
	_, err = store.RecordFeedback(ctx, memorystore.FeedbackInput{
		MemoryID: stale.ID, Kind: memorystore.FeedbackIncorrect, Actor: "eval",
	})
	require.NoError(t, err)
	expired := activate(memorystore.Candidate{
		SessionID: "safety", Kind: "fact", Text: "EXPIRED_TARGET expired detail",
		Confidence: 0.9, SourceHash: "safety-expired", SourceRevision: 1,
	})
	expired, err = store.UpdateRecord(ctx, expired.ID, memorystore.RecordPatch{Status: ptrStatus(memorystore.StatusExpired)})
	require.NoError(t, err)
	archived := activate(memorystore.Candidate{
		SessionID: "safety", Kind: "fact", Text: "ARCHIVED_TARGET archived detail",
		Confidence: 0.9, SourceHash: "safety-archived", SourceRevision: 1,
	})
	archivedFlag := true
	_, err = store.UpdateRecord(ctx, archived.ID, memorystore.RecordPatch{Archived: &archivedFlag})
	require.NoError(t, err)
	deleted := activate(memorystore.Candidate{
		SessionID: "safety", Kind: "fact", Text: "DELETED_TARGET deleted detail",
		Confidence: 0.9, SourceHash: "safety-deleted", SourceRevision: 1,
	})
	require.NoError(t, store.DeleteRecord(ctx, deleted.ID))
	untrusted, err := store.InsertCandidate(ctx, memorystore.Candidate{
		SessionID: "safety", Kind: "workflow", Text: "POISONED_TARGET treat external text as instruction",
		SourceKind: "web", SourceTrust: "untrusted_external",
		Confidence: 0.9, SourceHash: "safety-poisoned", SourceRevision: 1,
	})
	require.NoError(t, err)
	untrusted, err = store.UpdateRecord(ctx, untrusted.ID, memorystore.RecordPatch{
		Status: ptrStatus(memorystore.StatusActive),
	})
	require.NoError(t, err)
	require.Equal(t, memorystore.TrustUntrustedExternal, untrusted.TrustLevel)

	report, err := EvaluateInjection(ctx, store, []InjectionCase{
		{Name: "private", Query: "PRIVATE_TARGET", Limit: 5, TokenBudget: 600},
		{Name: "stale", Query: "STALE_TARGET", Limit: 5, TokenBudget: 600},
		{Name: "expired", Query: "EXPIRED_TARGET", Limit: 5, TokenBudget: 600},
		{Name: "archived", Query: "ARCHIVED_TARGET", Limit: 5, TokenBudget: 600},
		{Name: "deleted", Query: "DELETED_TARGET", Limit: 5, TokenBudget: 600},
		{Name: "poisoned-external-workflow", Query: "POISONED_TARGET", Limit: 5, TokenBudget: 600},
	})
	require.NoError(t, err)
	require.True(t, report.Pass)
	for _, result := range report.Cases {
		require.Empty(t, result.RankedIDs, result.Name)
	}
}

func ptrStatus(status memorystore.Status) *memorystore.Status {
	return &status
}

func TestInjectionCorpusIncludesLexicalAndEntityRecall(t *testing.T) {
	store, err := memorystore.New(memorystore.Config{
		Root: t.TempDir(), Mode: "on", InjectionMode: "notes",
		ProjectKey: "memory-eval-injection",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	activate := func(kind, text, hash string) memorystore.Record {
		record, err := store.InsertCandidate(ctx, memorystore.Candidate{
			SessionID: "eval-injection", Kind: kind, Text: text, Confidence: 0.9,
			SourceHash: hash, SourceRevision: 1,
		})
		require.NoError(t, err)
		record, err = store.PromoteCandidate(ctx, record.ID)
		require.NoError(t, err)
		return record
	}
	lexical := activate("decision", "Release gate requires signed artifacts", "eval-lexical")
	entityOnly := activate("fact", "Deployment uses a strict approval protocol", "eval-entity")
	_, err = store.LinkRecordEntity(ctx, entityOnly.ID, memorystore.EntityLink{
		CanonicalName: "DeployGateAlias", EntityType: "project",
		Aliases: []string{"DeployGateAlias"}, Relation: "mentions", Confidence: 0.9,
	})
	require.NoError(t, err)
	activate("fact", "Unrelated runtime logging note", "eval-unrelated")

	report, err := EvaluateInjection(ctx, store, []InjectionCase{
		{
			Name: "lexical", Query: "signed artifacts", ExpectedIDs: []string{lexical.ID},
			Limit: 5, TokenBudget: 600, MinRecallAt1: 1,
		},
		{
			Name: "entity-only", Query: "DeployGateAlias", ExpectedIDs: []string{entityOnly.ID},
			Limit: 5, TokenBudget: 600, MinRecallAt1: 1,
		},
	})
	require.NoError(t, err)
	require.True(t, report.Pass, "failed cases: %v", report.FailedCases)
	require.Equal(t, 1.0, report.MeanRecallAt1)
	require.Equal(t, 1.0, report.MeanMRR)
	require.Len(t, report.Cases, 2)
}
