package ui

import (
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// transcript_residency_index_test.go —— T3 双代账本与索引的契约/属性测试：
// 前缀指纹链、分代行账本（定位/自校验/篡改检测）、窗口行前缀和与 foldTarget，
// 以及"layout(window) == filter(layout(all), window)"（模 leading gap）等价性对拍。

func indexTestCell(id scene.CellID, kind scene.CellKind, lines int) scene.TranscriptCell {
	var builder strings.Builder
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&builder, "cell %d 第 %d 行内容\n", id, i)
	}
	cell := scene.TranscriptCell{
		ID:       id,
		Sequence: uint64(id),
		Kind:     kind,
		Source:   builder.String(),
		Revision: uint64(1 + int(id)%5),
		Phase:    scene.CellCommitted,
	}
	cell.Presentation = scene.TranscriptPresentation{Kind: scene.PresentationPlain}
	return cell
}

func indexTestDocumentCell(id scene.CellID) scene.TranscriptCell {
	cell := indexTestCell(id, scene.KindAssistant, 0)
	cell.Source = "# 标题\n\n正文段落\n"
	cell.Presentation = scene.TranscriptPresentation{
		Kind: scene.PresentationDocument,
		Document: render.Document{Blocks: []render.Block{
			{Kind: render.BlockParagraph, Lines: []render.Line{
				{Spans: []render.Span{{Text: "标题", Style: render.Style{Bold: true}}}},
				{Spans: []render.Span{{Text: "正文段落"}}},
			}},
		}},
	}
	return cell
}

func TestGroupKeyFingerprintStableAndDistinct(t *testing.T) {
	a := GroupKeyFingerprint("chain-1", "grp-1")
	if a != GroupKeyFingerprint("chain-1", "grp-1") {
		t.Fatal("同输入指纹必须稳定")
	}
	if a == GroupKeyFingerprint("chain-2", "grp-1") || a == GroupKeyFingerprint("chain-1", "grp-2") {
		t.Fatal("不同分组键必须产生不同指纹")
	}
}

