// Package vt provides a shared, display-width aware virtual-terminal screen
// model: it replays the byte stream a surface actually writes and reconstructs
// the resulting rows. It is usable both by production surfaces (the P5 owned
// viewport backend) and by tests.
//
// The screen emulator replays the byte stream a surface actually writes and
// reconstructs the resulting rows, so tests can assert what the user sees
// instead of asserting escape sequences. Sequence-level assertions cannot catch
// row math errors (a band painted above the rows it reserved, a committed row
// overwritten by reserve compensation), and they cannot catch content errors
// (a missing block separator inside the active band).
//
// Supported VT100/xterm subset: CR, LF, IND, RI, DECSC/DECRC (ESC 7/8 and
// CSI s/u), CUP, CUU/CUD/CUF/CUB, DECSTBM, SD (CSI T), EL, ED, SGR and OSC
// skipping. Cell widths follow render.Width, so wide runes (CJK, emoji) occupy
// two columns and wrap like a real terminal.
package vt

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
)

// Cell is one reconstructed screen cell.
type Cell struct {
	// Text is the printed rune, empty for a blank cell.
	Text string
	// Cont marks the second column of a double-width rune.
	Cont bool
	// SGR holds the select-graphic-rendition codes active when the cell was
	// printed, in the order they were set ("1", "2", "38;5;12", ...).
	SGR []string
}

func (c Cell) blank() bool {
	return c.Text == "" && !c.Cont
}

// Screen is a reconstructed terminal screen.
type Screen struct {
	width, height int
	rows          [][]Cell
	scrollback    [][]Cell
	row, col      int
	top, bottom   int
	savedRow      int
	savedCol      int
	hasSaved      bool
	sgr           []string
}

// NewScreen builds an empty screen with a full-height scroll region.
func NewScreen(width, height int) *Screen {
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	s := &Screen{width: width, height: height, row: 1, col: 1, top: 1, bottom: height}
	s.rows = make([][]Cell, height)
	for i := range s.rows {
		s.rows[i] = blankRow(width)
	}
	return s
}

// Width reports the screen width in cells.
func (s *Screen) Width() int { return s.width }

// Height reports the screen height in rows.
func (s *Screen) Height() int { return s.height }

// CursorRow reports the 1-based cursor row.
func (s *Screen) CursorRow() int { return s.row }

// CursorCol reports the 1-based cursor column.
func (s *Screen) CursorCol() int { return s.col }

// ScrollbackRows returns a deep copy of rows that were pushed above the
// physical screen by a full-width scroll whose region starts at row 1.
//
// Rows displaced by a sub-region whose top is below row 1 are not native
// terminal scrollback and are deliberately not recorded. Reverse-index and
// scroll-down insert blank rows; they do not pull rows back from scrollback.
func (s *Screen) ScrollbackRows() [][]Cell {
	if s == nil || len(s.scrollback) == 0 {
		return nil
	}
	return cloneRows(s.scrollback)
}

// ScrollbackLines returns the visible text of ScrollbackRows in commit order.
func (s *Screen) ScrollbackLines() []string {
	if s == nil || len(s.scrollback) == 0 {
		return nil
	}
	lines := make([]string, len(s.scrollback))
	for i, row := range s.scrollback {
		lines[i] = cellLine(row)
	}
	return lines
}

func blankRow(width int) []Cell {
	return make([]Cell, width)
}

func cloneRows(rows [][]Cell) [][]Cell {
	if len(rows) == 0 {
		return nil
	}
	out := make([][]Cell, len(rows))
	for row := range rows {
		out[row] = make([]Cell, len(rows[row]))
		for col, cell := range rows[row] {
			out[row][col] = cell
			if len(cell.SGR) > 0 {
				out[row][col].SGR = append([]string(nil), cell.SGR...)
			}
		}
	}
	return out
}

func cellLine(row []Cell) string {
	var b strings.Builder
	for _, cell := range row {
		if cell.Cont {
			continue
		}
		if cell.Text == "" {
			b.WriteByte(' ')
			continue
		}
		b.WriteString(cell.Text)
	}
	return strings.TrimRight(b.String(), " ")
}

