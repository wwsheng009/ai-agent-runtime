package ui

import (
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
)

// TestDiag_DeepDuplicateProbe 诊断：构造贴近真实会话的序列（长回复 + band
// 出现/消失 + prompt 重绘 + 继续追加），把完整渲染字节流回放到 vt.Screen，
// 输出屏幕 dump 与重复检测。仅用于定位深层次重复源，验证后删除。
func TestDiag_DeepDuplicateProbe(t *testing.T) {
	surface := newOwnedTestFixedBottomSurfaceWithSize(80, 24)
	var stream strings.Builder
	capf := func(fn func()) {
		stream.WriteString(captureUIStdout(t, fn))
	}
	line := func(idx int) string {
		return fmt.Sprintf("D-%02d-这是第%02d行正文内容\n", idx, idx)
	}

	capf(func() { surface.ShowPrompt("> ") })
	// 轮 1：30 行长回复（超过一屏）
	for i := 0; i < 30; i++ {
		capf(func() {
			surface.WriteOutput(io.Discard, line(i))
		})
	}
	// band 出现（工具调用流式状态）
	capf(func() { surface.SetActiveBand([]string{"• Running grep"}) })
	// band 消失（shrink 恢复）
	capf(func() { surface.ClearActiveBand() })
	// prompt 重绘（用户输入前清空/重建 prompt）
	capf(func() { surface.ResetPrompt("", 1) })
	capf(func() { surface.ShowPrompt("> ") })
	// 轮 1 尾部追加
	capf(func() {
		surface.WriteOutput(io.Discard, line(30))
	})
	// 轮 2：继续追加 15 行（第二次超屏直写）
	for i := 31; i < 46; i++ {
		capf(func() {
			surface.WriteOutput(io.Discard, line(i))
		})
	}

	screen := vt.NewScreen(80, 24)
	screen.Feed(stream.String())
	t.Logf("=== 屏幕 dump ===\n%s", dumpScreen(screen))

	// 重复检测：每个 D-NN 行在屏幕上最多出现一次
	seen := map[string][]int{}
	for row := 1; row <= screen.Height(); row++ {
		text := strings.TrimSpace(screen.Line(row))
		if !strings.HasPrefix(text, "D-") {
			continue
		}
		seen[text] = append(seen[text], row)
	}
	for text, rows := range seen {
		if len(rows) > 1 {
			t.Errorf("重复渲染: %q 出现在行 %v", text, rows)
		}
	}
}
