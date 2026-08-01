package ui

import (
	"io"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/viewport"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
)

// findRowWithText returns the 1-based physical row whose plain text contains
// needle, or -1. It is used to bind component content to the owner table.
func findRowWithText(t *testing.T, frame [][]vt.Cell, needle string) int {
	t.Helper()
	for i, cells := range frame {
		if strings.Contains(cellRowPlainText(cells), needle) {
			return i + 1
		}
	}
	return -1
}

func validRowOwner(o viewport.RowOwner) bool {
	return o >= viewport.RowOwnerGap && o <= viewport.RowOwnerStatus
}

// TestOwnedRowPlanCoversEveryRowWithoutUndeclaredRows pins the stage C layout
// invariant: the composed plan spans exactly the screen height and every
// physical row carries a declared owner (Gap is a declared owner; an
// unannotated row is a defect). With no components active, the status row owns
// the last row and everything else is Gap.
func TestOwnedRowPlanCoversEveryRowWithoutUndeclaredRows(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	const width, height = 80, 24
	surface := newOwnedTestFixedBottomSurfaceWithSize(width, height)

	owners := surface.RowOwnersForTest()
	if len(owners) != height {
		t.Fatalf("owner table rows=%d want %d", len(owners), height)
	}
	for i, owner := range owners {
		if !validRowOwner(owner) {
			t.Fatalf("row %d has undeclared owner %v", i+1, owner)
		}
	}
	if got := owners[height-1]; got != viewport.RowOwnerStatus {
		t.Fatalf("status row owner=%v want status", got)
	}
	for i := 0; i < height-1; i++ {
		if owners[i] != viewport.RowOwnerGap {
			t.Fatalf("idle row %d owner=%v want gap", i+1, owners[i])
		}
	}
	if frame := surface.ComposedFrameForTest(); len(frame) != height {
		t.Fatalf("composed frame rows=%d want %d", len(frame), height)
	}
}

// TestOwnedRowPlanContentRowsHaveOwners activates every bottom component at
// once on a tall terminal and asserts: (a) every non-empty row is owned by a
// component (never Gap), and (b) each component's content maps to its declared
// owner.
func TestOwnedRowPlanContentRowsHaveOwners(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	const width, height = 80, 60
	surface := newOwnedTestFixedBottomSurfaceWithSize(width, height)

	captureUIStdout(t, func() {
		if !surface.ShowPrompt("> ") {
			t.Fatal("expected prompt to render")
		}
		if !surface.SetPromptNoticeLine("notice: n1") {
			t.Fatal("expected notice to render")
		}
		if !surface.SetActiveBand([]string{"B1", "B2"}) {
			t.Fatal("expected band to render")
		}
		surface.ShowPopup([]string{"P1", "P2"})
		surface.SetStatusModels(
			style.StatusLineModel{State: style.RunReady, StateText: "Plan OFF"},
			&style.StatusLineModel{State: style.RunThinking, StateText: "◦ Analyzing"},
		)
		if !surface.SetPromptInputState("> ", "hello", 1, 0, 1) {
			t.Fatal("expected prompt input state to be accepted")
		}
	})

	frame := surface.ComposedFrameForTest()
	owners := surface.RowOwnersForTest()
	if len(frame) != height || len(owners) != height {
		t.Fatalf("frame=%d owners=%d want both %d", len(frame), len(owners), height)
	}

	// Invariant: any row with visible content must have a component owner.
	for i, cells := range frame {
		if strings.TrimSpace(cellRowPlainText(cells)) == "" {
			continue
		}
		if owners[i] == viewport.RowOwnerGap {
			t.Fatalf("row %d has content %q but owner gap", i+1, strings.TrimSpace(cellRowPlainText(cells)))
		}
	}

	assertOwner := func(needle string, want viewport.RowOwner) {
		t.Helper()
		row := findRowWithText(t, frame, needle)
		if row < 0 {
			t.Fatalf("content %q not found in frame:\n%s", needle, frameDump(frame))
		}
		if got := owners[row-1]; got != want {
			t.Fatalf("row %d (%q) owner=%v want %v", row, needle, got, want)
		}
	}
	assertOwner("B1", viewport.RowOwnerBand)
	assertOwner("B2", viewport.RowOwnerBand)
	assertOwner("P1", viewport.RowOwnerPopup)
	assertOwner("P2", viewport.RowOwnerPopup)
	assertOwner("notice: n1", viewport.RowOwnerPrompt)
	assertOwner(">", viewport.RowOwnerPrompt)
	assertOwner("hello", viewport.RowOwnerPrompt)
	assertOwner("◦ Analyzing", viewport.RowOwnerStatus)
	assertOwner("Plan OFF", viewport.RowOwnerStatus)
}

// TestOwnedRowPlanBandGapRowsAreOwned pins that band margin/gap rows inside
// the prompt area are declared Gap rather than left undeclared.
func TestOwnedRowPlanBandGapRowsAreOwned(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	const width, height = 80, 24
	surface := newOwnedTestFixedBottomSurfaceWithSize(width, height)

	captureUIStdout(t, func() {
		if !surface.SetActiveBand([]string{"B1", "B2", "B3"}) {
			t.Fatal("expected band to render")
		}
	})

	owners := surface.RowOwnersForTest()
	bandRows := 0
	gapRows := 0
	for i, owner := range owners {
		if !validRowOwner(owner) {
			t.Fatalf("row %d has undeclared owner %v", i+1, owner)
		}
		switch owner {
		case viewport.RowOwnerBand:
			bandRows++
		case viewport.RowOwnerGap:
			gapRows++
		case viewport.RowOwnerStatus:
			if i != height-1 {
				t.Fatalf("status owner on non-terminal row %d", i+1)
			}
		}
	}
	if bandRows != 3 {
		t.Fatalf("band rows=%d want 3", bandRows)
	}
	if gapRows == 0 {
		t.Fatal("expected declared gap rows above band")
	}
}

// TestOwnedRowPlanDebugString renders the owner table for /debug display.
func TestOwnedRowPlanDebugString(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	const width, height = 80, 24
	surface := newOwnedTestFixedBottomSurfaceWithSize(width, height)
	captureUIStdout(t, func() {
		surface.WriteOutput(io.Discard, "line1\n")
		surface.SetActiveBand([]string{"B1"})
		surface.ShowPopup([]string{"P1"})
	})

	got := surface.RowPlanDebugString()
	if got == "" {
		t.Fatal("expected non-empty owner table")
	}
	if !strings.Contains(got, "Row Ownership (80x24)") {
		t.Fatalf("missing header: %q", got)
	}
	for _, name := range []string{"transcript", "band", "popup", "status", "gap"} {
		if !strings.Contains(got, name) {
			t.Fatalf("owner table missing %q:\n%s", name, got)
		}
	}
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) != height+1 { // header + one line per physical row
		t.Fatalf("owner table lines=%d want %d", len(lines), height+1)
	}
}
