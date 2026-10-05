package ui

import (
	"reflect"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// TestTranscriptCellsNotMutatedInPlaceByReducers 守护 P1.2 的承载不变量：生产
// reducer 不得原地修改既有 Transcript.Cells 的底层数组。transcriptPlanSnapshot
// 直接别名该切片（值切片共享底层数组），一旦有原地写者，worker 侧快照会读到
// 撕裂数据。
//
// 做法：持有旧切片本身（不拷贝元素），另存逐 cell 值拷贝；跑一串会改 transcript
// 的 reducer 后断言旧切片可见元素与拷贝一致。append 复用底层数组只会写在 len
// 之外，不影响旧切片可见部分——这正是允许的行为。
func TestTranscriptCellsNotMutatedInPlaceByReducers(t *testing.T) {
	state := reduceUIControllerState(UIControllerState{}, Resize{Width: 90, Height: 40, Generation: 1}, 1)
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: resumeParitySnapshot()}, 2)

	old := state.Transcript.Cells
	backup := append([]scene.TranscriptCell(nil), old...)

	// 1) 语义替换：修改既有 cell 的 revision（触发 clone-suffix 重建路径）。
	modified := resumeParitySnapshot()
	modified.Revision++
	if len(modified.Cells) > 1 {
		modified.Cells[1].Revision++
	}
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: modified}, 3)

	// 2) 几何变化（布局/主题路径）。
	state = reduceUIControllerState(state, Resize{Width: 100, Height: 30, Generation: 2}, 4)

	// 3) armed replay（显式失效 + 重规划路径）。
	replay := resumeParitySnapshot()
	replay.Revision += 2
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: replay, ArmScrollbackReplay: true}, 5)

	if len(old) != len(backup) {
		t.Fatalf("旧切片长度被改写：%d → %d", len(backup), len(old))
	}
	for index := range backup {
		if !reflect.DeepEqual(old[index], backup[index]) {
			t.Fatalf("transcript cell %d 被原地修改：\n快照时: %+v\n现在:   %+v",
				index, backup[index], old[index])
		}
	}
}
