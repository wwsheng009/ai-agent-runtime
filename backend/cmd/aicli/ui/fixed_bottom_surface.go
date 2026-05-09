package ui

import (
	"fmt"
	"strings"
	"sync"
)

// FixedBottomSurface reserves the last terminal row for lightweight status
// while normal chat output scrolls in the region above it.
type FixedBottomSurface struct {
	terminal             *Terminal
	mu                   sync.Mutex
	enabled              bool
	statusLine           string
	popupLines           []string
	composerLine         string
	popupRenderedRows    int
	popupRenderedGapRows int
	lastWidth            int
	lastHeight           int
	lastBottomRows       int
}

func NewFixedBottomSurface(term *Terminal) *FixedBottomSurface {
	if term == nil {
		term = NewTerminal()
	}
	return &FixedBottomSurface{
		terminal:   term,
		statusLine: "Ready",
	}
}

func (s *FixedBottomSurface) Enable() bool {
	if s == nil || s.terminal == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.canEnableLocked() {
		return false
	}
	s.enabled = true
	s.applyLayoutLocked()
	s.renderPopupLocked()
	s.renderStatusLocked()
	s.moveToOutputLocked()
	return true
}

func (s *FixedBottomSurface) Disable() {
	if s == nil || s.terminal == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return
	}
	s.terminal.SaveCursor()
	s.terminal.ResetScrollRegion()
	s.clearPopupAreaLocked(s.popupRenderedRows, s.popupRenderedGapRows)
	s.terminal.MoveTo(s.statusRowLocked(), 1)
	s.terminal.ClearLine()
	s.terminal.RestoreCursor()
	s.popupRenderedRows = 0
	s.popupRenderedGapRows = 0
	s.popupLines = nil
	s.composerLine = ""
	s.enabled = false
}

func (s *FixedBottomSurface) Enabled() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabled
}

func (s *FixedBottomSurface) BeginOutput() {
	if s == nil || s.terminal == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return
	}
	s.applyLayoutLocked()
	s.moveToOutputLocked()
}

func (s *FixedBottomSurface) ShowPopup(lines []string) {
	if s == nil || s.terminal == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.popupLines = cloneAndSanitizePopupLines(lines)
	s.composerLine = ""
	if !s.enabled {
		return
	}
	s.applyLayoutLocked()
	s.renderPopupLocked()
	s.renderStatusLocked()
	s.moveToOutputLocked()
}

func (s *FixedBottomSurface) ShowPopupPreserveCursor(lines []string) {
	if s == nil || s.terminal == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.popupLines = cloneAndSanitizePopupLines(lines)
	s.composerLine = ""
	if !s.enabled {
		return
	}
	s.terminal.SaveCursor()
	defer s.terminal.RestoreCursor()
	s.applyLayoutLocked()
	s.renderPopupLocked()
	s.renderStatusLocked()
}

func (s *FixedBottomSurface) ShowPopupInput(lines []string, prompt string) {
	if s == nil || s.terminal == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	prompt = strings.TrimRight(SanitizeTerminalText(prompt), "\r\n")
	s.popupLines = cloneAndSanitizePopupLines(lines)
	s.composerLine = prompt
	if !s.enabled {
		return
	}
	s.applyLayoutLocked()
	s.renderPopupLocked()
	s.renderStatusLocked()
	s.moveToPopupInputLocked()
}

func (s *FixedBottomSurface) ShowPendingPastePreview(lines int, text string) {
	if s == nil || s.terminal == nil {
		return
	}
	text = NormalizePastedText(text)
	lines = maxInt(0, lines)
	preview := buildPendingPastePreviewLines(lines, text)
	s.ShowPopup(preview)
}

func (s *FixedBottomSurface) ClearPendingPastePreview() {
	if s == nil || s.terminal == nil {
		return
	}
	s.ClearPopup()
}

func (s *FixedBottomSurface) ClearPopup() {
	if s == nil || s.terminal == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.popupLines) == 0 && s.popupRenderedRows == 0 && strings.TrimSpace(s.composerLine) == "" {
		return
	}
	s.popupLines = nil
	s.composerLine = ""
	if !s.enabled {
		s.popupRenderedRows = 0
		s.popupRenderedGapRows = 0
		return
	}
	s.applyLayoutLocked()
	s.clearPopupAreaLocked(s.popupRenderedRows, s.popupRenderedGapRows)
	s.popupRenderedRows = 0
	s.popupRenderedGapRows = 0
	s.renderStatusLocked()
	s.moveToOutputLocked()
}

