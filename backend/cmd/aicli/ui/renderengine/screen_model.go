package renderengine

import (
	"strconv"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
)

const sgrReset = "\x1b[0m"

// ProjectionValidity describes whether the front buffer is still a trustworthy
// physical-terminal projection. It is deliberately independent of the staged
// semantic back frame.
type ProjectionValidity uint8

const (
	ProjectionUnknown ProjectionValidity = iota
	ProjectionKnown
)

func (v ProjectionValidity) String() string {
	if v == ProjectionKnown {
		return "known"
	}
	return "unknown"
}

// ScreenModel is a double-buffered cell grid for an owned terminal region.
// The front buffer represents the last known terminal frame, and the back
// buffer is the next composed frame. Flush emits their minimal ANSI diff.
type ScreenModel struct {
	width, height int
	front, back   [][]vt.Cell
	forceRepaint  bool
	projection    ProjectionValidity
	trace         *PaintTrace
}

// NewScreenModel creates a blank owned screen model.
func NewScreenModel(width, height int) *ScreenModel {
	width, height = clampSize(width, height)
	return &ScreenModel{
		width:      width,
		height:     height,
		front:      blankGrid(width, height),
		back:       blankGrid(width, height),
		projection: ProjectionKnown,
	}
}

// Size reports the model geometry in cells and rows.
func (m *ScreenModel) Size() (int, int) {
	if m == nil {
		return 0, 0
	}
	return m.width, m.height
}

// ProjectionValidity reports whether incremental diffing may rely on front.
func (m *ScreenModel) ProjectionValidity() ProjectionValidity {
	if m == nil {
		return ProjectionUnknown
	}
	return m.projection
}

// Clone returns an independent transactional candidate. Terminal presenters
// can resize, stage, and prepare a flush on the clone without consuming the
// last confirmed front buffer before the target accepts every byte.
func (m *ScreenModel) Clone() *ScreenModel {
	if m == nil {
		return nil
	}
	clone := *m
	clone.front = cloneCellGrid(m.front)
	clone.back = cloneCellGrid(m.back)
	return &clone
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
	m.projection = ProjectionUnknown
}

// Invalidate forgets the physical terminal state while preserving the staged
// back frame. The next Flush clears and repaints all rows.
func (m *ScreenModel) Invalidate() {
	if m != nil {
		m.forceRepaint = true
		m.projection = ProjectionUnknown
	}
}

// ClearForceRepaint drops a pending full repaint after the caller has
// re-synchronized the committed front buffer with the terminal (for example
// ApplyRegionAppend mirrors an already-emitted scroll). The following diffing
// Flush then proceeds row-wise; rows the caller did not touch are still
// reconciled against the staged back frame.
func (m *ScreenModel) ClearForceRepaint() {
	if m != nil && m.projection == ProjectionKnown {
		m.forceRepaint = false
	}
}

