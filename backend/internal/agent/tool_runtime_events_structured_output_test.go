package agent

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolprotocol"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// TestExtractToolTextOutput_MCPStructuredOutputs covers tool execution paths
// whose raw output is a structured MCP result instead of a plain string.
// Previously extractToolTextOutput returned "" for these, so
// toolCompletedEventPayload omitted render_output and the apply_patch diff
// (and other fenced diffs) degraded to the 3-line summary in the aicli UI.
func TestExtractToolTextOutput_MCPStructuredOutputs(t *testing.T) {
	diffText := strings.Join([]string{
		"补丁已应用：修改 1；影响 1 个路径",
		"",
		"文件差异:",
		"```diff",
		"--- a/hello.txt",
		"+++ b/hello.txt",
		"@@ -1 +1 @@",
		"-old line",
		"+new line",
		"```",
	}, "\n")

	cases := []struct {
		name string
		in   interface{}
		want string
	}{
		{
			name: "*protocol.CallToolResult",
			in: &protocol.CallToolResult{Content: []protocol.Content{
				{Type: "text", Text: diffText},
			}},
			want: diffText,
		},
		{
			name: "[]protocol.Content",
			in: []protocol.Content{
				{Type: "text", Text: "head\n"},
				{Type: "image", Data: "ignored"},
				{Type: "text", Text: "tail"},
			},
			want: "head\ntail",
		},
		{
			name: "*toolprotocol.Result",
			in: &toolprotocol.Result{Content: []toolprotocol.ContentBlock{
				{Type: "text", Text: diffText},
			}},
			want: diffText,
		},
		{
			name: "[]toolprotocol.ContentBlock",
			in: []toolprotocol.ContentBlock{
				{Type: "text", Text: "a"},
				{Type: "resource", Text: ""},
				{Type: "text", Text: "b"},
			},
			want: "ab",
		},
		{
			name: "map[string]interface{} with content list (JSON-decoded MCP result)",
			in: map[string]interface{}{
				"content": []interface{}{
					map[string]interface{}{"type": "text", "text": "json-decoded\n"},
					map[string]interface{}{"type": "image", "data": "xx"},
					map[string]interface{}{"type": "text", "text": "tail"},
				},
			},
			want: "json-decoded\ntail",
		},
		{
			name: "arbitrary map without content key is ignored",
			in:   map[string]interface{}{"text": "not tool output", "type": "text"},
			want: "",
		},
		{
			name: "nil *protocol.CallToolResult",
			in:   (*protocol.CallToolResult)(nil),
			want: "",
		},
		{
			name: "nil *toolprotocol.Result",
			in:   (*toolprotocol.Result)(nil),
			want: "",
		},
		{
			name: "plain string still works",
			in:   diffText,
			want: diffText,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractToolTextOutput(tc.in); got != tc.want {
				t.Fatalf("extractToolTextOutput(%T) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestToolCompletedPayload_RenderOutputForStructuredMCPResult proves the
// regression fix end to end: a structured MCP output still produces
// render_output on the tool.completed payload, so the aicli diff renderer
// has the full fenced diff to work with.
func TestToolCompletedPayload_RenderOutputForStructuredMCPResult(t *testing.T) {
	diffText := strings.Join([]string{
		"补丁已应用：修改 1；影响 1 个路径",
		"",
		"文件差异:",
		"```diff",
		"--- a/hello.txt",
		"+++ b/hello.txt",
		"@@ -1 +1 @@",
		"-old line",
		"+new line",
		"```",
	}, "\n")

	result := toolExecutionResult{
		Call:   types.ToolCall{ID: "call_1", Name: "apply_patch"},
		Output: &protocol.CallToolResult{Content: []protocol.Content{{Type: "text", Text: diffText}}},
	}
	payload := toolCompletedEventPayload(result, 1, "trace", map[string]interface{}{})
	if payload["render_output"] == nil {
		t.Fatalf("render_output missing for structured MCP output: %v", payload)
	}
	if got, want := payload["render_output"], diffText; got != want {
		t.Fatalf("render_output = %q, want %q", got, want)
	}
	if got, ok := payload["render_output_untruncated"].(bool); !ok || !got {
		t.Fatalf("render_output_untruncated missing or false: %v", payload["render_output_untruncated"])
	}
}
