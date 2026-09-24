package commands

// Batch 11a 的 TUI 剧本（把实施计划的"手工剧本"固化为进程内 e2e）：
// 真实 chat 主循环 + 真实 stdin 注入 + 真实渲染字节流（复用
// chat_tty_live_loop_test.go 的 driveTTYLiveLoop 基础设施），断言
// `/profile use` 的切换确实作用于"下一轮请求面"，`/profile off` 回到基线。
//
// 覆盖：A1/A2 的 TUI 端到端（下一轮触达 executor 时 tools/prompt 已切换）、
// A7 的 TUI 端到端（off 后不再携带 profile 生效面）、Switch Report 渲染到屏幕。

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestTTY_LiveLoop_ProfileUseSwitchesNextTurnSurface(t *testing.T) {
	profilesRoot := t.TempDir()
	writeProfileSwitchFixture(t, profilesRoot, "dev", `profile:
  name: dev
  default_agent: coder
agents:
  coder:
    model: fixture
`, "DEV TTY PROFILE PROMPT", `allowlist: [read_file, grep]
`)

	profileSession, cleanup := newProfileSwitchTestSession(t, profilesRoot)
	defer cleanup()

	type turnSurface struct {
		ref     string
		allowed string
		prompt  string
		anchor  string
	}
	turns := make([]turnSurface, 0, 2)

	run := runTTYLiveLoop(t, "ok", []ttyLiveScriptStep{
		{wait: 700 * time.Millisecond, line: "/profile use dev\n"},
		{waitReady: true, wait: 100 * time.Millisecond, line: "hi\n"},
		{waitReady: true, wait: 100 * time.Millisecond, line: "/profile off\n"},
		{waitReady: true, wait: 100 * time.Millisecond, line: "bye\n"},
		{waitReady: true, wait: 100 * time.Millisecond, line: "/exit\n"},
	}, func(session *ChatSession, ex *fakeChatExecutor) {
		// 复用带 Config/SessionManager 的 profile 会话骨架（真实切换需要
		// 持久化管理器与 profile 注册表），保留 harness 的交互/渲染装配。
		session.Config = profileSession.Config
		session.SessionManager = profileSession.SessionManager
		session.RuntimeSession = profileSession.RuntimeSession
		session.SessionUserID = profileSession.SessionUserID
		// 预置"旧锚点"：切换必须删除它，而不是把它带进下一轮。
		session.SystemPromptText = "OLD HEAD"
		storeFrozenChatSystemPrompt(session.RuntimeSession, session, "OLD HEAD")
		ex.onCall = func(_ context.Context, s *ChatSession, _ string) (string, error) {
			surface := turnSurface{ref: s.ProfileReference, prompt: s.SystemPromptText}
			if s.ToolPolicy != nil {
				surface.allowed = strings.Join(sortedCopy(s.ToolPolicy.AllowedToolNames()), ",")
			}
			surface.anchor = loadFrozenChatSystemPrompt(s.RuntimeSession, s)
			turns = append(turns, surface)
			return ex.output, nil
		}
	})

	if len(run.executor.prompts) != 2 {
		t.Fatalf("应只有 2 次 executor 调用（/profile 不得触达 executor），实际 %d 次: %q", len(run.executor.prompts), run.executor.prompts)
	}
	if len(turns) != 2 {
		t.Fatalf("应有 2 个 turn 生效面快照，实际 %d", len(turns))
	}

	// A1/A2：`/profile use dev` 之后的第一轮，生效面已是新 profile。
	if turns[0].ref != "dev" {
		t.Errorf("第一轮 profile_ref 应为 dev，实际 %q", turns[0].ref)
	}
	if turns[0].allowed != "grep,read_file" {
		t.Errorf("第一轮工具面应为 profile 声明（grep,read_file），实际 %q", turns[0].allowed)
	}
	if !strings.Contains(turns[0].prompt, "DEV TTY PROFILE PROMPT") {
		t.Errorf("第一轮 system prompt 应来自新 profile，实际 %q", turns[0].prompt)
	}
	if strings.Contains(turns[0].anchor, "OLD HEAD") {
		t.Errorf("切换后旧锚点不得进入下一轮，实际锚点 %q", turns[0].anchor)
	}
	if turns[0].anchor != "" && !strings.Contains(turns[0].anchor, "DEV TTY PROFILE PROMPT") {
		t.Errorf("第一轮锚点（若已冻结）应来自新 profile，实际 %q", turns[0].anchor)
	}

	// A7：`/profile off` 之后的下一轮回落到无 profile 基线。
	if turns[1].ref != "" {
		t.Errorf("/profile off 后 profile_ref 应为空，实际 %q", turns[1].ref)
	}
	if strings.Contains(turns[1].prompt, "DEV TTY PROFILE PROMPT") {
		t.Errorf("/profile off 后 prompt 不应再含 profile 内容，实际 %q", turns[1].prompt)
	}
	if strings.Contains(turns[1].anchor, "DEV TTY PROFILE PROMPT") {
		t.Errorf("/profile off 后锚点不应再含 profile 内容，实际 %q", turns[1].anchor)
	}

	lines := run.screenLines(t)
	if !strings.Contains(strings.Join(lines, "\n"), "已切换 profile") {
		t.Errorf("Switch Report 未渲染到屏幕; lines=%q", lines)
	}
	assertNoAdjacentDuplicateLines(t, lines)
}
