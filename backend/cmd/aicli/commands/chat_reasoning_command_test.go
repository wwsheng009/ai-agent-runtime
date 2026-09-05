package commands

import (
	"strings"
	"testing"

	adapter "github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// TestApplyReasoningEffortCommandSelectionPublishesModelChanged 验证
// reasoning 单维度切换落地后发布 aicli.chat.model_selection_changed，
// web 客户端据此同步底部栏（TUI→web 方向的切换同步）。
func TestApplyReasoningEffortCommandSelectionPublishesModelChanged(t *testing.T) {
	bus := runtimeevents.NewBusWithRetention(16)
	var got []runtimeevents.Event
	bus.Subscribe(chatWebModelSelectionChangedBusEvent, func(ev runtimeevents.Event) {
		got = append(got, ev)
	})
	session := &ChatSession{
		ProviderName:     "alpha",
		Provider:         agentconfig.Provider{Protocol: "openai", DefaultModel: "alpha-model"},
		Adapter:          adapter.GetAdapterOrDefault("openai"),
		Model:            "alpha-model",
		ReasoningEffort:  "low",
		LocalRuntimeHost: &localChatRuntimeHost{EventBus: bus},
	}

	if err := applyReasoningEffortCommandSelection(session, "high", true); err != nil {
		t.Fatalf("apply reasoning_effort: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 model_selection_changed event, got %d", len(got))
	}
	if got[0].Payload["provider"] != "alpha" {
		t.Fatalf("event provider = %v, want alpha", got[0].Payload["provider"])
	}
	if got[0].Payload["reasoning_effort"] != session.ReasoningEffort {
		t.Fatalf("event reasoning_effort = %v, want %q", got[0].Payload["reasoning_effort"], session.ReasoningEffort)
	}
	if got[0].Type != chatWebModelSelectionChangedBusEvent {
		t.Fatalf("event type = %q, want %q", got[0].Type, chatWebModelSelectionChangedBusEvent)
	}
}

// TestApplyModelCommandSelectionPublishesModelChanged 验证 provider/model
// 切换路径（/model 命令、/login、交互 picker 共用）发布切换事件。
func TestApplyModelCommandSelectionPublishesModelChanged(t *testing.T) {
	bus := runtimeevents.NewBusWithRetention(16)
	var got []runtimeevents.Event
	bus.Subscribe(chatWebModelSelectionChangedBusEvent, func(ev runtimeevents.Event) {
		got = append(got, ev)
	})
	session := &ChatSession{
		ProviderName:     "alpha",
		Provider:         agentconfig.Provider{Protocol: "openai", DefaultModel: "alpha-model"},
		Adapter:          adapter.GetAdapterOrDefault("openai"),
		Model:            "alpha-model",
		ReasoningEffort:  "low",
		LocalRuntimeHost: &localChatRuntimeHost{EventBus: bus},
	}
	providerCtx := &providerExecutionContext{
		ProviderName:   "beta",
		Provider:       agentconfig.Provider{Protocol: "openai", BaseURL: "https://beta.example.com", DefaultModel: "beta-model"},
		Adapter:        adapter.GetAdapterOrDefault("openai"),
		Model:          "beta-model",
		RequestedModel: "beta-model",
	}

	if err := applyModelCommandSelection(session, providerCtx, "beta-model", "medium"); err != nil {
		t.Fatalf("apply model selection: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 model_selection_changed event, got %d", len(got))
	}
	if got[0].Payload["provider"] != "beta" {
		t.Fatalf("event provider = %v, want beta", got[0].Payload["provider"])
	}
	if got[0].Payload["model"] != "beta-model" {
		t.Fatalf("event model = %v, want beta-model", got[0].Payload["model"])
	}
	if got[0].Payload["reasoning_effort"] != "medium" {
		t.Fatalf("event reasoning_effort = %v, want medium", got[0].Payload["reasoning_effort"])
	}
}


func TestHandleCommand_ReasoningTogglesReasoningOutput(t *testing.T) {
	session := &ChatSession{}

	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/reasoning off", false); quit {
			t.Fatal("expected /reasoning off not to exit")
		}
	})
	if !session.SuppressReasoningOutput {
		t.Fatal("expected /reasoning off to suppress reasoning output")
	}
	if !strings.Contains(output, "当前 reasoning: off") {
		t.Fatalf("expected off status, got %q", output)
	}

	output = captureStdout(t, func() {
		if quit := handleCommand(session, "/reasoning on", false); quit {
			t.Fatal("expected /reasoning on not to exit")
		}
	})
	if session.SuppressReasoningOutput {
		t.Fatal("expected /reasoning on to restore reasoning output")
	}
	if !strings.Contains(output, "当前 reasoning: on") {
		t.Fatalf("expected on status, got %q", output)
	}
}

