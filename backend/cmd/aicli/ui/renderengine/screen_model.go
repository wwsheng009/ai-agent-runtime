package renderengine

import (
	"strconv"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
)

const sgrReset = "\x1b[0m"

// ScreenModel is a double-buffered cell grid for an owned terminal region.
// The front buffer represents the last known terminal frame, and the back
// buffer is the next composed frame. Flush emits their minimal ANSI diff.
type ScreenModel struct {
	width, height int
	front, back   [][]vt.Cell
	forceRepaint  bool
	trace         *PaintTrace
}

// NewScreenModel creates a blank owned screen model.
func NewScreenModel(width, height int) *ScreenModel {
	width, height = clampSize(width, height)
	return &ScreenModel{
		width:  width,
		height: height,
		front:  blankGrid(width, height),
		back:   blankGrid(width, height),
	}
}

// Size reports the model geometry in cells and rows.
func (m *ScreenModel) Size() (int, int) {
	if m == nil {
		return 0, 0
	}
	return m.width, m.height
}

// Resize discards both physical buffers and makes the next Flush repaint the
// complete frame. Source-backed content remains owned by the composer.
func (m *ScreenModel) Resize(width, height int) {
	if m == nil {
		return
	}
	width, height = clampSize(width, height)
	m.width, m.height = width, height
	m.front = blankGrid(width, height)
	m.back = blankGrid(width, height)
	m.forceRepaint = true
}

// Invalidate forgets the physical terminal state while preserving the staged
// back frame. The next Flush clears and repaints all rows.
func (m *ScreenModel) Invalidate() {
	if m != nil {
		m.forceRepaint = true
	}
}

// AttachTrace wires the paint reconciliation probe to this model. The probe
// is owned by the Engine and may be shared; attaching is idempotent. When no
// probe is attached, Flush performs no per-row bookkeeping.
func (m *ScreenModel) AttachTrace(trace *PaintTrace) {
	if m != nil {
		m.trace = trace
	}
}

// DetachTrace removes the reconciliation probe.
func (m *ScreenModel) DetachTrace() {
	if m != nil {
		m.trace = nil
	}
}

// StageFrame overwrites the complete back buffer. Rows and cells outside the
// supplied frame are normalized to blank cells.
func (m *ScreenModel) StageFrame(rows [][]vt.Cell) {
	if m == nil {
		return
	}
	for r := 0; r < m.height; r++ {
		var row []vt.Cell
		if r < len(rows) {
			row = rows[r]
		}
		m.back[r] = normalizeRow(row, m.width)
	}
}

// StageRow overwrites one 1-based row in the back buffer.
func (m *ScreenModel) StageRow(row int, cells []vt.Cell) {
	if m == nil || row < 1 || row > m.height {
		return
	}
	m.back[row-1] = normalizeRow(cells, m.width)
}

// CommitRange updates front from back without emitting ANSI for an inclusive
// 1-based row range. It is used after native terminal scrolling has already
// moved the corresponding owned history rows.
func (m *ScreenModel) CommitRange(top, bottom int) {
	if m == nil {
		return
	}
	if top < 1 {
		top = 1
	}
	if bottom > m.height {
		bottom = m.height
	}
	for r := top - 1; r < bottom; r++ {
		copy(m.front[r], m.back[r])
	}
}

// Flush returns the ANSI diff that transforms the front frame into the staged
// back frame, then commits back as the new front frame.
func (m *ScreenModel) Flush() string {
	if m == nil {
		return ""
	}
	var output strings.Builder
	var events []paintRowEvent
	if m.trace != nil {
		events = make([]paintRowEvent, 0, m.height)
	}
	if m.forceRepaint {
		for r := 0; r < m.height; r++ {
			m.emitForcedRow(&output, r)
			if events != nil {
				events = append(events, paintRowEvent{
					row:     r + 1,
					changed: !rowCellsEqual(m.front[r], m.back[r]),
					painted: true,
				})
			}
		}
	} else {
		for r := 0; r < m.height; r++ {
			painted := m.diffRow(&output, r)
			if events != nil {
				events = append(events, paintRowEvent{
					row:     r + 1,
					changed: !rowCellsEqual(m.front[r], m.back[r]),
					painted: painted,
				})
			}
		}
	}
	for r := 0; r < m.height; r++ {
		copy(m.front[r], m.back[r])
	}
	m.forceRepaint = false
	if m.trace != nil && len(events) > 0 {
		m.trace.recordFrame(events, m.height)
	}
	return output.String()
}

