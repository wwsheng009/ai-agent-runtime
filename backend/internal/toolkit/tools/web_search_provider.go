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
// domain filter. Answer carries provider-generated summary text for answer
// backends (Perplexity, P2-4 second batch); empty for rank-based backends.
type WebSearchProviderResult struct {
	Source  string
	Answer  string
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

// webSearchAnswerFunc is the answer-bearing provider signature (P2-4 second
// batch): answer backends return generated summary text next to their ranked
// sources.
type webSearchAnswerFunc func(ctx context.Context, query string, count int) (source, answer string, results []DuckDuckGoResult, err error)

// webSearchAnswerProviderFunc adapts an answer-bearing search func into the
// provider seam (same availability contract as webSearchProviderFunc).
type webSearchAnswerProviderFunc struct {
	name      string
	search    webSearchAnswerFunc
	available func() bool
}

// newWebSearchAnswerProvider builds a seam provider from one answer-bearing
// search func.
func newWebSearchAnswerProvider(name string, search webSearchAnswerFunc, available func() bool) WebSearchProvider {
	return webSearchAnswerProviderFunc{name: name, search: search, available: available}
}

func (p webSearchAnswerProviderFunc) Name() string { return p.name }

func (p webSearchAnswerProviderFunc) Available() bool {
	return p.available == nil || p.available()
}

func (p webSearchAnswerProviderFunc) Search(ctx context.Context, req WebSearchRequest) (WebSearchProviderResult, error) {
	ctx = withWebSearchOptions(ctx, webSearchOptions{timeRange: req.TimeRange, language: req.Language})
	source, answer, results, err := p.search(ctx, req.Query, req.Count)
	if err != nil {
		return WebSearchProviderResult{}, err
	}
	return WebSearchProviderResult{Source: source, Answer: answer, Results: results}, nil
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
	braveProviderName: func(tool *WebSearchTool) WebSearchProvider {
		return newWebSearchProvider(braveProviderName, tool.searchBraveProvider, func() bool { return tool.brave.available() })
	},
	tavilyProviderName: func(tool *WebSearchTool) WebSearchProvider {
		return newWebSearchProvider(tavilyProviderName, tool.searchTavilyProvider, func() bool { return tool.tavily.available() })
	},
	exaProviderName: func(tool *WebSearchTool) WebSearchProvider {
		return newWebSearchProvider(exaProviderName, tool.searchExaProvider, func() bool { return tool.exa.available() })
	},
	perplexityProviderName: func(tool *WebSearchTool) WebSearchProvider {
		return newWebSearchAnswerProvider(perplexityProviderName, tool.searchPerplexityProvider, func() bool { return tool.perplexity.available() })
	},
	webSearchNativeName: func(tool *WebSearchTool) WebSearchProvider {
		return newWebSearchProvider(webSearchNativeName, tool.searchNativeProvider, tool.nativeAvailable)
	},
}
