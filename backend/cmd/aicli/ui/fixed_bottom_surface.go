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
	popupOwner           string
	popupStack           []fixedBottomPopupState
	composerLine         string
	popupRenderedRows    int
	popupRenderedGapRows int
	lastWidth            int
	lastHeight           int
	lastBottomRows       int
}

type fixedBottomPopupState struct {
	lines        []string
	owner        string
	composerLine string
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
	WithTerminalWriteLock(func() {
		s.applyLayoutLocked()
		s.renderPopupLocked()
		s.renderStatusLocked()
		s.moveToOutputLocked()
	})
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
	WithTerminalWriteLock(func() {
		s.terminal.SaveCursor()
		s.terminal.ResetScrollRegion()
		s.clearPopupAreaLocked(s.popupRenderedRows, s.popupRenderedGapRows)
		s.terminal.MoveTo(s.statusRowLocked(), 1)
		s.terminal.ClearLine()
		s.terminal.RestoreCursor()
	})
	s.popupRenderedRows = 0
	s.popupRenderedGapRows = 0
	s.popupLines = nil
	s.popupOwner = ""
	s.popupStack = nil
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
	WithTerminalWriteLock(func() {
		s.applyLayoutLocked()
		s.moveToOutputLocked()
	})
}

// ClearPromptRows clears the currently visible interactive prompt rows without
// relying on cursor-relative movement inside the active scroll region.
func (s *FixedBottomSurface) ClearPromptRows(rows int) bool {
	if s == nil || s.terminal == nil {
		return false
	}
	if rows < 1 {
		rows = 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return false
	}
	WithTerminalWriteLock(func() {
		s.applyLayoutLocked()
		s.clearPromptRowsLocked(rows)
		s.moveToOutputLocked()
	})
	return true
}

func (s *FixedBottomSurface) ShowPopup(lines []string) {
	if s == nil || s.terminal == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setActivePopupStateLocked(cloneAndSanitizePopupLines(lines), "", "")
	if !s.enabled {
		return
	}
	WithTerminalWriteLock(func() {
		s.applyLayoutLocked()
		s.renderPopupLocked()
		s.renderStatusLocked()
		s.moveToOutputLocked()
	})
}

func (s *FixedBottomSurface) ShowPopupPreserveCursor(lines []string) {
	s.ShowPopupPreserveCursorForOwner(lines, "")
}

func (s *FixedBottomSurface) ShowPopupPreserveCursorForOwner(lines []string, owner string) {
	if s == nil || s.terminal == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.setActivePopupStateLocked(cloneAndSanitizePopupLines(lines), strings.TrimSpace(owner), "") {
		return
	}
	if !s.enabled {
		return
	}
	WithTerminalWriteLock(func() {
		s.terminal.SaveCursor()
		defer s.terminal.RestoreCursor()
		s.applyLayoutLocked()
		s.renderPopupLocked()
		s.renderStatusLocked()
	})
}

func (s *FixedBottomSurface) ShowPopupInput(lines []string, prompt string) {
	if s == nil || s.terminal == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	prompt = strings.TrimRight(SanitizeTerminalText(prompt), "\r\n")
	s.setActivePopupStateLocked(cloneAndSanitizePopupLines(lines), "", prompt)
	if !s.enabled {
		return
	}
	WithTerminalWriteLock(func() {
		s.applyLayoutLocked()
		s.renderPopupLocked()
		s.renderStatusLocked()
		s.moveToPopupInputLocked()
	})
}

func (s *FixedBottomSurface) ShowPopupInputPreserveCursor(lines []string, prompt string) {
	if s == nil || s.terminal == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	prompt = strings.TrimRight(SanitizeTerminalText(prompt), "\r\n")
	if !s.setActivePopupStateLocked(cloneAndSanitizePopupLines(lines), "", prompt) {
		return
	}
	if !s.enabled {
		return
	}
	WithTerminalWriteLock(func() {
		s.terminal.SaveCursor()
		defer s.terminal.RestoreCursor()
		s.applyLayoutLocked()
		s.renderPopupLocked()
		s.renderStatusLocked()
	})
}

func (s *FixedBottomSurface) ShowPendingPastePreview(lines int, text string) {
	if s == nil || s.terminal == nil {
		return
	}
	text = NormalizePastedText(text)
	lines = maxInt(0, lines)
	preview := buildPendingPastePreviewLines(lines, text)
	s.ShowPopupPreserveCursorForOwner(preview, "pending_paste")
}

func (s *FixedBottomSurface) ClearPendingPastePreview() {
	if s == nil || s.terminal == nil {
		return
	}
	s.ClearPopupForOwnerPreserveCursor("pending_paste")
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
	s.popupOwner = ""
	s.popupStack = nil
	s.composerLine = ""
	if !s.enabled {
		s.popupRenderedRows = 0
		s.popupRenderedGapRows = 0
		return
	}
	WithTerminalWriteLock(func() {
		s.applyLayoutLocked()
		s.clearPopupAreaLocked(s.popupRenderedRows, s.popupRenderedGapRows)
		s.popupRenderedRows = 0
		s.popupRenderedGapRows = 0
		s.renderStatusLocked()
		s.moveToOutputLocked()
	})
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
	s.popupOwner = ""
	s.popupStack = nil
	s.composerLine = ""
	if !s.enabled {
		s.popupRenderedRows = 0
		s.popupRenderedGapRows = 0
		return
	}
	WithTerminalWriteLock(func() {
		s.terminal.SaveCursor()
		defer s.terminal.RestoreCursor()
		s.applyLayoutLocked()
		s.clearPopupAreaLocked(s.popupRenderedRows, s.popupRenderedGapRows)
		s.popupRenderedRows = 0
		s.popupRenderedGapRows = 0
		s.renderStatusLocked()
	})
}