func (s *FixedBottomSurface) ClearPopupPreserveCursor() {
	if s == nil || s.terminal == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.popupLines) == 0 && s.popupRenderedRows == 0 && strings.TrimSpace(s.composerLine) == "" {
		return
	}
	s.popupLines = nil
	s.composerLine = ""
	if !s.enabled {
		s.popupRenderedRows = 0
		s.popupRenderedGapRows = 0
		return
	}
	s.terminal.SaveCursor()
	defer s.terminal.RestoreCursor()
	s.applyLayoutLocked()
	s.clearPopupAreaLocked(s.popupRenderedRows, s.popupRenderedGapRows)
	s.popupRenderedRows = 0
	s.popupRenderedGapRows = 0
	s.renderStatusLocked()
}

func (s *FixedBottomSurface) SetStatusLine(line string) {
	if s == nil || s.terminal == nil {
		return
	}
	line = strings.TrimSpace(SanitizeTerminalText(line))
	if line == "" {
		line = "Ready"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statusLine = line
	if !s.enabled {
		return
	}
	s.applyLayoutLocked()
	s.renderPopupLocked()
	s.renderStatusLocked()
	s.moveToOutputLocked()
}

// SetComposerPreview 在底部固定区额外保留一行 composer 预览。
// 这是一条过渡能力，用来承载 transient prompt / future composer。
func (s *FixedBottomSurface) SetComposerPreview(line string) {
	if s == nil || s.terminal == nil {
		return
	}
	line = strings.TrimRight(SanitizeTerminalText(line), "\r\n")
	s.mu.Lock()
	defer s.mu.Unlock()
	s.composerLine = line
	if !s.enabled {
		return
	}
	s.applyLayoutLocked()
	s.renderPopupLocked()
	s.renderStatusLocked()
	s.moveToPopupInputLocked()
}

// ClearComposerPreview 清理底部 composer 预览。
func (s *FixedBottomSurface) ClearComposerPreview() {
	if s == nil || s.terminal == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.composerLine == "" && s.popupRenderedRows == 0 {
		return
	}
	s.composerLine = ""
	if !s.enabled {
		s.popupRenderedRows = 0
		s.popupRenderedGapRows = 0
		return
	}
	s.applyLayoutLocked()
	s.clearPopupAreaLocked(s.popupRenderedRows, s.popupRenderedGapRows)
	s.popupRenderedRows = 0
	s.popupRenderedGapRows = 0
	s.renderPopupLocked()
	s.renderStatusLocked()
	s.moveToOutputLocked()
}

func (s *FixedBottomSurface) canEnableLocked() bool {
	caps := s.terminal.Capabilities()
	if !caps.Interactive || !caps.ANSI || !caps.ScrollRegion {
		return false
	}
	// Zellij has known DECSTBM incompatibilities; keep the safe legacy path
	// until the full viewport fallback is implemented.
	if strings.TrimSpace(caps.MultiplexerName) != "" && strings.Contains(strings.ToLower(caps.MultiplexerName), "zellij") {
		return false
	}
	_, height := s.terminal.RefreshSize()
	return height > s.bottomRowsLocked()
}

func (s *FixedBottomSurface) applyLayoutLocked() {
	width, height := s.terminal.RefreshSize()
	if width <= 0 {
		width = 80
	}
	bottomRows := s.bottomRowsLocked()
	if height <= bottomRows {
		return
	}
	if width == s.lastWidth && height == s.lastHeight && bottomRows == s.lastBottomRows {
		return
	}
	s.lastWidth = width
	s.lastHeight = height
	s.lastBottomRows = bottomRows
	s.terminal.SetScrollRegion(1, s.outputBottomRowLocked())
}

func (s *FixedBottomSurface) renderStatusLocked() {
	if !s.enabled {
		return
	}
	state := s.bottomPaneStateLocked()
	s.terminal.MoveTo(s.statusRowLocked(), 1)
	s.terminal.ClearLine()
	line := truncateFixedStatusLine(state.StatusLine, s.terminal.Width())
	if line != "" {
		fmt.Print(GetTheme(ThemeAuto).Dimmed(line))
	}
	s.terminal.ClearLine()
}

func (s *FixedBottomSurface) renderPopupLocked() {
	if !s.enabled {
		return
	}
	state := s.bottomPaneStateLocked()
	visibleLines := state.VisiblePopupLines(s.terminal.Height())
	composerRows := state.composerVisibleRowCount()
	gapRows := state.popupInputGapRowCount()
	rows := len(visibleLines) + composerRows
	if rows == 0 {
		if s.popupRenderedRows > 0 {
			s.clearPopupAreaLocked(s.popupRenderedRows, s.popupRenderedGapRows)
			s.popupRenderedRows = 0
			s.popupRenderedGapRows = 0
		}
		return
	}
	if s.popupRenderedRows > 0 && (s.popupRenderedRows != rows || s.popupRenderedGapRows != gapRows) {
		s.clearPopupAreaLocked(s.popupRenderedRows, s.popupRenderedGapRows)
	}
	startRow := s.popupStartRowLocked(rows, gapRows)
	if startRow < 1 {
		startRow = 1
	}
	for i, line := range visibleLines {
		row := startRow + i
		if row >= s.statusRowLocked() {
			break
		}
		s.terminal.MoveTo(row, 1)
		s.terminal.ClearLine()
		line = truncateFixedPopupLine(line, s.terminal.Width())
		if line != "" {
			fmt.Print(line)
		}
	}
	if composer := state.composerLineText(); composer != "" {
		row := startRow + len(visibleLines)
		if row < s.statusRowLocked() {
			s.terminal.MoveTo(row, 1)
			s.terminal.ClearLine()
			composer = truncateFixedPopupLine(composer, s.terminal.Width())
			if composer != "" {
				fmt.Print(composer)
			}
		}
	}
	s.popupRenderedRows = rows
	s.popupRenderedGapRows = gapRows
}

func (s *FixedBottomSurface) moveToOutputLocked() {
	s.terminal.MoveTo(s.outputBottomRowLocked(), 1)
}

func (s *FixedBottomSurface) moveToPopupInputLocked() {
	state := s.bottomPaneStateLocked()
	visibleLines := state.VisiblePopupLines(s.terminal.Height())
	composerRows := state.composerVisibleRowCount()
	if len(visibleLines) == 0 && composerRows == 0 {
		s.moveToOutputLocked()
		return
	}
	row := s.popupStartRowLocked(len(visibleLines)+composerRows, state.popupInputGapRowCount()) + len(visibleLines) + composerRows - 1
	if row < 1 {
		row = 1
	}
	if row >= s.statusRowLocked() {
		row = s.statusRowLocked() - 1
	}
	if row < 1 {
		row = 1
	}
	line := ""
	if composer := state.composerLineText(); composer != "" {
		line = truncateFixedPopupLine(composer, s.terminal.Width())
	} else if len(visibleLines) > 0 {
		line = truncateFixedPopupLine(visibleLines[len(visibleLines)-1], s.terminal.Width())
	}
	col := DisplayWidth(line) + 1
	if col < 1 {
		col = 1
	}
	width := s.terminal.Width()
	if width > 0 && col > width {
		col = width
	}
	s.terminal.MoveTo(row, col)
}

func (s *FixedBottomSurface) outputBottomRowLocked() int {
	height := s.terminal.Height()
	bottom := height - s.bottomRowsLocked()
	if bottom < 1 {
		return 1
	}
	return bottom
}

func (s *FixedBottomSurface) statusRowLocked() int {
	row := s.terminal.Height()
	if row < 1 {
		return 1
	}
	return row
}

func (s *FixedBottomSurface) popupStartRowLocked(rows int, gapRows int) int {
	row := s.statusRowLocked() - gapRows - rows
	if row < 1 {
		return 1
	}
	return row
}

func (s *FixedBottomSurface) bottomRowsLocked() int {
	state := s.bottomPaneStateLocked()
	rows := 1 + state.popupVisibleRowCount(s.terminal.Height()) + state.composerVisibleRowCount() + state.popupInputGapRowCount()
	if rows < 1 {
		rows = 1
	}
	return rows
}

func (s *FixedBottomSurface) popupVisibleRowCountLocked() int {
	if s == nil || s.terminal == nil {
		return 0
	}
	state := s.bottomPaneStateLocked()
	return state.popupVisibleRowCount(s.terminal.Height())
}

func (s *FixedBottomSurface) maxPopupRowsLocked() int {
	state := s.bottomPaneStateLocked()
	return maxBottomPanePopupRows(s.terminal.Height(), state.composerVisibleRowCount(), state.popupInputGapRowCount())
}

func (s *FixedBottomSurface) popupVisibleLinesLocked() []string {
	state := s.bottomPaneStateLocked()
	return state.VisiblePopupLines(s.terminal.Height())
}

func (s *FixedBottomSurface) clearPopupAreaLocked(rows int, gapRows int) {
	if rows < 1 {
		return
	}
	endRow := s.statusRowLocked() - gapRows
	if endRow < 1 {
		return
	}
	startRow := endRow - rows
	if startRow < 1 {
		startRow = 1
	}
	for row := startRow; row < endRow; row++ {
		s.terminal.MoveTo(row, 1)
		s.terminal.ClearLine()
	}
}

type BottomPaneState struct {
	StatusLine   string
	PopupLines   []string
	ComposerLine string
}

func (s BottomPaneState) composerLineText() string {
	return strings.TrimSpace(s.ComposerLine)
}

func (s BottomPaneState) composerVisibleRowCount() int {
	if strings.TrimSpace(s.ComposerLine) == "" {
		return 0
	}
	return 1
}

func (s BottomPaneState) popupInputGapRowCount() int {
	if len(s.PopupLines) == 0 || s.composerVisibleRowCount() > 0 {
		return 0
	}
	return 1
}

func (s BottomPaneState) popupVisibleRowCount(height int) int {
	maxRows := maxBottomPanePopupRows(height, s.composerVisibleRowCount(), s.popupInputGapRowCount())
	if maxRows <= 0 || len(s.PopupLines) == 0 {
		return 0
	}
	if len(s.PopupLines) <= maxRows {
		return len(s.PopupLines)
	}
	return maxRows
}

func (s BottomPaneState) VisiblePopupLines(height int) []string {
	rows := s.popupVisibleRowCount(height)
	if rows <= 0 || len(s.PopupLines) == 0 {
		return nil
	}
	if len(s.PopupLines) <= rows {
		return append([]string(nil), s.PopupLines...)
	}
	if rows == 1 {
		return []string{s.PopupLines[len(s.PopupLines)-1]}
	}
	if rows == 2 {
		return []string{s.PopupLines[0], s.PopupLines[len(s.PopupLines)-1]}
	}
	out := make([]string, 0, rows)
	out = append(out, s.PopupLines[0])
	out = append(out, "...")
	tailCount := rows - 2
	tailStart := len(s.PopupLines) - tailCount
	if tailStart < 1 {
		tailStart = 1
	}
	out = append(out, s.PopupLines[tailStart:]...)
	return out
}

func maxBottomPanePopupRows(height int, composerRows int, gapRows int) int {
	if height <= 2 {
		return 0
	}
	rows := height - 2 - composerRows - gapRows
	if rows < 0 {
		return 0
	}
	return rows
}

func (s *FixedBottomSurface) bottomPaneStateLocked() BottomPaneState {
	state := BottomPaneState{
		StatusLine: s.statusLine,
		PopupLines: append([]string(nil), s.popupLines...),
	}
	if strings.TrimSpace(s.composerLine) != "" {
		state.ComposerLine = s.composerLine
	}
	return state
}

func truncateFixedStatusLine(line string, width int) string {
	if width <= 0 {
		width = 80
	}
	if DisplayWidth(line) <= width {
		return line
	}
	if width <= 3 {
		return ""
	}
	var builder strings.Builder
	current := 0
	limit := width - 3
	for _, r := range line {
		w := DisplayWidth(string(r))
		if w <= 0 {
			continue
		}
		if current+w > limit {
			break
		}
		builder.WriteRune(r)
		current += w
	}
	builder.WriteString("...")
	return builder.String()
}

func truncateFixedPopupLine(line string, width int) string {
	if width <= 0 {
		width = 80
	}
	if DisplayWidth(line) <= width {
		return line
	}
	if width <= 3 {
		return ""
	}
	var builder strings.Builder
	current := 0
	limit := width - 3
	for _, r := range line {
		w := DisplayWidth(string(r))
		if w <= 0 {
			continue
		}
		if current+w > limit {
			break
		}
		builder.WriteRune(r)
		current += w
	}
	builder.WriteString("...")
	return builder.String()
}

func cloneAndSanitizePopupLines(lines []string) []string {
	if len(lines) == 0 {
		return nil
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimRight(SanitizeTerminalText(line), "\r\n")
		if strings.TrimSpace(line) == "" {
			out = append(out, "")
			continue
		}
		out = append(out, line)
	}
	return out
}

func buildPendingPastePreviewLines(lines int, text string) []string {
	title := "粘贴草稿预览"
	if lines <= 0 {
		lines = 1
	}
	out := []string{
		title,
		fmt.Sprintf("  行数: %d", lines),
		"  提示: 回车确认，Esc 取消",
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		out = append(out, "  (空内容)")
		return out
	}
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		out = append(out, "  (空内容)")
		return out
	}
	previewLines := strings.Split(text, "\n")
	maxPreviewLines := 8
	if len(previewLines) > maxPreviewLines {
		previewLines = append(append([]string(nil), previewLines[:maxPreviewLines]...), "  ...")
	}
	out = append(out, "  内容:")
	for _, line := range previewLines {
		if strings.TrimSpace(line) == "" {
			out = append(out, "  ")
			continue
		}
		out = append(out, "  "+line)
	}
	return out
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
