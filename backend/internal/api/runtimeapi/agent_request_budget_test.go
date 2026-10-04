package runtimeapi

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

// budgetStubProvider 只实现预算解析面；其余 llm.Provider 方法由内嵌接口占位，
// 本测试路径不会触达它们。
type budgetStubProvider struct {
	llm.Provider
	budget int
}

func (p *budgetStubProvider) Name() string { return "budget-stub" }

func (p *budgetStubProvider) ResolveRequestMaxTokens(model string) (int, bool) {
	return p.budget, true
}

// noBudgetStubProvider 模拟未实现预算解析面的既有 provider：必须返回 0，让
// 调用方保持既有兜底，而不是把别的 provider 的预算错配过来。
type noBudgetStubProvider struct{ llm.Provider }

func (p *noBudgetStubProvider) Name() string { return "no-budget-stub" }

func TestResolveAgentRequestMaxTokens(t *testing.T) {
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "budget-probe", MaxRetries: 0})
	if err := runtime.RegisterProvider("budget-provider", &budgetStubProvider{budget: 32000}); err != nil {
		t.Fatalf("register budget provider: %v", err)
	}
	if got := resolveAgentRequestMaxTokens(runtime, "budget-provider", "budget-probe"); got != 32000 {
		t.Fatalf("resolveAgentRequestMaxTokens = %d, want 32000", got)
	}

	if err := runtime.RegisterProvider("plain-provider", &noBudgetStubProvider{}); err != nil {
		t.Fatalf("register plain provider: %v", err)
	}
	if got := resolveAgentRequestMaxTokens(runtime, "plain-provider", "budget-probe"); got != 0 {
		t.Fatalf("provider without a budget resolver = %d, want 0", got)
	}
	if got := resolveAgentRequestMaxTokens(runtime, "missing-provider", "budget-probe"); got != 0 {
		t.Fatalf("unknown provider = %d, want 0", got)
	}
	if got := resolveAgentRequestMaxTokens(nil, "budget-provider", "budget-probe"); got != 0 {
		t.Fatalf("nil runtime = %d, want 0", got)
	}
}
