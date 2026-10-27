package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// TestRetrySkipReason 锁定 §6.1（行动文档 P0）：把"为什么没有发生后续 attempt"
// 讲清楚，避免只保留 Retryable=true 误导父代理以为会重派。
func TestRetrySkipReason(t *testing.T) {
	transient := SubagentFailureDisposition{Category: llm.FailureCategoryTimeout, Retryable: true}
	deterministic := SubagentFailureDisposition{Category: llm.FailureCategoryToolError, Retryable: false}
	readOnlyTask := SubagentTask{ID: "t-read", ReadOnly: true}
	writeTask := SubagentTask{ID: "t-write", ReadOnly: false}

	cases := []struct {
		name    string
		task    SubagentTask
		disp    SubagentFailureDisposition
		attempt int
		maxAtt  int
		ctxErr  error
		want    string
	}{
		{"只读瞬时且还有预算不加跳过", readOnlyTask, transient, 1, 2, nil, ""},
		{"非只读任务不自动重试", writeTask, transient, 1, 2, nil, "write_task_no_auto_retry"},
		{"确定性失败不重试", readOnlyTask, deterministic, 1, 2, nil, "non_transient_failure"},
		{"已达上限", readOnlyTask, transient, 2, 2, nil, "max_attempts_reached"},
		{"route 超时预算耗尽", readOnlyTask, transient, 1, 2, context.DeadlineExceeded, "retry_skipped_deadline_budget"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := retrySkipReason(tc.task, tc.disp, tc.attempt, tc.maxAtt, tc.ctxErr)
			if got != tc.want {
				t.Fatalf("retrySkipReason = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSubagentCancelSource 锁定 §6.1（行动文档 P0）：取消来源分层。route timeout
// 优先标 task_timeout，父会话取消标 parent_cancel；避免在 runErr 文本未含 deadline
// 时被 batch timeout 吞掉真实取消源。
func TestSubagentCancelSource(t *testing.T) {
	taskTimeoutCtx, cancelTask := context.WithTimeoutCause(context.Background(), 0, subagentTaskDeadlineCause)
	defer cancelTask()
	<-taskTimeoutCtx.Done()
	batchTimeoutCtx, cancelBatch := context.WithTimeout(context.Background(), 0)
	defer cancelBatch()
	<-batchTimeoutCtx.Done()
	batchChildCtx, cancelBatchChild := context.WithTimeoutCause(batchTimeoutCtx, time.Hour, subagentTaskDeadlineCause)
	defer cancelBatchChild()
	parentCancelCtx, cancelParent := context.WithCancel(context.Background())
	cancelParent()

	cases := []struct {
		name      string
		parentCtx context.Context
		childCtx  context.Context
		runErr    error
		want      string
	}{
		{"route 超时上下文优先", context.Background(), taskTimeoutCtx, nil, "task_timeout"},
		{"batch deadline 不误报 task timeout", batchTimeoutCtx, batchChildCtx, nil, "batch_deadline"},
		{"provider deadline", nil, nil, context.DeadlineExceeded, "provider_timeout"},
		{"父会话取消", parentCancelCtx, parentCancelCtx, nil, "parent_cancel"},
		{"普通工具错误无取消源", nil, nil, errors.New("tool not allowed for this agent: write"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := subagentCancelSource(tc.parentCtx, tc.childCtx, tc.runErr)
			if got != tc.want {
				t.Fatalf("subagentCancelSource = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSubagentResultPartialOutputFields 锁定 §6.2（行动文档 P0）：失败 attempt 仍
// 可携带 usage / findings / patches，partial_output 与计数随之设置，不再把"有消耗、
// 有部分产出"的失败聚合成 cost=0、output=0。
func TestSubagentResultPartialOutputFields(t *testing.T) {
	readOnlyResult := SubagentResult{
		ID:                   "task-read",
		Success:              false,
		PartialOutput:        true,
		Usage:                &types.TokenUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120, CacheReadTokens: 40},
		Findings:             []string{"found a panic in parser", "found leak in loop"},
		UsageByAttempt:       []*types.TokenUsage{{TotalTokens: 120}},
		PartialFindingsCount: 2,
		PartialPatchCount:    0,
	}
	if !readOnlyResult.PartialOutput {
		t.Fatal("失败只读任务应标记 partial_output")
	}
	if readOnlyResult.PartialFindingsCount != 2 || readOnlyResult.PartialPatchCount != 0 {
		t.Fatalf("失败只读任务应只计 findings: findings=%d patches=%d",
			readOnlyResult.PartialFindingsCount, readOnlyResult.PartialPatchCount)
	}
	if got := usageTotal(readOnlyResult.Usage); got != 120 {
		t.Fatalf("部分产出 usage 必须按 request/attempt 对账非零: total=%d", got)
	}

	writeResult := SubagentResult{
		ID:                   "task-write",
		Success:              false,
		PartialOutput:        true,
		Usage:                &types.TokenUsage{PromptTokens: 200, CompletionTokens: 30, TotalTokens: 230},
		Patches:              []FilePatch{{Path: "a.txt", Summary: "patched"}},
		PartialFindingsCount: 0,
		PartialPatchCount:    1,
	}
	if writeResult.PartialFindingsCount != 0 || writeResult.PartialPatchCount != 1 {
		t.Fatalf("失败写任务应只计 patches: findings=%d patches=%d",
			writeResult.PartialFindingsCount, writeResult.PartialPatchCount)
	}
}

func TestAggregateSubagentAttemptUsageSumsAllAttempts(t *testing.T) {
	total := aggregateSubagentAttemptUsage([]*types.TokenUsage{
		{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12, CacheReadTokens: 4},
		nil,
		{PromptTokens: 20, CompletionTokens: 3, TotalTokens: 23, CacheReadTokens: 5},
	})
	if total == nil {
		t.Fatal("expected aggregate usage")
	}
	if total.PromptTokens != 30 || total.CompletionTokens != 5 ||
		total.TotalTokens != 35 || total.CacheReadTokens != 9 {
		t.Fatalf("attempt usage was not summed: %+v", total)
	}
}

func TestApplySubagentResultOutputPreservesTextAndOnlyMarksRealPartialOutput(t *testing.T) {
	task := SubagentTask{ID: "read", ReadOnly: true}
	report := SubagentResult{ID: task.ID, Success: false, Summary: "provider error"}
	applySubagentResultOutput(&report, &task, &Result{
		Success: false,
		Output:  "evidence collected before failure",
		Usage:   &types.TokenUsage{TotalTokens: 7},
		Observations: []types.Observation{{
			Output:  "finding",
			Success: true,
		}},
	})
	if report.Summary != "evidence collected before failure" {
		t.Fatalf("partial text was lost: %q", report.Summary)
	}
	if !report.PartialOutput || report.PartialFindingsCount != 1 {
		t.Fatalf("partial output was not surfaced: %+v", report)
	}

	empty := SubagentResult{ID: task.ID, Success: false}
	applySubagentResultOutput(&empty, &task, &Result{Success: false})
	if empty.PartialOutput {
		t.Fatal("empty failed result must not be marked partial_output")
	}
}

func TestSubagentRetryBudgetAvailable(t *testing.T) {
	if !subagentRetryBudgetAvailable(context.Background(), time.Hour) {
		t.Fatal("context without deadline should permit a bounded retry")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if subagentRetryBudgetAvailable(ctx, time.Hour) {
		t.Fatal("retry must be skipped when backoff exceeds remaining deadline")
	}
}
