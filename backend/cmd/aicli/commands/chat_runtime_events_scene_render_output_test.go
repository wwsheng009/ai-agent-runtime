package commands

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// TestSceneEditRendersFullDiff is the end-to-end regression test for the
// user-observed bug: the edit tool's fenced diff never rendered in the aicli
// UI, showing only the 3-line summary. The tool.completed event carried a
// complete render_output, but the Scene data plane (EventEncoder →
// ChangeSetMapper) only read summary/summary_lines. The encoder now prefers
// render_output, so the Scene cell must contain the full diff body.
func TestSceneEditRendersFullDiff(t *testing.T) {
	fullOutput := strings.Join([]string{
		"成功替换了 1 处匹配项",
		"",
		"文件差异:",
		"```diff",
		"--- a/C:/Users/vince/AppData/Local/Temp/ai-edit-test/hello.md",
		"+++ b/C:/Users/vince/AppData/Local/Temp/ai-edit-test/hello.md",
		"@@ -2,6 +2,6 @@",
		" ",
		"-第一行列表项",
		"+第一行列表项（已修改）",
		"```",
	}, "\n")

	bridge := newChatRuntimeEventBridge(&ChatSession{})
	bridge.encodeRenderModelEvent(events.Event{
		Type:     "tool.requested",
		ToolName: "edit",
		Payload: map[string]interface{}{
			"tool_call_id": "call_edit_1",
			"tool_name":    "edit",
			"arg_preview":  "file_path=hello.md new_string=... old_string=...",
		},
	})
	bridge.encodeRenderModelEvent(events.Event{
		Type:     runtimechat.EventToolFinished,
		ToolName: "edit",
		Payload: map[string]interface{}{
			"tool_call_id":              "call_edit_1",
			"logical_tool":              "edit",
			"duration_ms":               int64(15),
			"render_output":             fullOutput,
			"render_output_format":      "markdown",
			"render_output_untruncated": true,
			"summary":                   "成功替换了 1 处匹配项\n文件差异:\n```diff",
			"summary_lines":             []string{"成功替换了 1 处匹配项", "文件差异:", "```diff"},
		},
	})

	cells := bridgeSceneCells(t, bridge)
	if len(cells) != 1 {
		t.Fatalf("cells=%d want 1", len(cells))
	}
	cell := cells[0]
	if cell.Kind != scene.KindToolChain {
		t.Fatalf("cell.Kind=%v want tool chain", cell.Kind)
	}
	if !strings.Contains(cell.Source, "• Completed edit") {
		t.Fatalf("cell.Source missing completed title:\n%s", cell.Source)
	}
	if !strings.Contains(cell.Source, "in 15ms") {
		t.Fatalf("cell.Source missing duration:\n%s", cell.Source)
	}
	for _, want := range []string{
		"成功替换了 1 处匹配项",
		"```diff",
		"--- a/C:/Users/vince/AppData/Local/Temp/ai-edit-test/hello.md",
		"+++ b/C:/Users/vince/AppData/Local/Temp/ai-edit-test/hello.md",
		"-第一行列表项",
		"+第一行列表项（已修改）",
		"```",
	} {
		if !strings.Contains(cell.Source, want) {
			t.Fatalf("cell.Source missing %q:\n%s", want, cell.Source)
		}
	}
	if cell.Presentation.Kind != scene.PresentationDiffSupplement {
		t.Fatalf("cell.Presentation=%+v want diff supplement", cell.Presentation)
	}
}
