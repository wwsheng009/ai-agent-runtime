package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/boundary"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// numberedToolLines 生成一个远超显示预算的工具结果正文。tag 让两个 cell 的
// 正文可以区分，从而能断言「哪一次折叠被展开/被隐藏」。
func numberedToolLines(tag string, rows int) string {
	lines := make([]string, 0, rows+1)
	lines = append(lines, "• Completed shell in 12ms")
	for i := 0; i < rows; i++ {
		lines = append(lines, fmt.Sprintf("  │  %s-%02d", tag, i))
	}
	return strings.Join(lines, "\n")
}

func committedToolCell(id scene.CellID, source string) scene.TranscriptCell {
	return scene.TranscriptCell{
		ID: id, Revision: uint64(id), Sequence: uint64(id),
		Kind: scene.KindToolChain, Source: source,
		Phase: scene.CellCommitted, Boundary: boundary.BoundaryNormal,
	}
}

func foldTestAppState(cells ...scene.TranscriptCell) AppState {
	refs := make([]*scene.TranscriptCell, 0, len(cells))
	for index := range cells {
		refs = append(refs, &cells[index])
	}
	return AppState{
		Revision:         7,
		LayoutGeneration: 1,
		Geometry:         GeometryState{Width: 80, Height: 40, Generation: 1},
		Transcript:       NewTranscriptState(&scene.Snapshot{Cells: refs}),
	}
}

// TestInitialScreenFoldStaysWithinFourRows 锁定初屏折叠契约：一个超长工具结果
// 在首屏最多占 4 行（3 行头 + 1 行尾），省略标记贴在尾行行尾。此前的 10+1+4
// 预算在真实终端上与「完全没折叠」无法区分——用户看到的就是折叠功能丢失。
//
// 标记的位置同样是契约：它独占一行时必然夹在 head 与 tail 之间，读起来像「结果
// 在中间断了」，而它真正表达的是「这一行之后还有正文没显示」。
func TestInitialScreenFoldStaysWithinFourRows(t *testing.T) {
	state := foldTestAppState(committedToolCell(1, numberedToolLines("row", 60)))
	// 宽度足够让「尾行 + 标记」落在同一行上（终端更窄时标记只会软折行，
	// 不会退回成尾行之前的独立标记行）。
	state.Geometry = GeometryState{Width: 120, Height: 40, Generation: 1}
	layout := LayoutAppScreen(state)
	rows := make([]string, 0, 8)
	for _, row := range layout.Rows {
		if row.CellID == 1 && !row.TranscriptGap {
			rows = append(rows, row.Text)
		}
	}
	joined := strings.Join(rows, "\n")

	if len(rows) > 4 {
		t.Fatalf("初屏折叠占 %d 行, want <= 4:\n%s", len(rows), joined)
	}
	if !strings.Contains(joined, "omitted from this preview (display only)") {
		t.Fatalf("折叠标记缺失:\n%s", joined)
	}
	if !strings.Contains(joined, "row-00") || !strings.Contains(joined, "row-59") {
		t.Fatalf("折叠必须保留头部与尾部:\n%s", joined)
	}
	if strings.Contains(joined, "row-30") {
		t.Fatalf("折叠泄漏了中段:\n%s", joined)
	}
	if !strings.Contains(joined, toolFoldHint) {
		t.Fatalf("最近一次折叠必须给出恢复键 %q:\n%s", toolFoldHint, joined)
	}
	last := rows[len(rows)-1]
	if !strings.Contains(last, "row-59") ||
		!strings.Contains(last, "omitted from this preview (display only)") ||
		!strings.Contains(last, toolFoldHint) {
		t.Fatalf("省略标记必须贴在尾行行尾, 实际尾行 %q:\n%s", last, joined)
	}
	for _, row := range rows[:len(rows)-1] {
		if strings.Contains(row, "omitted") {
			t.Fatalf("省略标记出现在尾行之前: %q\n%s", row, joined)
		}
	}
}

// TestFoldHintOnlyOnTheMostRecentFold 锁定提示归属：Ctrl+T 只展开最近一次折叠，
// 因此「Ctrl+T 查看完整文本」只能出现在那一次折叠上。印在每个折叠 cell 上等于
// 承诺一个并不存在的「一键全部展开」。
func TestFoldHintOnlyOnTheMostRecentFold(t *testing.T) {
	state := foldTestAppState(
		committedToolCell(1, numberedToolLines("old", 40)),
		committedToolCell(2, numberedToolLines("new", 40)),
	)
	layout := LayoutAppScreen(state)
	byCell := map[scene.CellID][]string{}
	all := make([]string, 0, len(layout.Rows))
	for _, row := range layout.Rows {
		if row.TranscriptGap {
			continue
		}
		all = append(all, row.Text)
		if row.CellID != 0 {
			byCell[row.CellID] = append(byCell[row.CellID], row.Text)
		}
	}
	flattened := strings.Join(all, "\n")
	// 提示贴在尾行末尾，窄终端会把那一行软折开；折行只是空白，因此按空白归一后
	// 再数提示出现的次数。
	if got := strings.Count(strings.Join(strings.Fields(flattened), " "), toolFoldHint); got != 1 {
		t.Fatalf("提示出现 %d 次, want 1:\n%s", got, flattened)
	}
	older := strings.Join(strings.Fields(strings.Join(byCell[1], "\n")), " ")
	if strings.Contains(older, toolFoldHint) {
		t.Fatalf("提示泄漏到更早的折叠上:\n%s", older)
	}
	if !strings.Contains(older, "omitted from this preview") {
		t.Fatalf("更早的折叠丢失了标记:\n%s", older)
	}
	if newer := strings.Join(strings.Fields(strings.Join(byCell[2], "\n")), " "); !strings.Contains(newer, toolFoldHint) {
		t.Fatalf("最近一次折叠缺少提示:\n%s", newer)
	}
}

