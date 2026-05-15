package ui

import (
	"fmt"
	"io"
	"strings"
	"sync"
)

// FixedBottomSurface reserves the last terminal row for lightweight status
// while normal chat output scrolls in the region above it.
type FixedBottomSurface struct {
	terminal               *Terminal
	mu                     sync.Mutex
	enabled                bool
	statusLine             string
	popupLines             []string
	popupOwner             string
	popupBelowPrompt       bool
	popupStack             []fixedBottomPopupState
	composerLine           string
	promptLine             string
	promptInput            string
	promptReservedRows     int
	promptCursorRow        int
	promptCursorCol        int
	promptRenderedStartRow int
	promptRenderedRows     int
	popupRenderedRows      int
	popupRenderedGapRows   int
	popupRenderedStartRow  int
	popupReservedRows      int
	scrollCompensatedRows  int
	lastWidth              int
	lastHeight             int
	lastBottomRows         int
}

type fixedBottomPopupState struct {
	lines             []string
	owner             string
	composerLine      string
	popupBelowPrompt  bool
	popupReservedRows int
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
		s.renderPromptRowsLocked(true)
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
	s.popupBelowPrompt = false
	s.popupReservedRows = 0
	s.popupStack = nil
	s.composerLine = ""
	s.promptLine = ""
	s.promptInput = ""
	s.promptReservedRows = 0
	s.promptCursorRow = 0
	s.promptCursorCol = 0
	s.promptRenderedStartRow = 0
	s.promptRenderedRows = 0
	s.enabled = false
	s.popupRenderedStartRow = 0
	s.popupReservedRows = 0
	s.scrollCompensatedRows = 0
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
		s.resetScrollCompensationLocked()
		s.moveToOutputLocked()
	})
}

func (s *FixedBottomSurface) PromptCursorPrefix(rowOffset, col int) (string, bool) {
	if s == nil || s.terminal == nil {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return "", false
	}
	row, column, ok := s.promptCursorPositionLocked(rowOffset, col)
	if !ok {
		return "", false
	}
	var builder strings.Builder
	s.appendApplyLayoutSequenceLocked(&builder)
	builder.WriteString(terminalMoveToSequence(row, column))
	return builder.String(), true
}

// WriteOutput moves the real terminal cursor into the scrollable output region
// and writes text while holding the terminal write lock. This keeps output
// writers from racing with the line editor's prompt cursor restoration.
func (s *FixedBottomSurface) WriteOutput(writer io.Writer, text string) (int, error, bool) {
	if s == nil || s.terminal == nil || writer == nil || text == "" {
		return 0, nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return 0, nil, false
	}
	var n int
	var err error
	WithTerminalWriteLock(func() {
		s.applyLayoutLocked()
		s.moveToOutputLocked()
		n, err = io.WriteString(writer, text)
		s.resetScrollCompensationLocked()
		s.restoreStoredPromptCursorLocked()
	})
	return n, err, true
}

func (s *FixedBottomSurface) ShowPrompt(line string) bool {
	if s == nil || s.terminal == nil {
		return false
	}
	line = strings.TrimRight(SanitizeTerminalText(line), "\r\n")
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return false
	}
	s.promptLine = line
	s.promptInput = ""
	s.promptReservedRows = 1
	s.setPromptCursorToLineEndLocked(line)
	WithTerminalWriteLock(func() {
		s.applyLayoutLocked()
		s.renderPopupLocked()
		s.renderStatusLocked()
		s.renderPromptRowsLocked(true)
		s.restoreStoredPromptCursorLocked()
	})
	return true
}

func (s *FixedBottomSurface) ResetPrompt(line string, rows int) bool {
	if s == nil || s.terminal == nil {
		return false
	}
	line = strings.TrimRight(SanitizeTerminalText(line), "\r\n")
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
		s.promptLine = line
		s.promptInput = ""
		s.promptReservedRows = 1
		s.setPromptCursorToLineEndLocked(line)
		s.applyLayoutLocked()
		s.renderPopupLocked()
		s.renderStatusLocked()
		s.renderPromptRowsLocked(true)
		s.restoreStoredPromptCursorLocked()
	})
	return true
}

