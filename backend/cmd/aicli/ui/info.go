package ui

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

const sessionInfoLabelWidth = 10

// SessionInfo 描述 chat 启动时展示的会话信息
type SessionInfo struct {
	ProviderName string
	Protocol     string
	ModelName    string
	EndpointURL  string
	Host         string
	KeyCount     int
	Timeout      string
	IsStream     bool
	// SupportsFast is true only when the active protocol can use Codex Fast
	// mode (service_tier=priority). When false, Fast is omitted from the UI.
	SupportsFast     bool
	IsFast           bool
	ReasoningEnabled bool
}

// PrintSessionInfo 打印会话信息
func PrintSessionInfo(info SessionInfo) bool {
	writeInfoDocument(SessionInfoDocument(info, GetTerminalWidth()))
	return true
}

// SessionInfoDocument builds the structured startup summary shown before chat.
func SessionInfoDocument(info SessionInfo, width int) render.Document {
	if width <= 0 {
		width = 80
	}
	theme := GetTheme(ThemeAuto)
	rootPrefix := theme.SystemIcon + " "
	childPrefix := strings.Repeat(" ", render.Width(rootPrefix))
	lines := []render.Line{{}, infoSeparatorLine(width)}

	lines = append(lines, sessionInfoRow(rootPrefix, "Provider:", "( "+info.ProviderName+" )", style.RoleSuccess))
	if info.Protocol != "" {
		lines = append(lines, sessionInfoRow(childPrefix, "Protocol:", info.Protocol, style.RoleTextMuted))
	}
	if info.EndpointURL != "" {
		lines = append(lines, sessionInfoRow(childPrefix, "Endpoint:", info.EndpointURL, style.RoleTextMuted))
	}
	if info.Host != "" {
		lines = append(lines, sessionInfoRow(childPrefix, "Host:", info.Host, style.RoleTextMuted))
	}
	if info.KeyCount > 0 {
		lines = append(lines, sessionInfoRow(childPrefix, "Auth Keys:", fmt.Sprintf("%d", info.KeyCount), style.RoleTextMuted))
	}
	if info.Timeout != "" {
		lines = append(lines, sessionInfoRow(childPrefix, "Timeout:", info.Timeout, style.RoleTextMuted))
	}
	lines = append(lines, sessionInfoRow(rootPrefix, "Model:", info.ModelName, style.RoleSuccess))

	streamStatus := "off"
	streamRole := style.RoleTextMuted
	if info.IsStream {
		streamStatus, streamRole = "on", style.RoleSuccess
	}
	lines = append(lines, sessionInfoRow(rootPrefix, "Stream:", streamStatus, streamRole))

	if info.SupportsFast {
		fastStatus := "off"
		fastRole := style.RoleTextMuted
		if info.IsFast {
			fastStatus, fastRole = "on", style.RoleSuccess
		}
		lines = append(lines, sessionInfoRow(rootPrefix, "Fast:", fastStatus, fastRole))
	}
	if info.ReasoningEnabled {
		lines = append(lines, sessionInfoRow(rootPrefix, "Reasoning:", "enabled", style.RoleWarning))
	}
	lines = append(lines, infoSeparatorLine(width), render.Line{})
	return constrainInfoDocument(render.LinesDoc(lines...), width)
}

func sessionInfoChildPrefix(theme *Theme) string {
	return strings.Repeat(" ", render.Width(theme.SystemIcon+" "))
}

func sessionInfoRow(prefix, label, value string, valueRole style.Role) render.Line {
	labelLine := render.Pad(render.Line{Spans: []render.Span{{
		Text:  label,
		Style: render.Style{Role: string(style.RoleMetaLabel)},
	}}}, sessionInfoLabelWidth, render.AlignLeft)
	spans := []render.Span{{Text: prefix, Style: render.Style{Role: string(style.RoleInfo)}}}
	spans = append(spans, labelLine.Spans...)
	spans = append(spans,
		render.Span{Text: " "},
		render.Span{Text: value, Style: render.Style{Role: string(valueRole)}},
	)
	return render.Line{Spans: spans}
}