// Feed replays a byte stream onto the screen.
func (s *Screen) Feed(stream string) {
	runes := []rune(stream)
	for i := 0; i < len(runes); i++ {
		switch runes[i] {
		case '\r':
			s.col = 1
		case '\n':
			s.index()
			s.col = 1
		case '\t':
			// Terminals advance to the next 8-column tab stop.
			next := ((s.col-1)/8+1)*8 + 1
			if next > s.width {
				next = s.width
			}
			s.col = next
		case 0x1b:
			i += s.escape(runes[i+1:])
		case 0x07:
			// BEL is audible only.
		default:
			s.put(runes[i])
		}
	}
}

// index moves down one row, scrolling the region when already at its bottom.
func (s *Screen) index() {
	if s.row == s.bottom {
		s.recordScrollbackRows(1)
		copy(s.rows[s.top-1:s.bottom-1], s.rows[s.top:s.bottom])
		s.rows[s.bottom-1] = blankRow(s.width)
		return
	}
	if s.row < s.height {
		s.row++
	}
}

// reverseIndex moves up one row, scrolling the region when already at its top.
func (s *Screen) reverseIndex() {
	if s.row == s.top {
		for i := s.bottom - 1; i > s.top-1; i-- {
			s.rows[i] = s.rows[i-1]
		}
		s.rows[s.top-1] = blankRow(s.width)
		return
	}
	if s.row > 1 {
		s.row--
	}
}

// scrollDown implements SD (CSI T): insert blank rows at the region top.
func (s *Screen) scrollDown(rows int) {
	if rows < 1 {
		rows = 1
	}
	if regionRows := s.bottom - s.top + 1; rows > regionRows {
		rows = regionRows
	}
	for row := s.bottom - 1; row >= s.top-1+rows; row-- {
		s.rows[row] = s.rows[row-rows]
	}
	for row := s.top - 1; row < s.top-1+rows; row++ {
		s.rows[row] = blankRow(s.width)
	}
}

func (s *Screen) recordScrollbackRows(count int) {
	if s == nil || s.top != 1 || count <= 0 {
		return
	}
	regionRows := s.bottom - s.top + 1
	if count > regionRows {
		count = regionRows
	}
	s.scrollback = append(s.scrollback, cloneRows(s.rows[:count])...)
}

// put prints one rune using its display width. Zero-width runes (combining
// marks, variation selectors) attach to the previous cell instead of consuming
// a column, and double-width runes claim a continuation cell so wrapping and
// row reconstruction match a real terminal.
func (s *Screen) put(r rune) {
	width := render.Width(string(r))
	if width <= 0 {
		s.attachZeroWidth(r)
		return
	}
	// Deferred wrap: the column overflow is only resolved when the next
	// printable rune arrives, so a trailing CR/CUP cancels it like a terminal.
	if s.col+width-1 > s.width {
		s.col = 1
		s.index()
	}
	if s.col < 1 {
		s.col = 1
	}
	row := s.rows[s.row-1]
	s.clearWideNeighbor(row, s.col-1)
	row[s.col-1] = Cell{Text: string(r), SGR: s.activeSGR()}
	for offset := 1; offset < width && s.col-1+offset < s.width; offset++ {
		s.clearWideNeighbor(row, s.col-1+offset)
		row[s.col-1+offset] = Cell{Cont: true, SGR: s.activeSGR()}
	}
	s.col += width
}

func (s *Screen) attachZeroWidth(r rune) {
	row := s.rows[s.row-1]
	index := s.col - 2
	for index >= 0 && row[index].Cont {
		index--
	}
	if index < 0 || row[index].Text == "" {
		return
	}
	row[index].Text += string(r)
}

