package cell

import (
	"fmt"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"github.com/wwsheng009/ai-agent-runtime/internal/pathdisplay"
)

// ToolCell is the structured tool-call presentation model.
// Callers fill fields; Document() produces the render tree.
type ToolCell struct {
	FunctionName string
	Arguments    map[string]interface{}
	Status       EventStatus
	Error        error
	Result       string
	Duration     string
	ExitCode     *int
	// AllowANSIResult enables SGR-preserving preview for trusted local terminal output.
	AllowANSIResult bool
	Preview         PreviewOptions
}

// Document builds the tool cell render model.
func (t ToolCell) Document() render.Document {
	var lines []render.Line

	// Header: status + name + arg summary
	var header []render.Span
	header = append(header, statusMarkerSpan(t.Status))
	header = append(header, render.Span{
		Text:  formatToolName(t.FunctionName),
		Style: render.Style{Role: string(style.RoleTool), Bold: true},
	})
	argSummary, filePathLine := formatArgPresentation(t.Arguments)
	if argSummary != "" {
		header = append(header, render.Span{
			Text:  " " + argSummary,
			Style: render.Style{Role: string(style.RoleTextSecondary)},
		})
	}
	if t.Duration != "" {
		header = append(header, render.Span{
			Text:  "  " + t.Duration,
			Style: render.Style{Role: string(style.RoleTextMuted), Dim: true},
		})
	}
	if t.ExitCode != nil {
		codeStyle := render.Style{Role: string(style.RoleTextMuted), Dim: true}
		if *t.ExitCode != 0 {
			codeStyle = render.Style{Role: string(style.RoleError)}
		}
		header = append(header, render.Span{
			Text:  fmt.Sprintf(" exit %d", *t.ExitCode),
			Style: codeStyle,
		})
	}
	if t.Status == StatusError {
		errText := ""
		if t.Error != nil {
			errText = t.Error.Error()
		} else if strings.TrimSpace(t.Result) != "" {
			errText = firstLine(render.ANSIToPlain(t.Result))
		}
		if errText != "" {
			header = append(header, render.Span{
				Text:  " - " + render.TruncateText(errText, 160, "…"),
				Style: render.Style{Role: string(style.RoleError)},
			})
		}
	}
	lines = append(lines, render.Line{Spans: header})
	if filePathLine != "" {
		lines = append(lines, render.Line{Spans: []render.Span{{
			Text:  "  file_path: " + filePathLine,
			Style: render.Style{Role: string(style.RoleTextSecondary)},
		}}})
	}

	// Running body: compact live progress under the header (ActiveBand updates).
	if t.Status == StatusRunning && strings.TrimSpace(t.Result) != "" {
		rows := 0
		for _, row := range strings.Split(strings.TrimRight(t.Result, "\r\n"), "\n") {
			row = strings.TrimSpace(row)
			if row == "" {
				continue
			}
			if rows >= 3 {
				break
			}
			lines = append(lines, render.Line{Spans: []render.Span{{
				Text:  "  " + render.TruncateText(row, 72, "…"),
				Style: render.Style{Role: string(style.RoleTextMuted), Dim: true},
			}}})
			rows++
		}
	}

	// Success/error body: head/tail preview of result
	if t.Status == StatusSuccess || (t.Status == StatusError && t.Error == nil && strings.TrimSpace(t.Result) != "") {
		opts := t.Preview
		if opts.MaxLines == 0 {
			opts = DefaultPreviewOptions()
			// Keep compact tool summaries closer to legacy 4-line default.
			opts.MaxLines = 6
			opts.HeadLines = 4
			opts.TailLines = 2
			opts.MaxBytes = 1024
		}
		opts.AllowANSI = t.AllowANSIResult
		preview := BuildPreview(t.Result, opts)
		for _, pl := range preview.Lines {
			// Indent preview under header.
			indented := append([]render.Span{{
				Text:  "    | ",
				Style: render.Style{Role: string(style.RoleBorder), Dim: true},
			}}, pl.Spans...)
			lines = append(lines, render.Line{Spans: indented})
		}
	}

	return render.Document{Blocks: []render.Block{{
		Kind:  render.BlockParagraph,
		Lines: lines,
	}}}
}

