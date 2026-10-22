package ui

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// transcript_pager_truncated_test.go —— T10（A）：冷区读回降级必须在界面上可见。
//
// 语义：开启逐出后，若写穿 store 不可用/校验失败，pager 只能显示驻留窗口。
// 这种"少显示"必须显式标注，否则用户会把它误读成"历史丢了"（设计 §14.9）。

func truncatedPagerFixture(cells int, truncated bool) TranscriptPagerModel {
	model := TranscriptPagerModel{Revision: 1, Truncated: truncated}
	for index := 1; index <= cells; index++ {
		model.Cells = append(model.Cells, scene.TranscriptCell{
			ID:       scene.CellID(index),
			Sequence: uint64(index),
			Revision: 1,
			Kind:     scene.KindAssistant,
			Source:   "row " + strings.Repeat("x", index),
			Phase:    scene.CellCommitted,
		})
	}
	return model
}

func TestTranscriptPagerTitleMarksTruncation(t *testing.T) {
	state := NewTranscriptPagerState()
	// height 足够容纳标题 + 内容 + 底栏。
	truncated := renderTranscriptPagerFrame(truncatedPagerFixture(5, true), state, 80, 20)
	if !strings.Contains(truncated, "early history unavailable") {
		t.Fatalf("冷区降级时标题栏必须标注；frame=%q", truncated)
	}
	healthy := renderTranscriptPagerFrame(truncatedPagerFixture(5, false), state, 80, 20)
	if strings.Contains(healthy, "early history unavailable") {
		t.Fatalf("正常读回时不得出现降级标注；frame=%q", healthy)
	}
}

// TestTranscriptPagerViewCarriesTruncation 锁定 view → model 的传递链路
// （生产 pager 的冷区读回失败时设置 PagerTruncated，渲染层据此标注）。
func TestTranscriptPagerViewCarriesTruncation(t *testing.T) {
	view := TranscriptPagerView{PagerTruncated: true}
	model := NewTranscriptPagerModel(view.Snapshot)
	if model.Truncated {
		t.Fatal("NewTranscriptPagerModel 不得自行推断降级（由调用方显式设置）")
	}
	model.Truncated = view.PagerTruncated
	if !model.Truncated {
		t.Fatal("view 的降级标记必须传到 model")
	}
	frame := renderTranscriptPagerFrame(model, NewTranscriptPagerState(), 80, 20)
	if !strings.Contains(frame, "early history unavailable") {
		t.Fatalf("传递后的降级标记未渲染；frame=%q", frame)
	}
}