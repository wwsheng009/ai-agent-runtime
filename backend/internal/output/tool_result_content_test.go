package output

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

func TestRenderFullToolResultContent(t *testing.T) {
	testCases := []struct {
		name    string
		content interface{}
		toolErr string
		want    string
	}{
		{
			name:    "raw text stays intact",
			content: "line 1\nline 2\nline 3",
			want:    "line 1\nline 2\nline 3",
		},
		{
			name:    "empty success becomes plain text",
			content: "",
			want:    "Tool returned no output.",
		},
		{
			name:    "error without output becomes failure line",
			toolErr: "exit status 1",
			want:    "Tool execution failed: exit status 1",
		},
		{
			name:    "error keeps full output",
			content: "stderr line 1\nstderr line 2",
			toolErr: "exit status 1",
			want:    "Tool execution failed: exit status 1\nstderr line 1\nstderr line 2",
		},
		{
			name: "structured output becomes json",
			content: map[string]interface{}{
				"success": true,
				"id":      "job-1",
			},
			want: "{\n  \"id\": \"job-1\",\n  \"success\": true\n}",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := RenderFullToolResultContent(tc.content, tc.toolErr)
			if got != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, got)
			}
		})
	}
}

func TestRenderToolResultContentForModel_PreservesStructuredEnvelopeSummary(t *testing.T) {
	envelope := &Envelope{
		Summary: "Created team run with 3 teammates and 3 tasks",
		Metadata: map[string]interface{}{
			toolresult.MetadataKey: toolresult.KindStructured,
		},
	}
	got := RenderToolResultContentForModel(map[string]interface{}{
		"team_id": "team-1",
		"task_id": "task-1",
	}, "", envelope)
	if got != "Created team run with 3 teammates and 3 tasks" {
		t.Fatalf("expected envelope summary, got %q", got)
	}
}

func TestRenderToolResultContentForModel_PreservesFullTextWhenExplicitlyMarkedText(t *testing.T) {
	envelope := &Envelope{
		Summary: "line 1",
		Metadata: map[string]interface{}{
			toolresult.MetadataKey: toolresult.KindText,
		},
	}
	got := RenderToolResultContentForModel("line 1\nline 2\nline 3", "", envelope)
	if got != "line 1\nline 2\nline 3" {
		t.Fatalf("expected full text output, got %q", got)
	}
}

func TestRenderToolResultContentForModel_ExternalMCPPreservesFullStructuredOutput(t *testing.T) {
	envelope := &Envelope{
		Summary: "reduced summary only",
		Metadata: map[string]interface{}{
			"mcp_name": "remote-filesystem",
		},
	}
	got := RenderToolResultContentForModel(map[string]interface{}{
		"files": []string{"a.txt", "b.txt", "c.txt"},
		"count": 3,
	}, "", envelope)
	want := "{\n  \"count\": 3,\n  \"files\": [\n    \"a.txt\",\n    \"b.txt\",\n    \"c.txt\"\n  ]\n}"
	if got != want {
		t.Fatalf("expected full structured output, got %q", got)
	}
}

func TestRenderToolResultContentForModel_ToolkitMCPPreservesStructuredSummary(t *testing.T) {
	envelope := &Envelope{
		Summary: "reduced toolkit summary",
		Metadata: map[string]interface{}{
			toolresult.MetadataKey: toolresult.KindStructured,
			"mcp_name":             "toolkit",
		},
	}
	got := RenderToolResultContentForModel(map[string]interface{}{
		"count": 3,
		"files": []string{"a.txt", "b.txt", "c.txt"},
	}, "", envelope)
	if got != "reduced toolkit summary" {
		t.Fatalf("expected reduced toolkit summary, got %q", got)
	}
}
