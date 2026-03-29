package commands

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/registry"
)

func TestBuildMCPToolOutputs(t *testing.T) {
	outputs := buildMCPToolOutputs([]*registry.ToolInfo{
		{
			MCPName: "server-b",
			Enabled: true,
			Tool: &protocol.Tool{
				Name:        "beta",
				Description: "beta tool",
				InputSchema: map[string]interface{}{"type": "object"},
			},
		},
		{
			MCPName: "server-a",
			Enabled: true,
			Tool: &protocol.Tool{
				Name:        "alpha",
				Description: "alpha tool",
			},
		},
	})

	if len(outputs) != 2 {
		t.Fatalf("expected 2 outputs, got %d", len(outputs))
	}
	if outputs[0].MCPName != "server-a" || outputs[0].Name != "alpha" {
		t.Fatalf("expected sorted alpha output first, got %+v", outputs[0])
	}
	if outputs[1].MCPName != "server-b" || outputs[1].Name != "beta" {
		t.Fatalf("expected beta output second, got %+v", outputs[1])
	}
	if outputs[1].InputSchema["type"] != "object" {
		t.Fatalf("expected input schema to be preserved, got %+v", outputs[1])
	}
}

func TestRenderMCPEmptyResult_JSONEnvelope(t *testing.T) {
	stdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w

	renderMCPEmptyResult(mcpCommandOptions{OutputFormat: "json", JSONEnvelope: true}, "")

	_ = w.Close()
	os.Stdout = stdout
	data, _ := io.ReadAll(r)
	_ = r.Close()

	var payload map[string]interface{}
	if err := json.Unmarshal(bytes.TrimSpace(data), &payload); err != nil {
		t.Fatalf("expected json payload, got %q (%v)", string(data), err)
	}
	if payload["ok"] != true || payload["command"] != "mcp" {
		t.Fatalf("unexpected envelope payload: %#v", payload)
	}
	items, ok := payload["data"].([]interface{})
	if !ok || len(items) != 0 {
		t.Fatalf("expected empty data array, got %#v", payload["data"])
	}
}