func (s *FixedBottomSurface) SetPromptRows(rows int) bool {
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
	if s.promptReservedRows == rows {
		return true
	}
	restorePromptCursor := s.bottomPaneStateLocked().popupExpandsBelowPrompt()
	WithTerminalWriteLock(func() {
		if restorePromptCursor {
			s.terminal.HideCursor()
			defer s.terminal.ShowCursor()
		}
		if !restorePromptCursor {
			s.terminal.SaveCursor()
			defer s.terminal.RestoreCursor()
		}
		if s.popupRenderedRows > 0 {
			s.clearPopupAreaLocked(s.popupRenderedRows, s.popupRenderedGapRows)
			s.popupRenderedRows = 0
			s.popupRenderedGapRows = 0
			s.popupRenderedStartRow = 0
		}
		s.promptReservedRows = rows
		s.applyLayoutLocked()
		s.renderPopupLocked()
		s.renderStatusLocked()
		s.renderPromptRowsLocked(true)
		if restorePromptCursor {
			s.restoreStoredPromptCursorLocked()
		}
	})
	return true
}

func normalizeFixedPromptInputState(line string, input string, rows int, cursorRow int, cursorCol int) (string, string, int, int, int) {
	line = strings.TrimRight(SanitizeTerminalText(line), "\r\n")
	input = strings.ReplaceAll(input, "\r\n", "\n")
	input = strings.ReplaceAll(input, "\r", "\n")
	input = SanitizeTerminalText(input)
	if rows < 1 {
		rows = 1
	}
	if cursorRow < 0 {
		cursorRow = 0
	}
	if cursorCol < 0 {
		cursorCol = 0
	}
	return line, input, rows, cursorRow, cursorCol
}

func (s *FixedBottomSurface) TrackPromptInputState(line string, input string, rows int, cursorRow int, cursorCol int) bool {
	if s == nil || s.terminal == nil {
		return false
	}
	line, input, rows, cursorRow, cursorCol = normalizeFixedPromptInputState(line, input, rows, cursorRow, cursorCol)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return false
	}
	s.promptLine = line
	s.promptInput = input
	s.promptReservedRows = rows
	s.promptCursorRow = cursorRow
	s.promptCursorCol = cursorCol
	return true
}

func (s *FixedBottomSurface) SetPromptInputState(line string, input string, rows int, cursorRow int, cursorCol int) bool {
	if s == nil || s.terminal == nil {
		return false
	}
	line, input, rows, cursorRow, cursorCol = normalizeFixedPromptInputState(line, input, rows, cursorRow, cursorCol)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return false
	}
	s.promptLine = line
	s.promptInput = input
	s.promptReservedRows = rows
	s.promptCursorRow = cursorRow
	s.promptCursorCol = cursorCol
	WithTerminalWriteLock(func() {
		s.terminal.HideCursor()
		defer s.terminal.ShowCursor()
		if s.popupRenderedRows > 0 && !s.bottomPaneStateLocked().popupExpandsBelowPrompt() {
			s.clearPopupAreaLocked(s.popupRenderedRows, s.popupRenderedGapRows)
			s.popupRenderedRows = 0
			s.popupRenderedGapRows = 0
			s.popupRenderedStartRow = 0
		}
		s.applyLayoutLocked()
		s.renderPopupLocked()
		s.renderStatusLocked()
		s.renderPromptRowsLocked(true)
		s.restoreStoredPromptCursorLocked()
	})
	return true
}

func (s *FixedBottomSurface) SetPromptCursor(rowOffset, col int) bool {
	if s == nil || s.terminal == nil {
		return false
	}
	if rowOffset < 0 {
		rowOffset = 0
	}
	if col < 0 {
		col = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return false
	}
	if _, _, ok := s.promptCursorPositionLocked(rowOffset, col); !ok {
		return false
	}
	s.promptCursorRow = rowOffset
	s.promptCursorCol = col
	return true
}

