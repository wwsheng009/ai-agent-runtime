package tools

// ---------------------------------------------------------------------------
// Normalized search result contract (P2-2).
//
// The scraper chain still works on DuckDuckGoResult internally; the tool layer
// converts the selected attempt into this provider-neutral shape, enforces the
// consumer-owned count cap, and projects the sources into result metadata so a
// UI/replay consumer never has to reverse-engineer the rendered text.
// ---------------------------------------------------------------------------

// WebSearchSource is one citable source. URL is the only guaranteed field;
// Title/Snippet/PublishedAt stay empty when the backend did not provide them
// (never invent data).
type WebSearchSource struct {
	Title       string `json:"title,omitempty"`
	URL         string `json:"url"`
	Snippet     string `json:"snippet,omitempty"`
	PublishedAt string `json:"published_at,omitempty"`
}

// WebSearchResult is the normalized search outcome. Answer carries
// provider-generated summary text (empty for the scraper chain today);
// Truncated is set by the tool layer when sources were cut to the count cap.
type WebSearchResult struct {
	Answer    string            `json:"answer,omitempty"`
	Sources   []WebSearchSource `json:"sources"`
	Truncated bool              `json:"truncated"`
}

// webSearchSourcesFromResults converts scraper results into normalized sources.
func webSearchSourcesFromResults(results []DuckDuckGoResult) []WebSearchSource {
	sources := make([]WebSearchSource, 0, len(results))
	for _, result := range results {
		sources = append(sources, WebSearchSource{
			Title:       result.Title,
			URL:         result.URL,
			Snippet:     result.Snippet,
			PublishedAt: result.PublishedAt,
		})
	}
	return sources
}

// capWebSearchResults enforces the consumer-owned count boundary: a provider
// returning more than the requested count is truncated here, regardless of
// what the provider's own parser does.
func capWebSearchResults(results []DuckDuckGoResult, maxResults int) ([]DuckDuckGoResult, bool) {
	if maxResults > 0 && len(results) > maxResults {
		return results[:maxResults], true
	}
	return results, false
}

// webSearchSourcesMetadata projects sources into the plain JSON shape carried
// by result metadata (the P3-1 presentation layer consumes it; absent optionals
// stay omitted).
func webSearchSourcesMetadata(sources []WebSearchSource) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(sources))
	for _, source := range sources {
		entry := map[string]interface{}{"url": source.URL}
		if source.Title != "" {
			entry["title"] = source.Title
		}
		if source.Snippet != "" {
			entry["snippet"] = source.Snippet
		}
		if source.PublishedAt != "" {
			entry["published_at"] = source.PublishedAt
		}
		out = append(out, entry)
	}
	return out
}
