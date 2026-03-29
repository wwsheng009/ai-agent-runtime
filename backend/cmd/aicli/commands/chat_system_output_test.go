package commands

import (
	"bytes"
	"testing"
)

func TestChatSystemOutputWriter_IndentsEachCompletedLine(t *testing.T) {
	var output bytes.Buffer
	writer := newChatSystemOutputWriter(&output)

	if _, err := writer.Write([]byte("[Manager] MCP 已启动: toolkit (工具: 13)\n[Manager] 加载工具失败: x\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	rendered := output.String()
	for _, expected := range []string{
		"    [Manager] MCP 已启动: toolkit (工具: 13)",
		"    [Manager] 加载工具失败: x",
	} {
		if !bytes.Contains([]byte(rendered), []byte(expected)) {
			t.Fatalf("expected rendered output to contain %q, got %q", expected, rendered)
		}
	}
}
