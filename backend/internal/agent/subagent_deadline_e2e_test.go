package agent

// subagent_deadline_e2e_test.go — §8.1/§13.4：task/batch deadline 档 E2E 注入。
//
// 既有单测（TestSubagentCancelSource、TestSubagentRetryBudget*）只锁函数分支；
// 这里用真实调度器 + 真实 deadline + 阻塞 provider，验证「运行中被 deadline
// 打断」时的归因（cancel_source）、重试闸门（retry_skipped）与批次终态，
// 确认 deadline 不会被吞成 provider 归因或静默重试。

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

// deadlineBlockingChildProvider：子请求阻塞到 ctx 结束（真实 deadline 触发取消）
// 并原样返回 ctx.Err()（保留 context cause，供归属分层）；父请求走脚本队列。
type deadlineBlockingChildProvider struct {
	*SequenceLLMProvider
	mu      sync.Mutex
	calls   int
	entered chan struct{}
}

func newDeadlineBlockingChildProvider() *deadlineBlockingChildProvider {
	return &deadlineBlockingChildProvider{
		SequenceLLMProvider: &SequenceLLMProvider{name: "test-provider"},
		entered:             make(chan struct{}, 4),
	}
}

func (p *deadlineBlockingChildProvider) Call(ctx context.Context, req *llm.LLMRequest) (*llm.LLMResponse, error) {
	if !isSubagentChildRequest(req) {
		return p.SequenceLLMProvider.Call(ctx, req)
	}
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	select {
	case p.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (p *deadlineBlockingChildProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// deadlineE2EFirstAttemptFinished 取第一条 attempt.finished 事件载荷。
func deadlineE2EFirstAttemptFinished(events []recordedLifecycleEvent) map[string]interface{} {
	for _, event := range events {
		if event.eventType == runtimeevents.EventSubagentAttemptFinished {
			return event.payload
		}
	}
	return nil
}

// TestTaskDeadlineE2EAttributionAndRetryGate 锁定 §8.1/§6.1-3：任务自身 deadline
// （TimeoutSec=1）在真实调度器上到期后：
//   - attempt 1 终态事件给出 cancel_source=task_timeout（不是 provider/batch 归因）；
//   - deadline 耗尽后重试闸门显式记录跳过（retry_skipped_*），且不再发起 attempt 2；
//   - 批次与任务行落到可解释的终态（批次未自身超时 → failed）。
func TestTaskDeadlineE2EAttributionAndRetryGate(t *testing.T) {
	rig := newAttemptEventRig(t)
	llmRuntime := llm.NewLLMRuntime(nil)
	llmRuntime.UpdateRetryConfig(0, llm.RetryTuning{}, nil)
	provider := newDeadlineBlockingChildProvider()
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))
	// 子代理走 agent.llmRuntime（child_factory 取 parent.llmRuntime）；本例不经父
	// turn，故直接装配同一 runtime。
	rig.agent.llmRuntime = llmRuntime

	coordinator := rig.agent.GetSubagentBatchCoordinator()
	require.NotNil(t, coordinator)
	batch, err := coordinator.StartBackground(context.Background(), BatchStartOptions{
		ParentSessionID: "parent-task-deadline",
		RootScopeID:     "parent-task-deadline",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
	}, []SubagentTask{
		{ID: "task-deadline-child", Goal: "Wait past the task deadline", ReadOnly: true, TimeoutSec: 1},
	})
	require.NoError(t, err)
	require.NotNil(t, batch)
	rig.waitTerminal(t)

	finished := deadlineE2EFirstAttemptFinished(rig.snapshot())
	require.NotNil(t, finished, "attempt 1 必须产生 finished 事件")
	require.Equal(t, 1, finished["attempt"])
	require.Equal(t, false, finished["success"])
	require.Equal(t, "task_timeout", finished["cancel_source"],
		"任务自身 deadline 先到时必须归因 task_timeout（不得吞成 provider/batch）")
	require.NotEmpty(t, finished["attempt_deadline"], "任务 deadline 必须已装配")
	retrySkipped, _ := finished["retry_skipped"].(string)
	require.True(t, strings.HasPrefix(retrySkipped, "retry_skipped_"),
		"deadline 归因的取消必须显式记录跳过原因，实际 retry_skipped=%q", retrySkipped)
	require.Equal(t, 1, provider.callCount(),
		"deadline 耗尽后不得再发起第二次 provider 调用（每次 attempt 一次）")

	stored, err := coordinator.Get(context.Background(), batch.BatchID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, subagentbatch.BatchFailed, stored.Status,
		"批次自身未超时：任务失败应落 failed 而不是 timed_out")
	tasks, err := coordinator.Store().ListTasks(context.Background(), batch.BatchID)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	// 取消的子级仍产出报告（会话引用可回收部分产出）→ 任务行是
	// failed_with_result 而不是裸 failed/timed_out；deadline 归因在载荷层。
	require.Equal(t, subagentbatch.TaskFailedWithResult, tasks[0].Status)
}

// TestBatchDeadlineE2EAttribution 锁定 §8.1/§6.1-3：批次级 deadline 到期后，
// 运行中的任务归因为 batch_deadline（不得误报 task_timeout——任务自身没有
// deadline），批次终态为 timed_out。
func TestBatchDeadlineE2EAttribution(t *testing.T) {
	rig := newAttemptEventRig(t)
	llmRuntime := llm.NewLLMRuntime(nil)
	llmRuntime.UpdateRetryConfig(0, llm.RetryTuning{}, nil)
	provider := newDeadlineBlockingChildProvider()
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))
	rig.agent.llmRuntime = llmRuntime

	coordinator := rig.agent.GetSubagentBatchCoordinator()
	require.NotNil(t, coordinator)
	batch, err := coordinator.StartBackground(context.Background(), BatchStartOptions{
		ParentSessionID: "parent-batch-deadline",
		RootScopeID:     "parent-batch-deadline",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		BatchDeadline:   time.Now().Add(1 * time.Second),
	}, []SubagentTask{
		{ID: "batch-deadline-child", Goal: "Wait past the batch deadline", ReadOnly: true},
	})
	require.NoError(t, err)
	require.NotNil(t, batch)
	rig.waitTerminal(t)

	finished := deadlineE2EFirstAttemptFinished(rig.snapshot())
	require.NotNil(t, finished, "attempt 1 必须产生 finished 事件")
	require.Equal(t, false, finished["success"])
	require.Equal(t, "batch_deadline", finished["cancel_source"],
		"批次 deadline 先到时必须归因 batch_deadline（不得误报 task_timeout）")
	retrySkipped, _ := finished["retry_skipped"].(string)
	require.True(t, strings.HasPrefix(retrySkipped, "retry_skipped_"),
		"batch deadline 归因的取消必须显式记录跳过原因，实际 retry_skipped=%q", retrySkipped)
	require.Equal(t, 1, provider.callCount(),
		"批次 deadline 到期后不得再发起第二次 provider 调用")

	stored, err := coordinator.Get(context.Background(), batch.BatchID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, subagentbatch.BatchTimedOut, stored.Status,
		"批次自身 deadline 到期必须落 timed_out")
	tasks, err := coordinator.Store().ListTasks(context.Background(), batch.BatchID)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, subagentbatch.TaskFailedWithResult, tasks[0].Status)
}
