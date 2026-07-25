package tools

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

func TestSourcegraphTool_EmptySearchMarksEmptySuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if !strings.Contains(string(body), "unlikely_token_xyz") {
			t.Fatalf("expected query payload in request, got %s", string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": {
				"search": {
					"results": {
						"results": [],
						"limitHit": false,
						"approximateResultCount": "0"
					}
				}
			}
		}`))
	}))
	defer server.Close()

	tool := NewSourcegraphTool()
	tool.baseURL = server.URL
	tool.httpClient = server.Client()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query": "unlikely_token_xyz",
		"count": float64(5),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected empty-search success, got error %v", result.Error)
	}
	if result.Metadata["match_count"] != 0 || result.Metadata["returned_count"] != 0 {
		t.Fatalf("expected zero match counts, got %#v", result.Metadata)
	}
	if result.Metadata[toolresult.MetadataEmptyResultKey] != true {
		t.Fatalf("expected empty_result=true, got %#v", result.Metadata)
	}
	if result.Metadata[toolresult.MetadataOutcomeKey] != toolresult.OutcomeEmpty {
		t.Fatalf("expected outcome=empty, got %#v", result.Metadata)
	}
	if !strings.Contains(result.Content, "未找到") {
		t.Fatalf("expected no-match content, got %q", result.Content)
	}
}

func TestSourcegraphTool_DescriptionGuidesQuerySplitting(t *testing.T) {
	tool := NewSourcegraphTool()

	desc := tool.Description()
	if !strings.Contains(desc, "拆分") || !strings.Contains(desc, "每次只聚焦一个搜索目标") {
		t.Fatalf("expected sourcegraph description to guide query splitting, got %q", desc)
	}

	params := tool.Parameters()
	props, ok := params["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected properties in schema, got %#v", params)
	}
	querySchema, ok := props["query"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected query schema in properties, got %#v", props)
	}
	queryDesc, _ := querySchema["description"].(string)
	if !strings.Contains(queryDesc, "拆分") || !strings.Contains(queryDesc, "每次只聚焦一个搜索目标") {
		t.Fatalf("expected query description to guide query splitting, got %q", queryDesc)
	}
}
