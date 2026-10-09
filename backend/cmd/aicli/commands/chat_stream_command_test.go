package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func newStreamCommandSession(t *testing.T) (*ChatSession, string) {
	t.Helper()
	isolateWorkspacePrefsForTest(t)

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	raw := strings.TrimSpace(`
providers:
  default_provider: alpha
  items:
    alpha:
      enabled: true
      protocol: openai
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
		Stream:       false,
		Config:       cfg,
	}, cfgPath
}

func loadStreamPreference(t *testing.T, cfgPath string) *bool {
	t.Helper()
	// Workspace-scoped persistence (D5): /stream writes to the workspace
	// preference file, not the global config.
	prefs := loadWorkspaceChatPrefsForTest(t)
	if prefs == nil {
		return nil
	}
	return prefs.Stream
}

func TestParseStreamCommandRequest(t *testing.T) {
	cases := []struct {
		input   string
		want    streamCommandAction
		value   bool
		wantErr bool
	}{
		{"/stream", streamCommandToggle, false, false},
		{"/stream toggle", streamCommandToggle, false, false},
		{"/stream on", streamCommandSet, true, false},
		{"/stream  TRUE  ", streamCommandSet, true, false},
		{"/stream off", streamCommandSet, false, false},
		{"/stream status", streamCommandStatus, false, false},
		{"/stream wat", 0, false, true},
	}
	for _, tc := range cases {
		got, err := parseStreamCommandRequest(tc.input)
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
		if got.Action == streamCommandSet && got.Value != tc.value {
			t.Fatalf("%s: value=%v want=%v", tc.input, got.Value, tc.value)
		}
	}
}

func TestApplyStreamCommand_TogglePersistsPreference(t *testing.T) {
	session, cfgPath := newStreamCommandSession(t)

	applyStreamCommand(session, "/stream")
	if !session.Stream {
		t.Fatalf("expected stream=true after toggle, got false")
	}
	stored := loadStreamPreference(t, cfgPath)
	if stored == nil || *stored != true {
		t.Fatalf("expected persisted stream=true, got %+v", stored)
	}

	applyStreamCommand(session, "/stream")
	if session.Stream {
		t.Fatalf("expected stream=false after second toggle")
	}
	stored = loadStreamPreference(t, cfgPath)
	if stored == nil || *stored != false {
		t.Fatalf("expected persisted stream=false, got %+v", stored)
	}
}

// TestStructuredStreamShortcuts_AllFormsClaimed 锁定 /s、/n、/normal 三个流式
// 快捷形式在结构化分派内全部被认领。背景（L5-3 Batch A）：/normal 是目录全名
// （catalog: Name=/normal，Alias=/n），此前只在 legacy switch 覆盖；统一渲染
// 会话输入全名曾落到未迁移拒绝面（Batch A 修复，门禁已在 Batch C 删除）。
func TestStructuredStreamShortcuts_AllFormsClaimed(t *testing.T) {
	cases := []struct {
		command    string
		unified    bool
		wantStream bool
	}{
		{command: "/s", wantStream: true},
		{command: "/n", wantStream: false},
		{command: "/normal", wantStream: false},
		{command: "/normal", unified: true, wantStream: false},
	}
	for _, tc := range cases {
		name := tc.command + "-plain"
		if tc.unified {
			name = tc.command + "-unified"
		}
		t.Run(name, func(t *testing.T) {
			session, _ := newStreamCommandSession(t)
			if tc.unified {
				// 统一出口判定：terminal 所有权边界已跨越（无需真实 TTY）。
				session.TerminalSession = &ui.TerminalSession{}
			}
			// 反相起点：确保快捷真正写入目标值，而不是 no-op。
			session.Stream = !tc.wantStream
			result, handled, err := tryExecuteStructuredChatCommand(session, tc.command)
			if err != nil || !handled {
				t.Fatalf("%s handled=%v err=%v（不得回落统一渲染门禁）", tc.command, handled, err)
			}
			if session.Stream != tc.wantStream {
				t.Fatalf("%s stream=%v，期望 %v", tc.command, session.Stream, tc.wantStream)
			}
			if strings.TrimSpace(ui.RenderDocumentPlain(result.Document())) == "" {
				t.Fatalf("%s 结果缺少命令单元格文档", tc.command)
			}
		})
	}
}

func TestApplyStreamCommand_ExplicitOnOff(t *testing.T) {
	session, cfgPath := newStreamCommandSession(t)

	applyStreamCommand(session, "/stream on")
	if !session.Stream {
		t.Fatalf("expected stream=true after /stream on")
	}
	stored := loadStreamPreference(t, cfgPath)
	if stored == nil || *stored != true {
		t.Fatalf("expected persisted stream=true, got %+v", stored)
	}

	applyStreamCommand(session, "/stream off")
	if session.Stream {
		t.Fatalf("expected stream=false after /stream off")
	}
	stored = loadStreamPreference(t, cfgPath)
	if stored == nil || *stored != false {
		t.Fatalf("expected persisted stream=false, got %+v", stored)
	}
}

func TestApplyStreamCommand_StatusDoesNotMutateOrPersist(t *testing.T) {
	session, cfgPath := newStreamCommandSession(t)

	applyStreamCommand(session, "/stream status")
	if session.Stream {
		t.Fatalf("expected stream to remain false after /stream status")
	}
	if stored := loadStreamPreference(t, cfgPath); stored != nil {
		t.Fatalf("expected no persisted stream after /stream status, got %+v", stored)
	}
}

func TestApplyStreamCommand_NoOpWhenAlreadyMatching(t *testing.T) {
	session, cfgPath := newStreamCommandSession(t)

	// Already false; explicit off should not write.
	applyStreamCommand(session, "/stream off")
	if stored := loadStreamPreference(t, cfgPath); stored != nil {
		t.Fatalf("expected no persisted stream when value unchanged, got %+v", stored)
	}
}

func TestApplyStreamShortcut_PersistsAndUpdatesSessionConfig(t *testing.T) {
	session, cfgPath := newStreamCommandSession(t)

	applyStreamShortcut(session, true)
	if !session.Stream {
		t.Fatal("expected stream=true after /s shortcut")
	}
	if session.Config.AICLI.Chat.Stream == nil || *session.Config.AICLI.Chat.Stream != true {
		t.Fatalf("expected in-memory cfg stream=true, got %+v", session.Config.AICLI.Chat.Stream)
	}
	stored := loadStreamPreference(t, cfgPath)
	if stored == nil || *stored != true {
		t.Fatalf("expected persisted stream=true, got %+v", stored)
	}

	applyStreamShortcut(session, false)
	if session.Stream {
		t.Fatal("expected stream=false after /normal shortcut")
	}
	stored = loadStreamPreference(t, cfgPath)
	if stored == nil || *stored != false {
		t.Fatalf("expected persisted stream=false, got %+v", stored)
	}
}
