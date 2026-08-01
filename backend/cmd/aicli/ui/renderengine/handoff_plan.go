package renderengine

import (
	"io"
	"strconv"
	"strings"
)

// HandoffPlan describes one immutable transcript handoff into native
// scrollback. The plan is pure data so Presenter can batch it with the rest of
// a frame and callers can test its ANSI independently of a terminal object.
type HandoffPlan struct {
	height       int
	outputBottom int
	rows         []string
}

// NewHandoffPlan creates a cursor-neutral DECSTBM handoff plan. Invalid
// geometry or an empty row set produces an empty plan.
func NewHandoffPlan(height, outputBottom int, rows []string) HandoffPlan {
	if height < 1 || outputBottom < 1 || len(rows) == 0 {
		return HandoffPlan{}
	}
	if outputBottom > height {
		outputBottom = height
	}
	return HandoffPlan{
		height:       height,
		outputBottom: outputBottom,
		rows:         append([]string(nil), rows...),
	}
}

// Empty reports whether the plan has no bytes to emit.
func (p HandoffPlan) Empty() bool {
	return p.height < 1 || p.outputBottom < 1 || len(p.rows) == 0
}

// Rows returns a defensive copy of the handoff rows.
func (p HandoffPlan) Rows() []string {
	return append([]string(nil), p.rows...)
}

// ANSI renders the cursor-neutral DECSTBM sequence used for native scrollback.
func (p HandoffPlan) ANSI() string {
	if p.Empty() {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("\x1b[s")
	builder.WriteString("\x1b[1;")
	builder.WriteString(strconv.Itoa(p.outputBottom))
	builder.WriteString("r")
	builder.WriteString("\x1b[")
	builder.WriteString(strconv.Itoa(p.outputBottom))
	builder.WriteString(";1H")
	for _, row := range p.rows {
		builder.WriteString("\r\n")
		builder.WriteString(row)
	}
	builder.WriteString("\x1b[" + strconv.Itoa(p.height) + "r")
	builder.WriteString("\x1b[u")
	return builder.String()
}

// WriteTo writes the complete handoff sequence to an in-memory or test writer.
func (p HandoffPlan) WriteTo(w io.Writer) (int, error) {
	if w == nil {
		return 0, nil
	}
	return io.WriteString(w, p.ANSI())
}
