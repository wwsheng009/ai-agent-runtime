package vt

import "testing"

func TestScreenWideRunesUseTwoColumns(t *testing.T) {
	s := NewScreen(10, 3)
	s.Feed("中文abc")
	if got, want := s.Line(1), "中文abc"; got != want {
		t.Fatalf("line=%q want %q\n%s", got, want, s.Dump())
	}
	if got, want := s.LineWidth(1), 7; got != want {
		t.Fatalf("line width=%d want %d\n%s", got, want, s.Dump())
	}
	if cell := s.CellAt(1, 2); !cell.Cont {
		t.Fatalf("second column should be the continuation of a wide rune: %+v", cell)
	}
}

func TestScreenWideRuneWrapsAsOneUnit(t *testing.T) {
	// Width 5 leaves one free column after "abcd", which cannot hold a wide
	// rune: a real terminal moves the whole rune to the next row.
	s := NewScreen(5, 3)
	s.Feed("abcd中")
	if got, want := s.Line(1), "abcd"; got != want {
		t.Fatalf("row1=%q want %q\n%s", got, want, s.Dump())
	}
	if got, want := s.Line(2), "中"; got != want {
		t.Fatalf("row2=%q want %q\n%s", got, want, s.Dump())
	}
}

func TestScreenDeferredWrapMatchesTerminal(t *testing.T) {
	s := NewScreen(4, 3)
	// Filling the last column must not advance the row on its own; a CR
	// cancels the pending wrap exactly like a terminal.
	s.Feed("abcd\rXY")
	if got, want := s.Line(1), "XYcd"; got != want {
		t.Fatalf("row1=%q want %q\n%s", got, want, s.Dump())
	}
	if !s.Blank(2) {
		t.Fatalf("pending wrap must not consume row 2\n%s", s.Dump())
	}
	// Without the CR the next printable rune wraps.
	s2 := NewScreen(4, 3)
	s2.Feed("abcdZ")
	if got, want := s2.Line(2), "Z"; got != want {
		t.Fatalf("row2=%q want %q\n%s", got, want, s2.Dump())
	}
}

func TestScreenZeroWidthRunesAttachToPreviousCell(t *testing.T) {
	s := NewScreen(6, 2)
	s.Feed("e\u0301x")
	if got, want := s.Line(1), "e\u0301x"; got != want {
		t.Fatalf("line=%q want %q", got, want)
	}
	if got, want := s.LineWidth(1), 2; got != want {
		t.Fatalf("combining mark consumed a column: width=%d want %d", got, want)
	}
}

func TestScreenTracksSGRPerCell(t *testing.T) {
	s := NewScreen(20, 2)
	s.Feed("\x1b[2mdim\x1b[0m plain\x1b[1;38;5;12mbold")
	if cell := s.CellAt(1, 1); len(cell.SGR) != 1 || cell.SGR[0] != "2" {
		t.Fatalf("dim cell lost its attribute: %+v", cell)
	}
	if cell := s.CellAt(1, 5); len(cell.SGR) != 0 {
		t.Fatalf("SGR reset must clear attributes: %+v", cell)
	}
	codes := s.RowSGRCodes(1)
	if !codes["2"] || !codes["1"] || !codes["38;5;12"] {
		t.Fatalf("row codes missing entries: %v", codes)
	}
	if got, want := s.Line(1), "dim plainbold"; got != want {
		t.Fatalf("SGR must not consume columns: %q want %q", got, want)
	}
}

