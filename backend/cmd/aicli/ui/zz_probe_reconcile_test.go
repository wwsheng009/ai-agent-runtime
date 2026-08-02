package ui

// Temporary probe: observe Reconcile frame semantics with the new
// content-addressed counter. DO NOT COMMIT.

import (
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
)

func TestProbeReconcileFrames(t *testing.T) {
	engine := renderengine.NewEngine()
	defer engine.Shutdown()
	surface := newOwnedTestFixedBottomSurfaceWithSize(80, 24)
	surface.SetEngine(engine)
	surface.SetPaintTraceEnabled(true)

	captureUIStdout(t, func() {
		for i := 0; i < 5; i++ {
			surface.WriteOutput(io.Discard, fmt.Sprintf("line-%d\n", i))
		}
	})
	engine.Trace().Reset()
	dump := func(step string) {
		t.Helper()
		lf := engine.Trace().LastFrame()
		t.Logf("-- %s: frame=%d white=%v missing=%v totalWhite=%d", step, lf.Frame, lf.White, lf.Missing, lf.TotalWhite)
		for _, row := range surface.ComposedFrameForTest() {
			text := rowText(row)
			if strings.Contains(text, "line-") {
				t.Logf("   %q", text[:min(30, len(text))])
			}
		}
	}
	dump("after reset")

	captureUIStdout(t, func() { surface.Reconcile() })
	dump("after reconcile 1")

	captureUIStdout(t, func() { surface.Reconcile() })
	dump("after reconcile 2")

	// Check byHash values through the tag w counters.
	stats := engine.Trace().Stats()
	totalWhite := uint64(0)
	for _, s := range stats {
		totalWhite += s.WhiteEmits
	}
	t.Logf("total WhiteEmits (row-based) = %d", totalWhite)
}

// TestProbe30Rows checks whether the 30-row write (scroll) produces white
// repaints before Reset, which determines whether the first Reconcile after
// Reset is a resync frame or a white frame.
func TestProbe30Rows(t *testing.T) {
	engine := renderengine.NewEngine()
	defer engine.Shutdown()
	surface := newOwnedTestFixedBottomSurfaceWithSize(80, 24)
	surface.SetEngine(engine)
	surface.SetPaintTraceEnabled(true)

	captureUIStdout(t, func() {
		for i := 0; i < 30; i++ {
			surface.WriteOutput(io.Discard, fmt.Sprintf("line-%d\n", i))
		}
	})
	lf := engine.Trace().LastFrame()
	t.Logf("before reset: frame=%d white=%v totalWhite=%d", lf.Frame, lf.White, lf.TotalWhite)
	for _, row := range surface.ComposedFrameForTest() {
		text := rowText(row)
		if strings.Contains(text, "line-") {
			t.Logf("   %q", text[:min(30, len(text))])
		}
	}
	engine.Trace().Reset()

	captureUIStdout(t, func() { surface.Reconcile() })
	lf = engine.Trace().LastFrame()
	t.Logf("after reconcile 1: frame=%d white=%v totalWhite=%d", lf.Frame, lf.White, lf.TotalWhite)
	for _, row := range surface.ComposedFrameForTest() {
		text := rowText(row)
		if strings.Contains(text, "line-") {
			t.Logf("   %q", text[:min(30, len(text))])
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
