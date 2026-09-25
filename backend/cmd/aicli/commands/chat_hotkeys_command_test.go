package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

func TestNextChatPermissionModeFollowsDocumentedCycle(t *testing.T) {
	cases := []struct {
		current runtimepolicy.Mode
		want    runtimepolicy.Mode
	}{
		{runtimepolicy.ModeDefault, runtimepolicy.ModeAcceptEdits},
		{runtimepolicy.ModeAcceptEdits, runtimepolicy.ModePlan},
		{runtimepolicy.ModePlan, runtimepolicy.ModeBypassPermissions},
		{runtimepolicy.ModeBypassPermissions, runtimepolicy.ModeDefault},
		{"", runtimepolicy.ModeDefault},
		{"unknown-mode", runtimepolicy.ModeDefault},
	}
	for _, tc := range cases {
		if got := nextChatPermissionMode(tc.current); got != tc.want {
			t.Fatalf("nextChatPermissionMode(%q) = %q, want %q", tc.current, got, tc.want)
		}
	}
}

func TestCycleChatPermissionModeAppliesAndConsumesKey(t *testing.T) {
	session := &ChatSession{PermissionMode: runtimepolicy.ModeDefault}
	if !cycleChatPermissionMode(session) {
		t.Fatal("循环键应消费按键（返回 true）")
	}
	if got := chatSessionPermissionMode(session); got != runtimepolicy.ModeAcceptEdits {
		t.Fatalf("循环后模式 = %q, want accept_edits", got)
	}
	if cycleChatPermissionMode(nil) {
		t.Fatal("无会话时不应消费按键")
	}
}

func TestCycleChatPermissionModeRequiresBypassConfirmation(t *testing.T) {
	session := &ChatSession{
		PermissionMode: runtimepolicy.ModePlan,
		InputReader:    nil,
	}
	if !cycleChatPermissionMode(session) {
		t.Fatal("循环键应消费按键（返回 true）")
	}
	if got := chatSessionPermissionMode(session); got == runtimepolicy.ModeBypassPermissions {
		t.Fatal("无交互输入时不应静默切到 bypass_permissions")
	}
}

func TestHotkeysCommandListsEffectiveBindingsAndUserOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AICLI_HOME", home)
	payload, err := json.Marshal(map[string]any{
		"app.permission.cycle": []string{"alt+c"},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "keybindings.json"), payload, 0o644); err != nil {
		t.Fatalf("写入 keybindings.json: %v", err)
	}
	reloadChatKeymapRegistry()
	defer reloadChatKeymapRegistry()

	session := &ChatSession{}
	output := captureStdout(t, func() {
		if quit := handleHotkeysCommand(session, "/hotkeys"); quit {
			t.Fatal("hotkeys 不应退出 chat")
		}
	})
	for _, want := range []string{
		"app.permission.cycle",
		"alt+c",
		"[用户覆盖]",
		"app.transcript.pager",
		"固定快捷键",
		"keybindings.json",
		"本终端:",
		"剪贴板图片",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("/hotkeys 输出缺少 %q:\n%s", want, output)
		}
	}
}

func TestHotkeysCommandReloadReportsPathAndRejectsUnknownArgs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AICLI_HOME", home)
	reloadChatKeymapRegistry()
	defer reloadChatKeymapRegistry()

	session := &ChatSession{}
	output := captureStdout(t, func() {
		handleHotkeysCommand(session, "/hotkeys reload")
	})
	if !strings.Contains(output, filepath.Join(home, "keybindings.json")) {
		t.Fatalf("reload 输出应包含配置文件路径:\n%s", output)
	}

	output = captureStdout(t, func() {
		handleHotkeysCommand(session, "/hotkeys bogus")
	})
	if !strings.Contains(output, "仅支持空参数或 reload") {
		t.Fatalf("未知子命令应报错:\n%s", output)
	}
}
