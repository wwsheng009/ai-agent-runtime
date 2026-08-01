package renderengine

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
)

func TestEngineScreenModelFacadeStagesFrame(t *testing.T) {
	engine := NewEngine()
	t.Cleanup(engine.Shutdown)
	screen := engine.NewScreenModel(4, 2)
	if screen == nil {
		t.Fatal("NewScreenModel returned nil")
	}
	if width, height := screen.Size(); width != 4 || height != 2 {
		t.Fatalf("screen size = %dx%d, want 4x2", width, height)
	}
	screen.StageFrame([][]vt.Cell{{{Text: "ok"}}})
	if diff := screen.Flush(); diff == "" {
		t.Fatal("staged screen frame did not produce a diff")
	}
}

func TestComposerFacadePreservesRowOwnership(t *testing.T) {
	composer := NewComposer()
	plan := composer.ComposePlan(4, 3,
		[]PlanRow{{Owner: RowOwnerTranscript, Cells: []vt.Cell{{Text: "h"}}}},
		[]PlanRow{{Owner: RowOwnerStatus, Cells: []vt.Cell{{Text: "s"}}}},
	)
	if len(plan) != 3 {
		t.Fatalf("plan rows = %d, want 3", len(plan))
	}
	if plan[1].Owner != RowOwnerTranscript || plan[2].Owner != RowOwnerStatus {
		t.Fatalf("plan owners = %v, %v; want transcript/status", plan[1].Owner, plan[2].Owner)
	}
}

func TestEngineOwnsSharedRenderCache(t *testing.T) {
	engine := NewEngine()
	t.Cleanup(engine.Shutdown)
	if engine.Cache() == nil {
		t.Fatal("Engine cache is nil")
	}
	if engine.Cache() != SharedRenderCache() {
		t.Fatal("Engine cache is not the shared RenderCache")
	}
}
