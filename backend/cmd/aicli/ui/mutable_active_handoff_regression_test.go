package ui

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// A mutable response may outgrow the active viewport before its final event
// arrives. Its stable prefix must already have crossed the physical writer;
// keeping only the viewport tail makes those earlier rows unreachable until
// finalization and loses them entirely if the stream is interrupted.
func TestMutableActiveOverflowWritesStablePrefixBeforeFinalize(t *testing.T) {
	const (
		earlyMarker  = "MUTABLE-EARLY-000"
		latestMarker = "MUTABLE-LATEST-029"
	)

	lines := make([]string, 30)
	for index := range lines {
		lines[index] = fmt.Sprintf("mutable-row-%03d", index)
	}
	lines[0] = earlyMarker
	lines[len(lines)-1] = latestMarker
	source := strings.Join(lines, "\n")

	controller := NewUIController(UIControllerConfig{}, nil, nil)
	go controller.Run()
	t.Cleanup(func() {
		controller.Close()
		controller.WaitIdle()
	})

	var physical bytes.Buffer
	executor := NewTerminalSessionExecutor(controller, NewTerminalSession(&physical))
	t.Cleanup(executor.Close)

	if !controller.Post(Resize{Width: 80, Height: 12, Generation: 1}) {
		t.Fatal("post resize")
	}
	if !controller.Post(SetSemanticActiveCellProjectionAction{Enabled: true}) {
		t.Fatal("enable semantic active projection")
	}
	if !controller.Post(SetActiveCellAction{Active: ActiveCellState{
		CellID:   41,
		Revision: 1,
		Kind:     scene.KindAssistant,
		Phase:    ActiveCellMutable,
		Source:   source,
		Stable:   SourceRange{Start: 0, End: len(source)},
	}}) {
		t.Fatal("post mutable active cell")
	}
	controller.WaitIdle()

	before := controller.State()
	if before.Active.Phase != ActiveCellMutable || len(before.Transcript.Cells) != 0 {
		t.Fatalf("fixture finalized or retained the cell unexpectedly: active=%+v transcript=%+v", before.Active, before.Transcript.Cells)
	}

	executor.Request()
	executor.WaitIdle()
	controller.WaitIdle()

	raw := physical.String()
	if !strings.Contains(raw, latestMarker) {
		t.Fatalf("fixture did not paint the live viewport tail; terminal bytes=%q", raw)
	}
	if !strings.Contains(raw, earlyMarker) {
		after := controller.State()
		projection := ProjectActiveCellBand(after.Active, after.Geometry)
		t.Fatalf(
			"stable mutable prefix never crossed the physical writer before finalize: marker=%q history_effects=%d projected_range=%+v projected_rows=%d terminal_bytes=%q",
			earlyMarker,
			len(after.HistoryEffects.Entries()),
			projection.SourceRange,
			len(projection.Lines),
			raw,
		)
	}
}
