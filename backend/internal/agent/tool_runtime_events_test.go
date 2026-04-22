package agent

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/output"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestToolRequestedEventPayloadIncludesArgPreview(t *testing.T) {
	payload := toolRequestedEventPayload(types.ToolCall{
		ID:   "call-1",
		Name: "bash",
		Args: map[string]interface{}{
			"command": "Get-ChildItem -Force",
		},
	}, 3, "trace-1", nil)

	if got := payload["arg_preview"]; got != "command=Get-ChildItem -Force" {
		t.Fatalf("expected command preview, got %#v", got)
	}
	if got := payload["command_text"]; got != "Get-ChildItem -Force" {
		t.Fatalf("expected command text, got %#v", got)
	}
}

func TestToolCompletedEventPayloadPrefersStructuredSummary(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-2",
			Name: "ls",
			Args: map[string]interface{}{"path": "."},
		},
		Output: "目录: .\n\n📁 a/\n📁 b/\n📄 main.go\n\n统计: 1 个文件, 2 个目录",
		Envelope: &output.Envelope{
			Summary: "directory listing summary that should not win",
			Metadata: map[string]interface{}{
				"tool_metadata": map[string]interface{}{
					"file_count": 2,
					"dir_count":  1,
				},
			},
		},
	}, 2, "trace-2", nil)

	summaryLines, ok := payload["summary_lines"].([]string)
	if !ok {
		t.Fatalf("expected summary lines, got %#v", payload["summary_lines"])
	}
	expected := []string{
		"目录: .",
		"📁 a/ · 📁 b/ · 📄 main.go",
		"统计: 1 个文件, 2 个目录",
	}
	if len(summaryLines) != len(expected) {
		t.Fatalf("expected %d summary lines, got %#v", len(expected), summaryLines)
	}
	for i, line := range expected {
		if summaryLines[i] != line {
			t.Fatalf("expected summary line %d to be %q, got %q", i, line, summaryLines[i])
		}
	}
	if got := payload["arg_preview"]; got != "path=." {
		t.Fatalf("expected path preview, got %#v", got)
	}
}

func TestToolCompletedEventPayloadFallsBackToEnvelopeSummary(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-3",
			Name: "bash",
			Args: map[string]interface{}{"command": "git status"},
		},
		Envelope: &output.Envelope{
			Summary: "On branch main\nnothing to commit, working tree clean",
		},
	}, 1, "trace-3", nil)

	summaryLines, ok := payload["summary_lines"].([]string)
	if !ok {
		t.Fatalf("expected summary lines, got %#v", payload["summary_lines"])
	}
	expected := []string{
		"On branch main",
		"nothing to commit, working tree clean",
	}
	if len(summaryLines) != len(expected) {
		t.Fatalf("expected %d summary lines, got %#v", len(expected), summaryLines)
	}
	for i, line := range expected {
		if summaryLines[i] != line {
			t.Fatalf("expected summary line %d to be %q, got %q", i, line, summaryLines[i])
		}
	}
	if got := payload["command_text"]; got != "git status" {
		t.Fatalf("expected command text, got %#v", got)
	}
}

func TestToolCompletedEventPayloadPrefersErrorOverGenericFallbackSummary(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-3b",
			Name: "execute_shell_command",
			Args: map[string]interface{}{"command": "git status"},
		},
		Error: "exit status 128",
		Envelope: &output.Envelope{
			Summary: "Tool execute_shell_command failed before producing output.",
			Error:   "exit status 128",
		},
	}, 1, "trace-3b", nil)

	summaryLines, ok := payload["summary_lines"].([]string)
	if !ok {
		t.Fatalf("expected summary lines, got %#v", payload["summary_lines"])
	}
	expected := []string{"failed: exit status 128"}
	if len(summaryLines) != len(expected) {
		t.Fatalf("expected %d summary lines, got %#v", len(expected), summaryLines)
	}
	for i, line := range expected {
		if summaryLines[i] != line {
			t.Fatalf("expected summary line %d to be %q, got %q", i, line, summaryLines[i])
		}
	}
}

func TestToolCompletedEventPayloadSkipsToolMetadataAppendix(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-4",
			Name: "view",
			Args: map[string]interface{}{"file_path": "README.md"},
		},
		Output: "line 1\nline 2\nline 3\n\nMetadata:\n{\"file_path\":\"README.md\"}",
	}, 1, "trace-4", nil)

	summaryLines, ok := payload["summary_lines"].([]string)
	if !ok {
		t.Fatalf("expected summary lines, got %#v", payload["summary_lines"])
	}
	expected := []string{"line 1", "line 2", "line 3"}
	if len(summaryLines) != len(expected) {
		t.Fatalf("expected %d summary lines, got %#v", len(expected), summaryLines)
	}
	for i, line := range expected {
		if summaryLines[i] != line {
			t.Fatalf("expected summary line %d to be %q, got %q", i, line, summaryLines[i])
		}
	}
}

func TestToolCompletedEventPayloadMergesAwaitingModelHint(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-5",
			Name: "web_search",
			Args: map[string]interface{}{"query": "weather"},
		},
		Output: "result 1\nresult 2",
	}, 1, "trace-5", map[string]interface{}{
		"awaiting_model": true,
	})

	if got := payload["awaiting_model"]; got != true {
		t.Fatalf("expected awaiting_model=true, got %#v", got)
	}
}
