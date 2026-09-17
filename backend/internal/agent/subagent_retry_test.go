package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

// TestRenderSubagentResultsCarriesRetryAdvice 锁定方案 §6.1：失败子代理的父级
// 上下文必须出现机器可读 retry_advice 与 failure_category。
func TestRenderSubagentResultsCarriesRetryAdvice(t *testing.T) {
	cases := []struct {
		name     string
		category string
		readOnly bool
		want     string
	}{
		{"timeout", llm.FailureCategoryTimeout, true, "retry_with_changed_inputs"},
		{"rate_limited", llm.FailureCategoryRateLimited, true, "retry_with_changed_inputs"},
		{"provider_error", llm.FailureCategoryProviderError, true, "retry_with_changed_inputs"},
		{"context_overflow", llm.FailureCategoryContextOverflow, true, "split_task"},
		{"budget_exceeded", llm.FailureCategoryBudgetExceeded, false, "split_task"},
		{"tool_error", llm.FailureCategoryToolError, true, "complete_locally_or_change_tool"},
		{"cancelled_read_only", llm.FailureCategoryCancelled, true, "retry_with_changed_inputs"},
		{"cancelled_write_task", llm.FailureCategoryCancelled, false, "complete_locally"},
		{"unknown", llm.FailureCategoryUnknown, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rendered := renderSubagentResults([]SubagentResult{{
				ID:              "sub-1",
				Success:         false,
				Summary:         "failed",
				Error:           "boom",
				FailureCategory: tc.category,
				RetryAdvice:     SubagentRetryAdvice(tc.category, tc.readOnly),
				Attempt:         2,
				MaxAttempts:     2,
			}})
			if tc.want == "" {
				if strings.Contains(rendered, "retry_advice=") {
					t.Fatalf("unknown 类别不应给出具体建议: %s", rendered)
				}
			} else if !strings.Contains(rendered, "retry_advice="+tc.want) {
				t.Fatalf("期望 retry_advice=%s，实际:\n%s", tc.want, rendered)
			}
			if !strings.Contains(rendered, "failure_category="+tc.category) {
				t.Fatalf("期望 failure_category=%s，实际:\n%s", tc.category, rendered)
			}
			if !strings.Contains(rendered, "attempts: 2/2") {
				t.Fatalf("重试后的失败应显示尝试次数:\n%s", rendered)
			}
		})
	}
}

// TestSubagentCompletedPayloadCarriesRetryAdvice 锁定 §6.1 第③条：建议同时
// 写入 subagent.completed 载荷（分析侧落 record_json）。
func TestSubagentCompletedPayloadCarriesRetryAdvice(t *testing.T) {
	result := SubagentResult{
		ID:              "sub-2",
		Success:         false,
		FailureCategory: llm.FailureCategoryTimeout,
		RetryAdvice:     SubagentRetryAdvice(llm.FailureCategoryTimeout, true),
	}
	if result.RetryAdvice != "retry_with_changed_inputs" {
		t.Fatalf("timeout 的 retry_advice 不符: %q", result.RetryAdvice)
	}
	if SubagentRetryAdvice(llm.FailureCategoryContextOverflow, true) != "split_task" {
		t.Fatalf("context_overflow 应建议 split_task")
	}
}

// TestSubagentAutoRetryOnlyTransientReadOnly 锁定 §6.2：只有只读任务 +
// 瞬时类别才自动重试；写任务与确定性失败一律不重试。
func TestSubagentAutoRetryOnlyTransientReadOnly(t *testing.T) {
	transient := SubagentFailureDisposition{Category: llm.FailureCategoryTimeout, Retryable: true}
	deterministic := SubagentFailureDisposition{Category: llm.FailureCategoryToolError, Retryable: false}
	readOnlyTask := SubagentTask{ID: "t-read", ReadOnly: true}
	writeTask := SubagentTask{ID: "t-write", ReadOnly: false}

	if !shouldAutoRetrySubagentTask(readOnlyTask, transient, 1, 2, nil) {
		t.Fatal("只读任务 + 瞬时失败应自动重试")
	}
	if shouldAutoRetrySubagentTask(writeTask, transient, 1, 2, nil) {
		t.Fatal("写任务不得自动重试（防重复副作用）")
	}
	if shouldAutoRetrySubagentTask(readOnlyTask, deterministic, 1, 2, nil) {
		t.Fatal("确定性失败不得自动重试")
	}
	if shouldAutoRetrySubagentTask(readOnlyTask, transient, 1, 2, context.Canceled) {
		t.Fatal("run 上下文已取消时不得自动重试")
	}
	if shouldAutoRetrySubagentTask(readOnlyTask, transient, 2, 2, nil) {
		t.Fatal("达到尝试上限后不得自动重试")
	}
}