func infoSeparatorLine(width int) render.Line {
	theme := GetTheme(ThemeAuto)
	fill := theme.BorderHorizontal
	if strings.TrimSpace(fill) == "" {
		fill = "═"
	}
	// Separator glyphs may be multi-byte; build by cell width.
	doc := style.SeparatorDocument(style.SeparatorModel{
		Kind:  style.SeparatorThick,
		Width: width,
		Fill:  fill,
	})
	if len(doc.Blocks) == 0 || len(doc.Blocks[0].Lines) == 0 {
		return render.Line{}
	}
	return doc.Blocks[0].Lines[0]
}

func infoKeyValueDocument(key, value string, labelWidth int) render.Document {
	key = strings.TrimSpace(key)
	if key == "" && value == "" {
		return render.Document{}
	}
	if labelWidth < 0 {
		labelWidth = 0
	}
	labelText := key
	if key != "" && !strings.HasSuffix(key, ":") {
		labelText = key + ":"
	}
	labelLine := render.Line{Spans: []render.Span{{
		Text:  labelText,
		Style: render.Style{Role: string(style.RoleMetaLabel)},
	}}}
	if labelWidth > 0 {
		labelLine = render.Pad(labelLine, labelWidth, render.AlignLeft)
	}
	spans := append([]render.Span{}, labelLine.Spans...)
	if value != "" {
		if len(spans) > 0 {
			spans = append(spans, render.Span{Text: " "})
		}
		spans = append(spans, render.Span{
			Text:  value,
			Style: render.Style{Role: string(style.RoleTextSecondary)},
		})
	}
	return render.LinesDoc(render.Line{Spans: spans})
}

func renderInfoDocument(doc render.Document) string {
	return style.RenderDocument(doc, CurrentThemeContext())
}