// MarkKnown accepts an externally completed terminal transaction whose exact
// physical effect has already been mirrored into front/back.
func (m *ScreenModel) MarkKnown() {
	if m != nil {
		m.projection = ProjectionKnown
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

// ScrollUp records that the terminal moved the complete owned screen up by
// count rows. It is the full-screen convenience form of ScrollRegionUp.
func (m *ScreenModel) ScrollUp(count int) {
	if m == nil {
		return
	}
	m.ScrollRegionUp(1, m.height, count)
}

// ScrollDown records that the terminal moved the complete owned screen down by
// count rows. It is the full-screen convenience form of ScrollRegionDown.
func (m *ScreenModel) ScrollDown(count int) {
	if m == nil {
		return
	}
	m.ScrollRegionDown(1, m.height, count)
}

// ScrollRegionUp mirrors an already-emitted terminal scroll for the inclusive,
// 1-based row region. Both buffers move together, rows outside the region stay
// untouched, and newly exposed bottom rows are blank.
func (m *ScreenModel) ScrollRegionUp(top, bottom, count int) {
	if m == nil || count <= 0 {
		return
	}
	top, bottom, ok := m.clampRegion(top, bottom)
	if !ok {
		return
	}
	scrollGridRegionUp(m.front, m.width, top-1, bottom, count)
	scrollGridRegionUp(m.back, m.width, top-1, bottom, count)
}

// ScrollRegionDown mirrors an already-emitted terminal reverse scroll for the
// inclusive, 1-based row region. Both buffers move together, rows outside the
// region stay untouched, and newly exposed top rows are blank.
func (m *ScreenModel) ScrollRegionDown(top, bottom, count int) {
	if m == nil || count <= 0 {
		return
	}
	top, bottom, ok := m.clampRegion(top, bottom)
	if !ok {
		return
	}
	scrollGridRegionDown(m.front, m.width, top-1, bottom, count)
	scrollGridRegionDown(m.back, m.width, top-1, bottom, count)
}

// ApplyRegionAppend mirrors the exact physical effect of writing rows at the
// bottom of a DECSTBM region as "\r\n<row>": every row scrolls the region up
// once, then occupies the newly exposed bottom row. It updates front and back
// together because the corresponding ANSI has already reached the terminal.
func (m *ScreenModel) ApplyRegionAppend(top, bottom int, rows [][]vt.Cell) {
	if m == nil || len(rows) == 0 {
		return
	}
	top, bottom, ok := m.clampRegion(top, bottom)
	if !ok {
		return
	}
	applyRegionAppend(m.front, m.width, top-1, bottom, rows)
	applyRegionAppend(m.back, m.width, top-1, bottom, rows)
}

// RegionPrefixEquals reports whether the current confirmed physical front
// begins with rows in an owned inclusive region. It is a projection-cache
// proof used only to choose a terminal scroll operation; callers must never
// recover semantic transcript content from this cache.
func (m *ScreenModel) RegionPrefixEquals(top, bottom int, rows [][]vt.Cell) bool {
	if m == nil || m.projection != ProjectionKnown || len(rows) == 0 {
		return false
	}
	top, bottom, ok := m.clampRegion(top, bottom)
	if !ok || len(rows) > bottom-top+1 {
		return false
	}
	for index, row := range rows {
		if !rowCellsEqual(m.front[top-1+index], normalizeRow(row, m.width)) {
			return false
		}
	}
	return true
}

func (m *ScreenModel) clampRegion(top, bottom int) (int, int, bool) {
	if m == nil || m.height < 1 {
		return 0, 0, false
	}
	if top < 1 {
		top = 1
	}
	if bottom > m.height {
		bottom = m.height
	}
	if top > bottom {
		return 0, 0, false
	}
	return top, bottom, true
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

// Flush is the compatibility API for callers that synchronously consume the
// returned bytes themselves. New terminal transactions must use
// PrepareFlush, then call ConfirmFlush only after the target accepted the
// entire frame (or MarkWriteFailed for a short/erroring write).
func (m *ScreenModel) Flush() string {
	output := m.PrepareFlush()
	m.ConfirmFlush()
	return output
}

// PrepareFlush builds the ANSI diff and advances the tentative front frame,
// but leaves its physical projection Unknown until ConfirmFlush. A later
// MarkWriteFailed therefore turns the following transaction into a recovery
// full repaint rather than allowing an unsafe incremental diff.
func (m *ScreenModel) PrepareFlush() string {
	if m == nil {
		return ""
	}
	var output strings.Builder
	var events []paintRowEvent
	if m.trace != nil {
		events = make([]paintRowEvent, 0, m.height)
	}
	if m.forceRepaint || m.projection != ProjectionKnown {
		for r := 0; r < m.height; r++ {
			m.emitForcedRow(&output, r)
			if events != nil {
				events = append(events, paintRowEvent{
					row:     r + 1,
					hash:    rowContentHash(m.back[r]),
					changed: !m.rowContentEqual(r),
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
					hash:    rowContentHash(m.back[r]),
					changed: !m.rowContentEqual(r),
					painted: painted,
				})
			}
		}
	}
	for r := 0; r < m.height; r++ {
		copy(m.front[r], m.back[r])
	}
	m.forceRepaint = false
	// The bytes have not reached a terminal yet. Keep the physical projection
	// unknown until the presenter confirms a complete target write.
	m.projection = ProjectionUnknown
	if m.trace != nil && len(events) > 0 {
		m.trace.recordFrame(events, m.height)
	}
	return output.String()
}

// ConfirmFlush marks the tentative front commit as physically known. It is a
// no-op for nil models and is intentionally separate from Flush so a failed
// terminal write cannot be mistaken for a rendered frame.
func (m *ScreenModel) ConfirmFlush() {
	if m != nil {
		m.projection = ProjectionKnown
	}
}

// MarkWriteFailed invalidates the physical projection after a zero-byte,
// short, or erroring terminal write. The staged semantic back frame remains
// intact for the recovery transaction.
func (m *ScreenModel) MarkWriteFailed() {
	if m != nil {
		m.projection = ProjectionUnknown
		m.forceRepaint = true
	}
}

// rowContentHash returns the plain-text content hash of a staged row with
// any leading surface debug annotation excluded, so the reconciliation
// probe's per-content white counters key on the message content itself
// rather than the volatile debug tag (whose w counter and star marker
// change exactly when a white repaint is recorded, which would otherwise
// make the row's own fingerprint unstable).
func rowContentHash(cells []vt.Cell) uint32 {
	return RowTextHash(stripDebugTagPrefix(cells))
}

// stripDebugTagPrefix removes a leading surface debug annotation
// ("[hhhh #NN wN*]") from the row and returns the remaining cells. Rows
// without the annotation are returned unchanged. The pattern mirrors the
// fixed format emitted by the surface's /debug row annotation.
func stripDebugTagPrefix(cells []vt.Cell) []vt.Cell {
	if len(cells) < 13 || len(cells[0].Text) != 1 || cells[0].Text != "[" {
		return cells
	}
	i := 1
	for ; i < 5; i++ { // 4 hex fingerprint digits
		if len(cells[i].Text) != 1 || !isHexChar(cells[i].Text[0]) {
			return cells
		}
	}
	if cells[i].Text != " " || cells[i+1].Text != "#" {
		return cells
	}
	i += 2
	rowDigits := 0
	for i < len(cells) && len(cells[i].Text) == 1 && isDigitChar(cells[i].Text[0]) {
		rowDigits++
		i++
	}
	if rowDigits == 0 || i+2 >= len(cells) {
		return cells
	}
	if cells[i].Text != " " || cells[i+1].Text != "w" {
		return cells
	}
	i += 2
	whiteDigits := 0
	for i < len(cells) && len(cells[i].Text) == 1 && isDigitChar(cells[i].Text[0]) {
		whiteDigits++
		i++
	}
	if whiteDigits == 0 {
		return cells
	}
	if i < len(cells) && cells[i].Text == "*" {
		i++
	}
	if i >= len(cells) || cells[i].Text != "]" {
		return cells
	}
	return cells[i+1:]
}

func isHexChar(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func isDigitChar(c byte) bool {
	return c >= '0' && c <= '9'
}

// rowContentEqual reports whether the staged back row equals the committed
// front row for reconciliation purposes. The comparison covers the full row:
// a row whose debug tag advanced differs from the previous frame and is
// classified as a content change (a tag-sync emit), while a row re-emitted
// with an identical full row is the duplicate-rendering signal (white).
func (m *ScreenModel) rowContentEqual(row int) bool {
	return rowCellsEqual(m.front[row], m.back[row])
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

func cloneCellGrid(source [][]vt.Cell) [][]vt.Cell {
	if len(source) == 0 {
		return nil
	}
	clone := make([][]vt.Cell, len(source))
	for row := range source {
		clone[row] = make([]vt.Cell, len(source[row]))
		for column, cell := range source[row] {
			clone[row][column] = cell
			clone[row][column].SGR = append([]string(nil), cell.SGR...)
		}
	}
	return clone
}

func scrollGridRegionUp(grid [][]vt.Cell, width, start, end, count int) {
	if start < 0 || end > len(grid) || start >= end || count <= 0 {
		return
	}
	height := end - start
	if count > height {
		count = height
	}
	copy(grid[start:end-count], grid[start+count:end])
	for row := end - count; row < end; row++ {
		grid[row] = make([]vt.Cell, width)
	}
}

func scrollGridRegionDown(grid [][]vt.Cell, width, start, end, count int) {
	if start < 0 || end > len(grid) || start >= end || count <= 0 {
		return
	}
	height := end - start
	if count > height {
		count = height
	}
	copy(grid[start+count:end], grid[start:end-count])
	for row := start; row < start+count; row++ {
		grid[row] = make([]vt.Cell, width)
	}
}

func applyRegionAppend(grid [][]vt.Cell, width, start, end int, rows [][]vt.Cell) {
	if len(rows) == 0 {
		return
	}
	scrollGridRegionUp(grid, width, start, end, len(rows))
	regionRows := end - start
	visible := rows
	if len(visible) > regionRows {
		visible = visible[len(visible)-regionRows:]
	}
	row := end - len(visible)
	for _, cells := range visible {
		grid[row] = normalizeRow(cells, width)
		row++
	}
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
