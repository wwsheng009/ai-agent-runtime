package output

import (
	"fmt"
	"strings"
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

func TestRenderToolResultContentForModel_TaskOutputPreservesRawStructuredOutput(t *testing.T) {
	envelope := &Envelope{
		ToolName: "task_output",
		Summary:  "Parsed JSON object with 5 keys.",
		Metadata: map[string]interface{}{
			toolresult.MetadataKey: toolresult.KindStructured,
		},
	}
	content := map[string]interface{}{
		"job_id":      "job_ref_42",
		"next_offset": 128,
		"output":      "line 1\nline 2",
		"status":      "completed",
	}

	got := RenderToolResultContentForModel(content, "", envelope)
	want := RenderFullToolResultContent(content, "")

	if got != want {
		t.Fatalf("expected task_output to preserve raw structured output, got %q", got)
	}
	if got == envelope.Summary {
		t.Fatalf("expected task_output to bypass envelope summary, got %q", got)
	}
	if !strings.Contains(got, `"job_id": "job_ref_42"`) {
		t.Fatalf("expected job_id in raw output, got %q", got)
	}
	if !strings.Contains(got, `"next_offset": 128`) {
		t.Fatalf("expected next_offset in raw output, got %q", got)
	}
	if !strings.Contains(got, `"output": "line 1\nline 2"`) {
		t.Fatalf("expected output payload in raw output, got %q", got)
	}
	if !strings.Contains(got, `"status": "completed"`) {
		t.Fatalf("expected status in raw output, got %q", got)
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

func TestRenderToolResultContentForModel_TruncatesLargeToolkitTextForHistory(t *testing.T) {
	envelope := &Envelope{
		Metadata: map[string]interface{}{
			toolresult.MetadataKey: toolresult.KindText,
			"mcp_name":             "toolkit",
		},
	}
	var builder strings.Builder
	for i := 0; i < 600; i++ {
		builder.WriteString(fmt.Sprintf("line-%03d-0123456789abcdefghijklmnopqrstuvwxyz\n", i))
	}
	content := builder.String()

	got := RenderToolResultContentForModel(content, "", envelope)

	if got == content {
		t.Fatal("expected large toolkit text to be truncated for model history")
	}
	if !strings.Contains(got, "Total output lines: 600") {
		t.Fatalf("expected total line count header, got %q", got)
	}
	if !strings.Contains(got, "output truncated for history safety") {
		t.Fatalf("expected truncation marker, got %q", got)
	}
	if !strings.Contains(got, "line-000-") {
		t.Fatalf("expected output head to be preserved, got %q", got)
	}
	if !strings.Contains(got, "line-599-") {
		t.Fatalf("expected output tail to be preserved, got %q", got)
	}
	if len(got) <= modelToolTextByteBudget/2 {
		t.Fatalf("expected meaningful head/tail payload, got only %d bytes", len(got))
	}
}

func TestRenderToolResultContentForModel_PreservesArtifactNoticeWhenToolkitTextIsTruncated(t *testing.T) {
	envelope := &Envelope{
		Metadata: map[string]interface{}{
			toolresult.MetadataKey:     toolresult.KindText,
			"mcp_name":                 "toolkit",
			"raw_output_artifact_path": `C:\temp\shell-output\toolkit\git_123.txt`,
		},
	}
	content := strings.Repeat("git-diff-line-abcdefghijklmnopqrstuvwxyz0123456789\n", 600)

	got := RenderToolResultContentForModel(content, "", envelope)

	if !strings.Contains(got, "output truncated for history safety") {
		t.Fatalf("expected truncation marker, got %q", got)
	}
	if !strings.Contains(got, `Full raw output artifact: C:\temp\shell-output\toolkit\git_123.txt`) {
		t.Fatalf("expected artifact notice to be preserved, got %q", got)
	}
}

func TestRenderToolResultContentForModel_TruncatesLargeErrorOutputForHistory(t *testing.T) {
	envelope := &Envelope{
		Metadata: map[string]interface{}{
			toolresult.MetadataKey: toolresult.KindText,
		},
	}
	content := strings.Repeat("stderr detail line for failure\n", 700)

	got := RenderToolResultContentForModel(content, "exit status 1", envelope)

	if !strings.Contains(got, "Tool execution failed: exit status 1") {
		t.Fatalf("expected failure prefix to be preserved, got %q", got)
	}
	if !strings.Contains(got, "output truncated for history safety") {
		t.Fatalf("expected truncation marker, got %q", got)
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

func TestRenderToolResultContentForModel_ExternalMCPPreservesLargeTextOutput(t *testing.T) {
	envelope := &Envelope{
		Metadata: map[string]interface{}{
			toolresult.MetadataKey: toolresult.KindText,
			"mcp_name":             "remote-filesystem",
		},
	}
	content := strings.Repeat("external-mcp-line\n", 900)

	got := RenderToolResultContentForModel(content, "", envelope)

	if got != strings.TrimSpace(content) {
		t.Fatalf("expected external MCP text to remain full content, got %q", got)
	}
	if strings.Contains(got, "output truncated for history safety") {
		t.Fatalf("did not expect external MCP content to be truncated, got %q", got)
	}
}

func TestRenderToolResultContentForModel_AppendsArtifactNoticeForSmallText(t *testing.T) {
	envelope := &Envelope{
		Metadata: map[string]interface{}{
			toolresult.MetadataKey:     toolresult.KindText,
			"mcp_name":                 "toolkit",
			"raw_output_artifact_path": `C:\temp\shell-output\toolkit\git_456.txt`,
		},
	}

	got := RenderToolResultContentForModel("short output", "", envelope)

	want := "short output\n\nFull raw output artifact: C:\\temp\\shell-output\\toolkit\\git_456.txt"
	if got != want {
		t.Fatalf("expected artifact notice for small text, got %q", got)
	}
}
