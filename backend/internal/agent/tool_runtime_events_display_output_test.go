package agent

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// 回归：非编辑类工具（shell/view/grep/…）的 tool.completed 必须携带真实输出
// 文本，而不是只带 summarizeToolExecutionLines 的 3 行摘要。缺失该字段时编码器
// 的 toolFinishedText 会落到 summary 上，live 单元格固定 3 行且不触发任何折叠
// 标记，pager 展开读到的仍是同一份 3 行（真实实例 session_20260921074434 复现）。
func TestToolCompletedEventPayloadPromotesDisplayOutput(t *testing.T) {
	raw := "1: package cell\n2: \n3: import (\n4: \t\"fmt\"\n5: )\n"
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call:   types.ToolCall{ID: "call-out", Name: "view"},
		Output: raw,
	}, 1, "trace-out", nil)

	got, _ := payload["output"].(string)
	if got != raw {
		t.Fatalf("expected verbatim display output %q, got %q", raw, got)
	}
	if lines, _ := payload["summary_lines"].([]string); len(lines) != 3 {
		t.Fatalf("expected the 3-line summary to stay alongside the output, got %#v", payload["summary_lines"])
	}
	if got == payload["summary"] {
		t.Fatal("output must not collapse back into the 3-line summary")
	}
}

func TestToolCompletedEventPayloadBoundsDisplayOutput(t *testing.T) {
	raw := strings.Repeat("line of tool output\n", 2000)
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call:   types.ToolCall{ID: "call-out-big", Name: "shell"},
		Output: raw,
	}, 1, "trace-out-big", nil)

	got, _ := payload["output"].(string)
	if !strings.HasPrefix(got, "line of tool output\n") {
		t.Fatalf("expected the head of the output to survive, got %q", got[:40])
	}
	wantMarker := fmt.Sprintf("display copy limited to the first %d bytes (display only)", toolCompletedDisplayOutputMaxBytes)
	if !strings.Contains(got, wantMarker) {
		t.Fatalf("expected the display-only cap marker, got tail %q", got[len(got)-90:])
	}
	if len(got) > toolCompletedDisplayOutputMaxBytes+128 {
		t.Fatalf("display output must stay bounded, got %d bytes", len(got))
	}
}

// 字节上界必须落在 rune 边界：多字节字符被劈开会污染终端渲染与 JSON 事件。
func TestToolCompletedDisplayOutputKeepsUTF8Boundary(t *testing.T) {
	raw := strings.Repeat("a", toolCompletedDisplayOutputMaxBytes-1) + "中文内容" + strings.Repeat("z", 64)
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call:   types.ToolCall{ID: "call-out-utf8", Name: "view"},
		Output: raw,
	}, 1, "trace-out-utf8", nil)

	got, _ := payload["output"].(string)
	if !utf8.ValidString(got) {
		t.Fatalf("display output split a UTF-8 rune: %q", got)
	}
	cut := strings.SplitN(got, "\n… display copy", 2)[0]
	if len(cut) != toolCompletedDisplayOutputMaxBytes-1 {
		t.Fatalf("expected the cut to back off to the last rune boundary (%d bytes), got %d", toolCompletedDisplayOutputMaxBytes-1, len(cut))
	}
}

// 编辑类工具的 render_output 仍然优先：它带 fenced diff，不能被普通 output 顶掉。
func TestToolCompletedEventPayloadKeepsRenderOutputPriority(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call:   types.ToolCall{ID: "call-edit", Name: "edit"},
		Output: "--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-old\n+new\n",
	}, 1, "trace-edit", nil)

	if got, _ := payload["render_output"].(string); got == "" {
		t.Fatal("expected render_output for editing tools")
	}
	if _, exists := payload["output"]; exists {
		t.Fatal("editing tools must not also carry the plain output field")
	}
}

func TestToolCompletedEventPayloadOmitsDisplayOutputWhenEmpty(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call:   types.ToolCall{ID: "call-empty", Name: "shell"},
		Output: "   \n",
		Error:  "exit status 1",
	}, 1, "trace-empty", nil)

	if _, exists := payload["output"]; exists {
		t.Fatalf("blank output must not promote an output field: %#v", payload["output"])
	}
	if got, _ := payload["error"].(string); got != "exit status 1" {
		t.Fatalf("expected the failure to stay on the payload, got %#v", payload["error"])
	}
}