func (m *ScreenModel) emitForcedRow(output *strings.Builder, row int) {
	if output == nil || row < 0 || row >= m.height {
		return
	}
	output.WriteString("\x1b[")
	output.WriteString(strconv.Itoa(row + 1))
	output.WriteString(";1H")
	output.WriteString(sgrReset)
	output.WriteString("\x1b[K")

	high := -1
	for column, cell := range m.back[row] {
		if cell.Text != "" || cell.Cont || len(cell.SGR) > 0 {
			high = column
		}
	}
	if high >= 0 {
		m.emitRowRange(output, row, 0, high)
	}
}

// diffRow emits the minimal ANSI update for one row and reports whether any
// terminal bytes were produced for it (painted). A row with no cell
// difference is not painted.
func (m *ScreenModel) diffRow(output *strings.Builder, row int) bool {
	front, back := m.front[row], m.back[row]
	low, high := -1, -1
	for column := 0; column < m.width; column++ {
		if !cellEqual(front[column], back[column]) {
			if low == -1 {
				low = column
			}
			high = column
		}
	}
	if low == -1 {
		return false
	}
	for low > 0 && back[low].Cont {
		low--
	}
	for column := low; column <= high; column++ {
		if cellBlank(back[column]) && !cellBlank(front[column]) {
			m.emitForcedRow(output, row)
			return true
		}
	}
	m.emitRowRange(output, row, low, high)
	return true
}

func (m *ScreenModel) emitRowRange(output *strings.Builder, row, low, high int) {
	if output == nil || row < 0 || row >= m.height || low < 0 || high < low || high >= m.width {
		return
	}
	back := m.back[row]
	output.WriteString("\x1b[")
	output.WriteString(strconv.Itoa(row + 1))
	output.WriteByte(';')
	output.WriteString(strconv.Itoa(low + 1))
	output.WriteByte('H')
	var activeSGR []string
	haveActiveSGR := false
	for column := low; column <= high; column++ {
		cell := back[column]
		if cell.Cont {
			continue
		}
		if !haveActiveSGR || !sgrEqual(activeSGR, cell.SGR) {
			output.WriteString(sgrReset)
			if len(cell.SGR) > 0 {
				output.WriteString("\x1b[")
				output.WriteString(strings.Join(cell.SGR, ";"))
				output.WriteByte('m')
			}
			activeSGR = cell.SGR
			haveActiveSGR = true
		}
		if cell.Text == "" {
			output.WriteByte(' ')
		} else {
			output.WriteString(cell.Text)
		}
	}
	output.WriteString(sgrReset)
}

func cellBlank(cell vt.Cell) bool {
	return cell.Text == "" && !cell.Cont && len(cell.SGR) == 0
}

func cellEqual(left, right vt.Cell) bool {
	if left.Text != right.Text || left.Cont != right.Cont || len(left.SGR) != len(right.SGR) {
		return false
	}
	for i := range left.SGR {
		if left.SGR[i] != right.SGR[i] {
			return false
		}
	}
	return true
}

// rowCellsEqual reports whether two full rows are cell-identical. It is used
// by the paint reconciliation probe to classify emitted rows as content
// changes versus white repaints.
func rowCellsEqual(left, right []vt.Cell) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if !cellEqual(left[i], right[i]) {
			return false
		}
	}
	return true
}

func sgrEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func normalizeRow(source []vt.Cell, width int) []vt.Cell {
	row := make([]vt.Cell, width)
	for column := 0; column < width && column < len(source); column++ {
		row[column] = source[column]
	}
	return row
}

func blankGrid(width, height int) [][]vt.Cell {
	grid := make([][]vt.Cell, height)
	for row := range grid {
		grid[row] = make([]vt.Cell, width)
	}
	return grid
}

func clampSize(width, height int) (int, int) {
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	return width, height
}
