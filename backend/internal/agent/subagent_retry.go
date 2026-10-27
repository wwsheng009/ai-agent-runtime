package agent

import (
	"context"
	"errors"
	"math/rand"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// subagentTaskDeadlineCause is attached only to the scheduler-owned task
// timeout. A parent batch context may also end with DeadlineExceeded, but that
// must remain observable as batch_deadline rather than being mislabeled as the
// child task's own timeout.
var subagentTaskDeadlineCause = errors.New("subagent task deadline exceeded")

// SubagentFailureDisposition 是一次子代理失败的稳定分类结果（方案 D5/§6.1）。
type SubagentFailureDisposition struct {
	Category  string
	ErrorCode string
	// Retryable 仅表示"该类别属于瞬时故障"，是否真的自动重试还取决于
	// 任务是否只读与 MaxAttemptsPerTask（§6.2）。
	Retryable bool
}

// ClassifySubagentFailure 复用 llm.ClassifyFailureCode 的错误码分类，再映射到
// D5 失败分类枚举；不新建第二套分类。错误码过于笼统（如 UPSTREAM_ERROR）时
// 用错误文本关键词兜底提升到更具体的类别，仍无法判定则回落到 unknown。
func ClassifySubagentFailure(err error) SubagentFailureDisposition {
	if err == nil {
		return SubagentFailureDisposition{Category: llm.FailureCategoryUnknown}
	}
	text := strings.ToLower(err.Error())
	// Artifact scope denials come from GetOwned: the id is valid but belongs to
	// another lineage. Retrying the same child cannot succeed, and the generic
	// classifier would otherwise surface this as UPSTREAM_ERROR/provider_error.
	if strings.Contains(text, "out of scope") || strings.Contains(err.Error(), "不属于当前会话或父会话的读取范围") {
		return SubagentFailureDisposition{
			Category:  llm.FailureCategoryToolError,
			ErrorCode: "TOOL_EXECUTION",
			Retryable: false,
		}
	}
	code := strings.TrimSpace(llm.ClassifyFailureCode(err))
	category := llm.FailureCategoryFromErrorCode(code)
	if refined := llm.FailureCategoryFromErrorCode(err.Error()); refined != llm.FailureCategoryUnknown {
		if category == llm.FailureCategoryUnknown || category == llm.FailureCategoryProviderError {
			category = refined
		}
	}
	disposition := SubagentFailureDisposition{Category: category, ErrorCode: code}
	disposition.Retryable = IsTransientSubagentFailure(category)
	return disposition
}

// IsTransientSubagentFailure 报告分类是否属于可重试的瞬时集合（§6.2）。
func IsTransientSubagentFailure(category string) bool {
	switch llm.NormalizeFailureCategory(category) {
	case llm.FailureCategoryTimeout,
		llm.FailureCategoryRateLimited,
		llm.FailureCategoryProviderError,
		llm.FailureCategoryInterrupted:
		return true
	default:
		return false
	}
}

// shouldAutoRetrySubagentTask 是任务级自动重试的唯一判定入口（§6.2）：
// 只读 + 瞬时类别 + 还有尝试预算 + run 上下文未取消。写任务一律不自动重试。
func shouldAutoRetrySubagentTask(task SubagentTask, disposition SubagentFailureDisposition, attempt, maxAttempts int, ctxErr error) bool {
	if attempt >= maxAttempts {
		return false
	}
	if !task.ReadOnly {
		return false
	}
	if !disposition.Retryable {
		return false
	}
	return ctxErr == nil
}

// SubagentRetryAdvice 返回父代理可执行的机器可读建议（§6.1 映射表）。
// 空字符串表示"不给具体建议"，父代理应自行本地完成。
func SubagentRetryAdvice(category string, readOnly bool) string {
	switch llm.NormalizeFailureCategory(category) {
	case llm.FailureCategoryTimeout, llm.FailureCategoryRateLimited, llm.FailureCategoryProviderError:
		return "retry_with_changed_inputs"
	case llm.FailureCategoryContextOverflow, llm.FailureCategoryBudgetExceeded:
		return "split_task"
	case llm.FailureCategoryToolError:
		return "complete_locally_or_change_tool"
	case llm.FailureCategoryCancelled, llm.FailureCategoryInterrupted:
		if readOnly {
			return "retry_with_changed_inputs"
		}
		return "complete_locally"
	default:
		return ""
	}
}

// subagentCompletionReason 把失败分类映射到 completion_reason（§5.2 兼容口径）。
func subagentCompletionReason(success bool, disposition SubagentFailureDisposition) string {
	if success {
		return "completed"
	}
	switch disposition.Category {
	case llm.FailureCategoryTimeout:
		return "timeout"
	case llm.FailureCategoryCancelled:
		return "cancelled"
	case llm.FailureCategoryBudgetExceeded:
		return "budget_exceeded"
	case llm.FailureCategoryInterrupted:
		return "stopped"
	default:
		return "failed"
	}
}

// statusAliasForCompletionReason 生成 status 兼容别名（completed/failed/stopped），
// 与 §5.2 的读取侧映射表保持一致。
func statusAliasForCompletionReason(completionReason string, success bool) string {
	if success {
		return "completed"
	}
	switch strings.ToLower(strings.TrimSpace(completionReason)) {
	case "stopped", "cancelled", "interrupted":
		return "stopped"
	default:
		return "failed"
	}
}

// subagentRetryBackoff 返回第 attempt 次失败后的指数退避（含 jitter，封顶 5s）。
func subagentRetryBackoff(attempt int, base, max time.Duration) time.Duration {
	if base <= 0 {
		base = defaultSubagentRetryBaseDelay
	}
	if max <= 0 {
		max = defaultSubagentRetryMaxDelay
	}
	if attempt < 1 {
		attempt = 1
	}
	delay := base
	for i := 1; i < attempt && delay < max; i++ {
		delay *= 2
	}
	if delay > max {
		delay = max
	}
	// jitter: ±25%，避免多个子任务同频重试打满上游；jitter 后仍需封顶。
	ceiling := int64(delay)
	jitterRange := ceiling / 2
	if jitterRange > 0 {
		delta := rand.Int63n(jitterRange) - jitterRange/2
		ceiling += delta
	}
	if ceiling > int64(max) {
		ceiling = int64(max)
	}
	if ceiling <= 0 {
		ceiling = int64(base)
	}
	return time.Duration(ceiling)
}

// sleepWithContext 等待退避时长；ctx 取消时立即返回 false。
func sleepWithContext(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return ctx == nil || ctx.Err() == nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	if ctx == nil {
		<-timer.C
		return true
	}
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// subagentRetryBudgetAvailable prevents starting a retry when the task has
// less time left than the backoff itself. It is intentionally conservative:
// the provider call still owns its own timeout, but sleeping through the
// remaining task deadline can never produce a useful second attempt.
func subagentRetryBudgetAvailable(ctx context.Context, backoff time.Duration) bool {
	if ctx == nil {
		return true
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return true
	}
	return time.Until(deadline) > backoff
}

func subagentRemainingMS(ctx context.Context) int64 {
	if ctx == nil {
		return 0
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return 0
	}
	remaining := time.Until(deadline).Milliseconds()
	if remaining <= 0 {
		return 0
	}
	return remaining
}

// subagentRetryBudgetMS 返回扣除 backoff 后仍可用于下一次 attempt 的毫秒
// 预算；0 表示没有可观测的 deadline 或预算已不足。与
// subagentRetryBudgetAvailable 的 remaining > backoff 放行口径一致；无
// deadline 时重试仍会被放行，此字段只表达"可观测到的剩余预算"（§6.1-1）。
func subagentRetryBudgetMS(ctx context.Context, backoff time.Duration) int64 {
	remaining := subagentRemainingMS(ctx)
	if remaining <= 0 {
		return 0
	}
	if budget := remaining - backoff.Milliseconds(); budget > 0 {
		return budget
	}
	return 0
}

// contextDeadlinePtr 返回 ctx 的 deadline 绝对时刻；nil 表示未配置（不可观测）。
func contextDeadlinePtr(ctx context.Context) *time.Time {
	if ctx == nil {
		return nil
	}
	if deadline, ok := ctx.Deadline(); ok {
		return &deadline
	}
	return nil
}

// subagentAttemptTiming 是一次 attempt 的时序与预算观测快照（§6.1-1）。
// StartedAt/Deadline/BatchDeadline 为 nil 表示不可观测（未配置或未起跑）；
// RetryBudgetMS 为 0 表示不可观测或预算已不足。
type subagentAttemptTiming struct {
	StartedAt     *time.Time
	Deadline      *time.Time
	BatchDeadline *time.Time
	RetryBudgetMS int64
}

// captureSubagentAttemptTiming 从 attempt 起跑时刻与 parent/child 上下文做
// 一次性快照：Deadline 取 route timeout（childCtx），BatchDeadline 取父级
// batch 上下文；RetryBudgetMS 用同一 backoff 公式推导。
func captureSubagentAttemptTiming(startedAt time.Time, parentCtx, childCtx context.Context, backoff time.Duration) subagentAttemptTiming {
	timing := subagentAttemptTiming{
		Deadline:      contextDeadlinePtr(childCtx),
		BatchDeadline: contextDeadlinePtr(parentCtx),
	}
	if !startedAt.IsZero() {
		value := startedAt
		timing.StartedAt = &value
	}
	timing.RetryBudgetMS = subagentRetryBudgetMS(childCtx, backoff)
	return timing
}

// rfc3339OrEmptyTime 把可空时刻渲染为 UTC RFC3339；nil/零值返回空串，让
// 读取侧区分"未观测"与"已观测"（与 remaining_ms 的 0 语义一致）。
func rfc3339OrEmptyTime(value *time.Time) string {
	if value == nil || value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

// maxAttemptsPerTask 归一化任务级重试上限：0 → 默认 2；1 → 关闭；
// 上限封顶 5（避免配置失误造成无界重试放大）。
func (s *SubagentScheduler) maxAttemptsPerTask() int {
	if s == nil {
		return defaultSubagentMaxAttemptsPerTask
	}
	attempts := s.config.MaxAttemptsPerTask
	switch {
	case attempts <= 0:
		return defaultSubagentMaxAttemptsPerTask
	case attempts == 1:
		return 1
	case attempts > 5:
		return 5
	default:
		return attempts
	}
}

// emitSubagentAttemptEvent 为一次中间失败尝试发 subagent.completed
// （attempt 可见，intermediate_attempt=true 供分析侧计入 conflict_count）。
func (s *SubagentScheduler) emitSubagentAttemptEvent(
	childSessionID string,
	options SubagentRunOptions,
	task SubagentTask,
	childAgentName string,
	spec ChildAgentSpec,
	attempt int,
	maxAttempts int,
	disposition SubagentFailureDisposition,
	runErr error,
	result *Result,
	timing subagentAttemptTiming,
) {
	if s == nil || s.parent == nil {
		return
	}
	errText := ""
	if runErr != nil {
		errText = runErr.Error()
	}
	attemptUsage := (*types.TokenUsage)(nil)
	if result != nil {
		attemptUsage = result.Usage
	}
	s.parent.emitRuntimeEvent("subagent.completed", childSessionID, "", mergeRouteAuditPayload(map[string]interface{}{
		"subagent_id":             task.ID,
		"role":                    task.Role,
		"task_type":               task.TaskType,
		"task_subject":            task.TaskSubject,
		"read_only":               task.ReadOnly,
		"success":                 false,
		"status":                  "failed",
		"completion_reason":       subagentCompletionReason(false, disposition),
		"failure_category":        disposition.Category,
		"error_code":              disposition.ErrorCode,
		"retryable":               disposition.Retryable,
		"attempt":                 attempt,
		"max_attempts":            maxAttempts,
		"retry_reason":            disposition.Category,
		"intermediate_attempt":    true,
		"source":                  "scheduler",
		"batch_id":                options.BatchID,
		"error":                   errText,
		"budget_tokens":           task.BudgetTokens,
		"parent_session_id":       options.ParentSessionID,
		"parent_tool_call_id":     options.ParentToolCallID,
		"child_agent_name":        childAgentName,
		"trace_id":                options.TraceID,
		"partial_output":          hasSubagentPartialOutput(result),
		"usage_total_tokens":      usageTotal(attemptUsage),
		"usage_prompt_tokens":     usagePrompt(attemptUsage),
		"usage_completion_tokens": usageCompletion(attemptUsage),
		"usage_cache_read_tokens": usageCacheRead(attemptUsage),
		"attempt_started_at":      rfc3339OrEmptyTime(timing.StartedAt),
		"attempt_deadline":        rfc3339OrEmptyTime(timing.Deadline),
		"batch_deadline":          rfc3339OrEmptyTime(timing.BatchDeadline),
		"retry_budget_ms":         timing.RetryBudgetMS,
	}, spec.Decision))
}
