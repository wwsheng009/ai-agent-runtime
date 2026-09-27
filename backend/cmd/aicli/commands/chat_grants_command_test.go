package commands

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

func newGrantsTestSession(t *testing.T) (*ChatSession, string) {
	t.Helper()
	root := t.TempDir()
	session := &ChatSession{}
	session.FolderTrust.ProjectRoot = root
	return session, root
}

func seedGrantsForTest(t *testing.T, root string, grants ...runtimepolicy.Grant) {
	t.Helper()
	store, err := runtimepolicy.OpenProjectGrantStore(root)
	if err != nil {
		t.Fatalf("OpenProjectGrantStore(%q): %v", root, err)
	}
	for _, grant := range grants {
		if err := store.Remember(grant); err != nil {
			t.Fatalf("Remember(%+v): %v", grant, err)
		}
	}
}

func listGrantsForTest(t *testing.T, root string) []runtimepolicy.Grant {
	t.Helper()
	store, err := runtimepolicy.OpenProjectGrantStore(root)
	if err != nil {
		t.Fatalf("OpenProjectGrantStore(%q): %v", root, err)
	}
	return store.List()
}

func TestChatGrantsCommandListsDurableGrants(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session, root := newGrantsTestSession(t)
	seedGrantsForTest(t, root,
		runtimepolicy.Grant{Tool: "write", Pattern: "path:README.md"},
		runtimepolicy.Grant{Tool: "mcp__fs__read"},
	)

	for _, command := range []string{"/grants", "/grants list", "/grants status"} {
		text, err := runChatGrantsCommand(session, command)
		if err != nil {
			t.Fatalf("%s: %v", command, err)
		}
		for _, want := range []string{
			"durable 授权: 2 条",
			runtimepolicy.ResolveProjectGrantsPath(root),
			"- write · path:README.md · project",
			"- mcp__fs__read · 全部 · project",
			"/grants revoke <tool> [pattern]",
		} {
			if !strings.Contains(text, want) {
				t.Fatalf("%s output missing %q:\n%s", command, want, text)
			}
		}
	}
}

func TestChatGrantsCommandEmptyStoreIsNotAnError(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session, root := newGrantsTestSession(t)

	text, err := runChatGrantsCommand(session, "/grants")
	if err != nil {
		t.Fatalf("/grants on empty store: %v", err)
	}
	for _, want := range []string{"当前没有 durable 授权", "「记住」", "Web 设置页"} {
		if !strings.Contains(text, want) {
			t.Fatalf("empty list output missing %q:\n%s", want, text)
		}
	}
	// A read must not materialize the grants file.
	if _, statErr := os.Stat(runtimepolicy.ResolveProjectGrantsPath(root)); !os.IsNotExist(statErr) {
		t.Fatalf("read-only /grants created %s (stat err=%v)", runtimepolicy.ResolveProjectGrantsPath(root), statErr)
	}
}