func TestColdLedgerAppendValidateLocate(t *testing.T) {
	rng := rand.New(rand.NewSource(20261020))
	ledger := &ColdLedger{}
	const cells = 5000
	expectedStarts := make([]int64, 0, cells)
	expectedRows := make([]int64, 0, cells)
	var running int64
	for i := 0; i < cells; i++ {
		// 行数 ≥1：零行 span 会让"行号归属"在二分与线性扫描间产生歧义，
		// 本用例专门校验可归属行号的定位一致性（零行 span 由 T4 的守卫处理）。
		rows := int32(1 + rng.Intn(299))
		gen := uint64(1 + rng.Intn(3))
		cell := indexTestCell(scene.CellID(i+1), scene.KindAssistant, 1)
		expectedStarts = append(expectedStarts, running)
		expectedRows = append(expectedRows, int64(rows))
		running += int64(rows)
		if _, err := ledger.AppendCell(cell, rows, 0, gen, int64(i+1), true); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	if ledger.Rows.Total() != running {
		t.Fatalf("行总数不符：ledger=%d want=%d", ledger.Rows.Total(), running)
	}
	if err := ledger.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	// 定位对拍：随机行号与线性扫描一致。
	for probe := 0; probe < 500; probe++ {
		row := rng.Int63n(running)
		index, offset, ok := ledger.Rows.Locate(row)
		if !ok {
			t.Fatalf("locate(%d) 失败", row)
		}
		wantIndex := -1
		for i := range expectedStarts {
			if row >= expectedStarts[i] && row < expectedStarts[i]+expectedRows[i] {
				wantIndex = i
				break
			}
		}
		if wantIndex < 0 {
			t.Fatalf("行 %d 未落在任何 span（账本构造错误）", row)
		}
		if index != wantIndex {
			t.Fatalf("locate(%d)=%d，期望 %d", row, index, wantIndex)
		}
		if row != expectedStarts[index]+int64(offset) {
			t.Fatalf("locate(%d) offset=%d 与行起点不一致", row, offset)
		}
	}
	// 越界必须失败。
	if _, _, ok := ledger.Rows.Locate(running); ok {
		t.Fatal("越界行号必须失败")
	}
	if _, _, ok := ledger.Rows.Locate(-1); ok {
		t.Fatal("负行号必须失败")
	}
	// 分代汇总：与逐引用累加一致。
	byGen := map[uint64]int64{}
	for i := range ledger.Cells {
		byGen[ledger.Cells[i].Gen] += int64(ledger.Cells[i].RowsAtGen)
	}
	for _, span := range ledger.Rows.GenerationSpans() {
		if span.Rows != byGen[span.Gen] {
			t.Fatalf("代 %d 行数不符：ledger=%d want=%d", span.Gen, span.Rows, byGen[span.Gen])
		}
	}
}

func TestColdLedgerDetectsTamperAndOrderViolations(t *testing.T) {
	ledger := &ColdLedger{}
	for i := 0; i < 5; i++ {
		cell := indexTestCell(scene.CellID(i+1), scene.KindAssistant, 1)
		if _, err := ledger.AppendCell(cell, int32(2+i), 0, 1, int64(i+1), true); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if err := ledger.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	// 交付序必须严格递增。
	cell := indexTestCell(99, scene.KindAssistant, 1)
	if _, err := ledger.AppendCell(cell, 1, 0, 1, 5, true); err == nil {
		t.Fatal("非递增 deliveredAt 必须被拒绝")
	}
	// 篡改行数 → 行账本同步校验失败。
	tampered := &ColdLedger{Cells: append([]ColdCellRef(nil), ledger.Cells...)}
	tampered.Rows = ledger.Rows
	tampered.Cells[2].RowsAtGen++
	if err := tampered.Validate(); err == nil {
		t.Fatal("行数被篡改必须被检测到")
	}
	// 篡改前缀指纹 → 链校验失败。
	tampered2 := &ColdLedger{Cells: append([]ColdCellRef(nil), ledger.Cells...)}
	tampered2.Rows = ledger.Rows
	tampered2.Cells[3].PrefixFence ^= 0x1
	if err := tampered2.Validate(); err == nil {
		t.Fatal("前缀指纹被篡改必须被检测到")
	}
}

// TestColdLedgerPrefixFenceMatchesProductionFence 对拍：冷区账本的前缀链
// 必须与生产 transcriptFinalizedPrefixFence 完全一致（任务 T3 验收项，
// 5,000 cells 规模）。
func TestColdLedgerPrefixFenceMatchesProductionFence(t *testing.T) {
	const cells = 5000
	corpus := make([]scene.TranscriptCell, 0, cells)
	for i := 0; i < cells; i++ {
		kind := scene.KindAssistant
		switch i % 4 {
		case 1:
			kind = scene.KindUser
		case 2:
			kind = scene.KindToolChain
		case 3:
			kind = scene.KindReasoning
		}
		cell := indexTestCell(scene.CellID(i+1), kind, 1+i%4)
		if i%7 == 0 {
			cell.ChainKey = fmt.Sprintf("chain-%d", i/10)
		}
		if i%11 == 0 {
			cell.BoundaryGroupKey = fmt.Sprintf("grp-%d", i/50)
		}
		if i%13 == 0 {
			cell.HistoryCommitBlocked = true
		}
		corpus = append(corpus, cell)
	}
	state := TranscriptState{Cells: corpus, SceneID: 3, Revision: 1, ContentVersion: 1}
	want := transcriptFinalizedPrefixFence(state)

	ledger := &ColdLedger{}
	for index, cell := range corpus {
		if _, err := ledger.AppendCell(cell, 1, 0, 1, int64(index+1), true); err != nil {
			t.Fatalf("append %d: %v", index, err)
		}
	}
	if got := ledger.LastPrefixFence(); got != want {
		t.Fatalf("冷区前缀链=%d，生产 fence=%d（语义漂移）", got, want)
	}
	if err := ledger.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestWindowLedgerRowStartsLocateDetachAndFoldTarget(t *testing.T) {
	window := &WindowLedger{
		StartSeq:   10,
		Cells:      []scene.TranscriptCell{indexTestCell(11, scene.KindAssistant, 1), indexTestCell(12, scene.KindToolChain, 1), indexTestCell(13, scene.KindAssistant, 1)},
		LocalRows:  []int32{3, 2, 4},
		Generation: 7,
	}
	if window.TotalRows() != 9 {
		t.Fatalf("total rows=%d", window.TotalRows())
	}
	starts := window.RowStarts()
	if !reflect.DeepEqual(starts, []int64{0, 3, 5}) {
		t.Fatalf("starts=%v", starts)
	}
	if index, offset, ok := window.Locate(4); !ok || index != 1 || offset != 1 {
		t.Fatalf("locate(4)=%d,%d,%v", index, offset, ok)
	}
	if _, _, ok := window.Locate(9); ok {
		t.Fatal("越界行号必须失败")
	}
	// 折叠工具链：KindToolChain + PresentationPlain → foldTarget 命中。
	if target := window.LastFoldedCellID(); target != 12 {
		t.Fatalf("fold target=%d，期望 12", target)
	}
	cold := &ColdLedger{}
	coldFolded := indexTestCell(3, scene.KindToolChain, 1)
	if _, err := cold.AppendCell(coldFolded, 2, 0, 1, 1, true); err != nil {
		t.Fatalf("append folded: %v", err)
	}
	if target := GlobalFoldTarget(cold, window); target != 12 {
		t.Fatalf("窗口优先 foldTarget=%d，期望 12", target)
	}
	emptyWindow := &WindowLedger{StartSeq: 10}
	if target := GlobalFoldTarget(cold, emptyWindow); target != 3 {
		t.Fatalf("冷区回退 foldTarget=%d，期望 3", target)
	}
	detached, rows, err := window.DetachPrefix(1)
	if err != nil || len(detached) != 1 || len(rows) != 1 || detached[0].ID != 11 {
		t.Fatalf("detach: %v %d %d", err, len(detached), len(rows))
	}
	if window.StartSeq != 11 || len(window.Cells) != 2 || window.TotalRows() != 6 {
		t.Fatalf("detach 后窗口状态异常：start=%d cells=%d total=%d", window.StartSeq, len(window.Cells), window.TotalRows())
	}
}

// groupRowsByCell 把 layout rows 按归属 cell 分组（gap row 归属后继 cell，
// 因此与语义行同组且位于组首）。
func groupRowsByCell(t *testing.T, rows []scene.LayoutRow) ([]scene.CellID, map[scene.CellID][]scene.LayoutRow) {
	t.Helper()
	var order []scene.CellID
	groups := map[scene.CellID][]scene.LayoutRow{}
	for _, row := range rows {
		if len(order) == 0 || order[len(order)-1] != row.CellID {
			if len(order) > 0 {
				// 同一个 cell 的行必须连续。
				for _, seen := range order {
					if seen == row.CellID {
						t.Fatalf("cell %d 的行不连续（gap 归属/顺序异常）", row.CellID)
					}
				}
			}
			order = append(order, row.CellID)
		}
		groups[row.CellID] = append(groups[row.CellID], row)
	}
	return order, groups
}

func leadingGapRows(rows []scene.LayoutRow) int {
	count := 0
	for _, row := range rows {
		if row.Gap == 0 {
			break
		}
		count++
	}
	return count
}

func semanticRowTexts(rows []scene.LayoutRow) []string {
	var texts []string
	for _, row := range rows {
		if row.Gap == 0 {
			texts = append(texts, row.Text)
		}
	}
	return texts
}

func TestLayoutWindowEquivalenceAndRowLedgerCrossCheck(t *testing.T) {
	const cells = 30
	const windowCells = 12
	corpus := make([]scene.TranscriptCell, 0, cells)
	for i := 0; i < cells; i++ {
		id := scene.CellID(i + 1)
		switch {
		case i == 7:
			corpus = append(corpus, indexTestDocumentCell(id))
		case i == cells-3: // 尾部折叠工具链（foldTarget 落在窗口内）
			cell := indexTestCell(id, scene.KindToolChain, 40)
			cell.ChainKey = "chain-tail"
			corpus = append(corpus, cell)
		case i%9 == 4:
			corpus = append(corpus, indexTestCell(id, scene.KindUser, 2+int(id)%3))
		default:
			corpus = append(corpus, indexTestCell(id, scene.KindAssistant, 1+int(id)%4))
		}
	}
	full := TranscriptState{Cells: corpus, SceneID: 1, Revision: 1, ContentVersion: 1}
	allRows := full.LayoutRows(1)
	if len(allRows) == 0 {
		t.Fatal("全量布局为空")
	}
	order, groups := groupRowsByCell(t, allRows)
	if len(order) != cells {
		t.Fatalf("布局覆盖 cell 数=%d，期望 %d", len(order), cells)
	}

	// 冷区账本与真实 layout rows 交叉核对：行起点/行数/gap 归属必须一致。
	ledger := &ColdLedger{}
	running := int64(0)
	for index, id := range order {
		group := groups[id]
		rowsAtGen := int32(len(group))
		gap := int8(leadingGapRows(group))
		if _, err := ledger.AppendCell(corpus[index], rowsAtGen, gap, 1, int64(index+1), true); err != nil {
			t.Fatalf("append cell %d: %v", id, err)
		}
		start, ok := ledger.Rows.RowStart(index)
		if !ok || start != running {
			t.Fatalf("cell %d 行起点=%d，期望 %d", id, start, running)
		}
		running += int64(rowsAtGen)
	}
	if ledger.Rows.Total() != int64(len(allRows)) {
		t.Fatalf("账本行总数=%d，layout rows=%d", ledger.Rows.Total(), len(allRows))
	}
	if err := ledger.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}

	// 窗口等价性：layout(window) 必须等于 filter(layout(all), window)，
	// 唯一允许的差异是窗口首格丢失其冷区 leading gap（§14.7 gap 归属规则）。
	windowStart := cells - windowCells
	window := &WindowLedger{
		StartSeq:   windowStart,
		Cells:      append([]scene.TranscriptCell(nil), corpus[windowStart:]...),
		Generation: 1,
	}
	winState := TranscriptState{Cells: window.Cells, SceneID: 1, Revision: 1, ContentVersion: 1}
	winRows := winState.LayoutRows(1)
	_, winGroups := groupRowsByCell(t, winRows)

	for index, cell := range window.Cells {
		allGroup := groups[cell.ID]
		winGroup := winGroups[cell.ID]
		if len(winGroup) == 0 {
			t.Fatalf("窗口 cell %d 无布局行", cell.ID)
		}
		if !reflect.DeepEqual(semanticRowTexts(allGroup), semanticRowTexts(winGroup)) {
			t.Fatalf("cell %d 语义行不一致：\nall=%v\nwin=%v", cell.ID, semanticRowTexts(allGroup), semanticRowTexts(winGroup))
		}
		if index == 0 {
			// 窗口首格：全量视图中的 leading gap 来自冷区边界；窗口视图必须为 0。
			if got := leadingGapRows(winGroup); got != 0 {
				t.Fatalf("窗口首格不应含 leading gap：%d", got)
			}
			if ledger.Cells[windowStart].LeadingGapRows != int8(leadingGapRows(allGroup)) {
				t.Fatalf("账本记录的 leading gap=%d，与全量布局 %d 不一致",
					ledger.Cells[windowStart].LeadingGapRows, leadingGapRows(allGroup))
			}
			// 行数差异必须恰好等于该 leading gap（其余行逐行一致）。
			if len(allGroup)-len(winGroup) != leadingGapRows(allGroup) {
				t.Fatalf("窗口首格行数差=%d，期望=%d", len(allGroup)-len(winGroup), leadingGapRows(allGroup))
			}
			continue
		}
		if len(allGroup) != len(winGroup) {
			t.Fatalf("cell %d 行数不一致：all=%d win=%d", cell.ID, len(allGroup), len(winGroup))
		}
	}

	// foldTarget：尾部折叠落在窗口内 → 全局 foldTarget == 窗口内最后折叠。
	if target := GlobalFoldTarget(ledger, window); target != scene.CellID(cells-2) {
		t.Fatalf("foldTarget=%d，期望 %d", target, cells-2)
	}
	// 折叠 cell 的窗口投影与全量逐行一致（含 Ctrl+T 提示行）。
	foldedID := scene.CellID(cells - 2)
	if !reflect.DeepEqual(semanticRowTexts(groups[foldedID]), semanticRowTexts(winGroups[foldedID])) {
		t.Fatal("折叠工具链 cell 的窗口投影与全量不一致（foldHint 语义漂移）")
	}
}
