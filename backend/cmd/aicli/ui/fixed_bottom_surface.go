package ui

import (
	"fmt"
	"strings"
	"sync"
)

// FixedBottomSurface reserves the last terminal row for lightweight status
// while normal chat output scrolls in the region above it.
type FixedBottomSurface struct {
	terminal   *Terminal
	mu         sync.Mutex
	enabled    bool
	bottomRows int
	statusLine string
	lastWidth  int
	lastHeight int
}

func NewFixedBottomSurface(term *Terminal) *FixedBottomSurface {
	if term == nil {
		term = NewTerminal()
	}
	return &FixedBottomSurface{
		terminal:   term,
		bottomRows: 1,
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
	statusRow := s.statusRowLocked()
	s.terminal.MoveTo(statusRow, 1)
	s.terminal.ClearLine()
	s.terminal.RestoreCursor()
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
	s.renderStatusLocked()
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
	return height > s.bottomRows+1
}

func (s *FixedBottomSurface) applyLayoutLocked() {
	width, height := s.terminal.RefreshSize()
	if width <= 0 {
		width = 80
	}
	if height <= s.bottomRows+1 {
		return
	}
	if width == s.lastWidth && height == s.lastHeight {
		return
	}
	s.lastWidth = width
	s.lastHeight = height
	s.terminal.SetScrollRegion(1, s.outputBottomRowLocked())
}

func (s *FixedBottomSurface) renderStatusLocked() {
	if !s.enabled {
		return
	}
	s.terminal.SaveCursor()
	s.terminal.MoveTo(s.statusRowLocked(), 1)
	s.terminal.ClearLine()
	line := truncateFixedStatusLine(s.statusLine, s.terminal.Width())
	if line != "" {
		fmt.Print(GetTheme(ThemeAuto).Dimmed(line))
	}
	s.terminal.ClearLine()
	s.terminal.RestoreCursor()
}

func (s *FixedBottomSurface) moveToOutputLocked() {
	s.terminal.MoveTo(s.outputBottomRowLocked(), 1)
}

func (s *FixedBottomSurface) outputBottomRowLocked() int {
	height := s.terminal.Height()
	bottom := height - s.bottomRows
	if bottom < 1 {
		return 1
	}
	return bottom
}

func (s *FixedBottomSurface) statusRowLocked() int {
	row := s.terminal.Height() - s.bottomRows + 1
	if row < 1 {
		return 1
	}
	return row
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
