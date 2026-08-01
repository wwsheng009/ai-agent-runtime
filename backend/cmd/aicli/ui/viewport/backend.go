// Package viewport implements the P5 owned-viewport double buffer.
//
// A Backend owns a back buffer (the desired frame) and a front buffer (what the
// terminal currently shows). Callers stage cells into the back buffer, then
// Flush() diffs front->back and returns the minimal ANSI needed to transform the
// terminal from the front frame to the back frame, swapping front=back. The
// Backend is render-plane only: it never interprets content, only cells.
//
// P5.2 status: WIRED. FixedBottomSurface composes each owned frame with
// Compose and reconciles it through this Backend (renderOwnedViewportLocked);
// the surface reconciles (full repaint) on resize, lease release, popup close,
// and the public Reconcile() hook. Tests round-trip the diff through the
// ui/vt screen model (feed the front frame, then the diff, and assert the
// screen equals the target frame).
package viewport

import (
	"strconv"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
)

const sgrReset = "\x1b[0m"

// Backend is a double-buffered cell grid for one managed region.
type Backend struct {
	width, height int
	front, back   [][]vt.Cell
	forceRepaint  bool
}

// New builds a Backend with blank front and back buffers.
func New(width, height int) *Backend {
	width, height = clampSize(width, height)
	return &Backend{
		width:  width,
		height: height,
		front:  blankGrid(width, height),
		back:   blankGrid(width, height),
	}
}

// Size reports the current width and height in cells/rows.
func (b *Backend) Size() (int, int) { return b.width, b.height }

// Resize reallocates both buffers to (width,height) and clears them. Content is
// not preserved (resize-preserving reflow is P5.5); front is cleared too so the
// next Flush repaints the whole frame from blank.
func (b *Backend) Resize(width, height int) {
	width, height = clampSize(width, height)
	b.width, b.height = width, height
	b.front = blankGrid(width, height)
	b.back = blankGrid(width, height)
	b.forceRepaint = true
}

// Invalidate forgets the physical terminal contents without discarding the
// staged frame. The next Flush clears and repaints every row, including rows
// whose target is blank. Surfaces use this after a geometry transition whose
// host-side scroll cannot be represented by front.
func (b *Backend) Invalidate() {
	if b == nil {
		return
	}
	b.forceRepaint = true
}

// StageFrame overwrites the entire back buffer. Extra rows are ignored; missing
// rows/cells are padded with blanks.
func (b *Backend) StageFrame(rows [][]vt.Cell) {
	for r := 0; r < b.height; r++ {
		var src []vt.Cell
		if r < len(rows) {
			src = rows[r]
		}
		b.back[r] = normalizeRow(src, b.width)
	}
}

// StageRow overwrites one 1-based back-buffer row.
func (b *Backend) StageRow(row int, cells []vt.Cell) {
	if row < 1 || row > b.height {
		return
	}
	b.back[row-1] = normalizeRow(cells, b.width)
}

// Flush diffs front->back, returns the ANSI needed to apply the change, and
// swaps front=back. When nothing changed it returns "".
func (b *Backend) Flush() string {
	var sb strings.Builder
	if b.forceRepaint {
		for r := 0; r < b.height; r++ {
			b.emitForcedRow(&sb, r)
		}
	} else {
		for r := 0; r < b.height; r++ {
			b.diffRow(&sb, r)
		}
	}
	for r := 0; r < b.height; r++ {
		copy(b.front[r], b.back[r])
	}
	b.forceRepaint = false
	return sb.String()
}

