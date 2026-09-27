package tools

import (
	"context"
	"strings"
	"testing"
)

func TestNormalizeWebSearchDomain(t *testing.T) {
	cases := map[string]string{
		"Example.com":              "example.com",
		"  example.com.  ":         "example.com",
		"*.example.com":            "example.com",
		"https://Example.com/docs": "example.com",
		"example.com:8443/path":    "example.com",
		"":                         "",
		"*.":                       "",
	}
	for input, want := range cases {
		if got := normalizeWebSearchDomain(input); got != want {
			t.Fatalf("normalizeWebSearchDomain(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestHostMatchesDomainIncludesSubdomainsOnly(t *testing.T) {
	for _, tc := range []struct {
		host   string
		domain string
		want   bool
	}{
		{"example.com", "example.com", true},
		{"www.example.com", "example.com", true},
		{"a.b.example.com", "example.com", true},
		{"notexample.com", "example.com", false},
		{"example.com.evil.test", "example.com", false},
		{"example.com", "www.example.com", false},
	} {
		if got := hostMatchesDomain(tc.host, tc.domain); got != tc.want {
			t.Fatalf("hostMatchesDomain(%q, %q) = %v, want %v", tc.host, tc.domain, got, tc.want)
		}
	}
}

func TestParseWebSearchDomainFilterValidatesMutualExclusion(t *testing.T) {
	if _, err := parseWebSearchDomainFilter(map[string]interface{}{
		"allowed_domains": []interface{}{"example.com"},
		"blocked_domains": "spam.example",
	}); err == nil {
		t.Fatal("expected allowed+blocked to be rejected")
	}
	if _, err := parseWebSearchDomainFilter(map[string]interface{}{
		"allowed_domains": []interface{}{"  ", "https://"},
	}); err == nil {
		t.Fatal("expected a list with no usable domain to be rejected")
	}
	filter, err := parseWebSearchDomainFilter(map[string]interface{}{
		"blocked_domains": "spam.example, ads.example;spam.example",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(filter.blocked) != 2 || filter.blocked[0] != "spam.example" || filter.blocked[1] != "ads.example" {
		t.Fatalf("expected normalized, deduped blocked list, got %#v", filter.blocked)
	}
	if filter.active() != true {
		t.Fatal("expected the filter to be active")
	}
	if _, err := parseWebSearchDomainFilter(map[string]interface{}{"query": "x"}); err != nil {
		t.Fatalf("absent filters must stay valid: %v", err)
	}
}

func stubWebSearchTool(results []DuckDuckGoResult) *WebSearchTool {
	tool := NewWebSearchTool()
	tool.providers = []webSearchProvider{{
		name: "stub",
		search: func(context.Context, string, int) (string, []DuckDuckGoResult, error) {
			return "stub", results, nil
		},
	}}
	return tool
}

func TestWebSearchBlockedDomainsFilterResults(t *testing.T) {
	tool := stubWebSearchTool([]DuckDuckGoResult{
		{Title: "keep", URL: "https://docs.example.com/guide", Snippet: "a"},
		{Title: "drop", URL: "https://pinterest.com/pin/1", Snippet: "b"},
		{Title: "drop-sub", URL: "https://cdn.pinterest.com/x", Snippet: "c"},
	})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query":           "needle",
		"blocked_domains": []interface{}{"pinterest.com"},
	})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("unexpected result: %v %+v", err, result)
	}
	if !strings.Contains(result.Content, "docs.example.com") {
		t.Fatalf("kept result must survive the filter, got %q", result.Content)
	}
	if strings.Contains(result.Content, "pinterest.com") {
		t.Fatalf("blocked domains must be filtered out, got %q", result.Content)
	}
	if result.Metadata["provider_count"] != 3 || result.Metadata["filtered_out"] != 2 || result.Metadata["returned_count"] != 1 {
		t.Fatalf("unexpected filter metadata: %#v", result.Metadata)
	}
	blocked, _ := result.Metadata["blocked_domains"].([]string)
	if len(blocked) != 1 || blocked[0] != "pinterest.com" {
		t.Fatalf("expected the active rule in metadata, got %#v", result.Metadata["blocked_domains"])
	}
}

func TestWebSearchAllowedDomainsKeepsOnlyMatches(t *testing.T) {
	tool := stubWebSearchTool([]DuckDuckGoResult{
		{Title: "keep", URL: "https://www.example.com/a", Snippet: "a"},
		{Title: "drop", URL: "https://other.test/b", Snippet: "b"},
	})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query":           "needle",
		"allowed_domains": "example.com",
	})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("unexpected result: %v %+v", err, result)
	}
	if !strings.Contains(result.Content, "www.example.com") || strings.Contains(result.Content, "other.test") {
		t.Fatalf("allowed filter must keep only the allowlisted host, got %q", result.Content)
	}
	if result.Metadata["filtered_out"] != 1 {
		t.Fatalf("expected filtered_out=1, got %#v", result.Metadata["filtered_out"])
	}
}

// TestWebSearchFilteredToEmptyKeepsFilterRoute: an all-filtered result set is
// still a successful search, but the next_action must point at the filter
// rather than the generic empty-result wording.
func TestWebSearchFilteredToEmptyKeepsFilterRoute(t *testing.T) {
	tool := stubWebSearchTool([]DuckDuckGoResult{
		{Title: "drop", URL: "https://pinterest.com/pin/1", Snippet: "b"},
	})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query":           "needle",
		"blocked_domains": []interface{}{"pinterest.com"},
	})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("unexpected result: %v %+v", err, result)
	}
	if !strings.Contains(result.Content, "全部被域名过滤器排除") {
		t.Fatalf("expected the filter-specific notice, got %q", result.Content)
	}
	next, _ := result.Metadata["next_action"].(string)
	if !strings.Contains(next, "allowed_domains/blocked_domains") {
		t.Fatalf("expected a filter-specific next_action, got %#v", result.Metadata["next_action"])
	}
}

func TestWebSearchUnfilteredMetadataUnchanged(t *testing.T) {
	tool := stubWebSearchTool([]DuckDuckGoResult{
		{Title: "keep", URL: "https://example.com/a", Snippet: "a"},
	})
	result, err := tool.Execute(context.Background(), map[string]interface{}{"query": "needle"})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("unexpected result: %v %+v", err, result)
	}
	if _, exists := result.Metadata["filtered_out"]; exists {
		t.Fatalf("unfiltered calls must not grow filter metadata: %#v", result.Metadata)
	}
	if _, exists := result.Metadata["provider_count"]; exists {
		t.Fatalf("unfiltered calls must not grow provider_count: %#v", result.Metadata)
	}
}