// FormatANSI encodes with theme.
func (t ToolCell) FormatANSI(theme style.ThemeContext) string {
	return style.RenderDocument(t.Document(), theme)
}

// FormatPlain returns plain text.
func (t ToolCell) FormatPlain() string {
	return render.PlainBackend{}.Render(t.Document())
}

func statusMarkerSpan(st EventStatus) render.Span {
	switch st {
	case StatusSuccess:
		return render.Span{Text: "✓ ", Style: render.Style{Role: string(style.RoleSuccess), Bold: true}}
	case StatusError, StatusDenied:
		return render.Span{Text: "✗ ", Style: render.Style{Role: string(style.RoleError), Bold: true}}
	case StatusRunning:
		// Stable width marker (not a spinner) so redraws do not shift columns.
		return render.Span{Text: "○ ", Style: render.Style{Role: string(style.RoleWarning)}}
	case StatusPending:
		return render.Span{Text: "· ", Style: render.Style{Role: string(style.RoleTextMuted), Dim: true}}
	default:
		return render.Span{Text: "○ ", Style: render.Style{Role: string(style.RoleTextMuted)}}
	}
}

func formatToolName(name string) string {
	shortNames := map[string]string{
		"shell":                 "Shell",
		"bash":                  "Shell",
		"execute_shell_command": "Shell",
	}
	if short, ok := shortNames[name]; ok {
		return short
	}
	name = strings.TrimPrefix(name, "execute_")
	name = strings.TrimPrefix(name, "run_")
	name = strings.TrimPrefix(name, "get_")
	name = strings.TrimPrefix(name, "mcp_")
	return toCamel(name)
}

func toCamel(s string) string {
	s = strings.ReplaceAll(s, "-", "_")
	parts := strings.Split(s, "_")
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		b.WriteString(strings.ToUpper(p[:1]) + strings.ToLower(p[1:]))
	}
	return b.String()
}

func formatArgSummary(args map[string]interface{}) string {
	if len(args) == 0 {
		return ""
	}
	priority := []string{"command", "path", "file", "file_path", "filepath", "url", "query", "pattern", "name", "title", "message", "text"}
	for _, key := range priority {
		if val, ok := args[key]; ok {
			return formatArgValue(val)
		}
	}
	// Stable key order for remaining simple args.
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, 3)
	for _, k := range keys {
		if len(parts) >= 3 {
			parts = append(parts, "...")
			break
		}
		switch v := args[k].(type) {
		case string:
			parts = append(parts, k+"="+render.TruncateText(v, 20, "…"))
		case int, int64, float64, bool:
			parts = append(parts, fmt.Sprintf("%s=%v", k, v))
		default:
			continue
		}
	}
	return strings.Join(parts, " ")
}

func formatArgPresentation(args map[string]interface{}) (string, string) {
	fileKey, filePath := pathdisplay.File(args)
	if fileKey == "" || !pathdisplay.NeedsOwnLine(filePath) {
		return formatArgSummary(args), ""
	}
	remaining := make(map[string]interface{}, len(args)-1)
	for key, value := range args {
		if pathdisplay.IsFileArgument(key) {
			continue
		}
		remaining[key] = value
	}
	return formatArgSummary(remaining), filePath
}

func formatArgValue(val interface{}) string {
	switch v := val.(type) {
	case string:
		return render.TruncateText(strings.TrimSpace(v), 60, "…")
	case int, int64, float64, bool:
		return fmt.Sprintf("%v", v)
	default:
		return "..."
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return strings.Join(strings.Fields(s), " ")
}
