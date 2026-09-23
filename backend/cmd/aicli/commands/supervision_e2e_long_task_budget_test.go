package commands

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// §5 AC-P3-4b：一次「声明式长预算 + 中途一次延长」的端到端回放。
//
// 真实桌面上的 ≥2h 长任务不可能在测试里等完，所以这里把「时间」固定成账本上的
// 声明值：batch / task / run 三处一致地声明 2h 预算，然后让**生产代码**走完
// 「派发 → 挂起 → 中途延长 → 终局汇报」四个阶段，只把「等两小时」替换成直接
// 推进 durable 状态。除「模型选择工具」和「真实墙钟」外每一段都是生产路径：
//
//	派发 + 挂起：subagentbatch.BatchStore（§6.12 parked-turn 记录）
//	中途决策　：host.Supervision.Actions（extend_deadline 的 request/accept/execute）
//	终局汇报　：injectLocalSupervisionPreflight（父 turn 的 rollup 注入）
//
// L4 留白：本用例证明「语义 + 宿主接线」成立；真实 2h 墙钟下的观测量（§7.3
// 指标 3/4 的采样值）仍需真实运行期数据，见 §13.12 留白表。
func TestSupervisionE2E_LongTaskDeclaredBudgetWithOneExtension(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	host, agentStore, runStore := newSupervisionE2EHost(t)

	const (
		parent   = supervisionE2EParent
		turnID   = "turn-long-budget"
		declared = 2 * time.Hour
		extendBy = 30 * time.Minute
	)

	executionDeadline := now.Add(declared)
	progressDeadline := now.Add(declared)
	decisionWindow := now.Add(30 * time.Minute)
	longRuns := []struct {
		runID     string
		sessionID string
	}{
		{runID: "run-long-1", sessionID: "worker-long-1"},
		{runID: "run-long-2", sessionID: "worker-long-2"},
	}

	// --- 阶段 1：声明式长预算 -------------------------------------------------
	// 三处一致地声明 ≥2h：batch.BatchDeadline、task.TaskDeadline、
	// run.DeclaredBudget/ExecutionDeadlineAt（§6.2「声明式 deadline」）。
	batch := &subagentbatch.SubagentBatch{
		BatchID:         subagentbatch.NewID("batch"),
		RootScopeID:     parent,
		ParentSessionID: parent,
		ParentTurnID:    turnID,
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchRunning,
		TaskCount:       2,
		RunningCount:    2,
		BatchDeadline:   now.Add(declared),
		HeartbeatAt:     now,
		CreatedAt:       now,
		UpdatedAt:       now,
		Version:         1,
	}
	tasks := []subagentbatch.SubagentTaskRecord{
		{TaskID: "task-long-1", ChildSessionID: longRuns[0].sessionID, Status: subagentbatch.TaskRunning, OrderIndex: 1, TaskDeadline: now.Add(declared), UpdatedAt: now, Version: 1},
		{TaskID: "task-long-2", ChildSessionID: longRuns[1].sessionID, Status: subagentbatch.TaskRunning, OrderIndex: 2, TaskDeadline: now.Add(declared), UpdatedAt: now, Version: 1},
	}
	created, err := host.SubagentBatches.CreateBatch(ctx, batch, tasks)
	require.NoError(t, err)
	require.True(t, created, "the long-task batch fixture must be created")

	// 子会话的 durable 身份（与真实 spawn 的落点一致）：digest 的动作能力判定会按
	// root scope 校验 target agent，缺 registry 行时它（正确地）拒绝宣告动作。
	for _, longRun := range longRuns {
		seedSupervisionE2EChild(t, agentStore, longRun.sessionID, agentcontrol.AgentStatusActive, now)
	}

	for _, longRun := range longRuns {
		created, err := runStore.CreateExecutionRun(ctx, supervision.ExecutionRun{
			RunID:               longRun.runID,
			Kind:                supervision.RunKindAgentRun,
			Workflow:            supervision.RunWorkflowSpawnAgent,
			RootSessionID:       parent,
			ParentSessionID:     parent,
			SessionID:           longRun.sessionID,
			AgentID:             longRun.sessionID,
			TurnID:              turnID,
			Status:              supervision.RunStatusRunning,
			OwnerID:             "host-1",
			StartedAt:           now,
			LastHeartbeatAt:     now,
			LastProgressAt:      now,
			ProgressSeq:         1,
			ExecutionDeadlineAt: &executionDeadline,
			ProgressDeadlineAt:  &progressDeadline,
			MaxAttempts:         1,
			FencingToken:        1,
			DeclaredBudget:      declared,
			Version:             1,
			CreatedAt:           now,
			UpdatedAt:           now,
		})
		require.NoError(t, err)
		require.True(t, created, "the long-task run row must be created")
	}

	// 派发后父 turn park 自己（§6.12），而不是内联阻塞（C4-1 之后的统一形态）：
	// turn_id 全程不变，是「全程 turn 不中断」的账本证据。
	require.NoError(t, host.SubagentBatches.ParkTurnSuspension(ctx, &subagentbatch.TurnSuspension{
		TurnID:              turnID,
		SessionID:           parent,
		RootScopeID:         parent,
		ObligationIDs:       []string{batch.BatchID},
		ParkedAt:            now,
		DecisionWindowUntil: decisionWindow,
		ResumeQueue:         []string{"wake:" + batch.BatchID},
	}))

	// 挂起期间的巡检：长任务的 progress 行对父 turn 可见（正常进度不是「什么
	// 都没发生」）。
	parkedReport, err := injectLocalSupervisionPreflight(ctx, host, parent, "USER PROMPT", nil)
	require.NoError(t, err)
	require.Contains(t, parkedReport, batch.BatchID+": 0/2 completed, 2 running")
	require.Contains(t, parkedReport, "USER PROMPT")

	// --- 阶段 2：中途一次真实延长 ---------------------------------------------
	require.NotNil(t, host.Supervision.Actions, "the durable control plane must expose the action service")

	// watchdog 的 escalate-first 上报（§6.11 I8）：与生产 projectDecision 同一
	// 投影入口，父会话因此拿到一条 critical/action_required 的通知行。
	_, err = supervision.ProjectLifecycle(ctx, host.Supervision.Store, host.Supervision.Wakes, supervision.LifecycleProjection{
		RootScopeID:           parent,
		TargetParentSessionID: parent,
		SubjectKind:           supervision.SubjectAgentRun,
		SubjectID:             "run-long-1",
		EventType:             "progress_stalled",
		Severity:              supervision.SeverityCritical,
		SupervisionState:      supervision.SupervisionStalled,
		Reason:                "no progress for 120s",
		RecommendedAction:     "inspect run and decide cancel/retry",
	})
	require.NoError(t, err)

	// 父会话的决策走生产 ActionService 全链路（requested → accepted → completed），
	// 而不是直接改账本。
	record, err := host.Supervision.Actions.RequestAction(ctx, supervision.ActionRequest{
		RootScopeID:     parent,
		RequestedByKind: "parent_session",
		RequestedByID:   parent,
		TargetKind:      supervision.SubjectAgentRun,
		TargetID:        "run-long-1",
		Action:          supervision.ActionExtendDeadline,
		Reason:          "long task is still making progress; give it one more window",
		ExtendBy:        extendBy,
		ExtendWhich:     "both",
	})
	require.NoError(t, err)
	require.Equal(t, supervision.ActionRequested, record.Status)

	accepted, err := host.Supervision.Actions.AcceptAction(ctx, record.ActionID)
	require.NoError(t, err)
	require.Equal(t, supervision.ActionAccepted, accepted.Status)

	terminal, err := host.Supervision.Actions.ExecuteAction(ctx, record.ActionID)
	require.NoError(t, err)
	require.Equal(t, supervision.ActionCompleted, terminal.Status)
	require.Contains(t, terminal.Result, "已延长 ×1")
	require.Contains(t, terminal.Result, "+30m0s")

	extended, err := runStore.GetExecutionRun(ctx, "run-long-1")
	require.NoError(t, err)
	require.Equal(t, 1, extended.ExtensionCount)
	require.Equal(t, extendBy, extended.ExtendedTotal)
	require.NotNil(t, extended.ExecutionDeadlineAt)
	require.NotNil(t, extended.ProgressDeadlineAt)
	require.WithinDuration(t, executionDeadline.Add(extendBy), *extended.ExecutionDeadlineAt, time.Second)
	require.WithinDuration(t, progressDeadline.Add(extendBy), *extended.ProgressDeadlineAt, time.Second)
	require.Nil(t, extended.DecisionWindowUntil,
		"决策已作出：escalate-first 窗口被花掉，下一次停摆重新上报（I8）")
	require.Empty(t, extended.CancelSource, "延长意味着没有走兜底强制分支")

	// 延长是逐义务的：同 scope 的另一个 run 不受影响。
	untouched, err := runStore.GetExecutionRun(ctx, "run-long-2")
	require.NoError(t, err)
	require.Equal(t, 0, untouched.ExtensionCount)
	require.NotNil(t, untouched.ExecutionDeadlineAt)
	require.WithinDuration(t, executionDeadline, *untouched.ExecutionDeadlineAt, time.Second)

	// 延长对父 Agent 可见：durable 生命周期行 + digest 文本都带「已延长 ×1, +30m0s」。
	events, err := host.Supervision.Store.ListNotifications(ctx, supervision.NotificationFilter{
		RootScopeID:     parent,
		SubjectKind:     supervision.SubjectAgentRun,
		SubjectID:       "run-long-1",
		IncludeResolved: true,
	})
	require.NoError(t, err)
	var extensionEvent *supervision.Notification
	for i := range events {
		if events[i].EventType == "obligation.deadline.extended" {
			extensionEvent = &events[i]
			break
		}
	}
	require.NotNil(t, extensionEvent, "the extension must leave a durable, parent-visible event")
	require.Contains(t, extensionEvent.Reason, "已延长 ×1")
	require.Contains(t, extensionEvent.Reason, "+30m0s")
	require.Contains(t, extensionEvent.Reason, "long task is still making progress")

	digest, err := supervision.BuildDigest(ctx, host.Supervision.Store, supervision.DigestRequest{
		RootScopeID:           parent,
		TargetParentSessionID: parent,
		Limit:                 20,
		IncludeResolvedSince:  true,
	})
	require.NoError(t, err)
	require.Contains(t, digest.Text, "已延长 ×1, +30m0s")

	// --- 阶段 3：终局（完成，而不是被取消）-------------------------------------
	// 墙钟推进被替换成 durable 状态推进：两个 task 成功 → batch 收敛为 completed。
	finishedAt := now
	for _, task := range tasks {
		_, err := host.SubagentBatches.UpdateTask(ctx, batch.BatchID, task.TaskID, -1, func(record *subagentbatch.SubagentTaskRecord) {
			record.Status = subagentbatch.TaskSucceeded
			record.FinishedAt = &finishedAt
		})
		require.NoError(t, err)
	}
	finalBatch, err := host.SubagentBatches.UpdateBatch(ctx, batch.BatchID, -1, func(record *subagentbatch.SubagentBatch) {
		record.Status = subagentbatch.BatchCompleted
		record.RunningCount = 0
		record.CompletedCount = len(tasks)
		record.FinishedAt = &finishedAt
	})
	require.NoError(t, err)
	require.Equal(t, subagentbatch.BatchCompleted, finalBatch.Status)
	require.Nil(t, finalBatch.CancelRequestedAt, "长任务走的是完成路径，全程没有取消请求")

	// 终局报告完整：父 turn 的 rollup 给出「2/2 completed (terminal …)」。
	report, err := injectLocalSupervisionPreflight(ctx, host, parent, "USER PROMPT", nil)
	require.NoError(t, err)
	require.Contains(t, report, batch.BatchID+": 2/2 completed (terminal")

	// 全程 turn 不中断：parked-turn 记录仍在、turn_id 未变、义务清单未改，
	// 且只排了一次 resume（预算内，无唤醒风暴）。
	parked, ok, err := host.SubagentBatches.GetTurnSuspension(ctx, parent, turnID)
	require.NoError(t, err)
	require.True(t, ok, "the parked turn record must survive the whole episode")
	require.Equal(t, turnID, parked.TurnID)
	require.Equal(t, []string{batch.BatchID}, parked.ObligationIDs)
	require.Len(t, parked.ResumeQueue, 1)

	// 终局不是「被 runtime 强制取消」：CancelSource 全程为空。
	finalRun, err := runStore.GetExecutionRun(ctx, "run-long-1")
	require.NoError(t, err)
	require.Empty(t, finalRun.CancelSource)
	require.NotEqual(t, supervision.RunStatusCanceled, finalRun.Status)
}

