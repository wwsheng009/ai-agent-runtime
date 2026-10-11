package commands

import (
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

// TestParseChatCommandOptions_BudgetTokens 钉住主 chat 的 --budget-tokens → ChatOptions
// 这一跳（PR-4 §6.4 待落地第 2 条：--budget-tokens → ChatOptions → loopRunOptions.BudgetTokens）。
func TestParseChatCommandOptions_BudgetTokens(t *testing.T) {
	cmd := NewChatCommand(func() *config.Config { return nil })
	if err := cmd.ParseFlags([]string{"--budget-tokens", "12345"}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	opts, err := parseChatCommandOptions(cmd, &config.Config{})
	if err != nil {
		t.Fatalf("parseChatCommandOptions: %v", err)
	}
	if opts.BudgetTokens != 12345 {
		t.Fatalf("BudgetTokens = %d, want 12345", opts.BudgetTokens)
	}

	// 未显式指定时保持 0（不限制），不得从别处继承脏值。
	plain := NewChatCommand(func() *config.Config { return nil })
	plainOpts, err := parseChatCommandOptions(plain, &config.Config{})
	if err != nil {
		t.Fatalf("parseChatCommandOptions(default): %v", err)
	}
	if plainOpts.BudgetTokens != 0 {
		t.Fatalf("default BudgetTokens = %d, want 0", plainOpts.BudgetTokens)
	}
}

// TestBuildLocalChatLoopConfig_CopiesTurnBudgetTokens 钉住 ChatSession → LoopReActConfig
// 这一跳：actor 的 LoopConfig 必须带上会话预算，否则循环看不到 --budget-tokens。
func TestBuildLocalChatLoopConfig_CopiesTurnBudgetTokens(t *testing.T) {
	cfg := buildLocalChatLoopConfig(nil, &ChatSession{TurnBudgetTokens: 42000})
	if cfg == nil || cfg.TurnBudgetTokens != 42000 {
		t.Fatalf("TurnBudgetTokens = %v, want 42000", cfg)
	}
	if got := buildLocalChatLoopConfig(nil, &ChatSession{}); got == nil || got.TurnBudgetTokens != 0 {
		t.Fatalf("empty session must keep budget disabled, got %v", got)
	}
	if got := buildLocalChatLoopConfig(nil, nil); got == nil || got.TurnBudgetTokens != 0 {
		t.Fatalf("nil session must keep budget disabled, got %v", got)
	}
}

// TestBuildLocalChatLoopConfig_ContextAccountingMode pins the P1-3 config
// plumbing: context.accountingMode must reach LoopReActConfig, and an absent
// value must keep the loop on its provider_first default.
func TestBuildLocalChatLoopConfig_ContextAccountingMode(t *testing.T) {
	cfg := buildLocalChatLoopConfig(&runtimecfg.RuntimeConfig{
		Context: runtimecfg.ContextConfig{AccountingMode: "estimate_only"},
	}, nil)
	if cfg == nil || cfg.ContextAccountingMode != "estimate_only" {
		t.Fatalf("ContextAccountingMode = %v, want estimate_only", cfg)
	}

	cfg = buildLocalChatLoopConfig(&runtimecfg.RuntimeConfig{}, nil)
	if cfg == nil || cfg.ContextAccountingMode != "" {
		t.Fatalf("unset accounting mode must stay empty (provider_first default), got %v", cfg)
	}

	cfg = buildLocalChatLoopConfig(nil, nil)
	if cfg == nil || cfg.ContextAccountingMode != "" {
		t.Fatalf("nil runtime config must keep accounting mode empty, got %v", cfg)
	}
}
