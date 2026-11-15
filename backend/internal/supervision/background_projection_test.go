package supervision

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/background"
)

func TestBackgroundJobTerminalInputFromEvent(t *testing.T) {
	created := time.Unix(1700000000, 123).UTC()
	event := background.JobEvent{
		JobID:     "job-1",
		Type:      "completed",
		CreatedAt: created,
		Payload: map[string]interface{}{
			"exit_code":     float64(137),
			"error_code":    "E_TIMEOUT",
			"message":       "done",
			"cancel_source": "user",
		},
	}
	origin := background.JobOrigin{
		ParentTurnID:       "turn-1",
		ParentEpisodeID:    "ep-1",
		SettlementID:       "settle-1",
		SettlementCohortID: "cohort-1",
		FinalizationPolicy: "required",
	}
	in := BackgroundJobTerminalInputFromEvent(event, "session-1", "root-1", "go test", true, origin)
	require.Equal(t, "root-1", in.RootScopeID)
	require.Equal(t, "session-1", in.TargetParentSessionID)
	require.Equal(t, "job-1", in.JobID)
	require.Equal(t, "turn-1", in.ParentTurnID)
	require.Equal(t, "required", in.FinalizationPolicy)
	require.Equal(t, "completed", in.Status)
	require.Equal(t, "137", in.ExitCode)
	require.Equal(t, "E_TIMEOUT", in.ErrorCode)
	require.Equal(t, "done", in.Message)
	require.Equal(t, "user", in.CancelSource)
	require.True(t, in.Observed)
	require.Equal(t, created.UnixNano(), in.Epoch)
}

func TestBackgroundJobMonitorInputFromEvent(t *testing.T) {
	event := background.JobEvent{
		JobID:     "job-2",
		Type:      "task_monitor",
		CreatedAt: time.Unix(1700000001, 0).UTC(),
		Payload: map[string]interface{}{
			"status":          "running",
			"elapsed_ms":      int64(1500),
			"check_after_ms":  float64(250),
			"max_duration_ms": 9000,
		},
	}
	in := BackgroundJobMonitorInputFromEvent(event, "session-1", "root-1", "sleep 1", background.JobOrigin{})
	require.Equal(t, "running", in.Status)
	require.Equal(t, 1500*time.Millisecond, in.Elapsed)
	require.Equal(t, 250*time.Millisecond, in.CheckAfter)
	require.Equal(t, 9000*time.Millisecond, in.MaxDuration)
	require.Equal(t, event.CreatedAt.UnixNano(), in.Epoch)
}
