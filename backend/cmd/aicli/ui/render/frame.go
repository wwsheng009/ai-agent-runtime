package render

import (
	"strings"
	"sync"
	"time"
)

// DefaultMaxFPS is the coalesced redraw ceiling for active cells / surface.
const DefaultMaxFPS = 30

// FrameRequest is a coalesced redraw intent.
type FrameRequest struct {
	// Reason is diagnostic only (theme, resize, stream, motion).
	Reason string
	// At is when the request was enqueued.
	At time.Time
}

// FrameScheduler merges rapid redraw requests and emits at most maxFPS.
// It does not own a ticker by itself; callers call Poll/Consume after
// Request, or drive Tick from an external clock (MotionPolicy interval).
type FrameScheduler struct {
	mu       sync.Mutex
	maxFPS   int
	minGap   time.Duration
	pending  bool
	reason   string
	lastEmit time.Time
	// onEmit is optional; invoked synchronously from Consume when a frame is due.
	onEmit func(reason string)
}

// NewFrameScheduler builds a scheduler capped at maxFPS (default 30).
func NewFrameScheduler(maxFPS int) *FrameScheduler {
	if maxFPS <= 0 {
		maxFPS = DefaultMaxFPS
	}
	return &FrameScheduler{
		maxFPS: maxFPS,
		minGap: time.Second / time.Duration(maxFPS),
	}
}

// SetOnEmit registers a callback invoked when Consume emits a frame.
func (s *FrameScheduler) SetOnEmit(fn func(reason string)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.onEmit = fn
	s.mu.Unlock()
}

// Request marks that a redraw is needed. Multiple requests coalesce.
func (s *FrameScheduler) Request(reason string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = true
	if reason != "" {
		s.reason = reason
	}
}

// Pending reports whether a redraw is waiting.
func (s *FrameScheduler) Pending() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pending
}

// NextDelay reports when a pending frame may be emitted without violating the
// FPS cap. The bool is false when no redraw is pending.
func (s *FrameScheduler) NextDelay(now time.Time) (time.Duration, bool) {
	if s == nil {
		return 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.pending {
		return 0, false
	}
	if s.lastEmit.IsZero() {
		return 0, true
	}
	remaining := s.minGap - now.Sub(s.lastEmit)
	if remaining < 0 {
		remaining = 0
	}
	return remaining, true
}

// Consume emits at most one frame if pending and minGap elapsed.
// Returns true when a frame was emitted.
func (s *FrameScheduler) Consume(now time.Time) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	if !s.pending {
		s.mu.Unlock()
		return false
	}
	if !s.lastEmit.IsZero() && now.Sub(s.lastEmit) < s.minGap {
		s.mu.Unlock()
		return false
	}
	reason := s.reason
	s.pending = false
	s.reason = ""
	s.lastEmit = now
	fn := s.onEmit
	s.mu.Unlock()
	if fn != nil {
		fn(reason)
	}
	return true
}

// ForceConsume emits immediately, ignoring FPS cap (resize/theme confirm).
func (s *FrameScheduler) ForceConsume(now time.Time) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	if !s.pending {
		s.mu.Unlock()
		return false
	}
	reason := s.reason
	s.pending = false
	s.reason = ""
	s.lastEmit = now
	fn := s.onEmit
	s.mu.Unlock()
	if fn != nil {
		fn(reason)
	}
	return true
}

// BufferBackend holds an in-memory screen buffer for active cells.
// Unlike Plain/ANSI backends, it does not write to a terminal; callers
// diff previous vs current lines for viewport redraw without scrollback spam.
type BufferBackend struct {
	Width  int
	Height int
	// Lines is the current committed frame (no trailing \n).
	Lines []string
	// StyledLines retains the same frame as structured lines for semantic
	// rendering by fixed-bottom surfaces.
	StyledLines []Line
}

// Render materializes a Document into the buffer (replacing previous content).
func (b *BufferBackend) Render(doc Document) string {
	if b == nil {
		return ""
	}
	b.StyledLines = b.Layout(doc)
	b.Lines = PlainBackend{}.RenderLines(LinesDoc(b.StyledLines...))
	return strings.Join(b.Lines, "\n")
}

