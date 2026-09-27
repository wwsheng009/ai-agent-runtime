package commands

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

// §4.12：模式入口族（--permission-mode / --yolo / --accept-edits / --plan）。
// 冲突必须报错而不是静默取一个——权限模式是安全语义。

func TestResolveChatPermissionModeFlags(t *testing.T) {
	cases := []struct {
		name                    string
		modeFlag                string
		changed                 bool
		yolo, acceptEdits, plan bool
		want                    runtimepolicy.Mode
		wantErrContains         string
	}{
		{name: "默认", want: runtimepolicy.ModeDefault},
		{name: "yolo", yolo: true, want: runtimepolicy.ModeBypassPermissions},
		{name: "accept-edits", acceptEdits: true, want: runtimepolicy.ModeAcceptEdits},
		{name: "plan", plan: true, want: runtimepolicy.ModePlan},
		{name: "显式 canonical", modeFlag: "accept_edits", changed: true, want: runtimepolicy.ModeAcceptEdits},
		{name: "显式别名", modeFlag: "bypass", changed: true, want: runtimepolicy.ModeBypassPermissions},
		{name: "显式与简写一致", modeFlag: "plan", changed: true, plan: true, want: runtimepolicy.ModePlan},
		{name: "显式与简写冲突", modeFlag: "plan", changed: true, yolo: true, wantErrContains: "参数冲突"},
		{name: "两个简写冲突", yolo: true, plan: true, wantErrContains: "参数冲突"},
		{name: "非法显式值", modeFlag: "bogus", changed: true, wantErrContains: "无效的 permission-mode"},
		{name: "非法显式值优先于简写", modeFlag: "bogus", changed: true, plan: true, wantErrContains: "无效的 permission-mode"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			got, err := resolveChatPermissionModeFlags(item.modeFlag, item.changed, item.yolo, item.acceptEdits, item.plan)
			if item.wantErrContains != "" {
				if err == nil || !strings.Contains(err.Error(), item.wantErrContains) {
					t.Fatalf("err = %v, want contains %q", err, item.wantErrContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveChatPermissionModeFlags: %v", err)
			}
			if got != item.want {
				t.Fatalf("mode = %q, want %q", got, item.want)
			}
		})
	}
}

func TestParseChatCommandOptionsModeShorthands(t *testing.T) {
	cases := []struct {
		name            string
		args            []string
		want            runtimepolicy.Mode
		wantErrContains string
	}{
		{name: "accept-edits", args: []string{"--accept-edits"}, want: runtimepolicy.ModeAcceptEdits},
		{name: "plan", args: []string{"--plan"}, want: runtimepolicy.ModePlan},
		{name: "alias via permission-mode", args: []string{"--permission-mode", "auto-accept"}, want: runtimepolicy.ModeAcceptEdits},
		{name: "conflict", args: []string{"--yolo", "--plan"}, wantErrContains: "参数冲突"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			cmd := NewChatCommand(func() *config.Config { return nil })
			if err := cmd.ParseFlags(item.args); err != nil {
				t.Fatalf("ParseFlags: %v", err)
			}
			opts, err := parseChatCommandOptions(cmd, &config.Config{})
			if item.wantErrContains != "" {
				if err == nil || !strings.Contains(err.Error(), item.wantErrContains) {
					t.Fatalf("err = %v, want contains %q", err, item.wantErrContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseChatCommandOptions: %v", err)
			}
			if opts.PermissionMode != item.want {
				t.Fatalf("permission mode = %q, want %q", opts.PermissionMode, item.want)
			}
		})
	}
}

func TestPermissionModeCommandMatchesAndArgument(t *testing.T) {
	for _, command := range []string{"/mode", "/mode accept_edits", "/mode:accept_edits", "/permission-mode:plan", "/PERMISSION-MODE:plan"} {
		if !permissionModeCommandMatches(strings.ToLower(command)) {
			t.Fatalf("command %q should match the permission-mode entry", command)
		}
	}
	for _, command := range []string{"/model", "/modes", "/modem:1", "/permission-modes"} {
		if permissionModeCommandMatches(strings.ToLower(command)) {
			t.Fatalf("command %q must not match the permission-mode entry", command)
		}
	}
	// 冒号简写单独判定（基础命令名保持字面量，供 catalog 源码扫描护栏识别）。
	for _, command := range []string{"/mode:accept_edits", "/permission-mode:plan", "/MODE:plan"} {
		if !permissionModeColonShorthand(command) {
			t.Fatalf("command %q should match the colon shorthand", command)
		}
	}
	for _, command := range []string{"/mode", "/mode accept_edits", "/model:gpt", "/modes:x"} {
		if permissionModeColonShorthand(command) {
			t.Fatalf("command %q must not match the colon shorthand", command)
		}
	}

	argumentCases := map[string]string{
		"/mode:accept_edits":      "accept_edits",
		"/MODE:accept_edits":      "accept_edits",
		"/mode: accept_edits":     "accept_edits",
		"/permission-mode:bypass": "bypass",
		"/permission-mode bypass": "bypass",
		"/mode":                   "",
		"/model gpt-4.1":          "",
	}
	for command, want := range argumentCases {
		if got := permissionModeCommandArgument(command); got != want {
			t.Fatalf("permissionModeCommandArgument(%q) = %q, want %q", command, got, want)
		}
	}
}

func TestStructuredPermissionModeCommandAcceptsColonShorthandAndAlias(t *testing.T) {
	session := &ChatSession{PermissionMode: runtimepolicy.ModeDefault}

	result, handled, err := tryExecuteStructuredChatCommand(session, "/mode:accept_edits")
	if err != nil || !handled {
		t.Fatalf("/mode:accept_edits match=(%t, %v), want handled", handled, err)
	}
	if plain := ui.RenderDocumentPlain(result.Document()); !strings.Contains(plain, "已切换到 permission-mode=accept_edits") {
		t.Fatalf("colon shorthand did not switch mode:\n%s", plain)
	}
	if session.PermissionMode != runtimepolicy.ModeAcceptEdits {
		t.Fatalf("session mode = %q, want accept_edits", session.PermissionMode)
	}

	// 兼容别名同样可用（§4.12），且 `/permission-mode:` 形式与 `/mode:` 等价。
	result, handled, err = tryExecuteStructuredChatCommand(session, "/permission-mode:manual")
	if err != nil || !handled {
		t.Fatalf("/permission-mode:manual match=(%t, %v), want handled", handled, err)
	}
	if session.PermissionMode != runtimepolicy.ModeDefault {
		t.Fatalf("alias manual should map to default, got %q", session.PermissionMode)
	}

	// 非法值仍然被拒（不静默降级）。
	result, handled, err = tryExecuteStructuredChatCommand(session, "/mode:bogus")
	if err != nil || !handled {
		t.Fatalf("/mode:bogus match=(%t, %v), want handled", handled, err)
	}
	if plain := ui.RenderDocumentPlain(result.Document()); !strings.Contains(plain, "无效的 permission-mode") {
		t.Fatalf("unknown mode must be rejected:\n%s", plain)
	}

	// 裸命令仍展示当前模式。
	result, handled, err = tryExecuteStructuredChatCommand(session, "/mode")
	if err != nil || !handled {
		t.Fatalf("/mode match=(%t, %v), want handled", handled, err)
	}
	if plain := ui.RenderDocumentPlain(result.Document()); !strings.Contains(plain, "当前 permission-mode: default") {
		t.Fatalf("bare /mode should show the current mode:\n%s", plain)
	}
}
