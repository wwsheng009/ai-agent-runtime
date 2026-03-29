package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ai-gateway/ai-agent-runtime/internal/toolkit"
)

// WebSearchTool 网络搜索工具
type WebSearchTool struct {
	*toolkit.BaseTool
	sandboxPolicy
	httpClient *http.Client
}

// NewWebSearchTool 创建网络搜索工具
func NewWebSearchTool() *WebSearchTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "搜索关键词或问题",
			},
			"count": map[string]interface{}{
				"type":        "integer",
				"description": "返回结果数量（默认 5，最大 10）",
				"default":     5,
			},
		},
		"required": []string{"query"},
	}

	return &WebSearchTool{
		BaseTool: toolkit.NewBaseTool(
			"web_search",
			"使用 DuckDuckGo 搜索网络信息，返回相关网页标题、链接和摘要",
			"1.0.0",
			parameters,
			true,
		),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// DuckDuckGoResult DuckDuckGo 搜索结果
type DuckDuckGoResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// DDGInstantAnswer DuckDuckGo Instant Answer API 响应结构
type DDGInstantAnswer struct {
	AbstractText   string `json:"AbstractText"`
	AbstractURL    string `json:"AbstractURL"`
	AbstractSource string `json:"AbstractSource"`
	Heading        string `json:"Heading"`
	Results        []struct {
		Text     string `json:"text"`
		FirstURL string `json:"FirstURL"`
	} `json:"Results"`
	RelatedTopics []struct {
		Text string `json:"text"`
		URL  string `json:"FirstURL"`
	} `json:"RelatedTopics"`
}

// Execute 实现 Tool 接口
func (w *WebSearchTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	// 解析查询
	query, ok := params["query"].(string)
	if !ok || query == "" {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("query 参数缺失或为空"),
		}, nil
	}

	// 解析结果数量
	count := 5
	if c, ok := params["count"].(float64); ok && c > 0 {
		count = int(c)
		if count > 10 {
			count = 10
		}
	}

	// 方法1: 使用 DuckDuckGo Instant Answer API
	results, err := w.searchDuckDuckGo(ctx, query, count)
	if err == nil && len(results) > 0 {
		return &toolkit.ToolResult{
			Success: true,
			Content: w.formatResults(results),
			Metadata: map[string]interface{}{
				"query":  query,
				"count":  len(results),
				"source": "duckduckgo",
			},
		}, nil
	}

	// 方法2: 备用 - 使用 HTML 搜索页面解析
	results, err = w.searchDuckDuckGoHTML(ctx, query, count)
	if err != nil {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("搜索失败: %w", err),
		}, nil
	}

	return &toolkit.ToolResult{
		Success: true,
		Content: w.formatResults(results),
		Metadata: map[string]interface{}{
			"query":  query,
			"count":  len(results),
			"source": "duckduckgo_html",
		},
	}, nil
}

// searchDuckDuckGo 使用 DuckDuckGo Instant Answer API
func (w *WebSearchTool) searchDuckDuckGo(ctx context.Context, query string, count int) ([]DuckDuckGoResult, error) {
	apiURL := fmt.Sprintf("https://api.duckduckgo.com/?q=%s&format=json&no_html=1", url.QueryEscape(query))
	if err := w.checkURL(apiURL); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var ddg DDGInstantAnswer
	if err := json.Unmarshal(body, &ddg); err != nil {
		return nil, err
	}

	results := make([]DuckDuckGoResult, 0)

	// 添加主要摘要
	if ddg.AbstractText != "" {
		results = append(results, DuckDuckGoResult{
			Title:   ddg.Heading,
			URL:     ddg.AbstractURL,
			Snippet: ddg.AbstractText,
		})
	}

	// 添加相关主题
	for i, topic := range ddg.RelatedTopics {
		if i >= count-1 {
			break
		}
		if topic.Text != "" && topic.URL != "" {
			results = append(results, DuckDuckGoResult{
				Title:   extractTitleFromText(topic.Text),
				URL:     topic.URL,
				Snippet: topic.Text,
			})
		}
	}

	if len(results) == 0 {
		return nil, fmt.Errorf("no results")
	}

	return results[:min(count, len(results))], nil
}