func (s *FixedBottomSurface) MoveToPromptCursor(rowOffset, col int) bool {
	if s == nil || s.terminal == nil {
		return false
	}
	if rowOffset < 0 {
		rowOffset = 0
	}
	if col < 0 {
		col = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return false
	}
	row, column, ok := s.promptCursorPositionLocked(rowOffset, col)
	if !ok {
		return false
	}
	s.promptCursorRow = rowOffset
	s.promptCursorCol = col
	WithTerminalWriteLock(func() {
		s.applyLayoutLocked()
		s.terminal.MoveTo(row, column)
	})
	return true
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
		s.promptLine = ""
		s.promptInput = ""
		s.promptReservedRows = 0
		s.promptCursorRow = 0
		s.promptCursorCol = 0
		s.promptRenderedStartRow = 0
		s.promptRenderedRows = 0
		s.applyLayoutLocked()
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
	s.setActivePopupStateLocked(cloneAndSanitizePopupLines(lines), "", "", false)
	if !s.enabled {
		return
	}
	WithTerminalWriteLock(func() {
		s.applyLayoutLocked()
		s.renderPopupLocked()
		s.renderStatusLocked()
		s.renderPromptRowsLocked(true)
		s.moveToOutputLocked()
	})
}

func (s *FixedBottomSurface) ShowPopupPreserveCursor(lines []string) {
	s.ShowPopupPreserveCursorForOwner(lines, "")
}

func (s *FixedBottomSurface) ShowPopupPreserveCursorForOwner(lines []string, owner string) {
	s.showPopupPreserveCursorForOwner(lines, owner, false)
}

func (s *FixedBottomSurface) ShowPopupPreserveCursorForOwnerBelowPrompt(lines []string, owner string) {
	s.showPopupPreserveCursorForOwner(lines, owner, true)
}

func (s *FixedBottomSurface) showPopupPreserveCursorForOwner(lines []string, owner string, belowPrompt bool) {
	if s == nil || s.terminal == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.setActivePopupStateLocked(cloneAndSanitizePopupLines(lines), strings.TrimSpace(owner), "", belowPrompt) {
		return
	}
	if !s.enabled {
		return
	}
	restorePromptCursor := belowPrompt || s.bottomPaneStateLocked().popupExpandsBelowPrompt()
	WithTerminalWriteLock(func() {
		if restorePromptCursor {
			s.terminal.HideCursor()
			defer s.terminal.ShowCursor()
		}
		if !restorePromptCursor {
			s.terminal.SaveCursor()
			defer s.terminal.RestoreCursor()
		}
		s.applyLayoutLocked()
		s.renderPopupLocked()
		s.renderStatusLocked()
		s.renderPromptRowsLocked(true)
		if restorePromptCursor {
			s.restoreStoredPromptCursorLocked()
		}
	})
}

