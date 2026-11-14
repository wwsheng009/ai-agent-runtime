package memoryruntime

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

type sessionEndContextExtractor struct {
	deadlineRemaining time.Duration
}

func (e *sessionEndContextExtractor) Extract(ctx context.Context, session SessionRef, transcript Transcript) (ExtractionResult, error) {
	if deadline, ok := ctx.Deadline(); ok {
		e.deadlineRemaining = time.Until(deadline)
	}
	return ExtractionResult{Candidates: []Candidate{{
		Kind: "decision", Text: "Keep the session-end extraction durable.",
		Confidence: 0.95, SourceMessageIDs: []string{transcript.Messages[len(transcript.Messages)-1].ID},
	}}}, nil
}

type sessionEndContextSummarizer struct {
	deadlineRemaining time.Duration
}

func (s *sessionEndContextSummarizer) SummarizeProject(ctx context.Context, input ProjectSummaryInput) (ProjectSummaryOutput, error) {
	if deadline, ok := ctx.Deadline(); ok {
		s.deadlineRemaining = time.Until(deadline)
	}
	if len(input.Records) == 0 {
		return ProjectSummaryOutput{}, fmt.Errorf("missing records")
	}
	return ProjectSummaryOutput{
		Text:  fmt.Sprintf("## Project decisions\n- Keep the session-end extraction durable. (id=%s)", input.Records[0].ID),
		Model: "offline",
	}, nil
}

func TestSessionEndPipelineUsesIndependentBoundedPhaseBudgets(t *testing.T) {
	now := time.Now().UTC()
	source := &fakeSource{
		sessions: []SessionRef{{
			ID: "pipeline-session", WorkspacePath: "/workspace", State: "closed",
			LastActivityAt: now.Add(-time.Hour), CanonicalCount: 2,
		}},
		transcripts: map[string]Transcript{"pipeline-session": validTranscript("pipeline-session", "pipeline-hash")},
	}
	extractor := &sessionEndContextExtractor{}
	runtime, store := newRuntimeForTest(t, "on", source, &fakeExtractor{})
	runtime.Extractor = extractor
	summarizer := &sessionEndContextSummarizer{}
	runtime.Config.Summarizer = summarizer
	runtime.Config.ConsolidationDisabled = false
	runtime.Config.ConsolidationInterval = time.Hour
	runtime.Config.MaxConsolidationRecords = 20
	runtime.Config.SummaryTokenBudget = 400
	runtime.Source = source
	runtime.Clock = func() time.Time { return now }

	result, err := runtime.RunSessionEndPipeline(context.Background(), "pipeline-session", SessionEndPipelineOptions{
		ExtractionTimeout: 750 * time.Millisecond, ConsolidationTimeout: 900 * time.Millisecond, Consolidate: true,
	})
	require.NoError(t, err)
	require.Equal(t, "complete", result.Stage)
	require.Equal(t, 1, result.Run.Processed)
	require.NotEmpty(t, result.Consolidation.SummaryID)
	require.Greater(t, extractor.deadlineRemaining, 500*time.Millisecond)
	require.LessOrEqual(t, extractor.deadlineRemaining, 750*time.Millisecond)
	require.Greater(t, summarizer.deadlineRemaining, 600*time.Millisecond)
	require.LessOrEqual(t, summarizer.deadlineRemaining, 900*time.Millisecond)
	summary, err := store.ActiveSummary(context.Background())
	require.NoError(t, err)
	require.NotNil(t, summary)
}

func TestSessionEndPipelineReportsExtractionTimeoutAndDoesNotConsolidate(t *testing.T) {
	now := time.Now().UTC()
	source := &fakeSource{
		sessions: []SessionRef{{
			ID: "timeout-session", WorkspacePath: "/workspace", State: "closed",
			LastActivityAt: now.Add(-time.Hour), CanonicalCount: 2,
		}},
		transcripts: map[string]Transcript{"timeout-session": validTranscript("timeout-session", "timeout-hash")},
	}
	store, err := memorystore.New(memorystore.Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	runtime := New(store, source, recoveryExtractorFunc(func(ctx context.Context, _ SessionRef, _ Transcript) (ExtractionResult, error) {
		<-ctx.Done()
		return ExtractionResult{}, ctx.Err()
	}), fakeModes{}, Config{
		Mode: "on", Workspace: "/workspace", ProjectKey: store.ProjectKey(),
		MaxSourceMessages: 20, ConsolidationDisabled: false,
	})
	runtime.Clock = func() time.Time { return now }
	result, err := runtime.RunSessionEndPipeline(context.Background(), "timeout-session", SessionEndPipelineOptions{
		ExtractionTimeout: 25 * time.Millisecond, ConsolidationTimeout: time.Second, Consolidate: true,
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, "extraction", result.Stage)
	require.Zero(t, result.Run.Processed)
	require.Empty(t, result.Consolidation.SummaryID)
	require.NotEmpty(t, err.Error())
}
