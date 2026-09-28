package commands

import (
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

type chatSlashHelpRow struct {
	Label   string
	Summary string
}

func buildChatSlashHelpLines() []string {
	rows := make([]chatSlashHelpRow, 0, len(chatSlashCommandCatalog()))
	for _, spec := range chatSlashCommandCatalog() {
		if spec.Hidden {
			continue
		}
		label := spec.Usage
		if label == "" {
			label = spec.Name
		}
		if len(spec.Aliases) > 0 {
			label += ", " + strings.Join(spec.Aliases, ", ")
		}
		summary := strings.TrimSpace(spec.Summary)
		if summary == "" {
			summary = "命令"
		}
		// 批次 5：副屏命令在帮助里显式标注退出键（Esc 一次回主屏，I4）。
		if chatSlashHelpPrimaryScreen(spec.Name) {
			summary += chatSlashHelpScreenMarker
		}
		rows = append(rows, chatSlashHelpRow{Label: label, Summary: summary})
	}

	maxLabelWidth := 0
	for _, row := range rows {
		if w := ui.DisplayWidth(row.Label); w > maxLabelWidth {
			maxLabelWidth = w
		}
	}
	if maxLabelWidth < 1 {
		maxLabelWidth = 1
	}

	lines := []string{"", "可用命令:"}
	for _, row := range rows {
		lines = append(lines, fmt.Sprintf("  %-*s - %s", maxLabelWidth, row.Label, row.Summary))
	}

	lines = append(lines,
		"",
		"Shell 命令:",
		"  ![--output-bytes-cap <bytes> | --disable-output-cap] <命令>",
		"                    - 执行 shell 命令并分享输出给 AI",
		"  /shell [--output-bytes-cap <bytes> | --disable-output-cap] <命令>",
		"  /cmd   [--output-bytes-cap <bytes> | --disable-output-cap] <命令>",
		"                    - 执行 shell 命令并分享输出给 AI",
		"                      例如: !git status --short",
		"                            /shell --output-bytes-cap 1048576 git diff --stat",
		"                            /cmd --disable-output-cap git diff HEAD -- README.md",
		"                      安全保护: 超时 30s；终端实时输出完整显示，分享给 AI 的 capture 默认保留 256KB，可通过上述参数覆盖",
		"",
	)
	return lines
}

// chatSlashHelpScreenMarker 是副屏命令的统一帮助标注（Esc 契约，方案 §6 批次 5）。
const chatSlashHelpScreenMarker = "（副屏，Esc 返回）"

// chatSlashHelpPrimaryScreen 报告命令的主入口（裸命令声明）是否走副屏交互。
// 仅主入口为 screen 的命令标注，避免把「子命令才开副屏」的命令整体误标。
func chatSlashHelpPrimaryScreen(name string) bool {
	entry, ok := runtimeCommandRegistry[name]
	if !ok || entry.Bare == nil {
		return false
	}
	return entry.Bare.Mode == runtimeModeScreen
}

func printChatSlashHelp(session *ChatSession) {
	printChatCommandOutput(session, strings.Join(buildChatSlashHelpLines(), "\n"))
}
