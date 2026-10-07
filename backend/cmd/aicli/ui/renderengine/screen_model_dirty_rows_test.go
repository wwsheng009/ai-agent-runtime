package renderengine

import (
	"reflect"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
)

// S3 验收（P3 §2 S3）：StageFrame 脏行短路必须与「逐行 normalizeRow 全量
// 重写」的参考实现语义等价，且未变更行不得重新分配（切片身份保持）。

func dirtyRowCells(texts ...string) []vt.Cell {
	row := make([]vt.Cell, 4)
	for i, text := range texts {
		row[i] = vt.Cell{Text: text}
	}
	return row
}

// referenceStageFrame 是短路引入前的逐行全量重写语义（参考实现）。
func referenceStageFrame(m *ScreenModel, rows [][]vt.Cell) {
	for r := 0; r < m.height; r++ {
		var row []vt.Cell
		if r < len(rows) {
			row = rows[r]
		}
		m.back[r] = normalizeRow(row, m.width)
	}
}

func TestStageFrameDirtyShortCircuitMatchesFullNormalize(t *testing.T) {
	rowsA := [][]vt.Cell{
		dirtyRowCells("a", "b"),
		dirtyRowCells("c", "d"),
		dirtyRowCells("e", "f"),
	}
	rowsB := [][]vt.Cell{
		dirtyRowCells("a", "b"),
		dirtyRowCells("x", "y"),
		dirtyRowCells("e", "f"),
	}

	fast := NewScreenModel(4, 3)
	reference := NewScreenModel(4, 3)
	// 同一预热输入（fast 走 StageFrame，reference 走逐行参考实现）。
	fast.StageFrame(rowsA)
	referenceStageFrame(reference, rowsA)
	fast.ConfirmFlush()
	reference.ConfirmFlush()

	// 只有中间行变更：两侧 staged/back 与 flush 字节都必须一致。
	fast.StageFrame(rowsB)
	referenceStageFrame(reference, rowsB)
	if !reflect.DeepEqual(fast.back, reference.back) {
		t.Fatalf("staged back diverged:\nfast      = %#v\nreference = %#v", fast.back, reference.back)
	}
	fastBytes := fast.PrepareFlush()
	referenceBytes := reference.PrepareFlush()
	if fastBytes != referenceBytes {
		t.Fatalf("flush bytes diverged:\nfast      = %q\nreference = %q", fastBytes, referenceBytes)
	}
	if fastBytes == "" {
		t.Fatal("changed row produced no flush bytes")
	}
}

func TestStageFrameReusesUnchangedRowBuffers(t *testing.T) {
	rowsA := [][]vt.Cell{
		dirtyRowCells("a", "b"),
		dirtyRowCells("c", "d"),
		dirtyRowCells("e", "f"),
	}
	fast := NewScreenModel(4, 3)
	fast.StageFrame(rowsA)

	unchangedTop := &fast.back[0][0]
	unchangedBottom := &fast.back[2][0]

	rowsB := [][]vt.Cell{
		dirtyRowCells("a", "b"),
		dirtyRowCells("x", "y"),
		dirtyRowCells("e", "f"),
	}
	fast.StageFrame(rowsB)

	if &fast.back[0][0] != unchangedTop {
		t.Fatal("unchanged top row was reallocated instead of reusing its buffer")
	}
	if &fast.back[2][0] != unchangedBottom {
		t.Fatal("unchanged bottom row was reallocated instead of reusing its buffer")
	}
	want := []vt.Cell{{Text: "x"}, {Text: "y"}}
	if !reflect.DeepEqual(fast.back[1][:2], want) || fast.back[1][2].Text != "" {
		t.Fatalf("changed row content = %#v, want %#v padded", fast.back[1], want)
	}
}

// 相同行再次 StageFrame 必须零输出（短路不得引入重复 emit 或白重绘）。
func TestStageFrameRepeatedIdenticalRowsProduceNoBytes(t *testing.T) {
	rows := [][]vt.Cell{
		dirtyRowCells("a"),
		dirtyRowCells("b"),
	}
	m := NewScreenModel(4, 2)
	m.StageFrame(rows)
	if out := m.PrepareFlush(); out == "" {
		t.Fatal("initial staging produced no bytes")
	}
	m.ConfirmFlush()

	m.StageFrame(rows)
	if out := m.PrepareFlush(); out != "" {
		t.Fatalf("identical restage produced bytes: %q", out)
	}
}
