package commands

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/keymap"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/termcaps"
	"github.com/wwsheng009/ai-agent-runtime/internal/clipboardimage"
	"golang.org/x/term"
)

// fixedChatShortcuts 列出不可重映射的固定按键。keymap 只纳管交互层动作；
// 行编辑器内部键（Emacs 风格移动/删除）与终端差异键保持固定，避免把
// Backspace 之类的终端语义差异暴露给用户配置。
var fixedChatShortcuts = []struct {
	Keys    string
	Summary string
}{
	{"enter", "提交当前输入"},
	{"esc", "中断运行中的回合；空输入时打开回退选择器"},
	{"ctrl+c", "中断当前输入；空输入时退出 chat"},
	{"ctrl+d", "空输入时退出 chat，否则删除前向字符"},
	{"ctrl+j / ctrl+o", "插入换行（部分终端把 shift+enter 送成同一序列）"},
	{"tab", "触发补全（补全弹层持有该键）"},
	{"up / down / left / right", "光标与历史导航（行编辑器固定键）"},
}

func handleHotkeysCommand(session *ChatSession, command string) bool {
	argument := strings.TrimSpace(extractCommandArgument(command))
	switch {
	case argument == "":
	case strings.EqualFold(argument, "reload"):
		reloadChatKeymapRegistry()
		printChatCommandOutput(session, "已重新加载按键配置: "+chatKeybindingsPath())
	default:
		printChatCommandOutput(session, "错误: /hotkeys 仅支持空参数或 reload\n用法: /hotkeys 或 /hotkeys reload")
		return false
	}

	registry := chatKeymapRegistry()
	lines := []string{"快捷键（当前生效）"}
	for _, binding := range registry.Effective() {
		chords := formatChatBindingChords(binding.Chords)
		if chords == "" {
			chords = "（已禁用）"
		}
		line := fmt.Sprintf("  %-22s %-16s %s", binding.Action, chords, binding.Description)
		if binding.Source == "user" {
			line += "  [用户覆盖]"
		}
		lines = append(lines, line)
	}

	caps := termcaps.Detect(os.Getenv, runtime.GOOS, chatHotkeysStdoutIsTTY())
	// 剪贴板图片能力是运行时事实（平台 + 外部工具），由命令层覆盖 termcaps 的纯函数结果。
	if available, reason := clipboardimage.Availability(); available {
		caps.ClipboardImage = termcaps.Support{Supported: true, Reason: reason + "；alt+v 或 /attach paste 加入附件"}
	} else {
		caps.ClipboardImage = termcaps.Support{Supported: false, Reason: reason + "；可先用 /attach <path> 添加图片附件"}
	}
	lines = append(lines, "", "本终端: "+caps.Name)
	lines = append(lines,
		fmt.Sprintf("  %-16s %s", "shift+tab", caps.ShiftTab.Describe()),
		fmt.Sprintf("  %-16s %s", "alt+m", caps.AltM.Describe()),
		fmt.Sprintf("  %-16s %s", "多行输入", caps.ModifiedEnter.Describe()),
		fmt.Sprintf("  %-16s %s", "粘贴", caps.BracketedPaste.Describe()),
		fmt.Sprintf("  %-16s %s", "剪贴板文本", caps.ClipboardText.Describe()),
		fmt.Sprintf("  %-16s %s", "剪贴板图片", caps.ClipboardImage.Describe()),
	)
	for _, note := range caps.Notes {
		lines = append(lines, "  - "+note)
	}

	lines = append(lines, "", "固定快捷键（不可重映射）")
	for _, shortcut := range fixedChatShortcuts {
		lines = append(lines, fmt.Sprintf("  %-24s %s", shortcut.Keys, shortcut.Summary))
	}

	lines = append(lines, "", "配置文件: "+chatKeybindingsPath())
	lines = append(lines, `用法: {"app.permission.cycle": ["alt+c"]}；空数组 [] 表示禁用；改完执行 /hotkeys reload 生效`)
	lines = append(lines, "可重映射范围: shift+tab、alt+m、ctrl+t、alt+v；行编辑器内部键与终端差异键保持固定（见上表）")
	if warnings := registry.Warnings(); len(warnings) > 0 {
		lines = append(lines, "", "配置提示:")
		for _, warning := range warnings {
			lines = append(lines, "  - "+warning)
		}
	}
	printChatCommandOutput(session, strings.Join(lines, "\n"))
	return false
}

// chatHotkeysStdoutIsTTY 报告标准输出是否为终端；headless/JSON 输出下按键矩阵
// 不适用，需要如实标注。
func chatHotkeysStdoutIsTTY() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
}

func formatChatBindingChords(chords []keymap.Chord) string {
	if len(chords) == 0 {
		return ""
	}
	parts := make([]string, 0, len(chords))
	for _, chord := range chords {
		if text := strings.TrimSpace(chord.String()); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, ", ")
}
