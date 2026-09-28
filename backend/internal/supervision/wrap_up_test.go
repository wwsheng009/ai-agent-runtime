package supervision

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestReadResultWrapUpListsCompletedUnfinishedAndArtifacts is the 建议稿 §2.4
// acceptance guard: a failed/canceled record's read result must show 已完成 /
// 未完成 plus the artifact locations, so the parent can judge the failure
// without a fresh model turn (the child may already be gone).
func TestReadResultWrapUpListsCompletedUnfinishedAndArtifacts(t *testing.T) {
	record := AgentResultRecord{
		Source:    ResultSourceTaskResult,
		SessionID: "child-wrap",
		Status:    "failed",
		Changes: []AgentResultChange{
			{Path: "backend/internal/agent/loop.go", Status: "applied"},
			{Path: "backend/internal/agent/scheduler.go", Summary: "guard not applied", Status: "skipped"},
		},
		Artifacts: []string{"art_report_1"},
		Errors:    []AgentResultError{{Code: "verification_timeout", Message: "verification timed out"}},
	}

	payload := BuildReadResultPayload(record, ReadResultArgs{SessionID: "child-wrap"})

	require.NotNil(t, payload.WrapUp)
	require.Equal(t, []string{"backend/internal/agent/loop.go"}, payload.WrapUp.Completed)
	require.Equal(t, []string{"backend/internal/agent/scheduler.go — guard not applied (skipped)"}, payload.WrapUp.Unfinished)
	require.Equal(t, []string{"art_report_1"}, payload.WrapUp.Artifacts)
	// The §2.4-3 guidance must agree with the wrap-up: the applied patch is a
	// deliverable, so the read says do not re-dispatch.
	require.True(t, payload.ResultAvailable)
	require.True(t, payload.DoNotRetry)

	// The wrap-up must survive serialization under its named field: the model
	// reads the JSON, not the Go struct.
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"wrap_up"`)
}

// TestReadResultWrapUpSkippedOnlyFailureStaysRetryable pins the §2.4-3 split:
// an unlanded patch with no stored diff is not a deliverable, so the read must
// NOT carry do_not_retry — while the wrap-up still tells the parent what did
// not land and why.
func TestReadResultWrapUpSkippedOnlyFailureStaysRetryable(t *testing.T) {
	record := AgentResultRecord{
		Source:    ResultSourceTaskResult,
		SessionID: "child-retry",
		Status:    "failed",
		Changes: []AgentResultChange{
			{Path: "backend/internal/agent/loop.go", Status: "skipped"},
		},
	}

	payload := BuildReadResultPayload(record, ReadResultArgs{SessionID: "child-retry"})

	require.False(t, payload.ResultAvailable)
	require.False(t, payload.DoNotRetry)
	require.NotNil(t, payload.WrapUp)
	require.Empty(t, payload.WrapUp.Completed)
	require.Equal(t, []string{"backend/internal/agent/loop.go (skipped)"}, payload.WrapUp.Unfinished)
}

// TestReadResultWrapUpSkippedWithArtifactIsDeliverable: an unlanded patch that
// stored its diff as an artifact is still something the parent can read, so it
// counts as a deliverable and suppresses re-dispatch.
func TestReadResultWrapUpSkippedWithArtifactIsDeliverable(t *testing.T) {
	record := AgentResultRecord{
		Source:    ResultSourceTaskResult,
		SessionID: "child-art",
		Status:    "failed",
		Changes: []AgentResultChange{
			{Path: "backend/internal/agent/loop.go", Status: "skipped", ArtifactRefs: []string{"art_patch_9"}},
		},
	}

	payload := BuildReadResultPayload(record, ReadResultArgs{SessionID: "child-art"})

	require.True(t, payload.ResultAvailable)
	require.True(t, payload.DoNotRetry)
	require.NotNil(t, payload.WrapUp)
	require.Equal(t, []string{"backend/internal/agent/loop.go (skipped)"}, payload.WrapUp.Unfinished)
}

// TestReadResultWrapUpOmittedWhenNothingRecorded: a successful record carries
// no wrap-up, and a failure without recorded work state stays silent instead
// of fabricating a "nothing was done" claim.
func TestReadResultWrapUpOmittedWhenNothingRecorded(t *testing.T) {
	succeeded := BuildReadResultPayload(AgentResultRecord{
		Source:    ResultSourceCompletionPayload,
		SessionID: "child-ok",
		Status:    "succeeded",
		Success:   true,
		Summary:   "done",
		Changes:   []AgentResultChange{{Path: "a.go", Status: "applied"}},
	}, ReadResultArgs{SessionID: "child-ok"})
	require.Nil(t, succeeded.WrapUp)

	bareFailure := BuildReadResultPayload(AgentResultRecord{
		Source:    ResultSourceTaskResult,
		SessionID: "child-bare",
		Status:    "failed",
	}, ReadResultArgs{SessionID: "child-bare"})
	require.Nil(t, bareFailure.WrapUp)
}

// TestDropTrailingReadResultEntryShedsWrapUpLast pins the budget-shedding
// contract: the wrap-up overlaps the changes/artifacts sections, so it is shed
// only after they are gone, and an emptied wrap-up disappears entirely.
func TestDropTrailingReadResultEntryShedsWrapUpLast(t *testing.T) {
	payload := &ReadResultPayload{
		Changes: []AgentResultChange{{Path: "a.go", Status: "applied"}},
		WrapUp: &ResultWrapUp{
			Completed:  []string{"a.go"},
			Unfinished: []string{"b.go (skipped)"},
			Artifacts:  []string{"art_1"},
		},
	}

	require.True(t, dropTrailingReadResultEntry(payload)) // changes first
	require.Empty(t, payload.Changes)
	require.NotNil(t, payload.WrapUp)

	require.True(t, dropTrailingReadResultEntry(payload)) // then unfinished
	require.Empty(t, payload.WrapUp.Unfinished)
	require.True(t, dropTrailingReadResultEntry(payload)) // then completed
	require.Empty(t, payload.WrapUp.Completed)
	require.True(t, dropTrailingReadResultEntry(payload)) // then wrap-up artifacts
	require.Empty(t, payload.WrapUp.Artifacts)
	require.True(t, dropTrailingReadResultEntry(payload)) // emptied wrap-up disappears
	require.Nil(t, payload.WrapUp)

	require.False(t, dropTrailingReadResultEntry(payload), "nothing left to shed")
}