func writeInfoDocument(doc render.Document) {
	text := renderInfoDocument(doc)
	if text == "" {
		return
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	_, _ = WriteTerminalText(os.Stdout, text)
}

// PrintStatus 打印状态信息
func PrintStatus(label, value string) {
	writeInfoDocument(infoKeyValueDocument(label, value, 15))
}

// PrintTable 打印表格
func PrintTable(headers []string, rows [][]string) {
	if len(headers) == 0 || len(rows) == 0 {
		return
	}
	writeInfoDocument(TableDocument(headers, rows, GetTerminalWidth()))
}

// TableDocument builds a deterministic, cell-width-aware plain table.
func TableDocument(headers []string, rows [][]string, width int) render.Document {
	if len(headers) == 0 || len(rows) == 0 {
		return render.Document{}
	}
	colCount := len(headers)
	natural := make([]int, colCount)
	for i, header := range headers {
		natural[i] = max(1, render.Width(header))
	}
	minimum := append([]int(nil), natural...)
	for _, row := range rows {
		for i, cell := range row {
			if i >= colCount {
				break
			}
			if cellWidth := render.Width(cell); cellWidth > natural[i] {
				natural[i] = cellWidth
			}
		}
	}
	gap := infoTableGap(width, colCount)
	colWidths := fitInfoColumnWidthsWithMinimum(natural, minimum, width, gap)
	lines := []render.Line{infoTableRow(headers, colWidths, gap, style.RoleMetaLabel)}
	separator := make([]render.Span, 0, colCount*2-1)
	for index, colWidth := range colWidths {
		if index > 0 {
			separator = append(separator, render.Span{Text: strings.Repeat(" ", gap), Style: render.Style{Role: string(style.RoleBorder)}})
		}
		separator = append(separator, render.Span{
			Text:  strings.Repeat("-", colWidth),
			Style: render.Style{Role: string(style.RoleBorder)},
		})
	}
	lines = append(lines, render.Line{Spans: separator})
	for _, row := range rows {
		lines = append(lines, infoTableRow(row, colWidths, gap, style.RoleTextPrimary))
	}
	return constrainInfoDocument(render.LinesDoc(lines...), width)
}

// fitInfoColumnWidths shrinks natural column widths to fit totalWidth with a
// fixed gap between columns. When totalWidth is unknown/non-positive, natural
// widths are kept. Each column keeps at least 1 cell.
func fitInfoColumnWidths(natural []int, totalWidth, gap int) []int {
	return fitInfoColumnWidthsWithMinimum(natural, nil, totalWidth, gap)
}

func fitInfoColumnWidthsWithMinimum(natural, minimum []int, totalWidth, gap int) []int {
	n := len(natural)
	out := append([]int(nil), natural...)
	if n == 0 {
		return out
	}
	if gap < 0 {
		gap = 0
	}
	for i := range out {
		if out[i] < 1 {
			out[i] = 1
		}
	}
	if totalWidth <= 0 {
		return out
	}
	available := totalWidth - gap*(n-1)
	if available < n {
		available = n
	}
	naturalTotal := 0
	for _, columnWidth := range out {
		naturalTotal += columnWidth
	}
	if naturalTotal <= available {
		return out
	}

	// Allocate the bounded viewport budget instead of decrementing potentially
	// megabyte-sized natural widths one cell at a time.
	assigned := make([]int, n)
	assignedTotal := 0
	for index := range assigned {
		assigned[index] = 1
		if index < len(minimum) && minimum[index] > 1 {
			assigned[index] = min(minimum[index], out[index])
		}
		assignedTotal += assigned[index]
	}
	if assignedTotal > available {
		// The viewport cannot preserve every header. Reuse the bounded allocator
		// with a one-cell floor, then truncate individual headers visibly.
		return fitInfoColumnWidthsWithMinimum(natural, nil, totalWidth, gap)
	}
	remaining := available - assignedTotal
	// First make short identifier/status columns comfortably scannable. Giving
	// every spare cell to one huge description would hide small useful fields.
	for remaining > 0 {
		grew := false
		for index := range out {
			target := min(out[index], max(assigned[index], 12))
			if assigned[index] >= target {
				continue
			}
			assigned[index]++
			remaining--
			grew = true
			if remaining == 0 {
				break
			}
		}
		if !grew {
			break
		}
	}
	for ; remaining > 0; remaining-- {
		best := -1
		for index := range out {
			if assigned[index] >= out[index] {
				continue
			}
			if best < 0 || out[index]-assigned[index] > out[best]-assigned[best] {
				best = index
			}
		}
		if best < 0 {
			break
		}
		assigned[best]++
	}
	return assigned
}

func infoTableGap(totalWidth, columns int) int {
	if columns <= 1 {
		return 0
	}
	gap := 2
	if totalWidth > 0 {
		maxGap := (totalWidth - columns) / (columns - 1)
		if maxGap < gap {
			gap = max(0, maxGap)
		}
	}
	return gap
}

func infoTableRow(cells []string, colWidths []int, gap int, role style.Role) render.Line {
	spans := make([]render.Span, 0, len(colWidths)*2)
	for i, colWidth := range colWidths {
		if i > 0 {
			spans = append(spans, render.Span{Text: strings.Repeat(" ", gap)})
		}
		text := ""
		if i < len(cells) {
			text = cells[i]
		}
		text = render.TruncateText(text, colWidth, "…")
		padded := render.Pad(render.Line{Spans: []render.Span{{
			Text:  text,
			Style: render.Style{Role: string(role)},
		}}}, colWidth, render.AlignLeft)
		spans = append(spans, padded.Spans...)
	}
	return render.Line{Spans: spans}
}

func constrainInfoDocument(doc render.Document, width int) render.Document {
	if width <= 0 {
		return doc
	}
	out := render.Document{Blocks: make([]render.Block, len(doc.Blocks))}
	for blockIndex, block := range doc.Blocks {
		next := block
		next.Lines = make([]render.Line, len(block.Lines))
		for lineIndex, line := range block.Lines {
			if render.LineWidth(line) > width {
				line = render.Truncate(line, width, "…")
			}
			next.Lines[lineIndex] = line
		}
		out.Blocks[blockIndex] = next
	}
	return out
}

// PrintKeyValue 打印键值对
func PrintKeyValue(key, value string) {
	writeInfoDocument(infoKeyValueDocument(key, value, 0))
}

// PrintKeyValues 打印多个键值对
func PrintKeyValues(pairs map[string]string) {
	if len(pairs) == 0 {
		return
	}
	keys := make([]string, 0, len(pairs))
	maxKeyWidth := 0
	for key := range pairs {
		keys = append(keys, key)
		label := key
		if key != "" && !strings.HasSuffix(key, ":") {
			label = key + ":"
		}
		if width := render.Width(label); width > maxKeyWidth {
			maxKeyWidth = width
		}
	}
	sort.Strings(keys)
	lines := make([]render.Line, 0, len(keys))
	for _, key := range keys {
		doc := infoKeyValueDocument(key, pairs[key], maxKeyWidth)
		if len(doc.Blocks) == 0 || len(doc.Blocks[0].Lines) == 0 {
			continue
		}
		lines = append(lines, doc.Blocks[0].Lines[0])
	}
	writeInfoDocument(render.LinesDoc(lines...))
}

// PrintUsageInfo 打印使用信息
func PrintUsageInfo(promptTokens, completionTokens, totalTokens int64, duration int64) {
	theme := GetTheme(ThemeAuto)
	width := GetTerminalWidth()
	lines := []render.Line{{}, infoSeparatorLine(width)}

	iconPrefix := theme.InfoIcon + " "
	usageRow := func(label, value string, valueRole style.Role) render.Line {
		spans := []render.Span{
			{Text: iconPrefix, Style: render.Style{Role: string(style.RoleInfo)}},
			{Text: label, Style: render.Style{Role: string(style.RoleMetaLabel)}},
			{Text: " "},
			{Text: value, Style: render.Style{Role: string(valueRole)}},
		}
		return render.Line{Spans: spans}
	}

	if totalTokens > 0 {
		lines = append(lines, usageRow("Total Tokens:", fmt.Sprintf("%d", totalTokens), style.RoleSuccess))
	}
	if promptTokens > 0 {
		lines = append(lines, usageRow("  Prompt:", fmt.Sprintf("%d", promptTokens), style.RoleTextPrimary))
	}
	if completionTokens > 0 {
		lines = append(lines, usageRow("Completion:", fmt.Sprintf("%d", completionTokens), style.RoleTextPrimary))
	}
	if duration > 0 {
		seconds := float64(duration) / 1000.0
		lines = append(lines, render.Line{Spans: []render.Span{
			{Text: iconPrefix, Style: render.Style{Role: string(style.RoleInfo)}},
			{Text: "Duration:", Style: render.Style{Role: string(style.RoleMetaLabel)}},
			{Text: " "},
			{Text: fmt.Sprintf("%.2f", seconds), Style: render.Style{Role: string(style.RoleTextPrimary)}},
			{Text: "s", Style: render.Style{Role: string(style.RoleTextMuted)}},
		}})
	}

	lines = append(lines, infoSeparatorLine(width), render.Line{})
	writeInfoDocument(constrainInfoDocument(render.LinesDoc(lines...), width))
}

// PrintConfig 打印配置信息
func PrintConfig(config map[string]interface{}) {
	PrintSection("当前配置")
	pairs := make(map[string]string, len(config))
	for key, value := range config {
		var valueStr string
		switch v := value.(type) {
		case string:
			valueStr = v
		case bool:
			valueStr = fmt.Sprintf("%v", v)
		case int, int64:
			valueStr = fmt.Sprintf("%d", v)
		case float64:
			valueStr = fmt.Sprintf("%.2f", v)
		default:
			valueStr = fmt.Sprintf("%v", v)
		}
		pairs[key] = valueStr
	}
	PrintKeyValues(pairs)
	PrintEmptyLine()
}