// clearWideNeighbor blanks the other half of a double-width cell that is about
// to be partially overwritten, so no orphan continuation cell survives.
func (s *Screen) clearWideNeighbor(row []Cell, index int) {
	if index < 0 || index >= len(row) {
		return
	}
	if row[index].Cont {
		if index > 0 {
			row[index-1] = Cell{}
		}
		return
	}
	if row[index].Text != "" && index+1 < len(row) && row[index+1].Cont {
		row[index+1] = Cell{}
	}
}

func (s *Screen) activeSGR() []string {
	if len(s.sgr) == 0 {
		return nil
	}
	return append([]string(nil), s.sgr...)
}

// escape consumes one escape sequence and reports how many runes it used,
// excluding the leading ESC.
func (s *Screen) escape(rest []rune) int {
	if len(rest) == 0 {
		return 0
	}
	switch rest[0] {
	case 'M':
		s.reverseIndex()
		return 1
	case 'D':
		s.index()
		return 1
	case 'E':
		s.index()
		s.col = 1
		return 1
	case '7':
		s.savedRow, s.savedCol, s.hasSaved = s.row, s.col, true
		return 1
	case '8':
		if s.hasSaved {
			s.row, s.col = s.savedRow, s.savedCol
		}
		return 1
	case ']':
		// OSC: skip to BEL or ST.
		for i := 1; i < len(rest); i++ {
			if rest[i] == 0x07 {
				return i + 1
			}
			if rest[i] == 0x1b && i+1 < len(rest) && rest[i+1] == '\\' {
				return i + 2
			}
		}
		return len(rest)
	case '[':
		j := 1
		for j < len(rest) && isCSIParam(rest[j]) {
			j++
		}
		// Intermediate bytes (space, !, ", $, ') precede the final byte.
		for j < len(rest) && rest[j] >= 0x20 && rest[j] <= 0x2f {
			j++
		}
		if j >= len(rest) {
			return len(rest)
		}
		s.csi(string(rest[1:j]), rest[j])
		return j + 1
	}
	return 1
}

func isCSIParam(r rune) bool {
	return r == '?' || r == '>' || r == '<' || r == '=' || r == ';' || r == ':' || (r >= '0' && r <= '9')
}

func (s *Screen) csi(params string, final rune) {
	private := strings.HasPrefix(params, "?") || strings.HasPrefix(params, ">") ||
		strings.HasPrefix(params, "<") || strings.HasPrefix(params, "=")
	numeric := strings.TrimLeft(params, "?><=")
	fields := make([]int, 0, 4)
	for _, part := range strings.Split(numeric, ";") {
		n, err := strconv.Atoi(part)
		if err != nil {
			n = 0
		}
		fields = append(fields, n)
	}
	arg := func(i, def int) int {
		if i < len(fields) && fields[i] > 0 {
			return fields[i]
		}
		return def
	}
	switch final {
	case 'm':
		if !private {
			s.applySGR(numeric)
		}
	case 'r':
		if private {
			return
		}
		s.top, s.bottom = s.clampRow(arg(0, 1)), s.clampRow(arg(1, s.height))
		if s.bottom < s.top {
			s.top, s.bottom = 1, s.height
		}
	case 'H', 'f':
		s.row, s.col = s.clampRow(arg(0, 1)), s.clampCol(arg(1, 1))
	case 'd':
		s.row = s.clampRow(arg(0, 1))
	case 'G':
		s.col = s.clampCol(arg(0, 1))
	case 'A':
		s.row = s.clampRow(s.row - arg(0, 1))
	case 'B':
		s.row = s.clampRow(s.row + arg(0, 1))
	case 'C':
		s.col = s.clampCol(s.col + arg(0, 1))
	case 'D':
		s.col = s.clampCol(s.col - arg(0, 1))
	case 'T':
		s.scrollDown(arg(0, 1))
	case 'S':
		s.scrollUp(arg(0, 1))
	case 'L':
		s.insertLines(arg(0, 1))
	case 'M':
		s.deleteLines(arg(0, 1))
	case 'K':
		switch arg(0, 0) {
		case 1:
			s.clearCells(s.row, 1, s.col)
		case 2:
			s.clearCells(s.row, 1, s.width)
		default:
			s.clearCells(s.row, s.col, s.width)
		}
	case 'J':
		switch arg(0, 0) {
		case 1:
			s.clearCells(s.row, 1, s.col)
			for row := 1; row < s.row; row++ {
				s.rows[row-1] = blankRow(s.width)
			}
		case 2:
			for row := range s.rows {
				s.rows[row] = blankRow(s.width)
			}
		case 3:
			// XTerm ED3 purges saved lines without erasing the visible page.
			// Callers that need both effects emit ED2 followed by ED3.
			s.scrollback = nil
		default:
			s.clearCells(s.row, s.col, s.width)
			for row := s.row + 1; row <= s.height; row++ {
				s.rows[row-1] = blankRow(s.width)
			}
		}
	case 's':
		if !private {
			s.savedRow, s.savedCol, s.hasSaved = s.row, s.col, true
		}
	case 'u':
		if !private && s.hasSaved {
			s.row, s.col = s.savedRow, s.savedCol
		}
	}
}

