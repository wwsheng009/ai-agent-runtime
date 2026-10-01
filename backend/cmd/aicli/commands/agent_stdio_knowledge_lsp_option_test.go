package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/acp"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
)

// ACP `knowledge.lsp.mode` select（ADR-0002 §4.2）：下发门控、值域 off|self、
// 会话级覆盖在运行时配置副本上落地。

// newKnowledgeTestActivation 建一个最小知识层句柄（shadow + SkipInitialIndex）。
func newKnowledgeTestActivation(t *testing.T, lspEnabled bool, lspMode string) *knowledge.Activation {
	t.Helper()
	cfg := knowledge.DefaultConfig()
	cfg.Mode = knowledge.ModeShadow
	cfg.LSP.Enabled = lspEnabled
	cfg.LSP.Mode = lspMode
	act, err := knowledge.Activate(context.Background(), cfg, t.TempDir(), knowledge.ActivationOptions{SkipInitialIndex: true})
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	t.Cleanup(func() { _ = act.Close() })
	return act
}

func findConfigOption(options []acp.SessionConfigOption, id string) (acp.SessionConfigOption, bool) {
	for _, option := range options {
		if option.ID == id {
			return option, true
		}
	}
	return acp.SessionConfigOption{}, false
}

// 门控：没有知识层（mode=off）或 lsp.enabled=false（逃生舱）时不得下发，
// 否则客户端会看到一个点了没用的选择器。
func TestACPKnowledgeLSPModeOption_Gating(t *testing.T) {
	t.Parallel()

	if _, ok := acpKnowledgeLSPModeConfigOption(&ChatSession{}); ok {
		t.Fatal("knowledge=nil（mode=off）时不得下发 knowledge.lsp.mode")
	}
	disabled := &ChatSession{Knowledge: newKnowledgeTestActivation(t, false, string(knowledge.LSPModeSelf))}
	if _, ok := acpKnowledgeLSPModeConfigOption(disabled); ok {
		t.Fatal("lsp.enabled=false 是逃生舱，会话内不得暴露可打开它的开关")
	}

	enabled := &ChatSession{Knowledge: newKnowledgeTestActivation(t, true, string(knowledge.LSPModeSelf))}
	option, ok := acpKnowledgeLSPModeConfigOption(enabled)
	if !ok {
		t.Fatal("knowledge 已接入且 lsp.enabled=true 时必须下发")
	}
	if option.Type != acp.SessionConfigOptionTypeSelect {
		t.Fatalf("type=%q, want select（select 不受 boolean 能力门控）", option.Type)
	}
	if option.Category != acp.SessionConfigOptionCategoryKnowledge {
		t.Fatalf("category=%q, want %q", option.Category, acp.SessionConfigOptionCategoryKnowledge)
	}
	if option.CurrentValue != string(knowledge.LSPModeSelf) {
		t.Fatalf("currentValue=%q, want self（来自知识层配置）", option.CurrentValue)
	}
	values := map[string]bool{}
	for _, choice := range option.Options {
		values[choice.Value] = true
	}
	if !values["off"] || !values["self"] || len(values) != 2 {
		t.Fatalf("values=%v, want 恰好 off|self（v1 值域）", values)
	}
}

// 会话覆盖优先于配置；非法覆盖按 off 收敛（fail closed）。
func TestACPKnowledgeLSPModeOption_OverrideWins(t *testing.T) {
	t.Parallel()

	chat := &ChatSession{
		Knowledge:                newKnowledgeTestActivation(t, true, string(knowledge.LSPModeOff)),
		KnowledgeLSPModeOverride: "self",
	}
	option, ok := acpKnowledgeLSPModeConfigOption(chat)
	if !ok || option.CurrentValue != "self" {
		t.Fatalf("currentValue=%q ok=%v, want self（会话覆盖优先）", option.CurrentValue, ok)
	}

	chat.KnowledgeLSPModeOverride = "external" // 非法值不得透传给客户端
	option, _ = acpKnowledgeLSPModeConfigOption(chat)
	if option.CurrentValue != "off" {
		t.Fatalf("currentValue=%q, want off（非法覆盖 fail closed）", option.CurrentValue)
	}
}

