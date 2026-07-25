package tools

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestWebSearchTool_EmptySearchMarksEmptySuccess(t *testing.T) {
	tool := NewWebSearchTool()
	tool.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			host := req.URL.Host
			path := req.URL.Path
			switch {
			case strings.Contains(host, "api.duckduckgo.com"):
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"AbstractText":"","Heading":"","RelatedTopics":[]}`)),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			case strings.Contains(host, "html.duckduckgo.com") || strings.Contains(path, "/html"):
				// Empty HTML payload: zero hits is success, not transport failure.
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`<html><body></body></html>`)),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			default:
				t.Fatalf("unexpected request host/path: %s %s", host, path)
				return nil, nil
			}
		}),
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query": "unlikely_token_xyz_no_hits",
		"count": float64(3),
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

func TestWebSearchTool_EmptyInstantKeepsEmptyWhenHTMLFails(t *testing.T) {
	tool := NewWebSearchTool()
	tool.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if strings.Contains(req.URL.Host, "api.duckduckgo.com") {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"AbstractText":"","Heading":"","RelatedTopics":[]}`)),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusBadGateway,
				Body:       io.NopCloser(strings.NewReader("upstream unavailable")),
				Header:     make(http.Header),
				Request:    req,
			}, nil
		}),
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query": "unlikely_token_xyz_no_hits",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected empty success from Instant Answer, got error %v", result.Error)
	}
	if result.Metadata["source"] != "duckduckgo" {
		t.Fatalf("expected source=duckduckgo empty success, got %#v", result.Metadata)
	}
	if result.Metadata[toolresult.MetadataOutcomeKey] != toolresult.OutcomeEmpty {
		t.Fatalf("expected outcome=empty, got %#v", result.Metadata)
	}
}

func TestWebSearchTool_DescriptionGuidesQuerySplitting(t *testing.T) {
	tool := NewWebSearchTool()

	desc := tool.Description()
	if !strings.Contains(desc, "拆分") || !strings.Contains(desc, "每次只聚焦一个搜索目标") {
		t.Fatalf("expected web_search description to guide query splitting, got %q", desc)
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
