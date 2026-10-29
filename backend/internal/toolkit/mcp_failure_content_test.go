package toolkit

import (
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestBuildMCPCallToolResultIncludesFailureContent(t *testing.T) {
	result := &ToolResult{
		Success: false,
		Content: "Command failed: exit status 3\n\nstderr detail: access denied",
		Error:   errors.New("COMMAND_FAILED: command failed: exit status 3"),
	}

	projected := buildMCPCallToolResult(result)
	if projected == nil || !projected.IsError {
		t.Fatalf("projected result = %+v, want IsError", projected)
	}
	if len(projected.Content) != 1 {
		t.Fatalf("content count = %d, want 1", len(projected.Content))
	}
	textContent, ok := projected.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content type = %T, want *mcp.TextContent", projected.Content[0])
	}
	for _, want := range []string{"COMMAND_FAILED", "exit status 3", "stderr detail: access denied"} {
		if !strings.Contains(textContent.Text, want) {
			t.Fatalf("projected text lacks %q:\n%s", want, textContent.Text)
		}
	}
}

func TestBuildMCPCallToolResultDoesNotDuplicateIdenticalFailureText(t *testing.T) {
	message := "same failure"
	projected := buildMCPCallToolResult(&ToolResult{
		Success: false,
		Content: message,
		Error:   errors.New(message),
	})
	textContent := projected.Content[0].(*mcp.TextContent)
	if strings.Count(textContent.Text, message) != 1 {
		t.Fatalf("failure text duplicated: %q", textContent.Text)
	}
}