func (s *Screen) clampRow(row int) int {
	if row < 1 {
		return 1
	}
	if row > s.height {
		return s.height
	}
	return row
}

func (s *Screen) clampCol(col int) int {
	if col < 1 {
		return 1
	}
	if col > s.width {
		return s.width
	}
	return col
}

func (s *Screen) scrollUp(rows int) {
	if rows < 1 {
		rows = 1
	}
	if regionRows := s.bottom - s.top + 1; rows > regionRows {
		rows = regionRows
	}
	s.recordScrollbackRows(rows)
	for row := s.top - 1; row < s.bottom-rows; row++ {
		s.rows[row] = s.rows[row+rows]
	}
	for row := s.bottom - rows; row < s.bottom; row++ {
		s.rows[row] = blankRow(s.width)
	}
}

func (s *Screen) insertLines(rows int) {
	if s.row < s.top || s.row > s.bottom {
		return
	}
	if rows < 1 {
		rows = 1
	}
	if limit := s.bottom - s.row + 1; rows > limit {
		rows = limit
	}
	for row := s.bottom - 1; row >= s.row-1+rows; row-- {
		s.rows[row] = s.rows[row-rows]
	}
	for row := s.row - 1; row < s.row-1+rows; row++ {
		s.rows[row] = blankRow(s.width)
	}
}

func (s *Screen) deleteLines(rows int) {
	if s.row < s.top || s.row > s.bottom {
		return
	}
	if rows < 1 {
		rows = 1
	}
	if limit := s.bottom - s.row + 1; rows > limit {
		rows = limit
	}
	for row := s.row - 1; row < s.bottom-rows; row++ {
		s.rows[row] = s.rows[row+rows]
	}
	for row := s.bottom - rows; row < s.bottom; row++ {
		s.rows[row] = blankRow(s.width)
	}
}

// clearCells blanks the inclusive 1-based column range on one row and never
// leaves half of a double-width rune behind.
func (s *Screen) clearCells(row, from, to int) {
	if row < 1 || row > s.height {
		return
	}
	if from < 1 {
		from = 1
	}
	if to > s.width {
		to = s.width
	}
	cells := s.rows[row-1]
	for index := from - 1; index < to; index++ {
		s.clearWideNeighbor(cells, index)
		cells[index] = Cell{}
	}
}

// applySGR maintains the active graphic rendition codes. Extended color forms
// (38;5;n and 38;2;r;g;b) are kept as single composite codes so callers can
// compare them directly.
func (s *Screen) applySGR(params string) {
	if strings.TrimSpace(params) == "" {
		s.sgr = nil
		return
	}
	parts := strings.Split(params, ";")
	for i := 0; i < len(parts); i++ {
		code := strings.TrimSpace(parts[i])
		if code == "" || code == "0" {
			s.sgr = nil
			continue
		}
		if code == "38" || code == "48" {
			if composite, used := compositeColorCode(parts[i:]); used > 1 {
				s.setSGR(composite)
				i += used - 1
				continue
			}
		}
		s.setSGR(code)
	}
}

