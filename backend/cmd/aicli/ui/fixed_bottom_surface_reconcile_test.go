package ui

import (
	"io"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
)

// ownedFramesTextEqual compares two cell frames by glyph layout (Text and
// continuation flags), ignoring SGR style runs. Both frames come from the
// same composition source, so glyph equality proves the physical terminal
// (rebuilt from the emitted diff) converged on the composed scene.
func ownedFramesTextEqual(a, b [][]vt.Cell) bool {
	if len(a) != len(b) {
		return false
	}
	for r := 0; r < len(a); r++ {
		ra, rb := a[r], b[r]
		if len(ra) != len(rb) {
			return false
		}
		for c := 0; c < len(ra); c++ {
			if ra[c].Text != rb[c].Text || ra[c].Cont != rb[c].Cont {
				return false
			}
		}
	}
	return true
}

// ownedFrameDump renders a frame as text for failure diagnostics.
func ownedFrameDump(rows [][]vt.Cell) string {
	var sb strings.Builder
	for _, row := range rows {
		for _, cell := range row {
			if cell.Cont {
				continue
			}
			if cell.Text == "" {
				sb.WriteByte(' ')
			} else {
				sb.WriteString(cell.Text)
			}
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

// TestOwnedViewport_ReconcileConvergesAfterContentEvents drives a sequence of
// layout-changing events (history growth, ActiveBand grow/shrink, popup
// show/clear, status model change) and asserts that after the final
// reconcile the physical terminal rebuilt from every emitted byte equals the
// composed scene — the reconciliation property that replaces the legacy
// compensation state machine.
func TestOwnedViewport_ReconcileConvergesAfterContentEvents(t *testing.T) {
	const w, h = 30, 10
	surface := newOwnedTestFixedBottomSurfaceWithSize(w, h)

	output := captureUIStdout(t, func() {
		surface.WriteOutput(io.Discard, "alpha\nbeta\ngamma\n")
		surface.Reconcile()

		surface.SetActiveBand([]string{"band-a", "band-b"})
		surface.Reconcile()

		surface.WriteOutput(io.Discard, "delta\nepsilon\n")
		surface.Reconcile()

		surface.ClearActiveBand()
		surface.Reconcile()

		surface.ShowPopup([]string{"popup-line"})
		surface.Reconcile()

		surface.ClearPopup()
		surface.Reconcile()

		surface.SetStatusModel(style.StatusLineModel{
			State:     style.RunReady,
			Separator: " | ",
			Segments:  []style.StatusSegment{{Text: "Ready"}},
		})
		surface.Reconcile()
	})

	screen := vt.NewScreen(w, h)
	screen.Feed(output)
	want := surface.ComposedFrameForTest()
	got := screen.CellRows(1, h)
	if !ownedFramesTextEqual(want, got) {
		t.Fatalf("screen did not converge on composed scene:\n--- want ---\n%s--- got ---\n%s",
			ownedFrameDump(want), ownedFrameDump(got))
	}
}

// TestOwnedViewport_ReconcileConvergesAfterResize drives a geometry change
// and asserts a full-frame reconcile repaints the new size from the composed
// scene (the resize/blank-hole defect class).
func TestOwnedViewport_ReconcileConvergesAfterResize(t *testing.T) {
	const w, h = 30, 10
	surface := newOwnedTestFixedBottomSurfaceWithSize(w, h)

	output := captureUIStdout(t, func() {
		surface.WriteOutput(io.Discard, "alpha\nbeta\ngamma\ndelta\nepsilon\nzeta\n")
		surface.SetActiveBand([]string{"band"})
		surface.Reconcile()

		surface.terminal.SetSizeForTest(40, 14)
		surface.Reconcile()

		surface.WriteOutput(io.Discard, "eta\n")
		surface.Reconcile()
	})

	// The whole stream is absolute-positioned, so a screen at the final size
	// converges on the final composed frame regardless of earlier sizes.
	const w2, h2 = 40, 14
	screen := vt.NewScreen(w2, h2)
	screen.Feed(output)
	want := surface.ComposedFrameForTest()
	got := screen.CellRows(1, h2)
	if !ownedFramesTextEqual(want, got) {
		t.Fatalf("screen did not converge on composed scene after resize:\n--- want ---\n%s--- got ---\n%s",
			ownedFrameDump(want), ownedFrameDump(got))
	}
}

// TestOwnedViewport_ReconcileIsIdempotent asserts a second reconcile after
// convergence still reproduces the composed scene (no drift accumulates).
func TestOwnedViewport_ReconcileIsIdempotent(t *testing.T) {
	const w, h = 30, 10
	surface := newOwnedTestFixedBottomSurfaceWithSize(w, h)

	first := captureUIStdout(t, func() {
		surface.WriteOutput(io.Discard, "one\ntwo\nthree\n")
		surface.SetActiveBand([]string{"band"})
		surface.ShowPopup([]string{"popup"})
		surface.Reconcile()
	})
	second := captureUIStdout(t, func() {
		surface.Reconcile()
	})
	third := captureUIStdout(t, func() {
		surface.Reconcile()
	})

	screen := vt.NewScreen(w, h)
	screen.Feed(first)
	screen.Feed(second)
	screen.Feed(third)
	want := surface.ComposedFrameForTest()
	got := screen.CellRows(1, h)
	if !ownedFramesTextEqual(want, got) {
		t.Fatalf("reconcile drifted across repeated calls:\n--- want ---\n%s--- got ---\n%s",
			ownedFrameDump(want), ownedFrameDump(got))
	}
}
