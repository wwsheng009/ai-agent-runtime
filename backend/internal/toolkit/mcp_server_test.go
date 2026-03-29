package toolkit_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ai-gateway/ai-agent-runtime/internal/toolkit"
	"github.com/ai-gateway/ai-agent-runtime/internal/toolkit/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestToolkitMCPServer_ListToolsIncludesSchema(t *testing.T) {
	registry := toolkit.NewRegistry()
	if err := registry.Register(tools.NewLsTool()); err != nil {
		t.Fatalf("register tool failed: %v", err)
	}

	server := toolkit.NewToolkitMCPServer(registry)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server.GetMCPServer()
	}, &mcp.StreamableHTTPOptions{JSONResponse: true})

	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL}, nil)
	if err != nil {
		t.Fatalf("connect failed: %v", err)
	}
	defer session.Close()

	res, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools failed: %v", err)
	}

	var lsTool *mcp.Tool
	for _, tool := range res.Tools {
		if tool != nil && tool.Name == "ls" {
			lsTool = tool
			break
		}
	}
	if lsTool == nil {
		t.Fatalf("expected ls tool, got %v", res.Tools)
	}

	schema, ok := lsTool.InputSchema.(map[string]interface{})
	if !ok || schema == nil {
		t.Fatalf("expected ls InputSchema map, got %T", lsTool.InputSchema)
	}
	props, ok := schema["properties"].(map[string]interface{})
	if !ok || len(props) == 0 {
		t.Fatalf("expected properties in InputSchema, got %v", schema)
	}
	if _, ok := props["path"]; !ok {
		t.Fatalf("expected path property, got %v", props)
	}
}