func compositeColorCode(parts []string) (string, int) {
	if len(parts) < 2 {
		return "", 0
	}
	switch strings.TrimSpace(parts[1]) {
	case "5":
		if len(parts) >= 3 {
			return strings.Join([]string{parts[0], parts[1], parts[2]}, ";"), 3
		}
	case "2":
		if len(parts) >= 5 {
			return strings.Join(parts[:5], ";"), 5
		}
	}
	return "", 0
}

func (s *Screen) setSGR(code string) {
	switch code {
	case "22":
		s.dropSGR("1", "2")
		return
	case "23":
		s.dropSGR("3")
		return
	case "24":
		s.dropSGR("4")
		return
	case "27":
		s.dropSGR("7")
		return
	case "39":
		s.dropForeground()
		return
	case "49":
		s.dropBackground()
		return
	}
	if isForegroundCode(code) {
		s.dropForeground()
	}
	if isBackgroundCode(code) {
		s.dropBackground()
	}
	for _, existing := range s.sgr {
		if existing == code {
			return
		}
	}
	s.sgr = append(s.sgr, code)
}

func (s *Screen) dropSGR(codes ...string) {
	if len(s.sgr) == 0 {
		return
	}
	kept := s.sgr[:0]
	for _, existing := range s.sgr {
		drop := false
		for _, code := range codes {
			if existing == code {
				drop = true
				break
			}
		}
		if !drop {
			kept = append(kept, existing)
		}
	}
	s.sgr = kept
}

func (s *Screen) dropForeground() {
	kept := make([]string, 0, len(s.sgr))
	for _, code := range s.sgr {
		if !isForegroundCode(code) {
			kept = append(kept, code)
		}
	}
	s.sgr = kept
}

func (s *Screen) dropBackground() {
	kept := make([]string, 0, len(s.sgr))
	for _, code := range s.sgr {
		if !isBackgroundCode(code) {
			kept = append(kept, code)
		}
	}
	s.sgr = kept
}

func isForegroundCode(code string) bool {
	if strings.HasPrefix(code, "38") {
		return true
	}
	n, err := strconv.Atoi(code)
	if err != nil {
		return false
	}
	return (n >= 30 && n <= 37) || (n >= 90 && n <= 97)
}

func isBackgroundCode(code string) bool {
	if strings.HasPrefix(code, "48") {
		return true
	}
	n, err := strconv.Atoi(code)
	if err != nil {
		return false
	}
	return (n >= 40 && n <= 47) || (n >= 100 && n <= 107)
}

// Line returns the 1-based row as visible text with trailing blanks removed.
// A double-width rune contributes its two columns exactly once.
func (s *Screen) Line(row int) string {
	if row < 1 || row > s.height {
		return ""
	}
	var b strings.Builder
	for _, cell := range s.rows[row-1] {
		switch {
		case cell.Cont:
			// Owned by the preceding wide rune.
		case cell.Text == "":
			b.WriteByte(' ')
		default:
			b.WriteString(cell.Text)
		}
	}
	return strings.TrimRight(b.String(), " ")
}

// LineWidth returns the display width of the row content, i.e. the last used
// column. It is the assertion hook for "no row overflowed the terminal".
func (s *Screen) LineWidth(row int) int {
	if row < 1 || row > s.height {
		return 0
	}
	last := 0
	for index, cell := range s.rows[row-1] {
		if !cell.blank() {
			last = index + 1
		}
	}
	return last
}

// Blank reports whether the row has no visible content.
func (s *Screen) Blank(row int) bool {
	return strings.TrimSpace(s.Line(row)) == ""
}

// Lines returns rows [from, to] (1-based, inclusive) as visible text.
func (s *Screen) Lines(from, to int) []string {
	if from < 1 {
		from = 1
	}
	if to > s.height {
		to = s.height
	}
	if from > to {
		return nil
	}
	out := make([]string, 0, to-from+1)
	for row := from; row <= to; row++ {
		out = append(out, s.Line(row))
	}
	return out
}

