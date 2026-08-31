package renderengine

import "testing"

func TestLegacyReserveANSIPlansPreserveDECSTBMOrdering(t *testing.T) {
	if got, want := LegacyReserveScrollUpANSI(24, 3, 6), "\x1b[1;21r\x1b[1;1H\x1b[21;1H\n\n\n"; got != want {
		t.Fatalf("scroll-up ANSI = %q, want %q", got, want)
	}
	if got, want := LegacyReserveScrollDownANSI(24, 3, 3), "\x1b[1;21r\x1b[1;1H\x1b[1;1H\x1b[3T"; got != want {
		t.Fatalf("scroll-down ANSI = %q, want %q", got, want)
	}
	if got, want := LegacyReserveDebtANSI(24, 3, 2), "\x1b[21;1H\n\n"; got != want {
		t.Fatalf("debt ANSI = %q, want %q", got, want)
	}
}

func TestLegacyReserveANSIPlansClampInvalidGeometry(t *testing.T) {
	if got := LegacyReserveScrollUpANSI(1, 1, 2); got != "" {
		t.Fatalf("short terminal scroll-up = %q, want empty", got)
	}
	if got := LegacyReserveScrollDownANSI(24, 99, 99); got != "\x1b[1;1r\x1b[1;1H\x1b[1;1H\x1b[1T" {
		t.Fatalf("clamped scroll-down = %q", got)
	}
	if got := LegacyReserveDebtANSI(24, 99, 99); got != "\x1b[1;1H\n" {
		t.Fatalf("clamped debt = %q", got)
	}
}

func TestLegacyReserveStateGrowthCancelsPendingAndAbsorbsBlank(t *testing.T) {
	state := LegacyReserveState{
		ScrollCompensatedRows: 3,
		PendingScrollDownRows: 2,
		CursorOnBlankRow:      true,
	}
	transition := state.ApplyGeometry(80, 24, 6, 80, 24, 3)
	if transition.ScrollUpOldBottomRows != 0 || transition.ScrollUpNewBottomRows != 0 {
		t.Fatalf("absorbed growth should not emit scroll-up: %#v", transition)
	}
	if state.PendingScrollDownRows != 0 {
		t.Fatalf("pending rows = %d, want 0", state.PendingScrollDownRows)
	}
	if state.OutputScrollDebtRows != 1 || state.CursorOnBlankRow {
		t.Fatalf("state after blank absorption = %#v", state)
	}
	if state.ScrollCompensatedRows != 6 {
		t.Fatalf("compensated rows = %d, want 6", state.ScrollCompensatedRows)
	}
}

func TestLegacyReserveStateGrowthReturnsScrollPlan(t *testing.T) {
	state := LegacyReserveState{ScrollCompensatedRows: 3}
	transition := state.ApplyGeometry(80, 24, 6, 80, 24, 3)
	if transition.ScrollUpOldBottomRows != 3 || transition.ScrollUpNewBottomRows != 6 {
		t.Fatalf("transition = %#v, want old=3 new=6", transition)
	}
}

func TestLegacyReserveStateShrinkDefersScrollDown(t *testing.T) {
	state := LegacyReserveState{ScrollCompensatedRows: 6}
	transition := state.ApplyGeometry(80, 24, 3, 80, 24, 6)
	if transition != (LegacyReserveTransition{}) {
		t.Fatalf("shrink must not emit immediate scroll-up: %#v", transition)
	}
	if state.PendingScrollDownRows != 3 || state.ScrollCompensatedRows != 3 {
		t.Fatalf("state after shrink = %#v", state)
	}
}

func TestLegacyReserveStateResizeResetsDeferredState(t *testing.T) {
	state := LegacyReserveState{
		ScrollCompensatedRows: 6,
		PendingScrollDownRows: 3,
		OutputScrollDebtRows:  2,
		CursorOnBlankRow:      true,
	}
	state.ApplyGeometry(100, 30, 5, 80, 24, 6)
	if state.PendingScrollDownRows != 0 || state.OutputScrollDebtRows != 0 || state.CursorOnBlankRow {
		t.Fatalf("resize did not reset deferred state: %#v", state)
	}
	if state.ScrollCompensatedRows != 5 {
		t.Fatalf("compensated rows = %d, want 5", state.ScrollCompensatedRows)
	}
}
