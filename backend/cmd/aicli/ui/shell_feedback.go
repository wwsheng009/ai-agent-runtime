package ui

import (
	"fmt"
	"strings"
)

// ShellFeedback Shell 命令执行反馈组件
type ShellFeedback struct {
	theme    *Theme
	command  string
	output   string
	duration string
	exitCode int
	showFull bool
}

// NewShellFeedback 创建新的 Shell 反馈组件
func NewShellFeedback(command string) *ShellFeedback {
	return &ShellFeedback{
		theme:    GetTheme(ThemeAuto),
		command:  command,
		showFull: false,
	}
}

// SetTheme 设置主题
func (s *ShellFeedback) SetTheme(theme *Theme) *ShellFeedback {
	s.theme = theme
	return s
}

// SetOutput 设置输出
func (s *ShellFeedback) SetOutput(output string) *ShellFeedback {
	s.output = output
	return s
}

// SetDuration 设置执行时长
func (s *ShellFeedback) SetDuration(duration string) *ShellFeedback {
	s.duration = duration
	return s
}

// SetExitCode 设置退出码
func (s *ShellFeedback) SetExitCode(code int) *ShellFeedback {
	s.exitCode = code
	return s
}

// ShowFull 设置是否显示完整输出
func (s *ShellFeedback) ShowFull(show bool) *ShellFeedback {
	s.showFull = show
	return s
}

// Format 格式化反馈信息
func (s *ShellFeedback) Format() string {
	var builder strings.Builder

	// 命令
	builder.WriteString(s.theme.CommandColor.Sprintf("%s 执行: %s%s\n",
		s.theme.CommandIcon, s.theme.ShellIcon, s.command))

	// 输出
	if s.output != "" {
		if s.showFull || len(s.output) <= 200 {
			// 显示完整输出
			lines := strings.Split(s.output, "\n")
			for _, line := range lines {
				if line != "" {
					builder.WriteString(s.theme.Dimmed(fmt.Sprintf("  │ %s\n", line)))
				}
			}
		} else {
			// 截断输出
			truncated := s.output[:200]
			builder.WriteString(s.theme.Dimmed(fmt.Sprintf("  │ %s...\n", truncated)))
			builder.WriteString(fmt.Sprintf("  ╰─ %s\n", s.theme.InfoColor.Sprintf("(%d 字符)", len(s.output))))
		}
	}

	// 状态信息
	var statusParts []string

	if s.duration != "" {
		statusParts = append(statusParts, fmt.Sprintf("%s %s",
			s.theme.InfoIcon, s.theme.Dimmed(s.duration)))
	}

	if s.exitCode != 0 {
		statusParts = append(statusParts, fmt.Sprintf("%s 退出码: %s",
			s.theme.ErrorIcon, s.theme.ErrorColor.Sprintf("%d", s.exitCode)))
	} else {
		statusParts = append(statusParts, fmt.Sprintf("%s 成功",
			s.theme.SuccessIcon))
	}

	if len(statusParts) > 0 {
		builder.WriteString(fmt.Sprintf("  %s\n",
			strings.Join(statusParts, " ")))
	}

	return builder.String()
}

// Print 打印反馈信息
func (s *ShellFeedback) Print() {
	fmt.Println(s.Format())
}

// DisplayShellCommand 显示执行的 Shell 命令
func DisplayShellCommand(command string) {
	theme := GetTheme(ThemeAuto)
	fmt.Printf("%s 执行: %s%s\n",
		theme.CommandColor.Sprint(theme.CommandIcon),
		theme.CommandColor.Sprint(theme.ShellIcon),
		theme.CommandColor.Sprint(command))
}

// DisplayShellOutput 显示 Shell 命令输出
func DisplayShellOutput(output string, maxLines int) {
	theme := GetTheme(ThemeAuto)

	if output == "" {
		return
	}

	lines := strings.Split(output, "\n")
	displayLines := lines

	// 限制显示行数
	if maxLines > 0 && len(lines) > maxLines {
		displayLines = lines[:maxLines]
		for _, line := range displayLines {
			if line != "" {
				fmt.Println(theme.Dimmed(fmt.Sprintf("  │ %s", line)))
			}
		}
		fmt.Printf("  ╰─ %s\n", theme.InfoColor.Sprintf("(已省略 %d 行)", len(lines)-maxLines))
	} else {
		for _, line := range lines {
			if line != "" {
				fmt.Println(theme.Dimmed(fmt.Sprintf("  │ %s", line)))
			}
		}
	}
}

// DisplayShellError 显示 Shell 命令错误
func DisplayShellError(err error, exitCode int) {
	if err != nil {
		PrintError("Shell 命令执行失败: %v", err)
	}

	if exitCode != 0 {
		PrintWarning("退出码: %d", exitCode)
	}
}

// FormatShellSummary 格式化命令执行摘要
func FormatShellSummary(command string, exitCode int, durationMs int64) string {
	theme := GetTheme(ThemeAuto)

	// 计算时长
	var durationStr string
	if durationMs >= 1000 {
		durationStr = fmt.Sprintf("%.2fs", float64(durationMs)/1000.0)
	} else {
		durationStr = fmt.Sprintf("%dms", durationMs)
	}

	// 状态图标
	var statusIcon string
	if exitCode == 0 {
		statusIcon = theme.SuccessIcon
	} else {
		statusIcon = theme.ErrorIcon
	}

	return fmt.Sprintf("%s %s%s %s (%s)",
		statusIcon,
		theme.ShellIcon,
		theme.CommandColor.Sprint(command),
		theme.Dimmed(durationStr),
		theme.ColorizeSuccess(fmt.Sprintf("exit=%d", exitCode)),
	)
}
