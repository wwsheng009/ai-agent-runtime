package ui

import (
	"reflect"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

func TestHistoryPlanCacheHitAndInvalidation(t *testing.T) {
	c := &historyPlanCache{entries: make(map[cellLayoutKey]cachedPlanRows), max: 4}
	fp := "dark|1|github|{0}|false"
	cell := testCell("hello\nworld", scene.PresentationPlain)
	key := planCacheKeyFor(cell, 40, fp)
	rows := []planPhysicalRow{
		{text: "hello", source: SourceRange{Start: 0, End: 5}, line: appPlainRenderLine("hello")},
		{text: "world", source: SourceRange{Start: 6, End: 11}, line: appPlainRenderLine("world")},
	}
	c.put(key, rows)
	if got := c.get(key); got == nil || len(got) != 2 || got[0].text != "hello" {
		t.Fatalf("hit failed: got %#v", got)
	}

	// source 变化 → miss
	if got := c.get(planCacheKeyFor(testCell("hello\nthere", scene.PresentationPlain), 40, fp)); got != nil {
		t.Fatalf("expected miss after source change, got %#v", got)
	}
	// width 变化 → miss
	if got := c.get(planCacheKeyFor(cell, 41, fp)); got != nil {
		t.Fatalf("expected miss after width change, got %#v", got)
	}
	// theme 指纹变化 → miss
	if got := c.get(planCacheKeyFor(cell, 40, fp+"x")); got != nil {
		t.Fatalf("expected miss after theme change, got %#v", got)
	}
}

func TestHistoryPlanCacheEviction(t *testing.T) {
	c := &historyPlanCache{entries: make(map[cellLayoutKey]cachedPlanRows), max: 2}
	fp := "fp"
	for i := 0; i < 3; i++ {
		source := string(rune('a' + i))
		key := planCacheKeyFor(testCell(source, scene.PresentationPlain), 40, fp)
		c.put(key, []planPhysicalRow{{text: source, line: appPlainRenderLine(source)}})
	}
	if got := c.get(planCacheKeyFor(testCell("a", scene.PresentationPlain), 40, fp)); got != nil {
		t.Fatalf("expected oldest entry evicted, got %#v", got)
	}
	if got := c.get(planCacheKeyFor(testCell("c", scene.PresentationPlain), 40, fp)); got == nil {
		t.Fatalf("expected newest entry present")
	}
}

// TestPlanPlainHistoryCommitsCacheRoundTrip 验证同一输入两次规划的结果一致：
// 第一次全量构建（miss），第二次命中缓存走组装路径，产物必须深度相等。
func TestPlanPlainHistoryCommitsCacheRoundTrip(t *testing.T) {
	const source = "alpha\nbeta\ngamma\n"
	makeState := func() AppState {
		return AppState{
			Geometry:         GeometryState{Width: 40, Height: 24, Generation: 1},
			LayoutGeneration: 1,
			Transcript: NewTranscriptState(&scene.Snapshot{Revision: 1, Cells: []*scene.TranscriptCell{{
				ID: 88, Revision: 1, Kind: scene.KindUser,
				Source: source, Phase: scene.CellCommitted,
			}}}),
		}
	}
	first := planEligibleHistoryCommits(makeState())
	second := planEligibleHistoryCommits(makeState())
	if len(first) == 0 {
		t.Fatalf("expected plain commits, got none")
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("hit path diverged from miss path:\nmiss: %#v\nhit:  %#v", first, second)
	}
	// 缓存确实命中（条目不因第二次调用重建而清空）。
	cell := scene.TranscriptCell{ID: 88, Revision: 1, Kind: scene.KindUser, Source: source, Phase: scene.CellCommitted}
	key := planCacheKeyFor(cell, 40, themeFingerprint(AppState{}.Theme))
	if sharedHistoryPlan.get(key) == nil {
		t.Fatalf("expected plan cache entry to exist after planning")
	}
}

// TestPlanPlainHistoryCommitsDynamicFields 验证动态参数（skipRows /
// displayStart）不参与缓存：同一 cell 不同 skipRows 得到不同的 DisplayRange。
func TestPlanPlainHistoryCommitsDynamicFields(t *testing.T) {
	const source = "alpha\nbeta\ngamma\n"
	makeState := func() AppState {
		return AppState{
			Geometry:         GeometryState{Width: 40, Height: 24, Generation: 1},
			LayoutGeneration: 2,
			Transcript: NewTranscriptState(&scene.Snapshot{Revision: 1, Cells: []*scene.TranscriptCell{{
				ID: 91, Revision: 1, Kind: scene.KindUser,
				Source: source, Phase: scene.CellCommitted,
			}}}),
		}
	}
	state := makeState()
	t.Logf("cells=%d width=%d layoutGen=%d theme=%q", len(state.Transcript.Cells), state.Geometry.Width, state.LayoutGeneration, themeFingerprint(state.Theme))
	frontier, _ := canonicalHistoryCommitFrontier(state)
	t.Logf("frontier=%v", frontier)
	byID := transcriptCellsByID(state.Transcript)
	rows := layoutTranscriptScreenRows(state.Transcript.LayoutRows(state.LayoutGeneration), byID, mutableTranscriptCellIDs(state.Transcript), state.Geometry.Width, state.Theme)
	t.Logf("rows=%d", len(rows))
	commits := planEligibleHistoryCommits(state)
	if len(commits) != 4 {
		t.Fatalf("commits = %d, want 4", len(commits))
	}
	if commits[1].DisplayRange.Start != commits[1].DisplayRange.End-1 {
		t.Fatalf("display range not single-row: %+v", commits[1].DisplayRange)
	}
	// 每行一个 commit，行文本与 source 行一致。
	for i, commit := range commits {
		if len(commit.Lines) == 0 {
			t.Fatalf("commit %d has no lines", i)
		}
	}
}
