package syntax

// Resource limits for syntax highlighting (aligned with the Codex-inspired plan).
const (
	// MaxHighlightBytes is the maximum source size that will be lexed.
	MaxHighlightBytes = 512 * 1024
	// MaxHighlightLines is the maximum number of lines that will be lexed.
	MaxHighlightLines = 10000
)

// Limits holds overridable highlight budgets.
type Limits struct {
	MaxBytes int
	MaxLines int
}

// DefaultLimits returns the production defaults.
func DefaultLimits() Limits {
	return Limits{
		MaxBytes: MaxHighlightBytes,
		MaxLines: MaxHighlightLines,
	}
}

// Exceeded reports whether code exceeds the budget.
func (l Limits) Exceeded(code string) bool {
	maxBytes := l.MaxBytes
	if maxBytes <= 0 {
		maxBytes = MaxHighlightBytes
	}
	maxLines := l.MaxLines
	if maxLines <= 0 {
		maxLines = MaxHighlightLines
	}
	if len(code) > maxBytes {
		return true
	}
	lines := 1
	for i := 0; i < len(code); i++ {
		if code[i] == '\n' {
			lines++
			if lines > maxLines {
				return true
			}
		}
	}
	return false
}
