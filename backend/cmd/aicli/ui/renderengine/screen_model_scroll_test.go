package renderengine

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
)

func TestScreenModelScrollUpSynchronizesBuffersAndOnlyPaintsNewRows(t *testing.T) {
	model := NewScreenModel(8, 4)
	model.StageFrame(testGrid(8, 4, "one", "two", "three", "four"))
	if output := model.Flush(); output == "" {
		t.Fatal("initial frame did not paint")
	}
	model.ConfirmFlush()

	model.ScrollUp(2)
	assertScreenModelBuffers(t, model, testGrid(8, 4, "three", "four", "", ""))
	if output := model.Flush(); output != "" {
		t.Fatalf("synchronized scroll must not repaint, got %q", output)
	}

	model.StageFrame(testGrid(8, 4, "three", "four", "five", "six"))
	output := model.Flush()
	assertPaintedRows(t, output, []int{3, 4}, []int{1, 2})
	if !strings.Contains(output, "five") || !strings.Contains(output, "six") {
		t.Fatalf("new bottom rows are missing from diff: %q", output)
	}
}

func TestScreenModelUnknownProjectionForcesRecoveryBeforeIncrementalDiff(t *testing.T) {
	model := NewScreenModel(8, 3)
	model.StageFrame(testGrid(8, 3, "one", "two", "three"))
	if output := model.Flush(); output == "" {
		t.Fatal("initial frame did not paint")
	}
	model.ConfirmFlush()
	if got := model.ProjectionValidity(); got != ProjectionKnown {
		t.Fatalf("projection after full flush = %v, want known", got)
	}

	model.Invalidate()
	if got := model.ProjectionValidity(); got != ProjectionUnknown {
		t.Fatalf("projection after invalidation = %v, want unknown", got)
	}
	model.MarkWriteFailed()
	// A physical-scroll mirror is unsafe until a recovery paint succeeds.
	model.ScrollUp(1)
	if output := model.Flush(); !strings.Contains(output, "\x1b[1;1H") || !strings.Contains(output, "\x1b[2;1H") {
		t.Fatalf("unknown projection must force full recovery, got %q", output)
	}
	model.ConfirmFlush()
	if got := model.ProjectionValidity(); got != ProjectionKnown {
		t.Fatalf("projection after recovery = %v, want known", got)
	}
}

func TestScreenModelScrollDownSynchronizesBuffersAndOnlyPaintsNewRows(t *testing.T) {
	model := NewScreenModel(8, 4)
	model.StageFrame(testGrid(8, 4, "one", "two", "three", "four"))
	model.Flush()
	model.ConfirmFlush()

	model.ScrollDown(2)
	assertScreenModelBuffers(t, model, testGrid(8, 4, "", "", "one", "two"))
	if output := model.Flush(); output != "" {
		t.Fatalf("synchronized scroll must not repaint, got %q", output)
	}

	model.StageFrame(testGrid(8, 4, "alpha", "beta", "one", "two"))
	output := model.Flush()
	assertPaintedRows(t, output, []int{1, 2}, []int{3, 4})
	if !strings.Contains(output, "alpha") || !strings.Contains(output, "beta") {
		t.Fatalf("new top rows are missing from diff: %q", output)
	}
}

func TestScreenModelScrollMovesPendingFrontBackDifference(t *testing.T) {
	model := NewScreenModel(8, 4)
	model.StageFrame(testGrid(8, 4, "one", "two", "three", "four"))
	model.Flush()
	model.ConfirmFlush()

	model.StageRow(4, testRow(8, "pending"))
	model.ScrollUp(1)

	if want := testGrid(8, 4, "two", "three", "four", ""); !reflect.DeepEqual(model.front, want) {
		t.Fatalf("front after scroll = %#v, want %#v", model.front, want)
	}
	if want := testGrid(8, 4, "two", "three", "pending", ""); !reflect.DeepEqual(model.back, want) {
		t.Fatalf("back after scroll = %#v, want %#v", model.back, want)
	}

	output := model.Flush()
	assertPaintedRows(t, output, []int{3}, []int{1, 2, 4})
	if !strings.Contains(output, "pending") {
		t.Fatalf("shifted staged change is missing from diff: %q", output)
	}
}

