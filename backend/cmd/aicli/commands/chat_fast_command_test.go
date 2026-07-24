package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimechatcore "github.com/wwsheng009/ai-agent-runtime/internal/chatcore"
)

func newFastCommandSession(t *testing.T, protocol string) (*ChatSession, string) {
	t.Helper()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	raw := strings.TrimSpace(`
providers:
  default_provider: alpha
  items:
    alpha:
      enabled: true
      protocol: ` + protocol + `
      base_url: https://alpha.example.com
      default_model: alpha-model
aicli:
  chat:
    default_provider: alpha
    default_model: alpha-model
`)
	if err := os.WriteFile(cfgPath, []byte(raw), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := agentconfig.InitGlobalConfig(cfgPath)
	if err != nil {
		t.Fatalf("InitGlobalConfig: %v", err)
	}
	return &ChatSession{
		ProviderName: "alpha",
		Provider:     cfg.Providers.Items["alpha"],
		Model:        "alpha-model",
		FastMode:     false,
		Config:       cfg,
	}, cfgPath
}

func loadFastModePreference(t *testing.T, cfgPath string) *bool {
	t.Helper()
	loaded, err := agentconfig.InitGlobalConfig(cfgPath)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if loaded.AICLI == nil || loaded.AICLI.Chat == nil {
		return nil
	}
	return loaded.AICLI.Chat.FastMode
}

func TestParseFastCommandRequest(t *testing.T) {
	cases := []struct {
		input   string
		want    fastCommandAction
		value   bool
		wantErr bool
	}{
		{"/fast", fastCommandToggle, false, false},
		{"/fast toggle", fastCommandToggle, false, false},
		{"/fast on", fastCommandSet, true, false},
		{"/fast  PRIORITY  ", fastCommandSet, true, false},
		{"/fast off", fastCommandSet, false, false},
		{"/fast default", fastCommandSet, false, false},
		{"/fast status", fastCommandStatus, false, false},
		{"/fast wat", 0, false, true},
	}
	for _, tc := range cases {
		got, err := parseFastCommandRequest(tc.input)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%s: expected error, got %+v", tc.input, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: unexpected error %v", tc.input, err)
		}
		if got.Action != tc.want {
			t.Fatalf("%s: action=%d want=%d", tc.input, got.Action, tc.want)
		}
		if got.Action == fastCommandSet && got.Value != tc.value {
			t.Fatalf("%s: value=%v want=%v", tc.input, got.Value, tc.value)
		}
	}
}

func TestApplyFastCommand_CodexOnly(t *testing.T) {
	session, _ := newFastCommandSession(t, "openai")
	applyFastCommand(session, "/fast on")
	if session.FastMode {
		t.Fatalf("expected FastMode to remain false on non-codex protocol")
	}
}

func TestApplyFastCommand_TogglePersistsPreference(t *testing.T) {
	session, cfgPath := newFastCommandSession(t, "codex")

	applyFastCommand(session, "/fast")
	if !session.FastMode {
		t.Fatalf("expected FastMode=true after toggle")
	}
	stored := loadFastModePreference(t, cfgPath)
	if stored == nil || *stored != true {
		t.Fatalf("expected persisted fast_mode=true, got %+v", stored)
	}

	applyFastCommand(session, "/fast")
	if session.FastMode {
		t.Fatalf("expected FastMode=false after second toggle")
	}
	stored = loadFastModePreference(t, cfgPath)
	if stored == nil || *stored != false {
		t.Fatalf("expected persisted fast_mode=false, got %+v", stored)
	}
}

func TestApplyFastCommand_ExplicitOnOff(t *testing.T) {
	session, cfgPath := newFastCommandSession(t, "codex")

	applyFastCommand(session, "/fast on")
	if !session.FastMode {
		t.Fatalf("expected FastMode=true after /fast on")
	}
	stored := loadFastModePreference(t, cfgPath)
	if stored == nil || *stored != true {
		t.Fatalf("expected persisted fast_mode=true, got %+v", stored)
	}

	applyFastCommand(session, "/fast off")
	if session.FastMode {
		t.Fatalf("expected FastMode=false after /fast off")
	}
	stored = loadFastModePreference(t, cfgPath)
	if stored == nil || *stored != false {
		t.Fatalf("expected persisted fast_mode=false, got %+v", stored)
	}
}

