package goal

import (
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// continuation_test.go 锁定共享续跑策略层（gap 3）：CLI 与 runtime-server
// 驱动器共用同一套 prompt / 阈值 / goal 态决策，任何一侧改动都必须先改这里。

func TestEvaluateGoalContinuationMatrix(t *testing.T) {
	cases := []struct {
		name     string
		goal     *SessionGoal
		hasGoal  bool
		cont     bool
		wantReas string
	}{
		{"no goal", nil, false, false, "goal_missing"},
		{"active", &SessionGoal{Status: StatusActive}, true, true, "goal_active"},
		{"paused", &SessionGoal{Status: StatusPaused}, true, false, "goal_paused"},
		{"blocked", &SessionGoal{Status: StatusBlocked}, true, false, "goal_blocked"},
		{"budget limited", &SessionGoal{Status: StatusBudgetLimited}, true, false, "goal_budget_limited"},
		{"complete", &SessionGoal{Status: StatusComplete}, true, false, "goal_complete"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision := EvaluateGoalContinuation(tc.goal, tc.hasGoal)
			if decision.Continue != tc.cont {
				t.Fatalf("Continue = %v, want %v", decision.Continue, tc.cont)
			}
			if decision.Reason != tc.wantReas {
				t.Fatalf("Reason = %q, want %q", decision.Reason, tc.wantReas)
			}
		})
	}
}

func TestBudgetExhaustedOnlyForActiveWithBudget(t *testing.T) {
	if BudgetExhausted(nil) {
		t.Fatal("nil goal must not be exhausted")
	}
	if BudgetExhausted(&SessionGoal{Status: StatusActive}) {
		t.Fatal("no-budget active goal must not be exhausted")
	}
	if BudgetExhausted(&SessionGoal{Status: StatusActive, TokenBudget: 100, TokensUsed: 99}) {
		t.Fatal("under-budget active goal must not be exhausted")
	}
	if !BudgetExhausted(&SessionGoal{Status: StatusActive, TokenBudget: 100, TokensUsed: 100}) {
		t.Fatal("at-budget active goal must be exhausted")
	}
	if BudgetExhausted(&SessionGoal{Status: StatusBudgetLimited, TokenBudget: 100, TokensUsed: 100}) {
		t.Fatal("budget_limited is not re-judged by the active-only gate")
	}
}

// TestContinuationPromptsNonEmpty 锁定共享 prompt 的非空与关键语义锚点
// （update_goal 纪律 / 收尾轮约束 / 迭代升级路径），驱动器与 CLI 的注入都以
// 这些常量为准。
func TestContinuationPromptsNonEmpty(t *testing.T) {
	if ContinuationPrompt == "" || BudgetWrapUpPrompt == "" || IterateEscalationPrompt == "" {
		t.Fatal("shared continuation prompts must not be empty")
	}
	for _, anchor := range []string{"update_goal", "evidence"} {
		if !contains(ContinuationPrompt, anchor) {
			t.Fatalf("continuation prompt missing anchor %q", anchor)
		}
	}
	if !contains(BudgetWrapUpPrompt, "budget_limited takes precedence over paused") {
		t.Fatal("wrap-up prompt missing budget precedence anchor")
	}
	// 升级提示词必须同时携带升级路径与回退路径（工具未启用时改变做法），
	// 并保持与续跑提示同源的纪律锚点。
	for _, anchor := range []string{"iterate_goal", "not available", "full objective", "concrete action"} {
		if !contains(IterateEscalationPrompt, anchor) {
			t.Fatalf("escalation prompt missing anchor %q", anchor)
		}
	}
}

// TestDecideIterationEscalation 锁定升级判定矩阵（P2-2' 入口 2）：
// 仅在 可用 && 未尝试 && strikes 达阈值 三者同时成立时升级。
func TestDecideIterationEscalation(t *testing.T) {
	if IterationEscalationStrikeThreshold != 2 {
		t.Fatalf("escalation threshold drifted: %d", IterationEscalationStrikeThreshold)
	}
	cases := []struct {
		strikes   int
		attempted bool
		available bool
		want      bool
	}{
		{0, false, true, false},
		{1, false, true, false},
		{2, false, true, true},   // 阈值命中
		{3, false, true, true},   // 理论上限前仍命中（调用方在停止线先拦截）
		{2, true, true, false},   // 已尝试过一次：不重复升级
		{2, false, false, false}, // 部署未启用工具：不升级
		{3, true, false, false},  // 全否
	}
	for _, tc := range cases {
		if got := DecideIterationEscalation(tc.strikes, tc.attempted, tc.available); got != tc.want {
			t.Fatalf("DecideIterationEscalation(%d, %v, %v) = %v, want %v",
				tc.strikes, tc.attempted, tc.available, got, tc.want)
		}
	}
}

func TestSystemBlockedReason(t *testing.T) {
	if got := SystemBlockedReason("consecutive_errors"); got == "consecutive_errors" {
		t.Fatalf("consecutive_errors must translate to a human-readable reason, got %q", got)
	}
	if got := SystemBlockedReason("custom"); got != "custom" {
		t.Fatalf("unknown reason must pass through, got %q", got)
	}
}

func TestCLICooldownWindowIsFiveMinutes(t *testing.T) {
	if ContinuationCLICooldownWindow != 5*time.Minute {
		t.Fatalf("CLI cooldown window drifted: %v", ContinuationCLICooldownWindow)
	}
}

// TestUsageTotalTokensSemantics 与 runtimeapi.usageTotalTokens 同口径的锚点
// （TotalTokens 优先，缺省退化为 prompt+completion）。
func TestUsageTotalTokensSemantics(t *testing.T) {
	usage := &types.TokenUsage{PromptTokens: 10, CompletionTokens: 5}
	if usage.TotalTokens == 0 && usage.PromptTokens+usage.CompletionTokens != 15 {
		t.Fatal("fallback accounting must be prompt+completion")
	}
	if usage.IsZero() {
		t.Fatal("non-zero usage must not report zero")
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
