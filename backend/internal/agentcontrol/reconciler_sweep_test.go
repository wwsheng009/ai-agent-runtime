package agentcontrol

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestReconcilerRunsSweepHookInBothModes 钉住第三层自愈的接线语义：挂起记录清扫
// 不是模式门控的破坏性动作（observe 下也要跑，否则默认配置下 "Waiting for
// subagents" 永不收敛），失败只记录不失败整趟，计数进入报告与摘要。
func TestReconcilerRunsSweepHookInBothModes(t *testing.T) {
	for _, mode := range []ReconcileMode{ReconcileModeObserve, ReconcileModeEnforce} {
		calls := 0
		reconciler := &Reconciler{
			Mode: mode,
			List: func(context.Context) ([]AgentRecord, error) { return nil, nil },
			Sweep: func(context.Context, time.Time) (TurnSweepOutcome, error) {
				calls++
				return TurnSweepOutcome{Supported: true, Scanned: 3, Cleared: 1}, nil
			},
		}
		report, err := reconciler.RunOnce(context.Background())
		require.NoError(t, err, "mode=%s", mode)
		require.Equal(t, 1, calls, "mode=%s", mode)
		require.True(t, report.Sweep.Supported)
		require.Equal(t, 1, report.Sweep.Cleared)
		require.Contains(t, report.Summary(), "sweep_cleared=1")
	}

	// 失败只记录：审计报告不能因为一次清扫失败而变空。
	reconciler := &Reconciler{
		List: func(context.Context) ([]AgentRecord, error) { return nil, nil },
		Sweep: func(context.Context, time.Time) (TurnSweepOutcome, error) {
			return TurnSweepOutcome{}, errors.New("store unavailable")
		},
	}
	report, err := reconciler.RunOnce(context.Background())
	require.NoError(t, err)
	require.Contains(t, report.SweepError, "store unavailable")
	require.Contains(t, report.Summary(), "sweep_error=store unavailable")
}