// ACP 路径端到端：session/set_config_option 切换 knowledge.lsp.mode。
func TestACPHostSetSessionConfigOption_KnowledgeLSPMode(t *testing.T) {
	t.Parallel()

	host, chat := newConfigOptionTestHost("sess-1", "m1", "m1")
	chat.Knowledge = newKnowledgeTestActivation(t, true, string(knowledge.LSPModeOff))

	resp, err := host.SetSessionConfigOption(context.Background(), acp.SetSessionConfigOptionRequest{
		SessionID: "sess-1",
		ConfigID:  acpKnowledgeLSPModeConfigOptionID,
		Value:     configOptionTestValue(t, `"self"`),
	})
	if err != nil {
		t.Fatalf("SetSessionConfigOption: %v", err)
	}
	if chat.KnowledgeLSPModeOverride != "self" {
		t.Fatalf("override=%q, want self", chat.KnowledgeLSPModeOverride)
	}
	option, ok := findConfigOption(resp.ConfigOptions, acpKnowledgeLSPModeConfigOptionID)
	if !ok || option.CurrentValue != "self" {
		t.Fatalf("响应中的选项 = %+v ok=%v, want currentValue=self", option, ok)
	}

	// 非法值（external）必须拒绝且不改状态。
	if _, err := host.SetSessionConfigOption(context.Background(), acp.SetSessionConfigOptionRequest{
		SessionID: "sess-1",
		ConfigID:  acpKnowledgeLSPModeConfigOptionID,
		Value:     configOptionTestValue(t, `"external"`),
	}); err == nil {
		t.Fatal("external 必须被拒绝（ADR-0002 §4.3：无 no-op 实现）")
	}
	if chat.KnowledgeLSPModeOverride != "self" {
		t.Fatalf("拒绝后 override=%q, want 保持 self", chat.KnowledgeLSPModeOverride)
	}
}

// 未启用知识层的会话不得接受切换（选项也不下发，双保险）。
func TestACPHostSetSessionConfigOption_KnowledgeLSPModeRejectedWithoutKnowledge(t *testing.T) {
	t.Parallel()

	host, chat := newConfigOptionTestHost("sess-1", "m1", "m1")
	if _, err := host.SetSessionConfigOption(context.Background(), acp.SetSessionConfigOptionRequest{
		SessionID: "sess-1",
		ConfigID:  acpKnowledgeLSPModeConfigOptionID,
		Value:     configOptionTestValue(t, `"self"`),
	}); err == nil || !strings.Contains(err.Error(), "knowledge.mode=off") {
		t.Fatalf("err=%v, want 未启用知识层的明确拒绝", err)
	}
	if chat.KnowledgeLSPModeOverride != "" {
		t.Fatalf("override=%q, want 空（拒绝后不得改状态）", chat.KnowledgeLSPModeOverride)
	}
}

// 覆盖在运行时配置副本上落地：共享配置绝不被改（其它会话/子代理看原值）。
func TestRuntimeConfigWithKnowledgeLSPOverride(t *testing.T) {
	t.Parallel()

	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Knowledge.Mode = knowledge.ModeOn
	cfg.Knowledge.LSP.Enabled = true
	cfg.Knowledge.LSP.Mode = string(knowledge.LSPModeOff)

	if got := runtimeConfigWithKnowledgeLSPOverride(nil, &ChatSession{}); got != nil {
		t.Fatal("nil 配置必须原样返回")
	}
	if got := runtimeConfigWithKnowledgeLSPOverride(cfg, nil); got != cfg {
		t.Fatal("nil 会话必须原样返回")
	}
	if got := runtimeConfigWithKnowledgeLSPOverride(cfg, &ChatSession{}); got != cfg {
		t.Fatal("空覆盖必须原样返回（避免无谓复制）")
	}
	if got := runtimeConfigWithKnowledgeLSPOverride(cfg, &ChatSession{KnowledgeLSPModeOverride: "external"}); got != cfg {
		t.Fatal("非法覆盖必须原样返回（fail closed）")
	}
	if got := runtimeConfigWithKnowledgeLSPOverride(cfg, &ChatSession{KnowledgeLSPModeOverride: "off"}); got != cfg {
		t.Fatal("与配置同值的覆盖必须原样返回")
	}

	overridden := runtimeConfigWithKnowledgeLSPOverride(cfg, &ChatSession{KnowledgeLSPModeOverride: "SELF"})
	if overridden == cfg {
		t.Fatal("不同值必须返回副本")
	}
	if overridden.Knowledge.LSP.Mode != string(knowledge.LSPModeSelf) {
		t.Fatalf("副本 mode=%q, want self（大小写归一）", overridden.Knowledge.LSP.Mode)
	}
	if cfg.Knowledge.LSP.Mode != string(knowledge.LSPModeOff) {
		t.Fatalf("共享配置被改写：mode=%q, want off", cfg.Knowledge.LSP.Mode)
	}
}
