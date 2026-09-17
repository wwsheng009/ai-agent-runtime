package supervision

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// P0-4 改动 1：include_results 默认关闭必须逐字节兼容，开启时按预算截断。
type recordingResultProvider struct {
	items       []DescendantState
	plainCalls  int
	resultCalls int
}

func (p *recordingResultProvider) ListDescendants(ctx context.Context, scope Scope) ([]DescendantState, error) {
	p.plainCalls++
	return p.items, nil
}

func (p *recordingResultProvider) ListDescendantsWithResults(ctx context.Context, scope Scope) ([]DescendantState, error) {
	p.resultCalls++
	return p.items, nil
}

func TestBuildSnapshotIncludeResultsDefaultIsByteIdentical(t *testing.T) {
	store := newTestStore(t, "supervision-snapshot-results-default")
	ctx := context.Background()
	finished := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	provider := &recordingResultProvider{items: []DescendantState{
		{
			Kind:             SubjectAgentSession,
			ID:               "child-1",
			ExecutionStatus:  "succeeded",
			SupervisionState: SupervisionTerminated,
			ResultStatus:     "succeeded",
			ResultSummary:    "should not leak into the default payload",
			ArtifactRefs:     []string{"artifact://one"},
			ErrorClass:       "should-not-leak",
			FinishedAt:       &finished,
		},
	}}

	snapshot, err := BuildSnapshot(ctx, store, SnapshotRequest{
		Scope:    Scope{RootSessionID: "root-session-1"},
		Provider: provider,
	})
	require.NoError(t, err)
	require.Equal(t, 1, provider.plainCalls, "default read must not ask for results")
	require.Zero(t, provider.resultCalls)
	require.Len(t, snapshot.Descendants, 1)

	raw, err := json.Marshal(snapshot)
	require.NoError(t, err)
	for _, field := range []string{"result_status", "result_summary", "result_truncated", "artifact_refs", "error_class", "finished_at"} {
		require.NotContains(t, string(raw), `"`+field+`"`, "default include_results=false output must stay byte-identical")
	}
	row := snapshot.Descendants[0]
	require.Empty(t, row.ResultStatus)
	require.Empty(t, row.ResultSummary)
	require.Empty(t, row.ArtifactRefs)
	require.Empty(t, row.ErrorClass)
	require.Nil(t, row.FinishedAt)
}

func TestBuildSnapshotIncludeResultsFillsBoundedProjection(t *testing.T) {
	store := newTestStore(t, "supervision-snapshot-results-on")
	ctx := context.Background()
	finished := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	longSummary := strings.Repeat("x", MaxSnapshotResultSummaryRunes+40)
	longRef := strings.Repeat("r", MaxSnapshotArtifactRefChars+20)
	provider := &recordingResultProvider{items: []DescendantState{
		{
			Kind:             SubjectAgentSession,
			ID:               "child-1",
			ExecutionStatus:  "failed",
			SupervisionState: SupervisionTerminated,
			ResultStatus:     "failed",
			ResultSummary:    longSummary,
			ArtifactRefs:     []string{"artifact://one", "artifact://one", longRef, "artifact://two", "artifact://three", "artifact://four"},
			ErrorClass:       "TOOL_TIMEOUT",
			FinishedAt:       &finished,
		},
	}}

	snapshot, err := BuildSnapshot(ctx, store, SnapshotRequest{
		Scope:          Scope{RootSessionID: "root-session-1"},
		Provider:       provider,
		IncludeResults: true,
	})
	require.NoError(t, err)
	require.Equal(t, 1, provider.resultCalls)
	require.Zero(t, provider.plainCalls)

	row := snapshot.Descendants[0]
	require.Equal(t, "failed", row.ResultStatus)
	require.Equal(t, MaxSnapshotResultSummaryRunes, len([]rune(row.ResultSummary)))
	require.True(t, row.ResultTruncated)
	require.Equal(t, "TOOL_TIMEOUT", row.ErrorClass)
	require.NotNil(t, row.FinishedAt)
	require.Equal(t, finished, row.FinishedAt.UTC())
	require.Len(t, row.ArtifactRefs, MaxSnapshotResultArtifactRefs)
	require.Equal(t, "artifact://one", row.ArtifactRefs[0], "refs are deduped")
	for _, ref := range row.ArtifactRefs {
		require.LessOrEqual(t, len([]rune(ref)), MaxSnapshotArtifactRefChars)
	}

	raw, err := json.Marshal(snapshot)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"result_status":"failed"`)
	require.Contains(t, string(raw), `"result_truncated":true`)
}

func TestBuildSnapshotIncludeResultsWithoutResultProviderStaysLegacy(t *testing.T) {
	store := newTestStore(t, "supervision-snapshot-results-plain")
	ctx := context.Background()
	provider := &fakeDescendantProvider{items: []DescendantState{
		{
			Kind:             SubjectAgentSession,
			ID:               "child-1",
			SupervisionState: SupervisionRunning,
		},
	}}
	snapshot, err := BuildSnapshot(ctx, store, SnapshotRequest{
		Scope:          Scope{RootSessionID: "root-session-1"},
		Provider:       provider,
		IncludeResults: true,
	})
	require.NoError(t, err, "a provider without the optional interface must not fail the read")
	require.Len(t, snapshot.Descendants, 1)
	raw, err := json.Marshal(snapshot)
	require.NoError(t, err)
	require.NotContains(t, string(raw), `"result_status"`, "a provider that supplies no results yields no result fields")
}
