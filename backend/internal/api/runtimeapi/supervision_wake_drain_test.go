package runtimeapi

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// projectAPIStandaloneScanWake 复刻监督器扫描侧的产物：critical + action_required
// 的生命周期通知会在控制面排一条 durable wake（与 CLI 测试 projectScanWake 同形）。
func projectAPIStandaloneScanWake(t *testing.T, handler *Handler) {
	t.Helper()
	_, err := supervision.ProjectLifecycle(context.Background(), handler.getSupervisionStore(), handler.getSupervisionWakeScheduler(), supervision.LifecycleProjection{
		RootScopeID:           apiProgressCheckParentSession,
		TargetParentSessionID: apiProgressCheckParentSession,
		SubjectKind:           supervision.SubjectAgentRun,
		SubjectID:             "run-stalled-api-wake-ready",
		EventType:             "progress_stalled",
		Severity:              supervision.SeverityCritical,
		SupervisionState:      supervision.SupervisionStalled,
		Reason:                "scan produced a stalled lifecycle wake",
	})
	require.NoError(t, err)
}

// TestAPIExecutionSupervisorWiresWakeReadyDrain pins the API half of the
// 2026-10-04 parity fix: the lazily built execution supervisor must carry the
// scan-edge hook, and the hook must drain a scan-produced wake to an idle
// parent asynchronously (the scan loop must never block on delivery).
func TestAPIExecutionSupervisorWiresWakeReadyDrain(t *testing.T) {
	handler, _ := newAPIProgressCheckFixture(t, "api-wake-ready-drain", subagentbatch.BatchRunning)
	supervisor := handler.getExecutionSupervisor()
	require.NotNil(t, supervisor, "a configured supervision store must yield the watchdog")
	t.Cleanup(func() {
		if handler.executionSupervisorStop != nil {
			handler.executionSupervisorStop()
		}
	})
	require.NotNil(t, supervisor.WakeReady,
		"without the hook a scan wake for an idle parent has no drain edge (CLI parity)")

	// 投递在异步 goroutine 里发生：夹具的普通 int 计数会有数据竞争，换成原子计数。
	var deliveries int32
	handler.supervisionWakeMu.Lock()
	handler.supervisionWake.Deliver = func(context.Context, string, string, *supervision.Digest, []string) error {
		atomic.AddInt32(&deliveries, 1)
		return nil
	}
	handler.supervisionWakeMu.Unlock()

	projectAPIStandaloneScanWake(t, handler)
	supervisor.WakeReady(context.Background(), apiProgressCheckParentSession, apiProgressCheckParentSession, "")

	require.Eventually(t, func() bool { return atomic.LoadInt32(&deliveries) == 1 }, 5*time.Second, 10*time.Millisecond,
		"the scan wake must be delivered through the normal wake path")
	requireNoPendingAPIWake(t, handler, apiProgressCheckParentSession, "the delivered scan wake is claimed")
}

// TestAPIRequestSupervisedWakeDrainGuards keeps the hook body fail-quiet: a nil
// host or a blank parent identity never touches the control plane.
func TestAPIRequestSupervisedWakeDrainGuards(t *testing.T) {
	var nilHandler *Handler
	nilHandler.requestSupervisedWakeDrain("scope", "parent") // nil receiver: no panic

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.requestSupervisedWakeDrain("scope", "   ")            // blank parent: strict no-op
	handler.requestSupervisedWakeDrain("scope", "parent-unwired") // unwired control plane: background drain no-ops
}