// Layout applies frame height and terminal-cell width constraints while
// retaining span boundaries and semantic roles.
func (b *BufferBackend) Layout(doc Document) []Line {
	if b == nil {
		return nil
	}
	lines := bufferLayoutLines(doc, b.Height)
	if b.Width > 0 {
		wrapped := make([]Line, 0, len(lines))
		for _, line := range lines {
			if LineWidth(line) <= b.Width {
				wrapped = append(wrapped, line)
				continue
			}
			wrapped = append(wrapped, Wrap(line, b.Width, WrapOptions{BreakWord: true})...)
		}
		lines = wrapped
		if b.Height > 0 && len(lines) > b.Height {
			lines = lines[len(lines)-b.Height:]
		}
	}
	return lines
}

func bufferLayoutLines(doc Document, height int) []Line {
	if height <= 0 {
		lines := make([]Line, 0, doc.LineCount())
		for _, block := range doc.Blocks {
			for _, line := range block.Lines {
				lines = append(lines, cloneRenderLine(line))
			}
		}
		if len(lines) > 0 && LineWidth(lines[len(lines)-1]) == 0 {
			lines = lines[:len(lines)-1]
		}
		return lines
	}

	// The active viewport only needs the newest rows. Walk backwards so a
	// spinner repaint of a long response does not scan or clone the full tree.
	reversed := make([]Line, 0, height)
	skipTrailingEmpty := true
outer:
	for blockIndex := len(doc.Blocks) - 1; blockIndex >= 0; blockIndex-- {
		blockLines := doc.Blocks[blockIndex].Lines
		for lineIndex := len(blockLines) - 1; lineIndex >= 0; lineIndex-- {
			line := blockLines[lineIndex]
			if skipTrailingEmpty {
				skipTrailingEmpty = false
				if LineWidth(line) == 0 {
					continue
				}
			}
			// Never let the viewport start with layout whitespace: block
			// spacers must not consume the topmost row of the active band.
			if LineWidth(line) == 0 && len(reversed) >= height-1 {
				continue
			}
			reversed = append(reversed, cloneRenderLine(line))
			if len(reversed) >= height {
				break outer
			}
		}
	}
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	return reversed
}

// Diff returns lines that changed versus previous (by index).
// Unchanged indices are omitted; used for minimal active-region redraws.
func (b *BufferBackend) Diff(previous []string) []LineDiff {
	if b == nil {
		return nil
	}
	max := len(b.Lines)
	if len(previous) > max {
		max = len(previous)
	}
	var out []LineDiff
	for i := 0; i < max; i++ {
		var cur, prev string
		if i < len(b.Lines) {
			cur = b.Lines[i]
		}
		if i < len(previous) {
			prev = previous[i]
		}
		if cur != prev {
			out = append(out, LineDiff{Index: i, Text: cur, Cleared: cur == "" && prev != ""})
		}
	}
	return out
}

// LineDiff is one changed row in a buffer.
type LineDiff struct {
	Index   int
	Text    string
	Cleared bool
}

// Snapshot copies current lines.
func (b *BufferBackend) Snapshot() []string {
	if b == nil || len(b.Lines) == 0 {
		return nil
	}
	return append([]string(nil), b.Lines...)
}

// StyledSnapshot copies the current structured frame.
func (b *BufferBackend) StyledSnapshot() []Line {
	if b == nil || len(b.StyledLines) == 0 {
		return nil
	}
	out := make([]Line, len(b.StyledLines))
	for i, line := range b.StyledLines {
		out[i] = cloneRenderLine(line)
	}
	return out
}

// LinesEqual compares structured rows, including explicit styles and links.
// It is used when a theme change alters token colors without changing text.
func LinesEqual(a, b []Line) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Style != b[i].Style || len(a[i].Spans) != len(b[i].Spans) {
			return false
		}
		for j := range a[i].Spans {
			if a[i].Spans[j] != b[i].Spans[j] {
				return false
			}
		}
	}
	return true
}

func cloneRenderLine(line Line) Line {
	clone := line
	clone.Spans = append([]Span(nil), line.Spans...)
	return clone
}
