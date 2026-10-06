package ui

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// A2 第一刀（停铸 active）：mutable 溢出的稳定前缀在 finalize 之前不得跨物理
// writer；finalize 时整源从 0 一次交付，每个标记恰好一次。
func TestMutableActiveOverflowDefersStablePrefixUntilFinalize(t *testing.T) {
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
	if entries := before.HistoryEffects.Entries(); len(entries) != 0 || before.Active.Acked.End != 0 {
		t.Fatalf("mutable streaming minted history before finalize: active=%+v effects=%+v", before.Active, entries)
	}

	executor.Request()
	executor.WaitIdle()
	controller.WaitIdle()

	raw := physical.String()
	if !strings.Contains(raw, latestMarker) {
		t.Fatalf("fixture did not paint the live viewport tail; terminal bytes=%q", raw)
	}
	if strings.Contains(raw, earlyMarker) {
		t.Fatalf("stable mutable prefix crossed the physical writer before finalize; terminal bytes=%q", raw)
	}

	if !controller.Post(FinalizeActiveCellAction{
		Snapshot: &scene.Snapshot{Revision: 2, Cells: []*scene.TranscriptCell{{
			ID: 41, Revision: 2, Kind: scene.KindAssistant,
			Source: source, Phase: scene.CellCommitted,
		}}},
		ExpectedActiveCellID: 41, ExpectedActiveRevision: 1,
		ExpectedSceneRevision: 2,
		ExpectedActiveKind:    scene.KindAssistant, ExpectedActiveKindKnown: true,
	}) {
		t.Fatal("post finalize")
	}
	executor.Request()
	executor.WaitIdle()
	controller.WaitIdle()

	assertPhysicalMarkersExactlyOnce(t, physical.String(), 80, 12, []string{earlyMarker, latestMarker})
}
