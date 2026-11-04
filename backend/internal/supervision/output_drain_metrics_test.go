package supervision

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/outputdrain"
)

type stubOutputDrainSource struct {
	metrics outputdrain.Metrics
	err     error
}

func (s stubOutputDrainSource) ReadOutputDrainMetrics(context.Context, time.Time, time.Time) (outputdrain.Metrics, error) {
	return s.metrics, s.err
}

func TestMetricsSnapshotCollectsOutputDrainSources(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, "output-drain-metrics")
	background := outputdrain.Metrics{Records: 2, Versions: 4, AbsorbedBytes: 100, DrainedBytes: 90, LateBytes: 10, LateDiscarded: 1}
	batch := outputdrain.Metrics{Records: 1, Versions: 2, AbsorbedBytes: 50, DrainedBytes: 50, ConflictPolicies: map[string]int{"reject_terminal_write": 1}}

	snapshot, err := CollectMetricsSnapshot(ctx, store, MetricsSnapshotOptions{
		OutputDrainSources: OutputDrainSources{
			BackgroundJobs: stubOutputDrainSource{metrics: background},
			BatchTasks:     stubOutputDrainSource{metrics: batch},
		},
	})
	require.NoError(t, err)
	require.Equal(t, background, snapshot.OutputDrain.BackgroundJobs)
	require.Equal(t, batch, snapshot.OutputDrain.BatchTasks)
	require.NotContains(t, snapshot.Notes, "background output drain source 未接线")
	require.NotContains(t, snapshot.Notes, "batch output drain source 未接线")
}