func committedPagerToolCell(id scene.CellID, source string) scene.TranscriptCell {
	return committedToolCell(id, source)
}

// TestTranscriptPagerExpandsOnlyMostRecentFold 锁定 Ctrl+T 的语义：pager 首帧
// 只展开最近一次折叠，其余折叠保持与首屏一致的收起投影，而不是把每条消息都
// 铺开。ExpandAll（'e'）是显式的全部展开路径，保证更早的折叠仍然可达。
func TestTranscriptPagerExpandsOnlyMostRecentFold(t *testing.T) {
	model := TranscriptPagerModel{Cells: []scene.TranscriptCell{
		committedPagerToolCell(1, numberedToolLines("old", 40)),
		committedPagerToolCell(2, numberedToolLines("new", 40)),
	}}
	text := transcriptPagerRowsText(model.Rows(80))
	if !strings.Contains(text, "new-20") {
		t.Fatalf("最近一次折叠必须展开:\n%s", text)
	}
	if strings.Contains(text, "old-20") {
		t.Fatalf("pager 把更早的折叠也展开了:\n%s", text)
	}
	if !strings.Contains(text, "omitted from this preview") || !strings.Contains(text, transcriptPagerFoldHint) {
		t.Fatalf("收起的折叠必须给出标记与展开键:\n%s", text)
	}

	model.ExpandAll = true
	expanded := transcriptPagerRowsText(model.Rows(80))
	if !strings.Contains(expanded, "old-20") {
		t.Fatalf("展开全部后更早的折叠仍不可见:\n%s", expanded)
	}
	if strings.Contains(expanded, "omitted from this preview") {
		t.Fatalf("展开全部后仍渲染了折叠标记:\n%s", expanded)
	}
}

// TestTranscriptPagerExpandIntent 锁定 'e' 的意图与 reducer 落地：展开是
// durable 的用户意图，pager 自身不持有第二份可写状态。
func TestTranscriptPagerExpandIntent(t *testing.T) {
	model := TranscriptPagerModel{Cells: []scene.TranscriptCell{
		committedPagerToolCell(1, numberedToolLines("old", 40)),
	}}
	action := transcriptPagerIntentForKey(17, model, 80, 24, editorKey{kind: editorKeyRune, r: 'e'})
	expand, ok := action.(TranscriptPagerSetExpand)
	if !ok {
		t.Fatalf("e 键动作 = %T, want TranscriptPagerSetExpand", action)
	}
	if expand.LeaseID != 17 || !expand.Expand {
		t.Fatalf("e 键动作 = %#v, want lease=17 expand=true", expand)
	}

	model.ExpandAll = true
	collapse, ok := transcriptPagerIntentForKey(17, model, 80, 24, editorKey{kind: editorKeyRune, r: 'e'}).(TranscriptPagerSetExpand)
	if !ok || collapse.Expand {
		t.Fatalf("第二次按键必须收起: %#v", collapse)
	}

	state := UIControllerState{}
	state = reduceUIControllerState(state, Resize{Width: 80, Height: 24}, 1)
	state = reduceUIControllerState(state, LeaseAcquired{LeaseID: 9}, 2)
	state = reduceUIControllerState(state, OpenTranscriptOverlay{LeaseID: 9}, 3)
	first := committedPagerToolCell(1, numberedToolLines("old", 40))
	second := committedPagerToolCell(2, numberedToolLines("new", 40))
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: &scene.Snapshot{
		Revision: 4, Cells: []*scene.TranscriptCell{&first, &second},
	}}, 4)
	if state.TranscriptOverlay.Pager.ExpandAll {
		t.Fatal("pager 默认必须是「只展开最近一次折叠」")
	}
	state = reduceUIControllerState(state, TranscriptPagerSetExpand{LeaseID: 9, Expand: true}, 5)
	if !state.TranscriptOverlay.Pager.ExpandAll {
		t.Fatalf("展开意图被丢弃: %#v", state.TranscriptOverlay.Pager)
	}
	state = reduceUIControllerState(state, TranscriptPagerSetExpand{LeaseID: 8, Expand: false}, 6)
	if !state.TranscriptOverlay.Pager.ExpandAll {
		t.Fatalf("过期 lease 修改了展开状态: %#v", state.TranscriptOverlay.Pager)
	}
}
