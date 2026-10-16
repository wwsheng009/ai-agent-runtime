package tools

import "context"

// ---------------------------------------------------------------------------
// Provider seam (P2-1).
//
// The chain is typed by WebSearchProvider. Built-in scrapers are wrapped by
// webSearchProviderFunc, and new backends register in webSearchProviderRegistry
// without touching Execute or the chain loop. The seam intentionally carries
// the scraped row type (consumed by the quality gate and domain filter); the
// normalized WebSearchResult (P2-2) is produced later at the tool layer.
// ---------------------------------------------------------------------------

// WebSearchRequest is the normalized provider input. TimeRange/Language travel
// on the request; the func adapter re-injects them into ctx so the existing
// scraper implementations (which read them from context) stay untouched.
type WebSearchRequest struct {
	Query     string
	Count     int
	TimeRange string
	Language  string
}

// WebSearchProviderResult carries one provider attempt: the source tag recorded
// in result metadata plus the scraped rows consumed by the quality gate and
// domain filter.
type WebSearchProviderResult struct {
	Source  string
	Results []DuckDuckGoResult
}

// WebSearchProvider is one chain backend. Available is a cheap local check that
// must not perform network I/O (mirrors the DSH seam contract).
type WebSearchProvider interface {
	Name() string
	Available() bool
	Search(ctx context.Context, req WebSearchRequest) (WebSearchProviderResult, error)
}

// webSearchSearchFunc keeps the historical scraper signature; the adapter
// injects request options into ctx so existing implementations stay untouched.
type webSearchSearchFunc func(ctx context.Context, query string, count int) (source string, results []DuckDuckGoResult, err error)

// webSearchProviderFunc adapts one search func into the provider seam. A nil
// available means "always available" (the scrape chain needs no credentials).
type webSearchProviderFunc struct {
	name      string
	search    webSearchSearchFunc
	available func() bool
}

// newWebSearchProvider builds a seam provider from one search func. It is the
// constructor shared by the registry and by tests injecting fake providers.
func newWebSearchProvider(name string, search webSearchSearchFunc, available func() bool) WebSearchProvider {
	return webSearchProviderFunc{name: name, search: search, available: available}
}

func (p webSearchProviderFunc) Name() string { return p.name }

func (p webSearchProviderFunc) Available() bool {
	return p.available == nil || p.available()
}

func (p webSearchProviderFunc) Search(ctx context.Context, req WebSearchRequest) (WebSearchProviderResult, error) {
	ctx = withWebSearchOptions(ctx, webSearchOptions{timeRange: req.TimeRange, language: req.Language})
	source, results, err := p.search(ctx, req.Query, req.Count)
	if err != nil {
		return WebSearchProviderResult{}, err
	}
	return WebSearchProviderResult{Source: source, Results: results}, nil
}

// webSearchProviderRegistry is the built-in construction table (P2-1). Adding a
// backend is one entry here; the chain loop, gating, and Execute stay untouched.
var webSearchProviderRegistry = map[string]func(tool *WebSearchTool) WebSearchProvider{
	"bing": func(tool *WebSearchTool) WebSearchProvider {
		return newWebSearchProvider("bing", tool.searchBingProvider, nil)
	},
	"bing_cn": func(tool *WebSearchTool) WebSearchProvider {
		return newWebSearchProvider("bing_cn", tool.searchBingCNProvider, nil)
	},
	"duckduckgo": func(tool *WebSearchTool) WebSearchProvider {
		return newWebSearchProvider("duckduckgo", tool.searchDuckDuckGoProvider, nil)
	},
	webSearchNativeName: func(tool *WebSearchTool) WebSearchProvider {
		return newWebSearchProvider(webSearchNativeName, tool.searchNativeProvider, tool.nativeAvailable)
	},
}
