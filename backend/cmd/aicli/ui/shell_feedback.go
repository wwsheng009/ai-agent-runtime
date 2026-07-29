package ui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/cell"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
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

// truncateRunes returns the first n runes of s (UTF-8 safe).
func truncateRunes(s string, n int) string {
	if n <= 0 || s == "" {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i := 0
	for idx := range s {
		if i == n {
			return s[:idx]
		}
		i++
	}
	return s
}

// Document builds the structured shell feedback model (roles, no pre-colored ANSI).
func (s *ShellFeedback) Document() render.Document {
	theme := s.theme
	if theme == nil {
		theme = GetTheme(ThemeAuto)
	}
	var lines []render.Line

	// Command header — sanitize to prevent control-sequence injection.
	safeCommand := SanitizeTerminalText(s.command)
	header := fmt.Sprintf("%s 执行: %s%s", theme.CommandIcon, theme.ShellIcon, safeCommand)
	lines = append(lines, render.Line{Spans: []render.Span{{
		Text:  header,
		Style: render.Style{Role: string(style.RoleCommand)},
	}}})

	// Output — Phase 3 head/tail preview via cell; never pass raw ESC/OSC.
	if strings.TrimSpace(s.output) != "" {
		opts := cell.DefaultPreviewOptions()
		opts.AllowANSI = false
		if s.showFull {
			opts.MaxLines = 10000
			opts.HeadLines = 10000
			opts.TailLines = 0
			opts.MaxBytes = 0
		} else {
			opts.MaxLines = 8
			opts.HeadLines = 4
			opts.TailLines = 2
			opts.MaxBytes = 4096
			opts.MaxLineWidth = 200
		}
		preview := cell.BuildPreview(s.output, opts)
		for _, line := range preview.Lines {
			plain := render.PlainBackend{}.Render(render.Document{
				Blocks: []render.Block{{Lines: []render.Line{line}}},
			})
			if plain == "" {
				continue
			}
			lines = append(lines, render.Line{Spans: []render.Span{{
				Text:  "  │ " + plain,
				Style: render.Style{Role: string(style.RoleTextMuted), Dim: true},
			}}})
		}
		if preview.OmittedLines > 0 || preview.ByteTruncated {
			lines = append(lines, render.Line{Spans: []render.Span{
				{Text: "  ╰─ ", Style: render.Style{Role: string(style.RoleTextMuted), Dim: true}},
				{Text: fmt.Sprintf("(%d 行, 已截断)", preview.TotalLines), Style: render.Style{Role: string(style.RoleInfo)}},
			}})
		}
	}

	// Status row.
	var statusSpans []render.Span
	if s.duration != "" {
		statusSpans = append(statusSpans,
			render.Span{Text: theme.InfoIcon + " ", Style: render.Style{Role: string(style.RoleInfo)}},
			render.Span{Text: s.duration, Style: render.Style{Role: string(style.RoleTextMuted), Dim: true}},
		)
	}
	if s.exitCode != 0 {
		if len(statusSpans) > 0 {
			statusSpans = append(statusSpans, render.Span{Text: " "})
		}
		statusSpans = append(statusSpans,
			render.Span{Text: theme.ErrorIcon + " 退出码: ", Style: render.Style{Role: string(style.RoleError)}},
			render.Span{Text: fmt.Sprintf("%d", s.exitCode), Style: render.Style{Role: string(style.RoleError)}},
		)
	} else {
		if len(statusSpans) > 0 {
			statusSpans = append(statusSpans, render.Span{Text: " "})
		}
		statusSpans = append(statusSpans, render.Span{
			Text:  theme.SuccessIcon + " 成功",
			Style: render.Style{Role: string(style.RoleSuccess)},
		})
	}
	if len(statusSpans) > 0 {
		indented := append([]render.Span{{Text: "  "}}, statusSpans...)
		lines = append(lines, render.Line{Spans: indented})
	}

	return render.Document{Blocks: []render.Block{{
		Kind:  render.BlockParagraph,
		Lines: lines,
	}}}
}

// Format 格式化反馈信息，并按当前终端能力解析语义颜色。
func (s *ShellFeedback) Format() string {
	doc := s.Document()
	return renderDocumentWithProfile(doc, s.theme)
}

// Print 打印反馈信息
func (s *ShellFeedback) Print() {
	fmt.Println(s.Format())
}

// FormatShellCommand formats a shell command header via Document roles.
// Untrusted command text is sanitized before styling.
func FormatShellCommand(command string) string {
	theme := GetTheme(ThemeAuto)
	safeCommand := SanitizeTerminalText(command)
	header := fmt.Sprintf("%s 执行: %s%s", theme.CommandIcon, theme.ShellIcon, safeCommand)
	doc := render.Document{Blocks: []render.Block{{
		Kind: render.BlockParagraph,
		Lines: []render.Line{{Spans: []render.Span{{
			Text:  header,
			Style: render.Style{Role: string(style.RoleCommand)},
		}}}},
	}}}
	return strings.TrimRight(renderDocumentWithProfile(doc, theme), "\n")
}

// DisplayShellCommand 显示执行的 Shell 命令
func DisplayShellCommand(command string) {
	fmt.Println(FormatShellCommand(command))
}

// FormatShellOutput formats shell stdout/stderr for display.
// Raw ESC/OSC is stripped via cell.BuildPreview (AllowANSI=false).
// maxLines <= 0 shows a large but still bounded head-only preview.
func FormatShellOutput(output string, maxLines int) string {
	if strings.TrimSpace(output) == "" {
		return ""
	}
	theme := GetTheme(ThemeAuto)
	opts := cell.DefaultPreviewOptions()
	opts.AllowANSI = false
	opts.MaxLineWidth = 200
	if maxLines > 0 {
		opts.MaxLines = maxLines
		opts.HeadLines = maxLines
		opts.TailLines = 0
		opts.MaxBytes = 0
	} else {
		// Keep a hard ceiling so unbounded dumps cannot freeze the UI.
		opts.MaxLines = 10000
		opts.HeadLines = 10000
		opts.TailLines = 0
		opts.MaxBytes = 0
	}
	preview := cell.BuildPreview(output, opts)

	var lines []render.Line
	for _, line := range preview.Lines {
		plain := render.PlainBackend{}.Render(render.Document{
			Blocks: []render.Block{{Lines: []render.Line{line}}},
		})
		if plain == "" {
			continue
		}
		// BuildPreview already emits an English omission marker; keep it muted.
		lines = append(lines, render.Line{Spans: []render.Span{{
			Text:  "  │ " + plain,
			Style: render.Style{Role: string(style.RoleTextMuted), Dim: true},
		}}})
	}
	if preview.OmittedLines > 0 || preview.ByteTruncated {
		lines = append(lines, render.Line{Spans: []render.Span{
			{Text: "  ╰─ ", Style: render.Style{Role: string(style.RoleTextMuted), Dim: true}},
			{Text: fmt.Sprintf("(已省略 %d 行)", preview.OmittedLines), Style: render.Style{Role: string(style.RoleInfo)}},
		}})
	}
	if len(lines) == 0 {
		return ""
	}
	doc := render.Document{Blocks: []render.Block{{
		Kind:  render.BlockParagraph,
		Lines: lines,
	}}}
	return strings.TrimRight(renderDocumentWithProfile(doc, theme), "\n")
}

// DisplayShellOutput 显示 Shell 命令输出
func DisplayShellOutput(output string, maxLines int) {
	formatted := FormatShellOutput(output, maxLines)
	if formatted == "" {
		return
	}
	fmt.Println(formatted)
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

// FormatShellSummary 格式化命令执行摘要（命令文本先消毒）。
func FormatShellSummary(command string, exitCode int, durationMs int64) string {
	theme := GetTheme(ThemeAuto)
	safeCommand := SanitizeTerminalText(command)

	var durationStr string
	if durationMs >= 1000 {
		durationStr = fmt.Sprintf("%.2fs", float64(durationMs)/1000.0)
	} else {
		durationStr = fmt.Sprintf("%dms", durationMs)
	}

	statusRole := style.RoleSuccess
	statusIcon := theme.SuccessIcon
	if exitCode != 0 {
		statusRole = style.RoleError
		statusIcon = theme.ErrorIcon
	}
	doc := render.Document{Blocks: []render.Block{{
		Kind: render.BlockParagraph,
		Lines: []render.Line{{Spans: []render.Span{
			{Text: statusIcon + " ", Style: render.Style{Role: string(statusRole)}},
			{Text: theme.ShellIcon + safeCommand, Style: render.Style{Role: string(style.RoleCommand)}},
			{Text: " " + durationStr, Style: render.Style{Role: string(style.RoleTextMuted), Dim: true}},
			{Text: " (", Style: render.Style{Role: string(style.RoleTextMuted)}},
			{Text: fmt.Sprintf("exit=%d", exitCode), Style: render.Style{Role: string(statusRole)}},
			{Text: ")", Style: render.Style{Role: string(style.RoleTextMuted)}},
		}}},
	}}}
	return strings.TrimRight(renderDocumentWithProfile(doc, theme), "\n")
}
