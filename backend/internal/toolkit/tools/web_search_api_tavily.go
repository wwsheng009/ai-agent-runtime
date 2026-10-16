package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strings"
)

// ---------------------------------------------------------------------------
// Tavily Search API provider (P2-4).
//
// POST {baseURL}/search with the Bearer credential. Tavily accepts query and
// max_results; time_range/language are not mapped (metadata marks them
// unsupported for this source). published_date is carried as publishedAt.
// ---------------------------------------------------------------------------

const (
	tavilyProviderName      = "tavily"
	tavilyDefaultBaseURL    = "https://api.tavily.com"
	tavilyDefaultKeyEnv     = "TAVILY_API_KEY"
	tavilyDefaultMaxResults = 10
)

type tavilySearchResponse struct {
	Results []struct {
		Title         string `json:"title"`
		URL           string `json:"url"`
		Content       string `json:"content"`
		PublishedDate string `json:"published_date"`
	} `json:"results"`
}

func mapTavilyResponse(resp *tavilySearchResponse, count int) []DuckDuckGoResult {
	results := make([]DuckDuckGoResult, 0, count)
	seen := make(map[string]struct{})
	for _, item := range resp.Results {
		urlText := strings.TrimSpace(item.URL)
		if urlText == "" {
			continue
		}
		if _, exists := seen[urlText]; exists {
			continue
		}
		seen[urlText] = struct{}{}
		title := strings.TrimSpace(item.Title)
		snippet := strings.TrimSpace(html.UnescapeString(stripTags(item.Content)))
		if snippet == "" {
			snippet = title
		}
		results = append(results, DuckDuckGoResult{
			Title:       title,
			URL:         urlText,
			Snippet:     snippet,
			PublishedAt: strings.TrimSpace(item.PublishedDate),
		})
		if len(results) >= count {
			break
		}
	}
	return results
}

func (w *WebSearchTool) searchTavilyProvider(ctx context.Context, query string, count int) (string, []DuckDuckGoResult, error) {
	cfg := w.tavily
	if !cfg.available() {
		return "", nil, &searchProviderUnavailableError{reason: "Tavily 缺少凭据或端点配置"}
	}
	effectiveCount := cfg.effectiveCount(count)

	body, err := json.Marshal(map[string]interface{}{
		"query":          query,
		"max_results":    effectiveCount,
		"include_answer": false,
		"search_depth":   "basic",
	})
	if err != nil {
		return "", nil, fmt.Errorf("Tavily 搜索请求编码失败: %w", err)
	}
	endpoint := strings.TrimRight(cfg.baseURL, "/") + "/search"

	payload, err := w.fetchWebSearchAPI(ctx, http.MethodPost, endpoint, "Tavily 搜索", map[string]string{
		"authorization": "Bearer " + cfg.apiKey,
		"content-type":  "application/json",
		"accept":        "application/json",
		"user-agent":    "ai-agent-runtime/web_search",
	}, body)
	if err != nil {
		return "", nil, err
	}

	var parsed tavilySearchResponse
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return "", nil, fmt.Errorf("Tavily 搜索响应无法解析: %w", err)
	}
	return tavilyProviderName, mapTavilyResponse(&parsed, effectiveCount), nil
}