func (b *Backend) emitForcedRow(sb *strings.Builder, r int) {
	if sb == nil || r < 0 || r >= b.height {
		return
	}
	// EL gives the VT model canonical empty cells instead of explicit space
	// glyphs and guarantees stale content to the right of the new row is gone.
	sb.WriteString("\x1b[")
	sb.WriteString(strconv.Itoa(r + 1))
	sb.WriteString(";1H")
	sb.WriteString(sgrReset)
	sb.WriteString("\x1b[K")

	hi := -1
	for c, cell := range b.back[r] {
		if cell.Text != "" || cell.Cont || len(cell.SGR) > 0 {
			hi = c
		}
	}
	if hi >= 0 {
		b.emitRowRangeCoalesced(sb, r, 0, hi)
	}
}

// diffRow appends the minimal ANSI to reconcile row r (0-based) into sb.
func (b *Backend) diffRow(sb *strings.Builder, r int) {
	front, back := b.front[r], b.back[r]
	lo, hi := -1, -1
	for c := 0; c < b.width; c++ {
		if !cellEqual(front[c], back[c]) {
			if lo == -1 {
				lo = c
			}
			hi = c
		}
	}
	if lo == -1 {
		return
	}
	// Never begin emission on a continuation cell: back up to its lead so the
	// wide rune is re-emitted whole.
	for lo > 0 && back[lo].Cont {
		lo--
	}
	// Blanking a previously occupied cell cannot use a space glyph: that would
	// leave Text=" " on the host/vt model while Compose keeps Text="". When any
	// dirty cell must become a true blank, repaint the whole row with EL so the
	// physical cells match the owned frame exactly (band/popup shrink path).
	for c := lo; c <= hi; c++ {
		if cellBlank(back[c]) && !cellBlank(front[c]) {
			b.emitForcedRow(sb, r)
			return
		}
	}
	b.emitRowRangeCoalesced(sb, r, lo, hi)
}

func cellBlank(c vt.Cell) bool {
	return c.Text == "" && !c.Cont && len(c.SGR) == 0
}

// emitRowRangeCoalesced keeps one SGR run active across adjacent cells, so
// plain text is emitted as a contiguous substring instead of reset-wrapped
// rune by rune. Both forced and minimal-diff paints use this representation.
func (b *Backend) emitRowRangeCoalesced(sb *strings.Builder, r, lo, hi int) {
	if sb == nil || r < 0 || r >= b.height || lo < 0 || hi < lo || hi >= b.width {
		return
	}
	back := b.back[r]
	sb.WriteString("\x1b[")
	sb.WriteString(strconv.Itoa(r + 1))
	sb.WriteByte(';')
	sb.WriteString(strconv.Itoa(lo + 1))
	sb.WriteByte('H')
	var activeSGR []string
	haveActiveSGR := false
	for c := lo; c <= hi; c++ {
		cell := back[c]
		if cell.Cont {
			continue
		}
		if !haveActiveSGR || !sgrEqual(activeSGR, cell.SGR) {
			sb.WriteString(sgrReset)
			if len(cell.SGR) > 0 {
				sb.WriteString("\x1b[")
				sb.WriteString(strings.Join(cell.SGR, ";"))
				sb.WriteByte('m')
			}
			activeSGR = cell.SGR
			haveActiveSGR = true
		}
		if cell.Text == "" {
			sb.WriteByte(' ')
		} else {
			sb.WriteString(cell.Text)
		}
	}
	sb.WriteString(sgrReset)
}

func sgrEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func cellEqual(a, b vt.Cell) bool {
	if a.Text != b.Text || a.Cont != b.Cont {
		return false
	}
	if len(a.SGR) != len(b.SGR) {
		return false
	}
	for i := range a.SGR {
		if a.SGR[i] != b.SGR[i] {
			return false
		}
	}
	return true
}

func normalizeRow(src []vt.Cell, width int) []vt.Cell {
	row := make([]vt.Cell, width)
	for c := 0; c < width && c < len(src); c++ {
		row[c] = src[c]
	}
	return row
}

func blankGrid(width, height int) [][]vt.Cell {
	g := make([][]vt.Cell, height)
	for i := range g {
		g[i] = make([]vt.Cell, width)
	}
	return g
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