// TestSubagentAutoRetryDisabledWhenMaxAttemptsIsOne 锁定 §6.2：配置 1 = 关闭。
func TestSubagentAutoRetryDisabledWhenMaxAttemptsIsOne(t *testing.T) {
	scheduler := NewSubagentScheduler(nil, SubagentSchedulerConfig{MaxAttemptsPerTask: 1})
	if got := scheduler.maxAttemptsPerTask(); got != 1 {
		t.Fatalf("MaxAttemptsPerTask=1 应被归一化为 1，实际 %d", got)
	}
	defaults := NewSubagentScheduler(nil, SubagentSchedulerConfig{})
	if got := defaults.maxAttemptsPerTask(); got != defaultSubagentMaxAttemptsPerTask {
		t.Fatalf("零值应使用默认 %d，实际 %d", defaultSubagentMaxAttemptsPerTask, got)
	}
	capped := NewSubagentScheduler(nil, SubagentSchedulerConfig{MaxAttemptsPerTask: 100})
	if got := capped.maxAttemptsPerTask(); got != 5 {
		t.Fatalf("上限应封顶 5，实际 %d", got)
	}
	if shouldAutoRetrySubagentTask(SubagentTask{ReadOnly: true}, SubagentFailureDisposition{Retryable: true}, 1, 1, nil) {
		t.Fatal("MaxAttemptsPerTask=1 时不得重试")
	}
}

// TestSubagentAutoRetryDoesNotCountTowardCircuitBreaker 锁定 §6.2 第④条：
// 自动重试链条对批次熔断只贡献**一个**最终结果（中间尝试不额外计入连续失败）。
func TestSubagentAutoRetryDoesNotCountTowardCircuitBreaker(t *testing.T) {
	scheduler := &SubagentScheduler{
		config: SubagentSchedulerConfig{MaxConsecutiveFailures: 2},
	}
	// 一个经历 2 次尝试仍失败的任务：批次结果列表长度仍为 1。
	results := []SubagentResult{{ID: "task-1", Success: false, Attempt: 2, MaxAttempts: 2}}
	scheduler.recordBatchOutcome(SubagentRunOptions{TraceID: "trace"}, results, nil)
	if scheduler.consecutiveFailures != 1 {
		t.Fatalf("一次重试链条只应计 1 次连续失败，实际 %d", scheduler.consecutiveFailures)
	}
	scheduler.recordBatchOutcome(SubagentRunOptions{TraceID: "trace"}, results, nil)
	if !scheduler.circuitOpen {
		t.Fatal("达到 MaxConsecutiveFailures 后熔断应打开")
	}
}

// TestClassifySubagentFailureMapsTransientCodes 锁定分类来源优先级：
// llm.ClassifyFailureCode 错误码 → D5 枚举；笼统码用错误文本兜底。
func TestClassifySubagentFailureMapsTransientCodes(t *testing.T) {
	timeout := ClassifySubagentFailure(errors.New("context deadline exceeded while awaiting response headers"))
	if timeout.Category != llm.FailureCategoryTimeout || !timeout.Retryable {
		t.Fatalf("deadline 错误应分类为 timeout 且可重试，实际 %+v", timeout)
	}
	rateLimited := ClassifySubagentFailure(errors.New("upstream 429 rate limit exceeded"))
	if rateLimited.Category != llm.FailureCategoryRateLimited || !rateLimited.Retryable {
		t.Fatalf("429 应分类为 rate_limited，实际 %+v", rateLimited)
	}
	deterministic := ClassifySubagentFailure(errors.New("permission denied by policy"))
	if deterministic.Category != llm.FailureCategoryToolError || deterministic.Retryable {
		t.Fatalf("权限拒绝应分类为 tool_error 且不可重试，实际 %+v", deterministic)
	}
	if empty := ClassifySubagentFailure(nil); empty.Category != llm.FailureCategoryUnknown {
		t.Fatalf("nil 错误应回落 unknown，实际 %+v", empty)
	}
}

// TestSubagentRetryBackoffBounded 指数退避 + jitter 必须有界（封顶 5s）。
func TestSubagentRetryBackoffBounded(t *testing.T) {
	for attempt := 1; attempt <= 10; attempt++ {
		delay := subagentRetryBackoff(attempt, 100*time.Millisecond, 5*time.Second)
		if delay <= 0 || delay > 5*time.Second {
			t.Fatalf("attempt=%d 退避越界: %v", attempt, delay)
		}
	}
}