// §5 AC-P3-4c / §7.3 指标 1-2 的读数口径：被 runtime 强制取消的 run 占比与
// decision_window_expired 兜底占比，在现有 durable 面上可直接读数（不改生产代码）。
//
// 两件事必须分开看：
//   - **口径**（本用例锁住的）：分母 = 窗口内的 run 行；分子 1 = CancelSource 属于
//     runtime 自己判的终态来源；分子 2 = CancelSource 恰为 decision_window_expired。
//     父/操作者主动取消（operator_cancel 等）不计入「强制取消」。
//   - **真实波动收敛**（L4 留白）：需要真实运行期采样，本用例只用一条**真实**兜底行
//     证明读数链路可用（兜底行由宿主装配的 watchdog 在 enforce 下产生）。
func TestSupervisionMetricsReadout_RuntimeCancelSources(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	t.Setenv(localExecutionSupervisorModeEnv, "enforce")

	host, _, runStore := newSupervisionE2EHost(t)
	// 不设 lifecycleCtx：不启动后台巡检循环，只用同步 ScanOnce 观察宿主装配的
	// watchdog 的判定（chat_actor_execution_supervisor.go:62 的「可用但不跑循环」形态）。

	const parent = supervisionE2EParent

	// 1) 真实兜底行：一条停摆且决策窗口已过期的 run，由**宿主装配的** watchdog
	//    在 enforce 模式下判终态（execution_supervisor.go:870 的兜底分支）。
	stalledProgressDeadline := now.Add(-30 * time.Minute)
	expiredWindow := now.Add(-time.Minute)
	healthyExecutionDeadline := now.Add(time.Hour)
	created, err := runStore.CreateExecutionRun(ctx, supervision.ExecutionRun{
		RunID:               "run-metrics-fallback",
		Kind:                supervision.RunKindAgentRun,
		Workflow:            supervision.RunWorkflowSpawnAgent,
		RootSessionID:       parent,
		ParentSessionID:     parent,
		SessionID:           "metrics-worker-fallback",
		AgentID:             "metrics-worker-fallback",
		Status:              supervision.RunStatusRunning,
		OwnerID:             "host-1",
		StartedAt:           now.Add(-time.Hour),
		LastHeartbeatAt:     now.Add(-40 * time.Minute),
		LastProgressAt:      now.Add(-40 * time.Minute),
		ProgressSeq:         1,
		ExecutionDeadlineAt: &healthyExecutionDeadline,
		ProgressDeadlineAt:  &stalledProgressDeadline,
		DecisionWindowUntil: &expiredWindow,
		MaxAttempts:         1,
		FencingToken:        1,
		DeclaredBudget:      time.Hour,
		Version:             1,
		CreatedAt:           now.Add(-time.Hour),
		UpdatedAt:           now.Add(-time.Hour),
	})
	require.NoError(t, err)
	require.True(t, created)

	supervisor := host.getLocalExecutionSupervisor()
	require.NotNil(t, supervisor)
	require.Equal(t, "enforce", supervisor.Config.Mode)
	decisions, err := supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, decisions, "the watchdog must judge the stalled run")

	fallback, err := runStore.GetExecutionRun(ctx, "run-metrics-fallback")
	require.NoError(t, err)
	require.Equal(t, supervision.RunStatusCancelRequested, fallback.Status)
	require.Equal(t, "decision_window_expired", fallback.CancelSource)

	// 2) 样本面：再补 11 条 run（9 条正常完成、1 条父/操作者主动取消、
	//    1 条 runtime 判的 progress_stalled 超时），凑出可复算的 12 条窗口。
	populations := []struct {
		runID        string
		status       string
		errorCode    string
		cancelSource string
	}{
		{runID: "run-metrics-1", status: supervision.RunStatusCompleted},
		{runID: "run-metrics-2", status: supervision.RunStatusCompleted},
		{runID: "run-metrics-3", status: supervision.RunStatusCompleted},
		{runID: "run-metrics-4", status: supervision.RunStatusCompleted},
		{runID: "run-metrics-5", status: supervision.RunStatusCompleted},
		{runID: "run-metrics-6", status: supervision.RunStatusCompleted},
		{runID: "run-metrics-7", status: supervision.RunStatusCompleted},
		{runID: "run-metrics-8", status: supervision.RunStatusCompleted},
		{runID: "run-metrics-9", status: supervision.RunStatusCompleted},
		{runID: "run-metrics-operator", status: supervision.RunStatusCanceled, errorCode: "operator_cancel", cancelSource: "operator_cancel"},
		{runID: "run-metrics-stalled", status: supervision.RunStatusTimedOut, errorCode: "progress_stalled", cancelSource: "progress_stalled"},
	}
	sessionIDs := []string{"metrics-worker-fallback"}
	for i, sample := range populations {
		sessionID := "metrics-worker-" + sample.runID
		created, err := runStore.CreateExecutionRun(ctx, supervision.ExecutionRun{
			RunID:           sample.runID,
			Kind:            supervision.RunKindAgentRun,
			Workflow:        supervision.RunWorkflowSpawnAgent,
			RootSessionID:   parent,
			ParentSessionID: parent,
			SessionID:       sessionID,
			AgentID:         sessionID,
			Status:          supervision.RunStatusRunning,
			OwnerID:         "host-1",
			StartedAt:       now,
			LastHeartbeatAt: now,
			LastProgressAt:  now,
			ProgressSeq:     1,
			MaxAttempts:     1,
			FencingToken:    int64(i + 2),
			DeclaredBudget:  time.Hour,
			Version:         1,
			CreatedAt:       now,
			UpdatedAt:       now,
		})
		require.NoError(t, err)
		require.True(t, created)
		if sample.cancelSource != "" {
			// 取消来源只能由 RequestExecutionCancel 写入（MarkExecutionRunTerminal
			// 只写 error_code，见 execution_store.go:412）：先请求取消，再落终态，
			// 与生产 watchdog 的两步一致。
			requested, err := runStore.RequestExecutionCancel(ctx, sample.runID, sample.cancelSource, 15*time.Second, now)
			require.NoError(t, err)
			require.True(t, requested)
		}
		finished, err := runStore.MarkExecutionRunTerminal(ctx, sample.runID, sample.status, sample.errorCode, "", now)
		require.NoError(t, err)
		require.True(t, finished)
		sessionIDs = append(sessionIDs, sessionID)
	}

	metrics := readRuntimeCancelMetrics(t, ctx, runStore, sessionIDs)
	require.Equal(t, 12, metrics.Total)
	require.Equal(t, 2, metrics.ForcedCancel, "runtime 判的终态：progress_stalled + decision_window_expired")
	require.Equal(t, 1, metrics.DecisionWindowExpired)
	require.InDelta(t, 2.0/12.0, metrics.ForcedCancelRatio(), 1e-9)
	require.Less(t, metrics.DecisionWindowExpiredRatio(), 0.10,
		"§7.3 指标 2：兜底占比必须低于 10%")
}

