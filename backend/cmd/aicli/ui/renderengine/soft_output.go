package renderengine

import "strings"

// DefaultSoftOutputTailMaxLines bounds the rewriteable committed tail kept by
// the render engine. Rows older than this window are no longer safe to rewrite
// and must remain in irreversible scrollback.
const DefaultSoftOutputTailMaxLines = 64

// SoftOutputState owns the metadata for a rewriteable committed output tail.
// It deliberately does not know about terminal geometry or history handoff;
// those remain responsibilities of the surface/presenter while the migration
// is in progress.
type SoftOutputState struct {
	lines    []string
	valid    bool
	trimmed  bool
	partial  bool
	maxLines int
}

// NewSoftOutputState creates a soft-output owner with a bounded tail.
func NewSoftOutputState(maxLines int) *SoftOutputState {
	state := &SoftOutputState{}
	state.SetMaxLines(maxLines)
	return state
}

// SetMaxLines changes the hard cap and trims the current tail if necessary.
func (s *SoftOutputState) SetMaxLines(maxLines int) {
	if s == nil {
		return
	}
	if maxLines <= 0 {
		maxLines = DefaultSoftOutputTailMaxLines
	}
	s.maxLines = maxLines
	s.trimToCap()
}

// Valid reports whether the state owns at least one rewriteable line.
func (s *SoftOutputState) Valid() bool {
	return s != nil && s.valid && len(s.lines) > 0
}

// Trimmed reports whether older rows fell out of the hard-capped tail.
func (s *SoftOutputState) Trimmed() bool {
	return s != nil && s.trimmed
}

// Partial reports whether the final owned logical line is still open.
func (s *SoftOutputState) Partial() bool {
	return s != nil && s.partial && s.Valid()
}

// LineCount returns the number of rewriteable lines.
func (s *SoftOutputState) LineCount() int {
	if !s.Valid() {
		return 0
	}
	return len(s.lines)
}

// Lines returns a defensive copy of the owned tail.
func (s *SoftOutputState) Lines() []string {
	if !s.Valid() {
		return nil
	}
	return append([]string(nil), s.lines...)
}

// Invalidate drops ownership without changing any external history.
func (s *SoftOutputState) Invalidate() {
	if s == nil {
		return
	}
	s.lines = nil
	s.valid = false
	s.trimmed = false
	s.partial = false
}

// Adopt replaces ownership metadata with an already-validated history suffix.
// It never invents rows and always applies the same hard cap as Note.
func (s *SoftOutputState) Adopt(lines []string) {
	if s == nil {
		return
	}
	if len(lines) == 0 {
		s.Invalidate()
		return
	}
	s.lines = append([]string(nil), lines...)
	s.trimmed = false
	s.partial = false
	s.valid = true
	s.trimToCap()
}

// Replace updates the owned suffix after an in-place rewrite.
func (s *SoftOutputState) Replace(lines []string) {
	if s == nil {
		return
	}
	if len(lines) == 0 {
		s.Invalidate()
		return
	}
	s.lines = append([]string(nil), lines...)
	s.valid = true
	s.trimmed = false
	s.partial = false
	s.trimToCap()
}

// Note records a newly committed soft fragment. historyPartial describes the
// logical history state before this fragment is appended, allowing an owned
// partial line to be extended without claiming a foreign partial line.
func (s *SoftOutputState) Note(text string, historyPartial bool) {
	if s == nil {
		return
	}
	lines, partial := splitSoftOutputLines(text)
	if len(lines) == 0 {
		return
	}
	continuesOwnedPartial := historyPartial && s.Valid() && s.partial
	continuesForeignPartial := historyPartial && !continuesOwnedPartial
	if continuesOwnedPartial {
		last := len(s.lines) - 1
		s.lines[last] += lines[0]
		lines = lines[1:]
	} else if continuesForeignPartial {
		// The first segment completes a line owned by another writer. Do not
		// expose that composite line as rewriteable assistant output.
		s.Invalidate()
		lines = lines[1:]
	}
	addedOwnedSegments := len(lines) > 0
	s.lines = append(s.lines, lines...)
	s.valid = len(s.lines) > 0
	s.partial = partial && s.valid && (continuesOwnedPartial || addedOwnedSegments)
	s.trimToCap()
}

func (s *SoftOutputState) trimToCap() {
	if s == nil {
		return
	}
	maxLines := s.maxLines
	if maxLines <= 0 {
		maxLines = DefaultSoftOutputTailMaxLines
		s.maxLines = maxLines
	}
	if len(s.lines) > maxLines {
		drop := len(s.lines) - maxLines
		s.lines = append([]string(nil), s.lines[drop:]...)
		s.trimmed = true
	}
	s.valid = len(s.lines) > 0
	if !s.valid {
		s.partial = false
	}
}

func splitSoftOutputLines(text string) ([]string, bool) {
	if text == "" {
		return nil, false
	}
	normalized := strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	endsWithNewline := strings.HasSuffix(normalized, "\n")
	if endsWithNewline {
		normalized = strings.TrimSuffix(normalized, "\n")
	}
	if normalized == "" {
		if endsWithNewline {
			return []string{""}, false
		}
		return nil, true
	}
	return strings.Split(normalized, "\n"), !endsWithNewline
}