func TestScreenCellRowsReturnsDeepCopy(t *testing.T) {
	s := NewScreen(4, 2)
	s.Feed("\x1b[2m中x")

	rows := s.CellRows(1, 2)
	if got, want := len(rows), 2; got != want {
		t.Fatalf("rows=%d want %d", got, want)
	}
	if got, want := len(rows[0]), 4; got != want {
		t.Fatalf("columns=%d want %d", got, want)
	}
	if rows[0][0].Text != "中" || !rows[0][1].Cont || rows[0][2].Text != "x" {
		t.Fatalf("wide cells not preserved: %+v", rows[0])
	}
	rows[0][0].Text = "changed"
	rows[0][0].SGR[0] = "1"
	if cell := s.CellAt(1, 1); cell.Text != "中" || len(cell.SGR) != 1 || cell.SGR[0] != "2" {
		t.Fatalf("CellRows exposed screen storage: %+v", cell)
	}
}

func TestScreenSGRResetVariantsAndAttributeDrops(t *testing.T) {
	s := NewScreen(20, 2)
	s.Feed("\x1b[1;2mx\x1b[22my\x1b[31;39mz")
	if cell := s.CellAt(1, 1); len(cell.SGR) != 2 {
		t.Fatalf("expected bold+dim: %+v", cell)
	}
	if cell := s.CellAt(1, 2); len(cell.SGR) != 0 {
		t.Fatalf("SGR 22 must drop bold and dim: %+v", cell)
	}
	if cell := s.CellAt(1, 3); len(cell.SGR) != 0 {
		t.Fatalf("SGR 39 must drop the foreground: %+v", cell)
	}
}

func TestScreenEraseClearsWideNeighbor(t *testing.T) {
	s := NewScreen(6, 2)
	s.Feed("中文")
	// Erase from column 2, i.e. the continuation cell of the first rune.
	s.Feed("\x1b[1;2H\x1b[K")
	if got := s.Line(1); got != "" {
		t.Fatalf("erasing half a wide rune must clear the whole rune, got %q", got)
	}
}

func TestScreenScrollRegionKeepsRowsOutsideRegion(t *testing.T) {
	s := NewScreen(8, 4)
	s.Feed("\x1b[4;4Hbot")
	s.Feed("\x1b[1;3r\x1b[3;1Hone\ntwo\nthree")
	if got, want := s.Line(4), "   bot"; got != want {
		t.Fatalf("row outside the scroll region must not scroll: %q\n%s", got, s.Dump())
	}
	if got, want := s.Line(3), "three"; got != want {
		t.Fatalf("region bottom=%q want %q\n%s", got, want, s.Dump())
	}
	if got, want := s.Line(2), "two"; got != want {
		t.Fatalf("region row2=%q want %q\n%s", got, want, s.Dump())
	}
}

func TestScreenCursorForwardClampsToWidth(t *testing.T) {
	s := NewScreen(5, 2)
	s.Feed("\x1b[99CX")
	if got, want := s.Line(1), "    X"; got != want {
		t.Fatalf("CUF must clamp to the last column: %q want %q", got, want)
	}
	if rows := s.OverflowRows(); len(rows) != 0 {
		t.Fatalf("unexpected overflow rows %v\n%s", rows, s.Dump())
	}
}

func TestScreenInsertAndDeleteLines(t *testing.T) {
	s := NewScreen(6, 4)
	s.Feed("a\nb\nc")
	s.Feed("\x1b[2;1H\x1b[L")
	if !s.Blank(2) || s.Line(3) != "b" {
		t.Fatalf("CSI L must push rows down\n%s", s.Dump())
	}
	s.Feed("\x1b[2;1H\x1b[M")
	if s.Line(2) != "b" {
		t.Fatalf("CSI M must pull rows up\n%s", s.Dump())
	}
}

func TestScreenMaxBlankRunIgnoresHeadRoom(t *testing.T) {
	s := NewScreen(6, 6)
	s.Feed("\x1b[3;1Htop\n\n\nlow")
	run, at := s.MaxBlankRun(7)
	if run != 2 || at != 4 {
		t.Fatalf("blank run=%d at=%d want 2 at 4\n%s", run, at, s.Dump())
	}
	if got := s.LastNonBlankRowAbove(7); got != 6 {
		t.Fatalf("last non-blank row=%d want 6\n%s", got, s.Dump())
	}
}