// runtimeForcedCancelSources 是「被 runtime 强制取消」的口径白名单：这些来源由
// watchdog / 执行看门狗自己判定（execution_supervisor.go 的 enforceRunCancel
// 与兜底分支），父会话或操作者的主动取消（operator_cancel / parent_cancel /
// user_cancel）不计入分子。
var runtimeForcedCancelSources = map[string]struct{}{
	"decision_window_expired": {},
	"progress_stalled":        {},
	"execution_deadline":      {},
	"execution_timed_out":     {},
}

type runtimeCancelMetrics struct {
	Total                 int
	ForcedCancel          int
	DecisionWindowExpired int
}

func (m runtimeCancelMetrics) ForcedCancelRatio() float64 {
	if m.Total == 0 {
		return 0
	}
	return float64(m.ForcedCancel) / float64(m.Total)
}

func (m runtimeCancelMetrics) DecisionWindowExpiredRatio() float64 {
	if m.Total == 0 {
		return 0
	}
	return float64(m.DecisionWindowExpired) / float64(m.Total)
}

// readRuntimeCancelMetrics 是 §7.3 指标 1-2 的读数：按子会话遍历窗口内的 run 行，
// 再按 CancelSource 分组计数。它只用 ExecutionRunStore 的现有读接口
// （ListExecutionRunsBySession），因此线上可直接复用同一口径做采样。
func readRuntimeCancelMetrics(t *testing.T, ctx context.Context, store supervision.ExecutionRunStore, sessionIDs []string) runtimeCancelMetrics {
	t.Helper()
	metrics := runtimeCancelMetrics{}
	for _, sessionID := range sessionIDs {
		runs, err := store.ListExecutionRunsBySession(ctx, sessionID, 50)
		require.NoError(t, err)
		for _, run := range runs {
			metrics.Total++
			source := strings.TrimSpace(run.CancelSource)
			if source == "" {
				continue
			}
			if source == "decision_window_expired" {
				metrics.DecisionWindowExpired++
			}
			if _, ok := runtimeForcedCancelSources[source]; ok {
				metrics.ForcedCancel++
			}
		}
	}
	return metrics
}
