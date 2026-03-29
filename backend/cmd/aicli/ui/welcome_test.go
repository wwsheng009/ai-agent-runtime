package ui

import (
	"strings"
	"testing"

	"github.com/fatih/color"
)

func TestPrintWelcomeWithConfig_AlignsMetadataAndHints(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() {
		color.NoColor = oldNoColor
	}()
	SetTheme(ThemeAuto)

	output := captureUIStdout(t, func() {
		PrintWelcomeWithConfig(&WelcomeConfig{
			AppName:     "AI Gateway CLI",
			Version:     "v1.0.0",
			Description: "智能 AI 对话终端",
			ShowVersion: true,
			ShowHint:    true,
			Style:       "detailed",
		})
	})

	for _, expected := range []string{
		"Version:     v1.0.0",
		"Description: 智能 AI 对话终端",
		"  💡 输入 /help 查看命令帮助",
		"  💡 输入 ! 前缀执行 Shell 命令",
		"  💡 输入 /exit 或 Ctrl+C 退出",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected welcome output to contain %q, got:\n%s", expected, output)
		}
	}
}
