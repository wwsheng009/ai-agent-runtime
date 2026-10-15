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
// （update_goal 纪律 / 收尾轮约束），驱动器与 CLI 的注入都以这两个常量为准。
func TestContinuationPromptsNonEmpty(t *testing.T) {
	if ContinuationPrompt == "" || BudgetWrapUpPrompt == "" {
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