// Dump renders the whole screen with 1-based row numbers for failure messages.
func (s *Screen) Dump() string {
	var b strings.Builder
	for row := 1; row <= s.height; row++ {
		fmt.Fprintf(&b, "%02d|%s\n", row, s.Line(row))
	}
	return b.String()
}

// CellAt returns the cell at a 1-based position.
func (s *Screen) CellAt(row, col int) Cell {
	if row < 1 || row > s.height || col < 1 || col > s.width {
		return Cell{}
	}
	return s.rows[row-1][col-1]
}

// CellRows returns a deep copy of rows [from, to] (1-based, inclusive).
//
// The copy is suitable for handing reconstructed terminal output to the owned
// viewport composer: callers cannot mutate the screen through either the cells
// or their SGR slices.
func (s *Screen) CellRows(from, to int) [][]Cell {
	if from < 1 {
		from = 1
	}
	if to > s.height {
		to = s.height
	}
	if from > to {
		return nil
	}
	out := make([][]Cell, 0, to-from+1)
	for row := from; row <= to; row++ {
		cells := make([]Cell, s.width)
		for col, cell := range s.rows[row-1] {
			cells[col] = cell
			if len(cell.SGR) > 0 {
				cells[col].SGR = append([]string(nil), cell.SGR...)
			}
		}
		out = append(out, cells)
	}
	return out
}

// RowSGRCodes returns the union of graphic rendition codes on the row's visible
// cells. Style regressions (a dim holdback losing its attribute, a status
// segment losing its color) are only observable through this.
func (s *Screen) RowSGRCodes(row int) map[string]bool {
	codes := map[string]bool{}
	if row < 1 || row > s.height {
		return codes
	}
	for _, cell := range s.rows[row-1] {
		if cell.blank() && !cell.Cont {
			continue
		}
		for _, code := range cell.SGR {
			codes[code] = true
		}
	}
	return codes
}

// RowsContaining reports every 1-based row whose visible text contains marker.
func (s *Screen) RowsContaining(marker string) []int {
	var rows []int
	for row := 1; row <= s.height; row++ {
		if strings.Contains(s.Line(row), marker) {
			rows = append(rows, row)
		}
	}
	return rows
}

// LastNonBlankRowAbove returns the closest non-blank row above bottomExclusive,
// or 0 when there is none.
func (s *Screen) LastNonBlankRowAbove(bottomExclusive int) int {
	if bottomExclusive > s.height+1 {
		bottomExclusive = s.height + 1
	}
	for row := bottomExclusive - 1; row >= 1; row-- {
		if !s.Blank(row) {
			return row
		}
	}
	return 0
}

// MaxBlankRun returns the largest run of consecutive blank rows in
// [1, bottomExclusive) that sits below at least one content row, plus its start.
// Leading blank rows above the first content row are terminal head room, not a
// layout hole, so they are ignored.
func (s *Screen) MaxBlankRun(bottomExclusive int) (maxRun, startRow int) {
	if bottomExclusive > s.height+1 {
		bottomExclusive = s.height + 1
	}
	run, runStart, seenContent := 0, 0, false
	for row := 1; row < bottomExclusive; row++ {
		if s.Blank(row) {
			if !seenContent {
				continue
			}
			if run == 0 {
				runStart = row
			}
			run++
			if run > maxRun {
				maxRun, startRow = run, runStart
			}
			continue
		}
		seenContent = true
		run = 0
	}
	return maxRun, startRow
}

// OverflowRows returns the rows whose content exceeded the screen width. A
// non-empty result means the surface emitted a row a real terminal would have
// wrapped, which shifts every row below it.
func (s *Screen) OverflowRows() []int {
	var rows []int
	for row := 1; row <= s.height; row++ {
		if s.LineWidth(row) > s.width {
			rows = append(rows, row)
		}
	}
	return rows
}
