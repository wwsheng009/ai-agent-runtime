package ui

import (
	"reflect"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// TestLayoutTranscriptScreenRowsParallelMatchesSequential 钉住两阶段布局的并行
// 渲染与顺序渲染逐行等价：同一份 600-cell 快照、冷缓存下分别走顺序路径与
// 4-worker 并行路径，行数、归属、gap 与文本必须完全一致（P16 首渲染加速的
// 正确性前提；并行只影响延迟，不影响投影输出）。
func TestLayoutTranscriptScreenRowsParallelMatchesSequential(t *testing.T) {
	snapshot := benchResumedSnapshot(600)
	transcript := NewTranscriptState(snapshot)
	rows := transcript.LayoutRows(1)
	byID := transcriptCellsByID(transcript)
	width := 100
	theme := style.ThemeContext{}

	oldThreshold, oldWorkers, oldCache := layoutParallelRenderThreshold, layoutParallelRenderMaxWorkers, sharedCellRows
	defer func() {
		layoutParallelRenderThreshold, layoutParallelRenderMaxWorkers = oldThreshold, oldWorkers
		sharedCellRows = oldCache
	}()

	layout := func(threshold, workers int) []AppScreenRow {
		sharedCellRows = &cellRowsCache{lru: newCellLayoutLRU[cellLayoutKey, []AppScreenRow](cellRowsCacheMax, cellRowsCacheMaxBytes)}
		layoutParallelRenderThreshold, layoutParallelRenderMaxWorkers = threshold, workers
		return layoutTranscriptScreenRows(rows, byID, nil, width, theme)
	}

	sequential := layout(-1, 1) // 负门限 = 禁用并行
	parallel := layout(0, 4)    // 门限 0 + 4 worker = 强制并行
	if len(sequential) == 0 {
		t.Fatal("fixture produced no rows")
	}
	if len(sequential) != len(parallel) {
		t.Fatalf("row count mismatch: sequential=%d parallel=%d", len(sequential), len(parallel))
	}
	for index := range sequential {
		if !reflect.DeepEqual(sequential[index], parallel[index]) {
			t.Fatalf("row %d mismatch:\n sequential %+v\n parallel   %+v", index, sequential[index], parallel[index])
		}
	}
}
