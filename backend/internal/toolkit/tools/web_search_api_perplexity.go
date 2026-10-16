package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// ---------------------------------------------------------------------------
// Perplexity answer API provider (P2-4 second batch).
//
// POST {baseURL}/chat/completions with the Bearer credential and a sonar-style
// model. The generated answer is the payload (rendered as a "### <query>"
// section by the formatter); search_results (richer, preferred) or citations
// become the ranked sources. time_range/language are not mapped.
// ---------------------------------------------------------------------------

const (
	perplexityProviderName      = "perplexity"
	perplexityDefaultBaseURL    = "https://api.perplexity.ai"
	perplexityDefaultKeyEnv     = "PERPLEXITY_API_KEY"
	perplexityDefaultModel      = "sonar"
	perplexityDefaultMaxResults = 10
)

type perplexityChatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	SearchResults []struct {
		Title string `json:"title"`
		URL   string `json:"url"`
		Date  string `json:"date"`
	} `json:"search_results"`
	Citations []string `json:"citations"`
}

// mapPerplexitySources projects the answer's sources: search_results win when
// present (title/date available); citations are the URL-only fallback only
// when search_results is empty. URL first-seen dedupe, capped at count.
func mapPerplexitySources(resp *perplexityChatResponse, count int) []DuckDuckGoResult {
	results := make([]DuckDuckGoResult, 0, count)
	seen := make(map[string]struct{})
	for _, item := range resp.SearchResults {
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
			PublishedAt: strings.TrimSpace(item.Date),
		})
		if len(results) >= count {
			return results
		}
	}
	if len(results) > 0 {
		return results
	}
	for _, raw := range resp.Citations {
		urlText := strings.TrimSpace(raw)
		if urlText == "" {
			continue
		}
		if _, exists := seen[urlText]; exists {
			continue
		}
		seen[urlText] = struct{}{}
		results = append(results, DuckDuckGoResult{URL: urlText})
		if len(results) >= count {
			break
		}
	}
	return results
}

func (w *WebSearchTool) searchPerplexityProvider(ctx context.Context, query string, count int) (string, string, []DuckDuckGoResult, error) {
	cfg := w.perplexity
	if !cfg.available() {
		return "", "", nil, &searchProviderUnavailableError{reason: "Perplexity 缺少凭据或端点配置"}
	}
	model := cfg.model
	if model == "" {
		model = perplexityDefaultModel
	}

	body, err := json.Marshal(map[string]interface{}{
		"model": model,
		"messages": []map[string]string{
			{"role": "user", "content": query},
		},
	})
	if err != nil {
		return "", "", nil, fmt.Errorf("Perplexity 搜索请求编码失败: %w", err)
	}
	endpoint := strings.TrimRight(cfg.baseURL, "/") + "/chat/completions"

	payload, err := w.fetchWebSearchAPI(ctx, http.MethodPost, endpoint, "Perplexity 搜索", map[string]string{
		"authorization": "Bearer " + cfg.apiKey,
		"content-type":  "application/json",
		"accept":        "application/json",
		"user-agent":    "ai-agent-runtime/web_search",
	}, body)
	if err != nil {
		return "", "", nil, err
	}

	var parsed perplexityChatResponse
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return "", "", nil, fmt.Errorf("Perplexity 搜索响应无法解析: %w", err)
	}
	answer := ""
	if len(parsed.Choices) > 0 {
		answer = strings.TrimSpace(parsed.Choices[0].Message.Content)
	}
	results := mapPerplexitySources(&parsed, cfg.effectiveCount(count))
	if answer == "" && len(results) == 0 {
		return "", "", nil, fmt.Errorf("Perplexity 响应缺少答案与来源")
	}
	return perplexityProviderName, answer, results, nil
}
