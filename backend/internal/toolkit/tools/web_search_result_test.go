package tools

import (
	"context"
	"strings"
	"testing"
)

func TestWebSearchResultCapAndSourcesMetadata(t *testing.T) {
	tool := NewWebSearchTool()
	results := []DuckDuckGoResult{
		{Title: "A", URL: "https://a.example/1", Snippet: "s1", PublishedAt: "2026-10-01"},
		{Title: "B", URL: "https://b.example/2", Snippet: "s2"},
		{Title: "C", URL: "https://c.example/3", Snippet: "s3"},
	}

	capped := tool.successSearchResultWithCounts("q", results, "bing", webSearchDomainFilter{}, 3, 0, 2)
	if capped.Metadata["truncated"] != true {
		t.Fatalf("expected truncated metadata for over-returning provider: %#v", capped.Metadata)
	}
	sources, _ := capped.Metadata["sources"].([]map[string]interface{})
	if len(sources) != 2 {
		t.Fatalf("sources = %#v, want 2 entries", capped.Metadata["sources"])
	}
	if sources[0]["url"] != "https://a.example/1" || sources[0]["title"] != "A" || sources[0]["published_at"] != "2026-10-01" {
		t.Fatalf("first source projection = %#v", sources[0])
	}
	if _, present := sources[1]["published_at"]; present {
		t.Fatalf("absent publishedAt must stay omitted: %#v", sources[1])
	}
	if strings.Contains(capped.Content, "**C**") {
		t.Fatalf("rendered text must honor the count cap: %q", capped.Content)
	}

	uncapped := tool.successSearchResultWithCounts("q", results, "bing", webSearchDomainFilter{}, 3, 0, 5)
	if _, present := uncapped.Metadata["truncated"]; present {
		t.Fatalf("truncated must stay unset when the provider respects the cap: %#v", uncapped.Metadata)
	}
	allSources, _ := uncapped.Metadata["sources"].([]map[string]interface{})
	if len(allSources) != 3 {
		t.Fatalf("sources = %d, want 3", len(allSources))
	}
}

func TestWebSearchExecutionEnforcesCountBoundary(t *testing.T) {
	tool := NewWebSearchTool()
	tool.providers = []webSearchProvider{{name: "fake", search: func(context.Context, string, int) (string, []DuckDuckGoResult, error) {
		// Deliberately ignores the requested count.
		return "fake", []DuckDuckGoResult{
			{Title: "T1", URL: "https://one.example/1"},
			{Title: "T2", URL: "https://two.example/2"},
			{Title: "T3", URL: "https://three.example/3"},
		}, nil
	}}}

	result, err := tool.Execute(context.Background(), map[string]interface{}{"query": "boundary", "count": float64(2)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got: %v", result.Error)
	}
	if result.Metadata["truncated"] != true {
		t.Fatalf("truncated metadata missing: %#v", result.Metadata)
	}
	sources, _ := result.Metadata["sources"].([]map[string]interface{})
	if len(sources) != 2 {
		t.Fatalf("sources = %d, want 2", len(sources))
	}
	if strings.Contains(result.Content, "T3") {
		t.Fatalf("third result leaked past the count boundary: %q", result.Content)
	}
}

func TestBingRSSParsingPreservesPubDate(t *testing.T) {
	feed := `<rss version="2.0"><channel><item>` +
		`<title>RSS 标题</title>` +
		`<link>https://rss.example/1</link>` +
		`<description>摘要 &amp; more</description>` +
		`<pubDate>Wed, 15 Oct 2026 10:00:00 GMT</pubDate>` +
		`</item></channel></rss>`
	results, err := parseBingRSSResults(feed, 5)
	if err != nil {
		t.Fatalf("parseBingRSSResults: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	if results[0].PublishedAt != "Wed, 15 Oct 2026 10:00:00 GMT" {
		t.Fatalf("PublishedAt = %q", results[0].PublishedAt)
	}
	if results[0].Snippet != "摘要 & more" {
		t.Fatalf("Snippet = %q", results[0].Snippet)
	}
}

func TestNativeMappingCarriesPageAge(t *testing.T) {
	resp := &nativeAnthropicResponse{Content: []nativeAnthropicContentBlock{{
		Type: "web_search_tool_result",
		Content: []nativeAnthropicSearchResult{{
			Type:    "web_search_result",
			URL:     "https://native.example/1",
			Title:   "N",
			PageAge: "2026-10-01",
		}},
	}}}
	results, err := mapNativeAnthropicResponse(resp, 5)
	if err != nil {
		t.Fatalf("mapNativeAnthropicResponse: %v", err)
	}
	if len(results) != 1 || results[0].PublishedAt != "2026-10-01" {
		t.Fatalf("page_age not preserved: %#v", results)
	}
}
