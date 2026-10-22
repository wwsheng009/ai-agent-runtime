package toolkit

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const benchPayloadBytes = 32 * 1024

type mcpBenchTool struct {
	*BaseTool
	content string
}

func (t *mcpBenchTool) Execute(context.Context, map[string]interface{}) (*ToolResult, error) {
	return &ToolResult{
		Success:    true,
		OutputKind: "text",
		Content:    t.content,
		Metadata:   map[string]interface{}{"payload_bytes": len(t.content)},
	}, nil
}

func newMCPBenchTool() *mcpBenchTool {
	return &mcpBenchTool{
		BaseTool: NewBaseTool(
			"bench_32k",
			"32 KiB text fixture",
			"1.0.0",
			map[string]interface{}{
				"type":                 "object",
				"properties":           map[string]interface{}{},
				"additionalProperties": false,
			},
			true,
		),
		content: strings.Repeat("x", benchPayloadBytes),
	}
}

func newMCPBenchSession(b *testing.B) (*mcp.ClientSession, func(), *mcpBenchTool) {
	b.Helper()
	tool := newMCPBenchTool()
	registry := NewRegistry()
	if err := registry.Register(tool); err != nil {
		b.Fatalf("register: %v", err)
	}
	server := NewToolkitMCPServer(registry)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	go func() { _ = server.GetMCPServer().Run(ctx, serverTransport) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "bench-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		cancel()
		b.Fatalf("connect: %v", err)
	}
	cleanup := func() {
		_ = session.Close()
		cancel()
	}
	return session, cleanup, tool
}

// BenchmarkInProcess32KiB is the baseline: direct tool execution with the same
// 32 KiB payload, no MCP framing.
func BenchmarkInProcess32KiB(b *testing.B) {
	tool := newMCPBenchTool()
	b.ReportAllocs()
	b.SetBytes(benchPayloadBytes)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err := tool.Execute(context.Background(), nil)
		if err != nil || result == nil || len(result.Content) != benchPayloadBytes {
			b.Fatalf("unexpected result: %v", err)
		}
	}
}

// BenchmarkMCPConvert32KiB measures the legacy ToolResult -> MCP content
// conversion without transport framing.
func BenchmarkMCPConvert32KiB(b *testing.B) {
	tool := newMCPBenchTool()
	result, err := tool.Execute(context.Background(), nil)
	if err != nil {
		b.Fatalf("execute: %v", err)
	}
	b.ReportAllocs()
	b.SetBytes(benchPayloadBytes)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		converted := buildMCPCallToolResult(result)
		if converted == nil || len(converted.Content) == 0 {
			b.Fatal("empty conversion")
		}
	}
}

// BenchmarkMCPRoundTrip32KiB measures a full in-memory MCP session call for
// the same 32 KiB payload, including JSON-RPC framing and SDK marshaling.
func BenchmarkMCPRoundTrip32KiB(b *testing.B) {
	session, cleanup, _ := newMCPBenchSession(b)
	defer cleanup()
	ctx := context.Background()
	b.ReportAllocs()
	b.SetBytes(benchPayloadBytes)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "bench_32k"})
		if err != nil {
			b.Fatalf("call: %v", err)
		}
		if result == nil || len(result.Content) == 0 {
			b.Fatal("empty MCP result")
		}
	}
}
