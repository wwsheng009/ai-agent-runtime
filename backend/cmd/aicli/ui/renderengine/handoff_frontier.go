package renderengine

// HandoffFrontier records the exclusive logical-line boundary between retained
// transcript rows that have already entered native terminal scrollback and
// rows that remain mutable in the owned screen model. Its caller supplies the
// enclosing history length and owns synchronization with transcript changes.
type HandoffFrontier struct {
	line int
}

// NewHandoffFrontier creates an empty handoff boundary for a RenderEngine.
func NewHandoffFrontier() *HandoffFrontier {
	return &HandoffFrontier{}
}

// Value returns the number of leading retained lines already handed off.
func (f *HandoffFrontier) Value() int {
	if f == nil {
		return 0
	}
	return f.line
}

// Reset drops the boundary when its source history is replaced entirely.
func (f *HandoffFrontier) Reset() {
	if f != nil {
		f.line = 0
	}
}

// AdvanceTo moves the frontier forward only. total clamps malformed caller
// input so the boundary can never point past the retained history window.
func (f *HandoffFrontier) AdvanceTo(line, total int) bool {
	if f == nil {
		return false
	}
	line = clampFrontier(line, total)
	if line <= f.line {
		return false
	}
	f.line = line
	return true
}

// TrimPrefix rebases the frontier after the caller removes leading retained
// history rows. Only handed-off rows may be trimmed; requesting more rows is
// clamped to the current frontier.
func (f *HandoffFrontier) TrimPrefix(rows, remaining int) {
	if f == nil || rows <= 0 {
		return
	}
	if rows > f.line {
		rows = f.line
	}
	f.line -= rows
	f.line = clampFrontier(f.line, remaining)
}

// Clamp adjusts the boundary after an in-place history replacement.
func (f *HandoffFrontier) Clamp(total int) {
	if f != nil {
		f.line = clampFrontier(f.line, total)
	}
}

func clampFrontier(line, total int) int {
	if total < 0 {
		total = 0
	}
	if line < 0 {
		return 0
	}
	if line > total {
		return total
	}
	return line
}
