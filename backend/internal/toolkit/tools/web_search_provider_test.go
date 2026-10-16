package tools

import (
	"context"
	"errors"
	"testing"
)

func TestWebSearchProviderAdapterInjectsRequestOptions(t *testing.T) {
	var gotOptions webSearchOptions
	provider := newWebSearchProvider("probe", func(ctx context.Context, query string, count int) (string, []DuckDuckGoResult, error) {
		gotOptions = webSearchOptionsFromContext(ctx)
		return "probe", []DuckDuckGoResult{{Title: query}}, nil
	}, nil)
	if provider.Name() != "probe" || !provider.Available() {
		t.Fatalf("unexpected provider metadata: %q %v", provider.Name(), provider.Available())
	}

	result, err := provider.Search(context.Background(), WebSearchRequest{Query: "q", Count: 3, TimeRange: "week", Language: "zh-CN"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotOptions.timeRange != "week" || gotOptions.language != "zh-CN" {
		t.Fatalf("request options were not injected into ctx: %#v", gotOptions)
	}
	if result.Source != "probe" || len(result.Results) != 1 || result.Results[0].Title != "q" {
		t.Fatalf("unexpected provider result: %#v", result)
	}
}

func TestWebSearchProviderAvailability(t *testing.T) {
	always := newWebSearchProvider("scraper", func(context.Context, string, int) (string, []DuckDuckGoResult, error) {
		return "", nil, nil
	}, nil)
	if !always.Available() {
		t.Fatalf("a nil available func must mean always available")
	}
	gated := newWebSearchProvider("native", func(context.Context, string, int) (string, []DuckDuckGoResult, error) {
		return "", nil, nil
	}, func() bool { return false })
	if gated.Available() {
		t.Fatalf("availability gate ignored")
	}
}

func TestWebSearchProviderSearchErrorPropagates(t *testing.T) {
	sentinel := errors.New("boom")
	provider := newWebSearchProvider("err", func(context.Context, string, int) (string, []DuckDuckGoResult, error) {
		return "", nil, sentinel
	}, nil)
	result, err := provider.Search(context.Background(), WebSearchRequest{Query: "q"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("error must propagate unchanged: %v", err)
	}
	if result.Source != "" || result.Results != nil {
		t.Fatalf("failed search must return a zero result: %#v", result)
	}
}

func TestWebSearchProviderRegistryCoversBuiltinChain(t *testing.T) {
	for _, name := range []string{"bing", "bing_cn", "duckduckgo", webSearchNativeName} {
		if _, ok := webSearchProviderRegistry[name]; !ok {
			t.Fatalf("registry missing builtin provider %q", name)
		}
	}
	if _, ok := webSearchProviderRegistry["google"]; ok {
		t.Fatalf("registry must not contain unknown providers")
	}

	tool := NewWebSearchTool()
	for _, provider := range tool.providers {
		if _, ok := webSearchProviderRegistry[provider.Name()]; !ok {
			t.Fatalf("chain provider %q is not registry-backed", provider.Name())
		}
	}
}

func TestWebSearchProviderRegistrySkipsUnknownChainTokens(t *testing.T) {
	t.Setenv(webSearchProvidersEnv, "google,bing")
	tool := NewWebSearchTool()
	names := make([]string, 0, len(tool.providers))
	for _, provider := range tool.providers {
		names = append(names, provider.Name())
	}
	if len(names) != 1 || names[0] != "bing" {
		t.Fatalf("providers = %v, want [bing] after skipping the unknown token", names)
	}
}
