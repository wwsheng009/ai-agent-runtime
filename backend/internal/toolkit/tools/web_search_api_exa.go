package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// ---------------------------------------------------------------------------
// Exa Search API provider (P2-4 second batch).
//
// POST {baseURL}/search with the x-api-key header. Exa is a neural search
// backend: highlights (falling back to a length-capped page text) become the
// row snippet, publishedDate becomes publishedAt. time_range/language are not
// mapped; metadata marks them unsupported for this source.
// ---------------------------------------------------------------------------

const (
	exaProviderName      = "exa"
	exaDefaultBaseURL    = "https://api.exa.ai"
	exaDefaultKeyEnv     = "EXA_API_KEY"
	exaDefaultMaxResults = 10
	// exaTextMaxCharacters bounds contents.text so the response stays small.
	exaTextMaxCharacters = 1000
)

type exaSearchResponse struct {
	Results []struct {
		Title         string   `json:"title"`
		URL           string   `json:"url"`
		PublishedDate string   `json:"publishedDate"`
		Highlights    []string `json:"highlights"`
		Text          string   `json:"text"`
	} `json:"results"`
}

// exaSnippet picks the row snippet: first non-empty highlight, else the
// length-capped page text, else empty (never invent data).
func exaSnippet(highlights []string, text string) string {
	for _, highlight := range highlights {
		if snippet := strings.TrimSpace(htmlUnescapeAndStripTags(highlight)); snippet != "" {
			return snippet
		}
	}
	return strings.TrimSpace(htmlUnescapeAndStripTags(text))
}

func mapExaResponse(resp *exaSearchResponse, count int) []DuckDuckGoResult {
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
		results = append(results, DuckDuckGoResult{
			Title:       strings.TrimSpace(item.Title),
			URL:         urlText,
			Snippet:     exaSnippet(item.Highlights, item.Text),
			PublishedAt: strings.TrimSpace(item.PublishedDate),
		})
		if len(results) >= count {
			break
		}
	}
	return results
}

func (w *WebSearchTool) searchExaProvider(ctx context.Context, query string, count int) (string, []DuckDuckGoResult, error) {
	cfg := w.exa
	if !cfg.available() {
		return "", nil, &searchProviderUnavailableError{reason: "Exa 缺少凭据或端点配置"}
	}
	effectiveCount := cfg.effectiveCount(count)

	body, err := json.Marshal(map[string]interface{}{
		"query":      query,
		"numResults": effectiveCount,
		"contents": map[string]interface{}{
			"highlights": map[string]interface{}{"numSentences": 3, "highlightsPerUrl": 1},
			"text":       map[string]interface{}{"maxCharacters": exaTextMaxCharacters},
		},
	})
	if err != nil {
		return "", nil, fmt.Errorf("Exa 搜索请求编码失败: %w", err)
	}
	endpoint := strings.TrimRight(cfg.baseURL, "/") + "/search"

	payload, err := w.fetchWebSearchAPI(ctx, http.MethodPost, endpoint, "Exa 搜索", map[string]string{
		"x-api-key":    cfg.apiKey,
		"content-type": "application/json",
		"accept":       "application/json",
		"user-agent":   "ai-agent-runtime/web_search",
	}, body)
	if err != nil {
		return "", nil, err
	}

	var parsed exaSearchResponse
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return "", nil, fmt.Errorf("Exa 搜索响应无法解析: %w", err)
	}
	return exaProviderName, mapExaResponse(&parsed, effectiveCount), nil
}