func TestHandleCommand_ReasoningStatusAndInvalidArgument(t *testing.T) {
	session := &ChatSession{SuppressReasoningOutput: true}

	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/reasoning", false); quit {
			t.Fatal("expected /reasoning status not to exit")
		}
	})
	if !strings.Contains(output, "当前 reasoning: off") {
		t.Fatalf("expected status output, got %q", output)
	}

	output = captureStdout(t, func() {
		if quit := handleCommand(session, "/reasoning maybe", false); quit {
			t.Fatal("expected invalid /reasoning not to exit")
		}
	})
	if !strings.Contains(output, "无法识别的 /reasoning 参数") || !strings.Contains(output, "用法: /reasoning [on|off|status]") {
		t.Fatalf("expected invalid argument usage, got %q", output)
	}
	if !session.SuppressReasoningOutput {
		t.Fatal("expected invalid argument not to change reasoning output state")
	}
}

func TestHandleCommand_ReasoningEffortSetsAndPersistsPreference(t *testing.T) {
	cfg, cfgPath := testModelCommandConfig(t)
	session := &ChatSession{
		ProviderName:    "beta",
		Provider:        cfg.Providers.Items["beta"],
		Model:           "beta-model",
		ReasoningEffort: "low",
		Config:          cfg,
	}

	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/reasoning_effort max", true); quit {
			t.Fatal("expected /reasoning_effort not to exit")
		}
	})
	if session.ReasoningEffort != "max" {
		t.Fatalf("expected reasoning_effort max, got %q", session.ReasoningEffort)
	}
	if !strings.Contains(output, "当前 reasoning_effort: max") {
		t.Fatalf("expected max status, got %q", output)
	}

	loaded, err := agentconfig.InitGlobalConfig(cfgPath)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if loaded.AICLI == nil || loaded.AICLI.Chat == nil || loaded.AICLI.Chat.ReasoningEffort != "max" {
		t.Fatalf("expected persisted reasoning_effort max, got %+v", loaded.AICLI)
	}
	if cfg.AICLI == nil || cfg.AICLI.Chat == nil || cfg.AICLI.Chat.ReasoningEffort != "max" {
		t.Fatalf("expected in-memory config reasoning_effort max, got %+v", cfg.AICLI)
	}
}

func TestHandleCommand_ReasoningEffortClearAndStatus(t *testing.T) {
	cfg, _ := testModelCommandConfig(t)
	session := &ChatSession{
		ProviderName:    "beta",
		Provider:        cfg.Providers.Items["beta"],
		Model:           "beta-model",
		ReasoningEffort: "high",
		Config:          cfg,
	}

	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/reasoning_effort clear", true); quit {
			t.Fatal("expected /reasoning_effort clear not to exit")
		}
	})
	if session.ReasoningEffort != "" {
		t.Fatalf("expected reasoning_effort to be cleared, got %q", session.ReasoningEffort)
	}
	if !strings.Contains(output, "当前 reasoning_effort: (无)") {
		t.Fatalf("expected cleared status, got %q", output)
	}

	output = captureStdout(t, func() {
		if quit := handleCommand(session, "/reasoning_effort status", true); quit {
			t.Fatal("expected /reasoning_effort status not to exit")
		}
	})
	if !strings.Contains(output, "当前 reasoning_effort: (无)") {
		t.Fatalf("expected status output, got %q", output)
	}
}