func TestChatGrantsCommandRevokeToolWideAndPattern(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session, root := newGrantsTestSession(t)
	seedGrantsForTest(t, root,
		runtimepolicy.Grant{Tool: "write", Pattern: "path:README.md"},
		runtimepolicy.Grant{Tool: "write", Scope: "project"},
		runtimepolicy.Grant{Tool: "mcp__fs__read", Pattern: "exact:list"},
	)

	// Bare pattern revokes tool-wide grants only (matchEmptyPattern=true).
	text, err := runChatGrantsCommand(session, "/grants revoke write")
	if err != nil {
		t.Fatalf("/grants revoke write: %v", err)
	}
	if want := "已撤销 durable 授权: 1 条"; !strings.Contains(text, want) {
		t.Fatalf("tool-wide revoke output missing %q:\n%s", want, text)
	}
	if !strings.Contains(text, "pattern=全部(tool-wide)") {
		t.Fatalf("tool-wide revoke output missing tool-wide marker:\n%s", text)
	}
	remaining := listGrantsForTest(t, root)
	if len(remaining) != 2 {
		t.Fatalf("after tool-wide revoke remaining=%+v want 2", remaining)
	}
	for _, grant := range remaining {
		if grant.Tool == "write" && strings.TrimSpace(grant.Pattern) == "" {
			t.Fatalf("tool-wide grant survived: %+v", remaining)
		}
	}

	// A named pattern is matched exactly against the stored specifier.
	text, err = runChatGrantsCommand(session, "/grants revoke write path:README.md")
	if err != nil {
		t.Fatalf("/grants revoke write path:README.md: %v", err)
	}
	if want := "已撤销 durable 授权: 1 条"; !strings.Contains(text, want) {
		t.Fatalf("pattern revoke output missing %q:\n%s", want, text)
	}
	if !strings.Contains(text, "pattern=path:README.md") {
		t.Fatalf("pattern revoke output missing pattern:\n%s", text)
	}
	remaining = listGrantsForTest(t, root)
	if len(remaining) != 1 || remaining[0].Tool != "mcp__fs__read" {
		t.Fatalf("after pattern revoke remaining=%+v want only mcp__fs__read", remaining)
	}

	// Second revoke of the same pattern is a no-op success, not an error.
	text, err = runChatGrantsCommand(session, "/grants revoke write path:README.md")
	if err != nil {
		t.Fatalf("repeat revoke: %v", err)
	}
	if want := "已撤销 durable 授权: 0 条"; !strings.Contains(text, want) {
		t.Fatalf("repeat revoke output missing %q:\n%s", want, text)
	}
}

func TestChatGrantsCommandFailsLoudOnBadInput(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session, _ := newGrantsTestSession(t)

	for _, tc := range []struct {
		command string
		want    string
	}{
		{command: "/grants frobnicate", want: "未知的 /grants 子命令: frobnicate"},
		{command: "/grants revoke", want: "/grants revoke 需要指定 tool"},
		{command: "/grants list extra", want: "不接受额外参数"},
	} {
		if _, err := runChatGrantsCommand(session, tc.command); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s err=%v want containing %q", tc.command, err, tc.want)
		}
	}

	result := executeStructuredGrantsCommand(session, "/grants frobnicate")
	plain := ui.RenderDocumentPlain(result.Document())
	for _, want := range []string{"错误: 未知的 /grants 子命令: frobnicate", "用法: " + chatGrantsUsage} {
		if !strings.Contains(plain, want) {
			t.Fatalf("bad-input document missing %q:\n%s", want, plain)
		}
	}
}

func TestChatGrantsCommandStructuredPipelineDoesNotFallThroughToLegacy(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session, root := newGrantsTestSession(t)
	seedGrantsForTest(t, root, runtimepolicy.Grant{Tool: "mcp__fs__read", Pattern: "host:example.com"})

	result, handled, err := tryExecuteStructuredChatCommand(session, "/grants")
	if err != nil || !handled {
		t.Fatalf("/grants structured match=(%t, %v), want handled", handled, err)
	}
	plain := ui.RenderDocumentPlain(result.Document())
	for _, want := range []string{"durable 授权: 1 条", "- mcp__fs__read · host:example.com · project"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("/grants document missing %q:\n%s", want, plain)
		}
	}

	coord := newTestChatInteractionCoordinator(t, session)
	t.Cleanup(coord.Shutdown)
	session.Interaction = coord
	var retained bytes.Buffer
	coord.SetWriter(&retained)
	raw := captureStdout(t, func() {
		if dispatchChatCommand(session, "/grants", false) {
			t.Fatal("/grants unexpectedly requested chat exit")
		}
	})
	if raw != "" {
		t.Fatalf("/grants wrote raw legacy stdout:\n%q", raw)
	}
	if !strings.Contains(retained.String(), "durable 授权: 1 条") {
		t.Fatalf("coordinator retained output missing grants list:\n%s", retained.String())
	}
	if _, handled, err := tryExecuteStructuredChatCommand(nil, "/grants"); err != nil || !handled {
		t.Fatalf("nil-session /grants structured match=(%t, %v), want handled error result", handled, err)
	}
}
