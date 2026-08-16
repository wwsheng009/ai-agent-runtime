package encoding

import (
	"strings"
	"testing"
)

// TestToolFinishedText_PrefersRenderOutput reproduces the reported aicli UI
// bug: edit/apply_patch tool results rendered only the 3-line summary
// ("成功替换了 1 处匹配项 / 文件差异: / ```diff") instead of the full fenced
// diff, because the encoder's toolFinishedText read summary/summary_lines and
// never render_output — even though the tool.completed payload carried the
// complete diff in render_output.
func TestToolFinishedText_PrefersRenderOutput(t *testing.T) {
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

	ev := event("tool.completed", map[string]interface{}{
		"tool_call_id":            "call_edit_1",
		"logical_tool":            "edit",
		"render_output":           fullOutput,
		"render_output_format":    "markdown",
		"render_output_untruncated": true,
		"summary":                 "成功替换了 1 处匹配项\n文件差异:\n```diff",
		"summary_lines":           []string{"成功替换了 1 处匹配项", "文件差异:", "```diff"},
	})

	if got := toolFinishedText(ev); got != fullOutput {
		t.Fatalf("toolFinishedText = %q, want full render_output", got)
	}
}

// TestToolFinishedText_NoRenderOutputKeepsLegacyFallback guards the
// non-regression property: events without render_output (write/view/shell and
// old persisted events) must keep the legacy output → result → summary →
// summary_lines resolution order unchanged.
func TestToolFinishedText_NoRenderOutputKeepsLegacyFallback(t *testing.T) {
	cases := []struct {
		name    string
		payload map[string]interface{}
		want    string
	}{
		{"output wins", map[string]interface{}{"output": "out", "summary": "sum", "summary_lines": []string{"s1"}}, "out"},
		{"result wins", map[string]interface{}{"result": "res", "summary": "sum"}, "res"},
		{"summary wins", map[string]interface{}{"summary": "sum"}, "sum"},
		{"summary_lines last", map[string]interface{}{"summary_lines": []string{"a", "b"}}, "a\nb"},
		{"empty render_output falls through", map[string]interface{}{"render_output": "", "summary": "sum"}, "sum"},
		{"no output at all", map[string]interface{}{}, ""},
	}
	for _, tc := range cases {
		ev := event("tool.completed", tc.payload)
		if got := toolFinishedText(ev); got != tc.want {
			t.Fatalf("case %q: toolFinishedText = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestEncoderEditToolCompleted_RendersFullDiff covers the end-to-end encoder
// path for the user-observed edit event: the Scene item must carry the full
// diff body, not the 3-line summary.
func TestEncoderEditToolCompleted_RendersFullDiff(t *testing.T) {
	fullOutput := strings.Join([]string{
		"成功替换了 1 处匹配项",
		"",
		"文件差异:",
		"```diff",
		"--- a/hello.md",
		"+++ b/hello.md",
		"@@ -2,6 +2,6 @@",
		" ",
		"-第一行列表项",
		"+第一行列表项（已修改）",
		"```",
	}, "\n")

	e := NewEventEncoder()
	e.Encode(event("tool.requested", map[string]interface{}{
		"tool_call_id": "call_edit_1",
		"tool_name":    "edit",
		"arg_preview":  "file_path=hello.md new_string=... old_string=...",
	}))
	e.Encode(event("tool.completed", map[string]interface{}{
		"tool_call_id":              "call_edit_1",
		"logical_tool":              "edit",
		"duration_ms":               uint64(15),
		"render_output":             fullOutput,
		"render_output_format":      "markdown",
		"render_output_untruncated": true,
		"summary":                   "成功替换了 1 处匹配项\n文件差异:\n```diff",
		"summary_lines":             []string{"成功替换了 1 处匹配项", "文件差异:", "```diff"},
	}))

	items := e.Snapshot().Items
	if len(items) == 0 {
		t.Fatal("no items in encoder snapshot")
	}
	head := items[0].Head
	if !strings.Contains(head, "• Completed edit") {
		t.Fatalf("head = %q, want Completed edit title", head)
	}
	if !strings.Contains(head, "in 15ms") {
		t.Fatalf("head = %q, want duration suffix", head)
	}
	var allText strings.Builder
	for _, item := range items {
		allText.WriteString(item.Head)
		allText.WriteString("\n")
	}
	got := allText.String()
	for _, want := range []string{
		"成功替换了 1 处匹配项",
		"文件差异:",
		"```diff",
		"--- a/hello.md",
		"+++ b/hello.md",
		"-第一行列表项",
		"+第一行列表项（已修改）",
		"```",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("encoder output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "successfully created") {
		t.Fatalf("unexpected content:\n%s", got)
	}
}