func (s *FixedBottomSurface) ShowPopupInput(lines []string, prompt string) {
	if s == nil || s.terminal == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	prompt = strings.TrimRight(SanitizeTerminalText(prompt), "\r\n")
	s.setActivePopupStateLocked(cloneAndSanitizePopupLines(lines), "", prompt, false)
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
	if !s.setActivePopupStateLocked(cloneAndSanitizePopupLines(lines), "", prompt, false) {
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
	s.popupBelowPrompt = false
	s.popupReservedRows = 0
	s.popupStack = nil
	s.composerLine = ""
	if !s.enabled {
		s.popupRenderedRows = 0
		s.popupRenderedGapRows = 0
		s.popupRenderedStartRow = 0
		s.popupReservedRows = 0
		return
	}
	WithTerminalWriteLock(func() {
		s.applyLayoutLocked()
		s.clearPopupAreaLocked(s.popupRenderedRows, s.popupRenderedGapRows)
		s.popupRenderedRows = 0
		s.popupRenderedGapRows = 0
		s.popupRenderedStartRow = 0
		s.renderStatusLocked()
		s.renderPromptRowsLocked(true)
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
	restorePromptCursor := s.bottomPaneStateLocked().popupExpandsBelowPrompt()
	s.popupLines = nil
	s.popupOwner = ""
	s.popupBelowPrompt = false
	s.popupReservedRows = 0
	s.popupStack = nil
	s.composerLine = ""
	if !s.enabled {
		s.popupRenderedRows = 0
		s.popupRenderedGapRows = 0
		s.popupRenderedStartRow = 0
		s.popupReservedRows = 0
		return
	}
	WithTerminalWriteLock(func() {
		if restorePromptCursor {
			s.terminal.HideCursor()
			defer s.terminal.ShowCursor()
		}
		if !restorePromptCursor {
			s.terminal.SaveCursor()
			defer s.terminal.RestoreCursor()
		}
		s.applyLayoutLocked()
		s.clearPopupAreaLocked(s.popupRenderedRows, s.popupRenderedGapRows)
		s.popupRenderedRows = 0
		s.popupRenderedGapRows = 0
		s.popupRenderedStartRow = 0
		s.renderStatusLocked()
		s.renderPromptRowsLocked(true)
		if restorePromptCursor {
			s.restoreStoredPromptCursorLocked()
		}
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
	wasBelowPrompt := s.bottomPaneStateLocked().popupExpandsBelowPrompt()
	previousRows := s.popupRenderedRows
	previousGapRows := s.popupRenderedGapRows
	s.restorePopupStateFromStackLocked()
	if !s.enabled {
		return
	}
	restorePromptCursor := wasBelowPrompt || s.bottomPaneStateLocked().popupExpandsBelowPrompt()
	WithTerminalWriteLock(func() {
		if restorePromptCursor {
			s.terminal.HideCursor()
			defer s.terminal.ShowCursor()
		}
		if !restorePromptCursor {
			s.terminal.SaveCursor()
			defer s.terminal.RestoreCursor()
		}
		s.applyLayoutLocked()
		s.clearPopupAreaLocked(previousRows, previousGapRows)
		s.popupRenderedRows = 0
		s.popupRenderedGapRows = 0
		s.popupRenderedStartRow = 0
		s.renderPopupLocked()
		s.renderStatusLocked()
		s.renderPromptRowsLocked(true)
		if restorePromptCursor {
			s.restoreStoredPromptCursorLocked()
		}
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
	restorePromptCursor := s.bottomPaneStateLocked().popupExpandsBelowPrompt()
	WithTerminalWriteLock(func() {
		if restorePromptCursor {
			s.terminal.HideCursor()
			defer s.terminal.ShowCursor()
		}
		if !restorePromptCursor {
			s.terminal.SaveCursor()
			defer s.terminal.RestoreCursor()
		}
		s.applyLayoutLocked()
		s.renderPopupLocked()
		s.renderStatusLocked()
		s.renderPromptRowsLocked(true)
		if restorePromptCursor {
			s.restoreStoredPromptCursorLocked()
		}
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
	s.popupBelowPrompt = false
	s.popupReservedRows = 0
	s.promptLine = ""
	s.promptInput = ""
	s.promptReservedRows = 0
	s.promptRenderedStartRow = 0
	s.promptRenderedRows = 0
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
	s.promptReservedRows = 0
	if !s.enabled {
		s.popupRenderedRows = 0
		s.popupRenderedGapRows = 0
		s.popupRenderedStartRow = 0
		s.popupReservedRows = 0
		return
	}
	WithTerminalWriteLock(func() {
		s.applyLayoutLocked()
		s.clearPopupAreaLocked(s.popupRenderedRows, s.popupRenderedGapRows)
		s.popupRenderedRows = 0
		s.popupRenderedGapRows = 0
		s.popupRenderedStartRow = 0
		s.renderPopupLocked()
		s.renderStatusLocked()
		s.moveToOutputLocked()
	})
}

func (s *FixedBottomSurface) setActivePopupStateLocked(lines []string, owner string, composerLine string, belowPrompt bool) bool {
	owner = strings.TrimSpace(owner)
	reservedRows := s.popupReservedRowsForUpdateLocked(lines, owner, belowPrompt)
	if owner == "" {
		s.popupStack = nil
		s.popupLines = lines
		s.popupOwner = ""
		s.popupBelowPrompt = belowPrompt
		s.popupReservedRows = reservedRows
		s.composerLine = composerLine
		return true
	}
	if s.popupOwner == owner {
		s.popupLines = lines
		s.popupBelowPrompt = belowPrompt
		s.popupReservedRows = reservedRows
		s.composerLine = composerLine
		return true
	}
	if s.popupOwner != "" && popupOwnerPriority(owner) < popupOwnerPriority(s.popupOwner) {
		s.upsertPopupStateInStackLocked(fixedBottomPopupState{
			lines:             lines,
			owner:             owner,
			composerLine:      composerLine,
			popupBelowPrompt:  belowPrompt,
			popupReservedRows: reservedRows,
		})
		return false
	}
	if s.popupOwner != "" || len(s.popupLines) > 0 || strings.TrimSpace(s.composerLine) != "" {
		s.upsertPopupStateInStackLocked(fixedBottomPopupState{
			lines:             append([]string(nil), s.popupLines...),
			owner:             s.popupOwner,
			composerLine:      s.composerLine,
			popupBelowPrompt:  s.popupBelowPrompt,
			popupReservedRows: s.popupReservedRows,
		})
	}
	s.removePopupStateFromStackLocked(owner)
	s.popupLines = lines
	s.popupOwner = owner
	s.popupBelowPrompt = belowPrompt
	s.popupReservedRows = reservedRows
	s.composerLine = composerLine
	return true
}

func (s *FixedBottomSurface) popupReservedRowsForUpdateLocked(lines []string, owner string, belowPrompt bool) int {
	if !belowPrompt || len(lines) == 0 {
		return 0
	}
	rows := len(lines)
	if s.popupBelowPrompt && s.popupOwner == owner && s.popupReservedRows > rows {
		rows = s.popupReservedRows
	}
	if maxRows := maxBottomPanePopupRows(s.terminal.Height(), s.bottomPaneStateLocked().promptReservedRowCount(), 0); maxRows > 0 && rows > maxRows {
		rows = maxRows
	}
	return rows
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
		s.popupBelowPrompt = last.popupBelowPrompt
		s.popupReservedRows = last.popupReservedRows
		s.composerLine = last.composerLine
		return
	}
	s.popupLines = nil
	s.popupOwner = ""
	s.popupBelowPrompt = false
	s.popupReservedRows = 0
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
	var builder strings.Builder
	s.appendApplyLayoutSequenceLocked(&builder)
	if builder.Len() > 0 {
		fmt.Print(builder.String())
	}
}

func (s *FixedBottomSurface) appendApplyLayoutSequenceLocked(builder *strings.Builder) {
	if builder == nil {
		return
	}
	width, height := s.terminal.RefreshSize()
	if width <= 0 {
		width = 80
	}
	if height <= 1 {
		return
	}
	bottomRows := s.effectiveBottomRowsLocked(height)
	lastWidth := s.lastWidth
	lastHeight := s.lastHeight
	lastBottomRows := s.lastBottomRows
	if width == lastWidth && height == lastHeight && bottomRows == lastBottomRows {
		return
	}
	sameSize := width == lastWidth && height == lastHeight
	compensatedRows := s.scrollCompensatedRows
	if sameSize && compensatedRows > 0 && bottomRows > compensatedRows {
		appendOutputScrollUpForBottomReserveGrowthSequence(builder, height, compensatedRows, bottomRows)
		s.scrollCompensatedRows = bottomRows
	} else if !sameSize || compensatedRows <= 0 {
		s.scrollCompensatedRows = bottomRows
	}
	s.lastWidth = width
	s.lastHeight = height
	s.lastBottomRows = bottomRows
	builder.WriteString(terminalScrollRegionSequence(1, outputBottomRowForHeight(height, bottomRows)))
}

func (s *FixedBottomSurface) resetScrollCompensationLocked() {
	if s == nil || s.terminal == nil {
		return
	}
	height := s.terminal.Height()
	if height <= 0 {
		_, height = s.terminal.RefreshSize()
	}
	s.scrollCompensatedRows = s.effectiveBottomRowsLocked(height)
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
	gapRows := state.popupBottomGapRowCount()
	rows := len(visibleLines) + composerRows
	if rows == 0 {
		if s.popupRenderedRows > 0 {
			s.clearPopupAreaLocked(s.popupRenderedRows, s.popupRenderedGapRows)
			s.popupRenderedRows = 0
			s.popupRenderedGapRows = 0
			s.popupRenderedStartRow = 0
		}
		return
	}
	if s.popupRenderedRows > 0 && (s.popupRenderedRows != rows || s.popupRenderedGapRows != gapRows) {
		s.clearPopupAreaLocked(s.popupRenderedRows, s.popupRenderedGapRows)
		s.popupRenderedRows = 0
		s.popupRenderedGapRows = 0
		s.popupRenderedStartRow = 0
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
	s.popupRenderedStartRow = startRow
}

func (s *FixedBottomSurface) moveToOutputLocked() {
	s.terminal.MoveTo(s.outputBottomRowLocked(), 1)
}

func (s *FixedBottomSurface) moveToPromptLocked() {
	s.terminal.MoveTo(s.promptBottomRowLocked(), 1)
}

func (s *FixedBottomSurface) restoreStoredPromptCursorLocked() {
	if s.bottomPaneStateLocked().promptVisibleRowCount() < 1 {
		return
	}
	row, column, ok := s.promptCursorPositionLocked(s.promptCursorRow, s.promptCursorCol)
	if !ok {
		return
	}
	s.terminal.MoveTo(row, column)
}

func (s *FixedBottomSurface) setPromptCursorToLineEndLocked(line string) {
	width := s.terminal.Width()
	if width <= 0 {
		width = 80
	}
	row, col := fixedPromptLineEndPosition(line, width)
	s.promptCursorRow = row
	s.promptCursorCol = col
}

func (s *FixedBottomSurface) promptCursorPositionLocked(rowOffset, col int) (int, int, bool) {
	if rowOffset < 0 {
		rowOffset = 0
	}
	if col < 0 {
		col = 0
	}
	state := s.bottomPaneStateLocked()
	rows := state.promptVisibleRowCount()
	if rows < 1 {
		return 0, 0, false
	}
	if maxRows := s.promptMaxVisibleRowsLocked(); maxRows > 0 && rows > maxRows {
		rows = maxRows
	}
	if rowOffset >= rows {
		rowOffset = rows - 1
	}
	bottom := s.promptBottomRowLocked()
	start := bottom - rows + 1
	if start < 1 {
		start = 1
	}
	row := start + rowOffset
	if row > bottom {
		row = bottom
	}
	width := s.terminal.Width()
	if width > 0 && col >= width {
		col = width - 1
	}
	return row, col + 1, true
}

func (s *FixedBottomSurface) promptMaxVisibleRowsLocked() int {
	bottom := s.promptBottomRowLocked()
	outputBottom := s.outputBottomRowLocked()
	rows := bottom - outputBottom
	if rows < 1 {
		return 1
	}
	return rows
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
	bottom := height - s.effectiveBottomRowsLocked(height)
	if bottom < 1 {
		return 1
	}
	return bottom
}

func (s *FixedBottomSurface) promptBottomRowLocked() int {
	state := s.bottomPaneStateLocked()
	if state.popupExpandsBelowPrompt() {
		rows := state.promptVisibleRowCount()
		if rows < 1 {
			return s.outputBottomRowLocked()
		}
		row := s.outputBottomRowLocked() + rows
		if row < 1 {
			return 1
		}
		if row >= s.statusRowLocked() {
			return s.statusRowLocked() - 1
		}
		return row
	}
	if state.composerVisibleRowCount() > 0 {
		visibleLines := state.VisiblePopupLines(s.terminal.Height())
		row := s.popupStartRowLocked(len(visibleLines)+state.composerVisibleRowCount(), state.popupInputGapRowCount()) + len(visibleLines)
		if row < 1 {
			return 1
		}
		if row >= s.statusRowLocked() {
			return s.statusRowLocked() - 1
		}
		return row
	}
	if state.popupInputGapRowCount() > 0 || state.promptReservedRowCount() > 0 {
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
	if s.bottomPaneStateLocked().popupExpandsBelowPrompt() {
		row := s.promptBottomRowLocked() + 1
		if row < 1 {
			return 1
		}
		if row >= s.statusRowLocked() {
			return s.statusRowLocked() - 1
		}
		return row
	}
	row := s.statusRowLocked() - gapRows - rows
	if row < 1 {
		return 1
	}
	return row
}

func (s *FixedBottomSurface) bottomRowsLocked() int {
	state := s.bottomPaneStateLocked()
	rows := 1 + state.popupVisibleRowCount(s.terminal.Height())
	if state.popupExpandsBelowPrompt() {
		rows += state.promptVisibleRowCount()
	} else {
		rows += state.composerVisibleRowCount() + state.popupBottomGapRowCount()
	}
	if rows < 1 {
		rows = 1
	}
	return rows
}

func (s *FixedBottomSurface) effectiveBottomRowsLocked(height int) int {
	rows := s.bottomRowsLocked()
	if height <= 1 {
		return 1
	}
	maxRows := height - 1
	if rows > maxRows {
		return maxRows
	}
	if rows < 1 {
		return 1
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
	reservedRows := state.composerVisibleRowCount() + state.popupTopReservedRowCount()
	return maxBottomPanePopupRows(s.terminal.Height(), reservedRows, state.popupBottomGapRowCount())
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
	state := s.bottomPaneStateLocked()
	capToVisiblePrompt := false
	if reservedRows := state.promptVisibleRowCount(); reservedRows > 0 {
		rows = reservedRows
		capToVisiblePrompt = true
	} else if state.popupInputGapRowCount() > 0 && rows > 1 {
		rows = 1
	}
	if capToVisiblePrompt {
		if maxRows := s.promptMaxVisibleRowsLocked(); maxRows > 0 && rows > maxRows {
			rows = maxRows
		}
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

func (s *FixedBottomSurface) renderPromptRowsLocked(clear bool) {
	if s == nil || s.terminal == nil || !s.enabled {
		return
	}
	state := s.bottomPaneStateLocked()
	if state.composerVisibleRowCount() > 0 {
		return
	}
	rows := state.promptVisibleRowCount()
	if rows < 1 {
		return
	}
	if maxRows := s.promptMaxVisibleRowsLocked(); maxRows > 0 && rows > maxRows {
		rows = maxRows
	}
	bottom := s.promptBottomRowLocked()
	if bottom < 1 {
		return
	}
	start := bottom - rows + 1
	if start < 1 {
		start = 1
	}
	if s.promptRenderedStartRow > 0 && (s.promptRenderedStartRow != start || s.promptRenderedRows != rows) {
		s.clearRowsLocked(s.promptRenderedStartRow, s.promptRenderedRows)
	}
	if clear {
		s.clearRowsLocked(start, rows)
	}
	s.terminal.MoveTo(start, 1)
	if s.promptLine != "" {
		fmt.Print(s.promptLine)
	}
	if s.promptInput != "" {
		fmt.Print(renderInteractiveInputForTerminal([]rune(s.promptInput)))
	}
	s.promptRenderedStartRow = start
	s.promptRenderedRows = rows
}

func (s *FixedBottomSurface) clearRowsLocked(startRow int, rows int) {
	if rows < 1 {
		return
	}
	if startRow < 1 {
		startRow = 1
	}
	statusRow := s.statusRowLocked()
	endRow := startRow + rows - 1
	if endRow >= statusRow {
		endRow = statusRow - 1
	}
	for row := startRow; row <= endRow; row++ {
		s.terminal.MoveTo(row, 1)
		s.terminal.ClearLine()
	}
}

func (s *FixedBottomSurface) clearPopupAreaLocked(rows int, gapRows int) {
	if rows < 1 {
		return
	}
	if s.popupRenderedStartRow > 0 {
		s.clearRowsLocked(s.popupRenderedStartRow, rows)
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
	StatusLine         string
	PopupLines         []string
	PopupBelowPrompt   bool
	PopupReservedRows  int
	ComposerLine       string
	PromptReservedRows int
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

func (s BottomPaneState) popupExpandsBelowPrompt() bool {
	return s.PopupBelowPrompt && len(s.PopupLines) > 0 && s.composerVisibleRowCount() == 0
}

func (s BottomPaneState) popupTopReservedRowCount() int {
	if !s.popupExpandsBelowPrompt() {
		return 0
	}
	rows := s.promptReservedRowCount()
	if rows < 0 {
		return 0
	}
	return rows
}

func (s BottomPaneState) popupInputGapRowCount() int {
	if s.popupExpandsBelowPrompt() {
		return 0
	}
	if len(s.PopupLines) == 0 || s.composerVisibleRowCount() > 0 {
		return 0
	}
	return 1
}

func (s BottomPaneState) promptReservedRowCount() int {
	if s.PromptReservedRows < 0 {
		return 0
	}
	return s.PromptReservedRows
}

func (s BottomPaneState) promptVisibleRowCount() int {
	if s.composerVisibleRowCount() > 0 {
		return s.composerVisibleRowCount()
	}
	if s.popupExpandsBelowPrompt() {
		return s.promptReservedRowCount()
	}
	rows := s.promptReservedRowCount()
	if gapRows := s.popupInputGapRowCount(); gapRows > rows {
		rows = gapRows
	}
	return rows
}

func (s BottomPaneState) extraPromptReservedRowCount() int {
	if s.composerVisibleRowCount() > 0 || s.popupExpandsBelowPrompt() {
		return 0
	}
	rows := s.promptReservedRowCount()
	gapRows := s.popupInputGapRowCount()
	if rows <= gapRows {
		return 0
	}
	return rows - gapRows
}

func (s BottomPaneState) popupBottomGapRowCount() int {
	return s.popupInputGapRowCount() + s.extraPromptReservedRowCount()
}

func (s BottomPaneState) popupVisibleRowCount(height int) int {
	rows := s.popupLineVisibleRowCount(height)
	if !s.popupExpandsBelowPrompt() || s.PopupReservedRows <= rows {
		return rows
	}
	reservedRows := s.composerVisibleRowCount() + s.popupTopReservedRowCount()
	maxRows := maxBottomPanePopupRows(height, reservedRows, s.popupBottomGapRowCount())
	if maxRows > 0 && s.PopupReservedRows > maxRows {
		return maxRows
	}
	return s.PopupReservedRows
}

func (s BottomPaneState) VisiblePopupLines(height int) []string {
	rows := s.popupLineVisibleRowCount(height)
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

func (s BottomPaneState) popupLineVisibleRowCount(height int) int {
	reservedRows := s.composerVisibleRowCount() + s.popupTopReservedRowCount()
	maxRows := maxBottomPanePopupRows(height, reservedRows, s.popupBottomGapRowCount())
	if maxRows <= 0 || len(s.PopupLines) == 0 {
		return 0
	}
	if len(s.PopupLines) <= maxRows {
		return len(s.PopupLines)
	}
	return maxRows
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
		StatusLine:         s.statusLine,
		PopupLines:         append([]string(nil), s.popupLines...),
		PopupBelowPrompt:   s.popupBelowPrompt,
		PopupReservedRows:  s.popupReservedRows,
		PromptReservedRows: s.promptReservedRows,
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

func terminalMoveToSequence(row, col int) string {
	if row < 1 {
		row = 1
	}
	if col < 1 {
		col = 1
	}
	return fmt.Sprintf("\x1b[%d;%dH", row, col)
}

func terminalScrollRegionSequence(top, bottom int) string {
	if top < 1 {
		top = 1
	}
	if bottom < top {
		bottom = top
	}
	return fmt.Sprintf("\x1b[%d;%dr", top, bottom) + terminalMoveToSequence(top, 1)
}

func appendOutputScrollUpForBottomReserveGrowthSequence(builder *strings.Builder, height int, oldBottomRows int, newBottomRows int) {
	if builder == nil || height <= 1 || newBottomRows <= oldBottomRows {
		return
	}
	oldBottomRows = effectiveBottomRowsForHeight(height, oldBottomRows)
	newBottomRows = effectiveBottomRowsForHeight(height, newBottomRows)
	delta := newBottomRows - oldBottomRows
	if delta <= 0 {
		return
	}
	oldOutputBottom := outputBottomRowForHeight(height, oldBottomRows)
	if oldOutputBottom < 1 {
		return
	}
	if delta > oldOutputBottom {
		delta = oldOutputBottom
	}
	builder.WriteString(terminalScrollRegionSequence(1, oldOutputBottom))
	builder.WriteString(terminalMoveToSequence(oldOutputBottom, 1))
	builder.WriteString(strings.Repeat("\n", delta))
}

func outputBottomRowForHeight(height int, bottomRows int) int {
	bottom := height - effectiveBottomRowsForHeight(height, bottomRows)
	if bottom < 1 {
		return 1
	}
	return bottom
}

func effectiveBottomRowsForHeight(height int, bottomRows int) int {
	if height <= 1 {
		return 1
	}
	maxRows := height - 1
	if bottomRows > maxRows {
		return maxRows
	}
	if bottomRows < 1 {
		return 1
	}
	return bottomRows
}

func fixedPromptLineEndPosition(line string, termWidth int) (int, int) {
	if termWidth <= 0 {
		termWidth = 80
	}
	row, col := 0, 0
	for _, r := range stripTerminalEscapeSequences(line) {
		switch r {
		case '\r', '\n':
			row++
			col = 0
			continue
		}
		width := DisplayWidth(string(r))
		if width <= 0 {
			continue
		}
		col += width
		if col >= termWidth {
			row += col / termWidth
			col %= termWidth
		}
	}
	return row, col
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
