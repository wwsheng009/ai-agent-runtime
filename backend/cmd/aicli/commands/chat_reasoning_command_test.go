package commands

import (
	"strings"
	"testing"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

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
