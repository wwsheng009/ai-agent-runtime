package commands

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// chat_debug_window_test.go —— T7 尾巴窗口快照契约：
// 顺序/规模正确、零拷贝路径、与全量分组逐组等价（首组允许 leading gap 差异）。

func TestTailWindowSnapshotParityAndZeroCopy(t *testing.T) {
	s := scene.New()
	const total = 100
	for i := 1; i <= total; i++ {
		if _, err := s.ApplyCellMutation(&scene.AppendCell{Cell: scene.TranscriptCell{
			ID:       scene.CellID(i),
			Kind:     scene.KindAssistant,
			Source:   fmt.Sprintf("cell %d 内容\n", i),
			Revision: 1,
			Phase:    scene.CellCommitted,
		}}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	snapshot := s.Snapshot()

	windowed := tailWindowSnapshot(snapshot, 20)
	if windowed == snapshot {
		t.Fatal("超限时应产生窗口副本")
	}
	if len(windowed.Cells) != 20 || windowed.Cells[0].ID != 81 || windowed.Cells[19].ID != 100 {
		t.Fatalf("窗口内容不符：len=%d head=%d tail=%d", len(windowed.Cells), windowed.Cells[0].ID, windowed.Cells[19].ID)
	}

	fullGroups := sceneBlockGroups(snapshot)
	windowGroups := sceneBlockGroups(windowed)
	if len(fullGroups) != total || len(windowGroups) != 20 {
		t.Fatalf("分组数不符：full=%d win=%d", len(fullGroups), len(windowGroups))
	}
	offset := len(fullGroups) - len(windowGroups)
	for index := range windowGroups {
		full := fullGroups[offset+index]
		win := windowGroups[index]
		if full.kind != win.kind {
			t.Fatalf("组 %d kind 不符：%v vs %v", index, full.kind, win.kind)
		}
		if index == 0 {
			// 窗口首格失去前驱 gap：leading gap 归零，正文行等于全量组剥离 gap 前缀后的行。
			if win.leadingGapRows != 0 {
				t.Fatalf("窗口首组不应有 leading gap：%d", win.leadingGapRows)
			}
			if full.leadingGapRows != 1 || len(full.lines) != len(win.lines)+1 {
				t.Fatalf("全量首组形状不符：gap=%d lines=%d winLines=%d", full.leadingGapRows, len(full.lines), len(win.lines))
			}
			if !reflect.DeepEqual(full.lines[1:], win.lines) {
				t.Fatalf("窗口首组正文与全量不符：\nfull=%v\nwin =%v", full.lines[1:], win.lines)
			}
			continue
		}
		if full.leadingGapRows != win.leadingGapRows || !reflect.DeepEqual(full.lines, win.lines) {
			t.Fatalf("组 %d 不等价：gap=%d/%d lines=%v vs %v",
				index, full.leadingGapRows, win.leadingGapRows, full.lines, win.lines)
		}
	}

	// 零拷贝路径：未超限（含 maxCells<=0）时返回原快照。
	if tailWindowSnapshot(snapshot, 0) != snapshot {
		t.Fatal("maxCells=0 应零拷贝返回原快照")
	}
	if tailWindowSnapshot(snapshot, total) != snapshot {
		t.Fatal("maxCells=total 应零拷贝返回原快照")
	}
	if tailWindowSnapshot(nil, 10) != nil {
		t.Fatal("nil 快照应原样返回")
	}
}
