// Minimal stdio MCP server used by the ACP × MCP e2e scripts.
//
// It exists because cmd/echo-mcp-server only speaks WebSocket (no stdio), while
// the ACP MCP integration is exercised over stdio transports.
//
// Usage (built by the e2e scripts):
//
//	go build -o <tmp>/acp_e2e_mcp_helper.exe ./scripts/acp_e2e_mcp_stdio_helper.go
//
// Flags:
//
//	-tool    name of the single exposed tool (default acp_e2e_echo)
//	-marker  optional file written as soon as the process starts (negative baseline)
//	-pidfile optional file receiving the process id (child-process reaping checks)
//
// The tool echoes its input as "echo:<text>".
//
//go:build ignore

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	toolName := flag.String("tool", "acp_e2e_echo", "name of the exposed MCP tool")
	marker := flag.String("marker", "", "optional file written at process start")
	pidFile := flag.String("pidfile", "", "optional file receiving the process id")
	flag.Parse()

	if path := strings.TrimSpace(*marker); path != "" {
		if err := os.WriteFile(path, []byte("started\n"), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "marker write failed: %v\n", err)
			os.Exit(3)
		}
	}
	if path := strings.TrimSpace(*pidFile); path != "" {
		if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "pidfile write failed: %v\n", err)
			os.Exit(3)
		}
	}

	server := mcp.NewServer(&mcp.Implementation{Name: "aicli-acp-e2e-mcp", Version: "0.0.1"}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        strings.TrimSpace(*toolName),
		Description: "echo the provided text back to the caller",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input struct {
		Text string `json:"text"`
	}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "echo:" + input.Text}},
		}, nil, nil
	})

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintf(os.Stderr, "stdio MCP server stopped: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}