// searchDuckDuckGoHTML 使用 HTML 页面搜索（备用）
func (w *WebSearchTool) searchDuckDuckGoHTML(ctx context.Context, query string, count int) ([]DuckDuckGoResult, error) {
	searchURL := fmt.Sprintf("https://html.duckduckgo.com/html/?q=%s", url.QueryEscape(query))
	if err := w.checkURL(searchURL); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "GET", searchURL, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	html := string(body)
	results := make([]DuckDuckGoResult, 0)

	// 简单解析 HTML 提取结果
	// 查找结果容器
	resultStart := 0
	for i := 0; i < count && resultStart != -1; i++ {
		// 查找结果链接
		linkStart := strings.Index(html[resultStart:], "<a class=\"result__a\"")
		if linkStart == -1 {
			break
		}
		linkStart += resultStart

		hrefStart := strings.Index(html[linkStart:], "href=\"")
		if hrefStart == -1 {
			break
		}
		hrefStart += linkStart + 6

		hrefEnd := strings.Index(html[hrefStart:], "\"")
		if hrefEnd == -1 {
			break
		}
		href := html[hrefStart : hrefStart+hrefEnd]

		// 提取标题
		titleStart := hrefStart + hrefEnd + 2
		titleEnd := strings.Index(html[titleStart:], "</a>")
		if titleEnd == -1 {
			break
		}
		title := stripTags(html[titleStart : titleStart+titleEnd])

		// 提取摘要
		snippetStart := strings.Index(html[titleStart+titleEnd:], "<a class=\"result__snippet\"")
		var snippet string
		if snippetStart != -1 {
			snippetStart += titleStart + titleEnd
			snippetEnd := strings.Index(html[snippetStart:], "</a>")
			if snippetEnd != -1 {
				snippet = stripTags(html[snippetStart : snippetStart+snippetEnd])
			}
		}

		if snippet == "" {
			snippet = title
		}

		// 解码 URL
		if decodedURL, err := url.QueryUnescape(href); err == nil {
			href = decodedURL
		}

		results = append(results, DuckDuckGoResult{
			Title:   strings.TrimSpace(title),
			URL:     strings.TrimSpace(href),
			Snippet: strings.TrimSpace(snippet),
		})

		resultStart = titleStart + titleEnd + 1
	}

	if len(results) == 0 {
		return nil, fmt.Errorf("no results found")
	}

	return results, nil
}

// formatResults 格式化搜索结果
func (w *WebSearchTool) formatResults(results []DuckDuckGoResult) string {
	var output strings.Builder
	output.WriteString("🔍 搜索结果:\n\n")

	for i, result := range results {
		output.WriteString(fmt.Sprintf("%d. **%s**\n", i+1, result.Title))
		if result.URL != "" {
			output.WriteString(fmt.Sprintf("   📎 %s\n", result.URL))
		}
		if result.Snippet != "" {
			output.WriteString(fmt.Sprintf("   📝 %s\n", result.Snippet))
		}
		output.WriteString("\n")
	}

	return output.String()
}

// stripTags 移除 HTML 标签
func stripTags(html string) string {
	result := make([]byte, 0, len(html))
	inTag := false
	for i := 0; i < len(html); i++ {
		if html[i] == '<' {
			inTag = true
			continue
		}
		if html[i] == '>' {
			inTag = false
			continue
		}
		if !inTag {
			result = append(result, html[i])
		}
	}
	return string(result)
}

// extractTitleFromText 从文本中提取标题
func extractTitleFromText(text string) string {
	// 如果文本中有 - 分隔，取第一部分
	if idx := strings.Index(text, " - "); idx > 0 {
		return text[:idx]
	}
	// 如果文本太长，截取前 100 个字符
	if len(text) > 100 {
		return text[:97] + "..."
	}
	return text
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
