package llm

import (
	"context"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// AttemptUsageTracker 收集「已收到 provider 上报 usage、但该次尝试被判为失败/重放」
// 的 token 用量（P0-3 item 4）。这些用量此前被直接丢弃：只在最终成功的那一次
// 记账，导致退化重放（reasoning-only 烧满预算、参数非法重采样）真实消耗的 token
// 既不计入用量台账、也不计入 run 预算。
//
// 只接受 provider 上报值：调用方必须先用 extractUsageFromResponseBody 之类
// 的纯解析路径取值，禁止把本地估算写进台账（估算值会污染缓存命中率与预算核算）。
type AttemptUsageTracker struct {
	mu       sync.Mutex
	usage    types.TokenUsage
	attempts int
}

// NewAttemptUsageTracker 构造空账本。
func NewAttemptUsageTracker() *AttemptUsageTracker {
	return &AttemptUsageTracker{}
}

type attemptUsageTrackerKey struct{}

// WithAttemptUsageTracker 把账本挂到 ctx 上，供 provider/gateway 重试循环写入。
func WithAttemptUsageTracker(ctx context.Context, tracker *AttemptUsageTracker) context.Context {
	if ctx == nil || tracker == nil {
		return ctx
	}
	return context.WithValue(ctx, attemptUsageTrackerKey{}, tracker)
}

// AttemptUsageTrackerFromContext 读取账本；未挂载时返回 nil（调用方按旧行为处理）。
func AttemptUsageTrackerFromContext(ctx context.Context) *AttemptUsageTracker {
	if ctx == nil {
		return nil
	}
	tracker, _ := ctx.Value(attemptUsageTrackerKey{}).(*AttemptUsageTracker)
	return tracker
}

// Record 累加一次被判失败/重放的尝试用量。usage 为 nil 时为空操作。
func (t *AttemptUsageTracker) Record(usage *types.TokenUsage) {
	if t == nil || usage == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.usage.PromptTokens += usage.PromptTokens
	t.usage.CompletionTokens += usage.CompletionTokens
	t.usage.TotalTokens += usage.TotalTokens
	t.usage.CachedTokens += usage.CachedTokens
	t.usage.CacheReadTokens += usage.CacheReadTokens
	t.usage.CacheCreationTokens += usage.CacheCreationTokens
	t.usage.ReasoningTokens += usage.ReasoningTokens
	t.usage.CacheReadReported = t.usage.CacheReadReported || usage.CacheReadReported
	t.usage.CacheCreationReported = t.usage.CacheCreationReported || usage.CacheCreationReported
	if t.usage.TotalTokens == 0 {
		t.usage.TotalTokens = t.usage.PromptTokens + t.usage.CompletionTokens
	}
	t.attempts++
}

// Usage 返回累计用量的副本；没有记录时返回 nil。
func (t *AttemptUsageTracker) Usage() *types.TokenUsage {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.attempts == 0 {
		return nil
	}
	clone := t.usage
	return &clone
}

// Attempts 返回被记账的失败/重放尝试次数。
func (t *AttemptUsageTracker) Attempts() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.attempts
}

// RecordDiscardedAttemptUsage 从一次失败尝试的原始响应体里提取 provider 上报的
// usage 并记入 ctx 上的账本。提取不到（transport/HTTP 错误、无 usage 字段）时
// 不写任何值，绝不回退到本地估算。返回是否记录成功，便于测试断言。
func RecordDiscardedAttemptUsage(ctx context.Context, responseBody []byte) bool {
	tracker := AttemptUsageTrackerFromContext(ctx)
	if tracker == nil || len(responseBody) == 0 {
		return false
	}
	usage := extractUsageFromResponseBody(responseBody)
	if usage == nil {
		return false
	}
	tracker.Record(usage)
	return true
}

// RecordDiscardedChatUsage 记录非流式路径上被判失败/重放的尝试用量。Usage 来自
// 已解码的 wire 响应（provider 上报），全 0 时不写入。返回是否记录成功。
func RecordDiscardedChatUsage(ctx context.Context, resp *ChatResponse) bool {
	tracker := AttemptUsageTrackerFromContext(ctx)
	if tracker == nil || resp == nil {
		return false
	}
	wire := resp.Usage
	if wire.PromptTokens == 0 && wire.CompletionTokens == 0 && wire.TotalTokens == 0 && wire.ReasoningTokens == 0 {
		return false
	}
	total := wire.TotalTokens
	if total == 0 {
		total = wire.PromptTokens + wire.CompletionTokens
	}
	tracker.Record(&types.TokenUsage{
		PromptTokens:          wire.PromptTokens,
		CompletionTokens:      wire.CompletionTokens,
		TotalTokens:           total,
		CachedTokens:          wire.CachedTokens,
		CacheReadTokens:       wire.CacheReadTokens,
		CacheCreationTokens:   wire.CacheCreationTokens,
		CacheReadReported:     wire.CacheReadReported,
		CacheCreationReported: wire.CacheCreationReported,
		ReasoningTokens:       wire.ReasoningTokens,
	})
	return true
}
