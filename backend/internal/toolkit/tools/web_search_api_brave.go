package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// Brave Search API provider (P2-4).
//
// GET {baseURL}/res/v1/web/search with the X-Subscription-Token header. The
// backend supports the tool's time_range (freshness) and language (search_lang)
// options, so those metadata flags stay honest for this source.
// ---------------------------------------------------------------------------

const (
	braveProviderName      = "brave"
	braveDefaultBaseURL    = "https://api.search.brave.com"
	braveDefaultKeyEnv     = "BRAVE_API_KEY"
	braveDefaultMaxResults = 10
)

type braveSearchResponse struct {
	Web struct {
		Results []struct {
			Title       string `json:"title"`
			URL         string `json:"url"`
			Description string `json:"description"`
			PageAge     string `json:"page_age"`
		} `json:"results"`
	} `json:"web"`
}

// braveFreshness maps the tool's time_range to Brave's freshness parameter.
func braveFreshness(timeRange string) string {
	switch timeRange {
	case "day":
		return "pd"
	case "week":
		return "pw"
	case "month":
		return "pm"
	case "year":
		return "py"
	default:
		return ""
	}
}

func mapBraveResponse(resp *braveSearchResponse, count int) []DuckDuckGoResult {
	results := make([]DuckDuckGoResult, 0, count)
	seen := make(map[string]struct{})
	for _, item := range resp.Web.Results {
		urlText := strings.TrimSpace(item.URL)
		title := strings.TrimSpace(item.Title)
		if urlText == "" || title == "" {
			continue
		}
		if _, exists := seen[urlText]; exists {
			continue
		}
		seen[urlText] = struct{}{}
		snippet := strings.TrimSpace(htmlUnescapeAndStripTags(item.Description))
		if snippet == "" {
			snippet = title
		}
		results = append(results, DuckDuckGoResult{
			Title:   title,
			URL:     urlText,
			Snippet: snippet,
			// page_age is the provider's parseable timestamp; the human-readable
			// "age" field is deliberately not used as publishedAt.
			PublishedAt: strings.TrimSpace(item.PageAge),
		})
		if len(results) >= count {
			break
		}
	}
	return results
}

// htmlUnescapeAndStripTags reuses the scrape-chain text cleanup for API
// snippets that still carry markup.
func htmlUnescapeAndStripTags(value string) string {
	if !strings.Contains(value, "<") && !strings.Contains(value, "&") {
		return value
	}
	return html.UnescapeString(stripTags(value))
}

func (w *WebSearchTool) searchBraveProvider(ctx context.Context, query string, count int) (string, []DuckDuckGoResult, error) {
	cfg := w.brave
	if !cfg.available() {
		return "", nil, &searchProviderUnavailableError{reason: "Brave 缺少凭据或端点配置"}
	}
	effectiveCount := cfg.effectiveCount(count)

	options := webSearchOptionsFromContext(ctx)
	params := url.Values{}
	params.Set("q", query)
	params.Set("count", strconv.Itoa(effectiveCount))
	if freshness := braveFreshness(options.timeRange); freshness != "" {
		params.Set("freshness", freshness)
	}
	if language := apiSearchLanguage(options.language); language != "" {
		params.Set("search_lang", language)
	}
	endpoint := strings.TrimRight(cfg.baseURL, "/") + "/res/v1/web/search?" + params.Encode()

	payload, err := w.fetchWebSearchAPI(ctx, http.MethodGet, endpoint, "Brave 搜索", map[string]string{
		"x-subscription-token": cfg.apiKey,
		"accept":               "application/json",
		"user-agent":           "ai-agent-runtime/web_search",
	}, nil)
	if err != nil {
		return "", nil, err
	}

	var parsed braveSearchResponse
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return "", nil, fmt.Errorf("Brave 搜索响应无法解析: %w", err)
	}
	return braveProviderName, mapBraveResponse(&parsed, effectiveCount), nil
}
