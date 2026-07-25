package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/buildinfo"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

func TestFetchTool_Interface(t *testing.T) {
	tool := NewFetchTool()

	var _ toolkit.Tool = tool

	if tool.Name() != "fetch" {
		t.Errorf("expected name 'fetch', got '%s'", tool.Name())
	}

	if tool.Description() == "" {
		t.Error("description should not be empty")
	}

	if !tool.CanDirectCall() {
		t.Error("fetch tool should support direct call")
	}
}

func TestFetchTool_MissingURL(t *testing.T) {
	tool := NewFetchTool()

	result, err := tool.Execute(context.Background(), map[string]interface{}{})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Success {
		t.Error("expected failure for missing URL")
	}
}

func TestFetchTool_InvalidURL(t *testing.T) {
	tool := NewFetchTool()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"url": "not-a-valid-url",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Success {
		t.Error("expected failure for invalid URL")
	}
}

func TestFetchTool_UsesDefaultUserAgent(t *testing.T) {
	var gotUA string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	tool := NewFetchTool()
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"url":    server.URL,
		"format": "text",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}
	if gotUA != buildinfo.UserAgent() {
		t.Fatalf("User-Agent = %q, want %q", gotUA, buildinfo.UserAgent())
	}
}

func TestFetchTool_DescriptionGuidesURLSplitting(t *testing.T) {
	tool := NewFetchTool()

	desc := tool.Description()
	if !strings.Contains(desc, "拆分") || !strings.Contains(desc, "每次只处理一个 URL") {
		t.Fatalf("expected fetch description to guide URL splitting, got %q", desc)
	}

	params := tool.Parameters()
	props, ok := params["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected properties in schema, got %#v", params)
	}
	urlSchema, ok := props["url"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected url schema in properties, got %#v", props)
	}
	urlDesc, _ := urlSchema["description"].(string)
	if !strings.Contains(urlDesc, "拆分") || !strings.Contains(urlDesc, "每次只处理一个 URL") {
		t.Fatalf("expected url description to guide URL splitting, got %q", urlDesc)
	}
}