func TestChatSessionSupportsFastMode(t *testing.T) {
	if chatSessionSupportsFastMode(nil) {
		t.Fatalf("nil session should not support fast")
	}
	codex := &ChatSession{Provider: agentconfig.Provider{Protocol: "codex"}}
	if !chatSessionSupportsFastMode(codex) {
		t.Fatalf("codex protocol should support fast")
	}
	openai := &ChatSession{Provider: agentconfig.Provider{Protocol: "openai"}}
	if chatSessionSupportsFastMode(openai) {
		t.Fatalf("openai protocol should not support fast")
	}
}

func TestResolveChatFastModeChoice_Priority(t *testing.T) {
	trueVal := true
	falseVal := false
	cfgOn := &agentconfig.Config{
		AICLI: &agentconfig.AICLIConfig{
			Chat: &agentconfig.AICLIChatConfig{FastMode: &trueVal},
		},
	}
	cfgOff := &agentconfig.Config{
		AICLI: &agentconfig.AICLIConfig{
			Chat: &agentconfig.AICLIChatConfig{FastMode: &falseVal},
		},
	}

	// Flag wins over config and session metadata.
	sessionOn := &runtimechat.Session{
		Metadata: runtimechat.SessionMetadata{
			Context: map[string]interface{}{
				chatRuntimeContextFastMode: true,
			},
		},
	}
	if !resolveChatFastModeChoice(cfgOff, &chatCommandOptions{FastFlag: true, FastChanged: true}, nil) {
		t.Fatal("expected --fast flag to force true")
	}
	if resolveChatFastModeChoice(cfgOn, &chatCommandOptions{FastFlag: false, FastChanged: true}, sessionOn) {
		t.Fatal("expected --fast=false flag to force false over session/config")
	}

	// Session metadata wins over config.
	if !resolveChatFastModeChoice(cfgOff, &chatCommandOptions{}, sessionOn) {
		t.Fatal("expected session metadata fast_mode=true")
	}
	sessionOff := &runtimechat.Session{
		Metadata: runtimechat.SessionMetadata{
			Context: map[string]interface{}{
				chatRuntimeContextFastMode: false,
			},
		},
	}
	if resolveChatFastModeChoice(cfgOn, &chatCommandOptions{}, sessionOff) {
		t.Fatal("expected session metadata fast_mode=false to win over config")
	}

	// Config used when flag/session not set.
	if !resolveChatFastModeChoice(cfgOn, &chatCommandOptions{}, nil) {
		t.Fatal("expected config fast_mode=true")
	}
	if resolveChatFastModeChoice(cfgOff, &chatCommandOptions{}, nil) {
		t.Fatal("expected config fast_mode=false")
	}

	// Default false.
	if resolveChatFastModeChoice(nil, &chatCommandOptions{}, nil) {
		t.Fatal("expected default false")
	}
}

func TestAdapterRequestConfig_InjectsServiceTierForCodexFastMode(t *testing.T) {
	session := &ChatSession{
		ProviderName: "codex",
		Provider:     agentconfig.Provider{Enabled: true, Protocol: "codex"},
		Model:        "gpt-5.2-codex",
		FastMode:     true,
	}
	cfg := adapterRequestConfig(session, nil, runtimechatcore.ProviderTurnRequest{Stream: false})
	if got := cfg.Metadata["service_tier"]; got != codexServiceTierPriority {
		t.Fatalf("expected service_tier=%q, got %#v", codexServiceTierPriority, got)
	}
}

func TestAdapterRequestConfig_OmitsServiceTierForNonCodexFastMode(t *testing.T) {
	session := &ChatSession{
		ProviderName: "openai",
		Provider:     agentconfig.Provider{Enabled: true, Protocol: "openai"},
		Model:        "gpt-4.1",
		FastMode:     true,
	}
	cfg := adapterRequestConfig(session, nil, runtimechatcore.ProviderTurnRequest{Stream: false})
	if got := cfg.Metadata["service_tier"]; got != nil {
		t.Fatalf("expected service_tier omitted for non-codex, got %#v", got)
	}
}

func TestAdapterRequestConfig_OmitsServiceTierWhenFastModeOff(t *testing.T) {
	session := &ChatSession{
		ProviderName: "codex",
		Provider:     agentconfig.Provider{Enabled: true, Protocol: "codex"},
		Model:        "gpt-5.2-codex",
		FastMode:     false,
	}
	cfg := adapterRequestConfig(session, nil, runtimechatcore.ProviderTurnRequest{Stream: false})
	if got := cfg.Metadata["service_tier"]; got != nil {
		t.Fatalf("expected service_tier omitted when FastMode off, got %#v", got)
	}
}
