package ui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// TestActiveCellBandTailRowsMatchesFullTail 钉住尾部展开与全量展开的逐行等价。
// wrapAppScreenText 按逻辑行独立展开（不跨行携带状态），因此尾部展开必须与
// 「全量展开后取尾部 maxRows 行」完全一致——这是把 O(源长度) 降为 O(视口) 的
// 正确性前提。
func TestActiveCellBandTailRowsMatchesFullTail(t *testing.T) {
	corpus := []string{
		"",
		"单行",
		"单行\n",
		"\n",
		"\n\n\n",
		"a\r\nb\r\nc",
		"第一行中文内容比较长需要折行\n第二行\n第三行",
		"尾部中文折行内容很宽很宽很宽很宽很宽很宽\n短行",
		strings.Repeat("重复行内容\n", 40),
		strings.Repeat("非常长的单行内容非常长的单行内容非常长的单行内容非常长的单行内容", 3) + "\n尾行",
		"带\t制表符\n带\x1b[31m控制序列\x1b[0m的行\n普通行",
		"组合字符 ééé\n另起一行",
		"尾部\r回车\n正常",
	}
	widths := []int{1, 5, 20, 80}
	maxRowsList := []int{1, 2, 4, 6}
	for _, source := range corpus {
		for _, width := range widths {
			full := activeCellBandRows(source, width)
			for _, maxRows := range maxRowsList {
				want := full
				if len(want) > maxRows {
					want = want[len(want)-maxRows:]
				}
				got := activeCellBandTailRows(source, width, maxRows)
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("tail(source=%q,width=%d,maxRows=%d) = %#v, want %#v (full=%#v)",
						source, width, maxRows, got, want, full)
				}
			}
		}
	}
}

// TestActiveCellBandTailRowsBoundedWork：超大头部 + 小尾部时，展开分配必须有界
// （只与 maxRows 相关，不与源长度相关）。全量实现会为 2 万行各分配一个字符串。
func TestActiveCellBandTailRowsBoundedWork(t *testing.T) {
	source := strings.Repeat("头部行内容\n", 20000) + "尾部一\n尾部二"
	allocs := testing.AllocsPerRun(10, func() {
		rows := activeCellBandTailRows(source, 80, 4)
		if len(rows) != 4 {
			t.Fatalf("rows=%d, want 4", len(rows))
		}
	})
	if allocs > 64 {
		t.Fatalf("activeCellBandTailRows allocs=%.0f, 期望有界（≤64）——疑似回退到全源展开", allocs)
	}
}

// TestProjectActiveCellBandPlainUsesTailRows：投影接线后，plain 路径只产出
// 视口尾部行，且逐行与全量展开的尾部一致。
func TestProjectActiveCellBandPlainUsesTailRows(t *testing.T) {
	var builder strings.Builder
	for index := 0; index < 60; index++ {
		builder.WriteString("第")
		builder.WriteString(strings.Repeat("内容", index%3+1))
		builder.WriteString("行\n")
	}
	source := builder.String()
	geometry := GeometryState{Width: 20, Height: 24}
	active := ActiveCellState{
		CellID:   1,
		Revision: 1,
		Kind:     scene.KindAssistant,
		Phase:    ActiveCellMutable,
		Source:   source,
	}
	projection := ProjectActiveCellBandWithTheme(active, geometry, style.ThemeContext{})
	maxRows := ActiveBandRows(geometry.Height)
	full := activeCellBandRows(source, geometry.Width)
	if len(full) <= maxRows {
		t.Fatalf("测试源需要超过视口的行数：full=%d maxRows=%d", len(full), maxRows)
	}
	want := full[len(full)-maxRows:]
	if len(projection.Lines) != len(want) {
		t.Fatalf("projection lines=%d, want %d", len(projection.Lines), len(want))
	}
	for index, line := range projection.Lines {
		if len(line.Spans) != 1 || line.Spans[0].Text != want[index] {
			t.Fatalf("line[%d] = %#v, want text %q", index, line, want[index])
		}
	}
}
