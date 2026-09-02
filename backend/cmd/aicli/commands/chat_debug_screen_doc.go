package commands

import (
	"fmt"
	"strings"
)

// appendChatDebugScreenLines 在 /debug display 中追加当前屏幕合成帧的摘要：
// 尺寸、行数、以及前几行预览（?format=text 时可通过 /debug/chat/screen 获取完整内容）。
func appendChatDebugScreenLines(builder *chatDebugDocumentBuilder, session *ChatSession) {
	if builder == nil || session == nil {
		return
	}
	builder.heading("屏幕合成帧 (/debug/chat/screen):")
	if session.Surface == nil {
		builder.meta("Surface:", "<none>")
		return
	}
	frame := session.Surface.ComposedFrameForTest()
	if len(frame) == 0 {
		builder.meta("Frame:", "<empty>")
		return
	}
	width := 0
	if len(frame[0]) > 0 {
		width = len(frame[0])
	}
	builder.meta("Dimensions:", fmt.Sprintf("%d×%d", width, len(frame)))

	// 预览前 5 行（去除行尾空白）
	previewRows := 5
	if len(frame) < previewRows {
		previewRows = len(frame)
	}
	builder.plain("  Preview (first " + fmt.Sprintf("%d", previewRows) + " rows):")
	for _, row := range frame[:previewRows] {
		var sb strings.Builder
		for _, cell := range row {
			if !cell.Cont {
				sb.WriteString(cell.Text)
			}
		}
		line := strings.TrimRight(sb.String(), " ")
		if line == "" {
			line = "␣" // 空白行标记
		}
		builder.plain("    |" + line + "|")
	}
}
