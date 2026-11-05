package supervision

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFilterSnapshotToSubjectIDsKeepsOnlyCurrentSubjects(t *testing.T) {
	snapshot := &Snapshot{
		SnapshotSeq: 9,
		NextSeq:     10,
		Summary: SnapshotSummary{
			PendingWakes:       2,
			WakePendingOverdue: 1,
		},
		Descendants: []SnapshotItem{
			{
				ID:                "batch_1",
				SupervisionState:  SupervisionRunning,
				ActionRequired:    true,
				RecommendedAction: "inspect",
			},
			{
				ID:                "child-1",
				SupervisionState:  SupervisionTerminated,
				NotificationID:    "n-1",
				LastChangeSeq:     8,
				RecommendedAction: "inspect",
			},
			{
				ID:                "historical",
				SupervisionState:  SupervisionTerminated,
				NotificationID:    "n-old",
				LastChangeSeq:     8,
				RecommendedAction: "inspect",
			},
		},
	}

	filtered := FilterSnapshotToSubjectIDs(snapshot, []string{"batch_1", "child-1"}, 5)
	require.NotNil(t, filtered)
	require.Len(t, filtered.Descendants, 2)
	require.Equal(t, 1, filtered.Summary.Running)
	require.Equal(t, 1, filtered.Summary.TerminalUnacknowledged)
	require.Equal(t, 1, filtered.Summary.ActionRequired)
	require.Equal(t, 2, filtered.Summary.PendingWakes)
	require.Equal(t, 1, filtered.Summary.WakePendingOverdue)
	require.Equal(t, int64(9), filtered.SnapshotSeq)
	require.Equal(t, int64(10), filtered.NextSeq)
}

func TestFilterSnapshotToSubjectIDsEmptySet(t *testing.T) {
	snapshot := &Snapshot{
		Descendants: []SnapshotItem{{ID: "batch_1", SupervisionState: SupervisionRunning}},
		Summary:     SnapshotSummary{Running: 1, PendingWakes: 3},
	}
	filtered := FilterSnapshotToSubjectIDs(snapshot, nil, 0)
	require.NotNil(t, filtered)
	require.Empty(t, filtered.Descendants)
	require.Zero(t, filtered.Summary.Running)
	require.Equal(t, 3, filtered.Summary.PendingWakes)
}
