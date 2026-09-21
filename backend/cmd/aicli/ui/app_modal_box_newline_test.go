package ui

import (
	"strings"
	"testing"
)

// 回归（生产实测 session_20260920131756_C3nAwhVX）：提问正文来自 LLM，可能是多段
// 文本（含硬换行）。popup 行是“一行一个物理行”的契约：行内换行会被终端渲染成多行，
// 而底区行计划只按一行计数——盒子边框只出现在折行的首/尾，盒子高度与后续 Running /
// Waiting / prompt 行的位置整体错位，卡片在物理屏上因此无法成形（用户只看到 Running
// 行与 Waiting 行）。
func TestModalBoxLinesSplitsEmbeddedNewlines(t *testing.T) {
	const width, height = 156, 44
	lines := []string{
		"[提问] Agent 需要你的补充信息",
		"[提问] 问题：报告 §7.1 的清理已执行 3 项：§7.1.2（app/app.go 死代码）、§7.1.3（本地噪声）、" +
			"以及 §7.2.1（失效文档链接）。剩下两项需要你定夺：\n\n【§7.1.4】把 `docsArchive/**/*.go`" +
			"（82 文件 / 17,911 行，且 git 已跟踪）移出模块目录。",
		"[提问] 1. A + runtime/compute 暂不做",
		"[提问] 2. B + runtime/compute 暂不做",
		"[提问] 请输入回答，可输入建议编号（必答）：",
	}
	bottom := BottomPaneState{PopupOwner: modalBoxTestOwner, PopupLines: lines}

	box := modalBoxLines(bottom, height, width)
	if len(box) == 0 {
		t.Fatal("expected a bordered box for a multi-paragraph question")
	}
	boxWidth := DisplayWidth(box[0])
	for i, row := range box {
		if strings.ContainsAny(row, "\r\n") {
			t.Fatalf("box row %d carries an embedded newline (terminal would wrap it):\n%s",
				i, strings.Join(box, "\n"))
		}
		if got := DisplayWidth(row); got != boxWidth {
			t.Fatalf("box row %d width=%d, want %d (box must stay rectangular):\n%s",
				i, got, boxWidth, strings.Join(box, "\n"))
		}
	}
	if maxRows := ModalBoxMaxRows(height); len(box) > maxRows {
		t.Fatalf("box rows %d exceed the terminal ceiling %d", len(box), maxRows)
	}
	// 多段正文的每一段都必须落在盒子内，不能被换行吃掉。
	for _, want := range []string{"【§7.1.4】把", "移出模块目录。"} {
		if !strings.Contains(strings.Join(box, "\n"), want) {
			t.Fatalf("box lost question segment %q:\n%s", want, strings.Join(box, "\n"))
		}
	}
}
