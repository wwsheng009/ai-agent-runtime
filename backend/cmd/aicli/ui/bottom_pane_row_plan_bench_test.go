package ui

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
)

// 底部保留区每帧成本基准。锁住两件事：
//  1. 行数查询链（popupBottomGapRowCount → … → promptNoticeVisibleRowCount）
//     不再为了数行数渲染 status document / 分配切片；
//  2. 逐 rune 宽度测量走 render.RuneWidth，不再每字符分配一个 string。
//
// 运行：go test ./cmd/aicli/ui/ -run XXX -bench BenchmarkBottomPane -benchtime 20000x
var benchBottomSink int

func benchQuestionWaitState() BottomPaneState {
	return bottomPaneMatrixConfig{
		band:       []string{"Running [broker] ask_user_question prompt=…"},
		dynamic:    true,
		promptOn:   true,
		promptRows: 1,
		popup: []string{
			"[提问] 问题：这两个会话用户卡片希望怎么处理？",
			"[提问] 1. 保留现状",
			"[提问] 2. 增加说明",
			"[提问] 3. 增加可移除入口",
			"[提问] 4. 其他（直接输入）",
			"[提问] 提示：直接输入答案",
		},
	}.state()
}

func BenchmarkLayoutBottomPaneRowsQuestionWait(b *testing.B) {
	state := benchQuestionWaitState()
	geometry := GeometryState{Width: 120, Height: 30}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		plan := LayoutBottomPaneRows(state, geometry)
		benchBottomSink = len(plan.Rows)
	}
}

func BenchmarkBottomPaneCountProbeQuestionWait(b *testing.B) {
	derived := DeriveBottomPaneState(benchQuestionWaitState(), GeometryState{Width: 120, Height: 30})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchBottomSink = derived.popupBottomGapRowCount() +
			derived.promptAreaVisibleRowCount() +
			derived.activeBandLayoutRowCount() +
			derived.promptNoticeVisibleRowCount() +
			derived.dynamicStatusVisibleRowCount()
	}
}

func BenchmarkBottomPaneCountProbeComposer(b *testing.B) {
	state := benchQuestionWaitState()
	state.ComposerLine = "> typed answer"
	derived := DeriveBottomPaneState(state, GeometryState{Width: 120, Height: 30})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchBottomSink = derived.popupBottomGapRowCount() +
			derived.promptAreaVisibleRowCount() +
			derived.activeBandLayoutRowCount()
	}
}

// 长输入（composer 场景）下的整帧成本：逐 rune 宽度测量曾经每字符分配一次。
func BenchmarkLayoutBottomPaneRowsLongInput(b *testing.B) {
	state := benchQuestionWaitState()
	state.PromptInput = strings.Repeat("回答内容 mixed ASCII 与中文字符 ", 12)
	state.PromptCursor = len([]rune(state.PromptInput))
	state.PromptCursorKnown = true
	geometry := GeometryState{Width: 120, Height: 30}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		plan := LayoutBottomPaneRows(state, geometry)
		benchBottomSink = len(plan.Rows)
	}
}

// 逐 rune 宽度测量的两种写法对比（旧：DisplayWidth(string(r))）。
func BenchmarkRuneWidthOldForm(b *testing.B) {
	line := []rune(strings.Repeat("回答内容 mixed ASCII 与中文字符 ", 12))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		total := 0
		for _, r := range line {
			total += DisplayWidth(string(r))
		}
		benchBottomSink = total
	}
}

func BenchmarkRuneWidthNewForm(b *testing.B) {
	line := []rune(strings.Repeat("回答内容 mixed ASCII 与中文字符 ", 12))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		total := 0
		for _, r := range line {
			total += render.RuneWidth(r)
		}
		benchBottomSink = total
	}
}

// 宽度规划器（promptInputMaxVisibleRowsForGeometry）每帧调用：它的提示行计数
// 曾经重建整个切片只为取 len。
// 提示行必须非空，否则旧写法的快路径（nil 切片）掩盖了真实成本。
func benchPromptNoticeState() BottomPaneState {
	state := benchQuestionWaitState()
	state.PromptNoticeLine = "排队消息 2 条：第一条中文通知内容\n排队消息 1 条：第二条中文通知内容"
	state.PromptEditorStatusLine = "esc to cancel"
	return state
}

func BenchmarkPromptInputMaxVisibleRowsQuestionWait(b *testing.B) {
	state := benchPromptNoticeState()
	geometry := GeometryState{Width: 120, Height: 30}
	policy := BottomPanePolicyForGeometry(state, geometry)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchBottomSink = promptInputMaxVisibleRowsForGeometry(state, policy)
	}
}

// 提示行计数的两种写法对比（旧：重建切片后取 len）。
func BenchmarkPromptNoticeRowCountOldForm(b *testing.B) {
	state := benchPromptNoticeState()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchBottomSink = len(state.promptNoticeLines())
	}
}

func BenchmarkPromptNoticeRowCountNewForm(b *testing.B) {
	state := benchPromptNoticeState()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchBottomSink = state.promptNoticeLinesRowCount()
	}
}
