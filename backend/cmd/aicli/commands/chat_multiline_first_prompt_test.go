package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/formatter"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// TestUnifiedFirstMultilinePromptRendersOnce 针对真实现场：全新 TUI 会话的
// 首条 prompt 是两行文本（含换行与缩进），提交后多行 composer 必须整块让位给
// 一个 user 块；最终终端投影（scrollback + 视口）里该文本只能出现一次。
func TestUnifiedFirstMultilinePromptRendersOnce(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	const (
		width, height = 80, 24
		multiline     = "check ./.aicli/runtime.yaml配置 有没有schedule:\n  enabled: true"
	)
	session := &ChatSession{
		Provider:  config.Provider{Protocol: "codex"},
		Formatter: formatter.NewMarkdownFormatter(false),
	}
	session.Interaction = newTestChatInteractionCoordinator(t, session)
	t.Cleanup(session.Interaction.Shutdown)

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	surface.SetPhysicalWritesEnabled(false)
	session.Interaction.SetSurface(surface)
	var terminal bytes.Buffer
	if !session.Interaction.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}

	// 首帧 composer + 多行草稿（真实粘贴/输入后的状态），再提交。
	session.Interaction.PrintPrompt()
	session.Interaction.SetPromptInput(multiline)
	session.Interaction.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, session.Interaction)

	session.Interaction.RenderSubmittedUserInput(multiline)
	session.Interaction.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, session.Interaction)

	screen := vt.NewScreen(width, height)
	screen.Feed(terminal.String())
	projected := strings.Join(append(screen.ScrollbackLines(), screen.Lines(0, height)...), "\n")
	// presenter 会给每行加 "> " 前缀，无法整段匹配；按首行唯一 marker 计数即可
	// 判定 user 块是否只有一个（composer 若未清除，首行会出现两次）。
	for _, marker := range []string{
		"check ./.aicli/runtime.yaml配置 有没有schedule:",
		"enabled: true",
	} {
		if count := strings.Count(projected, marker); count != 1 {
			t.Fatalf("首条多行 prompt 的 %q 在最终终端投影中出现 %d 次，want 1\nprojection:\n%s",
				marker, count, projected)
		}
	}
}