func TestScreenModelScrollHandlesCountsOutsideViewport(t *testing.T) {
	tests := []struct {
		name  string
		count int
		apply func(*ScreenModel, int)
		clear bool
	}{
		{name: "up negative is no-op", count: -1, apply: (*ScreenModel).ScrollUp},
		{name: "down zero is no-op", count: 0, apply: (*ScreenModel).ScrollDown},
		{name: "up at height clears", count: 3, apply: (*ScreenModel).ScrollUp, clear: true},
		{name: "down past height clears", count: 7, apply: (*ScreenModel).ScrollDown, clear: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := NewScreenModel(6, 3)
			original := testGrid(6, 3, "one", "two", "three")
			model.StageFrame(original)
			model.Flush()
			model.ConfirmFlush()

			tt.apply(model, tt.count)

			want := original
			if tt.clear {
				want = testGrid(6, 3, "", "", "")
			}
			assertScreenModelBuffers(t, model, want)
			if output := model.Flush(); output != "" {
				t.Fatalf("front/back must remain synchronized, got diff %q", output)
			}
		})
	}

	var nilModel *ScreenModel
	nilModel.ScrollUp(1)
	nilModel.ScrollDown(1)
}

func TestScreenModelScrollRegionLeavesBottomPaneUntouched(t *testing.T) {
	model := NewScreenModel(8, 5)
	model.StageFrame(testGrid(8, 5, "one", "two", "three", "prompt", "status"))
	model.Flush()
	model.ConfirmFlush()

	model.ScrollRegionUp(1, 3, 1)
	assertScreenModelBuffers(t, model, testGrid(8, 5, "two", "three", "", "prompt", "status"))
	if output := model.Flush(); output != "" {
		t.Fatalf("synchronized region scroll must not repaint, got %q", output)
	}

	model.ScrollRegionDown(1, 3, 1)
	assertScreenModelBuffers(t, model, testGrid(8, 5, "", "two", "three", "prompt", "status"))
	if output := model.Flush(); output != "" {
		t.Fatalf("synchronized reverse region scroll must not repaint, got %q", output)
	}
}

func TestScreenModelApplyRegionAppendMirrorsTerminalBytes(t *testing.T) {
	model := NewScreenModel(8, 5)
	model.StageFrame(testGrid(8, 5, "one", "two", "three", "prompt", "status"))
	model.Flush()
	model.ConfirmFlush()

	model.ApplyRegionAppend(1, 3, testGrid(8, 2, "four", "five"))
	assertScreenModelBuffers(t, model, testGrid(8, 5, "three", "four", "five", "prompt", "status"))
	if output := model.Flush(); output != "" {
		t.Fatalf("mirrored terminal append must not repaint, got %q", output)
	}
}

func TestScreenModelWriteFailureRequiresRecoveryBeforeDiff(t *testing.T) {
	model := NewScreenModel(8, 3)
	model.StageFrame(testGrid(8, 3, "one", "two", "three"))
	if output := model.PrepareFlush(); output == "" {
		t.Fatal("initial frame did not paint")
	}
	if got := model.ProjectionValidity(); got != ProjectionUnknown {
		t.Fatalf("prepared frame projection = %v, want unknown", got)
	}
	model.MarkWriteFailed()

	model.StageRow(3, testRow(8, "changed"))
	output := model.Flush()
	for _, row := range []int{1, 2, 3} {
		marker := "\x1b[" + strconv.Itoa(row) + ";1H"
		if !strings.Contains(output, marker) {
			t.Fatalf("row %d missing from recovery frame: %q", row, output)
		}
	}
	model.ConfirmFlush()
	if got := model.ProjectionValidity(); got != ProjectionKnown {
		t.Fatalf("ConfirmFlush projection = %v, want known", got)
	}
}

func assertScreenModelBuffers(t *testing.T, model *ScreenModel, want [][]vt.Cell) {
	t.Helper()
	if !reflect.DeepEqual(model.front, want) {
		t.Fatalf("front = %#v, want %#v", model.front, want)
	}
	if !reflect.DeepEqual(model.back, want) {
		t.Fatalf("back = %#v, want %#v", model.back, want)
	}
}

func assertPaintedRows(t *testing.T, output string, painted, untouched []int) {
	t.Helper()
	for _, row := range painted {
		marker := "\x1b[" + strconv.Itoa(row) + ";1H"
		if !strings.Contains(output, marker) {
			t.Errorf("row %d was not painted; output = %q", row, output)
		}
	}
	for _, row := range untouched {
		marker := "\x1b[" + strconv.Itoa(row) + ";1H"
		if strings.Contains(output, marker) {
			t.Errorf("row %d was unexpectedly repainted; output = %q", row, output)
		}
	}
}
