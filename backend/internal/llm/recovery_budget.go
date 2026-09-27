package llm

import (
	"context"
	"sync"
)

// DefaultDegenerateRecoveryBudget 是本 run 内「退化恢复重放」的默认共享配额
// （P0-3 item 3）。provider 层与 agent 层各自的重放门控是独立的，同一次退化
// 事件会在两层相乘：provider 同预算重采样（1 次）+ 退化输出连续重放（最多 3 次）
// + agent 层 malformed 反馈（同工具名 2 次）+ agent 层 reasoning-only 反馈
// （2 次），单条路径上限可达 9～18 次 HTTP 尝试。共享配额把「整 run 的退化重放
// 总次数」收敛为一个数：任一层消耗后，另一层立即看到剩余额度。
//
// 取 3 的依据：正常恢复路径是「1 次同预算重采样 + 1 次 prompt 改变的反馈回注」，
// 留 1 次余量覆盖跨 step 的二次反馈；继续重放同一 prompt 无法改变退化样本。
const DefaultDegenerateRecoveryBudget = 3

// DegenerateRecoveryBudget 是跨层共享的可变配额。零值不可用，必须用
// NewDegenerateRecoveryBudget 构造；所有方法并发安全（provider 重试循环与
// agent 反馈路径可能在不同 goroutine）。
type DegenerateRecoveryBudget struct {
	mu   sync.Mutex
	used int
	max  int
}

// NewDegenerateRecoveryBudget 构造共享配额；max<=0 取默认值。
func NewDegenerateRecoveryBudget(max int) *DegenerateRecoveryBudget {
	if max <= 0 {
		max = DefaultDegenerateRecoveryBudget
	}
	return &DegenerateRecoveryBudget{max: max}
}

type degenerateRecoveryBudgetKey struct{}

// WithDegenerateRecoveryBudget 把共享配额挂到 ctx 上，供 provider 重试循环读取。
// nil ctx/配额原样返回，调用方无需判空。
func WithDegenerateRecoveryBudget(ctx context.Context, budget *DegenerateRecoveryBudget) context.Context {
	if ctx == nil || budget == nil {
		return ctx
	}
	return context.WithValue(ctx, degenerateRecoveryBudgetKey{}, budget)
}

// DegenerateRecoveryBudgetFromContext 读取共享配额；未挂载时返回 nil。
func DegenerateRecoveryBudgetFromContext(ctx context.Context) *DegenerateRecoveryBudget {
	if ctx == nil {
		return nil
	}
	budget, _ := ctx.Value(degenerateRecoveryBudgetKey{}).(*DegenerateRecoveryBudget)
	return budget
}

// Allow 消耗一次配额并报告是否成功。false 表示本 run 的退化恢复已用尽，
// 调用方必须停止重放并把错误交回上层。
func (b *DegenerateRecoveryBudget) Allow() bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.used >= b.max {
		return false
	}
	b.used++
	return true
}

// Used 返回已消耗次数。
func (b *DegenerateRecoveryBudget) Used() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used
}

// Max 返回配额上限。
func (b *DegenerateRecoveryBudget) Max() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.max
}

// Remaining 返回剩余配额（不消耗）。
func (b *DegenerateRecoveryBudget) Remaining() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.max - b.used
	if remaining < 0 {
		remaining = 0
	}
	return remaining
}

// isDegenerateRecoveryReason 报告错误类别是否属于「同 prompt 重放无法改变样本」
// 的退化恢复重放。真截断（有截断证据的 invalid_tool_arguments / truncated_tool_call）
// 走预算扩容路径，不计入共享配额；transport/5xx/429 等需要真实重试的类别同样
// 不受配额约束。
func isDegenerateRecoveryReason(err error) bool {
	if err == nil {
		return false
	}
	switch classifyRetryableLLMError(err).Reason {
	case "invalid_tool_arguments":
		return !hasToolCallTruncationEvidence(err)
	case "reasoning_only_empty_reply", "empty_reply":
		return true
	default:
		return false
	}
}

// consumeDegenerateRecoveryBudget 在错误属于退化恢复类别时消耗一次共享配额。
// 返回 false 表示「配额已用尽，必须停止重放」；错误类别不需要配额时返回 true
// （调用方保持原有重试决策，不受共享配额影响）。
func consumeDegenerateRecoveryBudget(ctx context.Context, err error) bool {
	if !isDegenerateRecoveryReason(err) {
		return true
	}
	budget := DegenerateRecoveryBudgetFromContext(ctx)
	if budget == nil {
		return true
	}
	return budget.Allow()
}