func (s *FixedBottomSurface) ClearPopupForOwnerPreserveCursor(owner string) {
	if s == nil || s.terminal == nil {
		return
	}
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.popupOwner != owner {
		s.removePopupStateFromStackLocked(owner)
		return
	}
	if len(s.popupLines) == 0 && s.popupRenderedRows == 0 && strings.TrimSpace(s.composerLine) == "" {
		return
	}
	previousRows := s.popupRenderedRows
	previousGapRows := s.popupRenderedGapRows
	s.restorePopupStateFromStackLocked()
	if !s.enabled {
		return
	}
	WithTerminalWriteLock(func() {
		s.terminal.SaveCursor()
		defer s.terminal.RestoreCursor()
		s.applyLayoutLocked()
		s.clearPopupAreaLocked(previousRows, previousGapRows)
		s.popupRenderedRows = 0
		s.popupRenderedGapRows = 0
		s.renderPopupLocked()
		s.renderStatusLocked()
	})
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
	WithTerminalWriteLock(func() {
		s.terminal.SaveCursor()
		defer s.terminal.RestoreCursor()
		s.applyLayoutLocked()
		s.renderPopupLocked()
		s.renderStatusLocked()
	})
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
	WithTerminalWriteLock(func() {
		s.applyLayoutLocked()
		s.renderPopupLocked()
		s.renderStatusLocked()
		s.moveToPopupInputLocked()
	})
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
	WithTerminalWriteLock(func() {
		s.applyLayoutLocked()
		s.clearPopupAreaLocked(s.popupRenderedRows, s.popupRenderedGapRows)
		s.popupRenderedRows = 0
		s.popupRenderedGapRows = 0
		s.renderPopupLocked()
		s.renderStatusLocked()
		s.moveToOutputLocked()
	})
}

func (s *FixedBottomSurface) setActivePopupStateLocked(lines []string, owner string, composerLine string) bool {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		s.popupStack = nil
		s.popupLines = lines
		s.popupOwner = ""
		s.composerLine = composerLine
		return true
	}
	if s.popupOwner == owner {
		s.popupLines = lines
		s.composerLine = composerLine
		return true
	}
	if s.popupOwner != "" && popupOwnerPriority(owner) < popupOwnerPriority(s.popupOwner) {
		s.upsertPopupStateInStackLocked(fixedBottomPopupState{
			lines:        lines,
			owner:        owner,
			composerLine: composerLine,
		})
		return false
	}
	if s.popupOwner != "" || len(s.popupLines) > 0 || strings.TrimSpace(s.composerLine) != "" {
		s.upsertPopupStateInStackLocked(fixedBottomPopupState{
			lines:        append([]string(nil), s.popupLines...),
			owner:        s.popupOwner,
			composerLine: s.composerLine,
		})
	}
	s.removePopupStateFromStackLocked(owner)
	s.popupLines = lines
	s.popupOwner = owner
	s.composerLine = composerLine
	return true
}

func (s *FixedBottomSurface) upsertPopupStateInStackLocked(state fixedBottomPopupState) {
	state.owner = strings.TrimSpace(state.owner)
	if state.owner == "" {
		return
	}
	state.lines = append([]string(nil), state.lines...)
	for i := range s.popupStack {
		if s.popupStack[i].owner == state.owner {
			s.popupStack[i] = state
			return
		}
	}
	s.popupStack = append(s.popupStack, state)
}

func (s *FixedBottomSurface) removePopupStateFromStackLocked(owner string) {
	owner = strings.TrimSpace(owner)
	if owner == "" || len(s.popupStack) == 0 {
		return
	}
	filtered := s.popupStack[:0]
	for _, state := range s.popupStack {
		if state.owner == owner {
			continue
		}
		filtered = append(filtered, state)
	}
	s.popupStack = filtered
}

func (s *FixedBottomSurface) restorePopupStateFromStackLocked() {
	for len(s.popupStack) > 0 {
		last := s.popupStack[len(s.popupStack)-1]
		s.popupStack = s.popupStack[:len(s.popupStack)-1]
		if last.owner == "" && len(last.lines) == 0 && strings.TrimSpace(last.composerLine) == "" {
			continue
		}
		s.popupLines = append([]string(nil), last.lines...)
		s.popupOwner = last.owner
		s.composerLine = last.composerLine
		return
	}
	s.popupLines = nil
	s.popupOwner = ""
	s.composerLine = ""
}

func popupOwnerPriority(owner string) int {
	switch strings.TrimSpace(owner) {
	case "slash_completion":
		return 100
	case "pending_paste":
		return 90
	case "":
		return 0
	default:
		return 10
	}
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

func (s *FixedBottomSurface) promptBottomRowLocked() int {
	state := s.bottomPaneStateLocked()
	if state.popupInputGapRowCount() > 0 {
		row := s.statusRowLocked() - 1
		if row < 1 {
			return 1
		}
		return row
	}
	return s.outputBottomRowLocked()
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

func (s *FixedBottomSurface) clearPromptRowsLocked(rows int) {
	if rows < 1 {
		rows = 1
	}
	bottom := s.promptBottomRowLocked()
	if bottom < 1 {
		return
	}
	if s.bottomPaneStateLocked().popupInputGapRowCount() > 0 && rows > 1 {
		rows = 1
	}
	startRow := bottom - rows + 1
	if startRow < 1 {
		startRow = 1
	}
	for row := startRow; row <= bottom; row++ {
		s.terminal.MoveTo(row, 1)
		s.terminal.ClearLine()
	}
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
