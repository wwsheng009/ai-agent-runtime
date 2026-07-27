package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
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
				"description": "搜索关键词或问题。若包含多个不同搜索意图，请拆分为多个更小的 query 调用，每次只聚焦一个搜索目标。",
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
			"使用 DuckDuckGo 搜索网络信息，返回相关网页标题、链接和摘要。若有多个不同搜索意图，请拆分为多个更小的 web_search 调用，每次只聚焦一个搜索目标。",
			"1.0.0",
			parameters,
			true,
		),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (w *WebSearchTool) DefinitionMetadata() map[string]interface{} {
	return map[string]interface{}{
		runtimetypes.ToolMetadataKindKey:             runtimetypes.ToolKindSearch,
		runtimetypes.ToolMetadataReadOnlyKey:         true,
		runtimetypes.ToolMetadataMutatesFSKey:        false,
		runtimetypes.ToolMetadataRequiresNetKey:      true,
		runtimetypes.ToolMetadataSupportsParallelKey: true,
		runtimetypes.ToolMetadataRetryClassKey:       runtimetypes.ToolRetryClassSafe,
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
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("query 参数缺失或为空"),
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
	results, instantErr := w.searchDuckDuckGo(ctx, query, count)
	if instantErr == nil && len(results) > 0 {
		return w.successSearchResult(query, results, "duckduckgo"), nil
	}

	// 方法2: 备用 - 使用 HTML 搜索页面解析
	htmlResults, htmlErr := w.searchDuckDuckGoHTML(ctx, query, count)
	if htmlErr == nil {
		return w.successSearchResult(query, htmlResults, "duckduckgo_html"), nil
	}

	// Instant API can succeed with zero hits. Prefer that empty-success evidence
	// over a later HTML transport/parse failure so models do not retry unchanged.
	if instantErr == nil {
		return w.successSearchResult(query, results, "duckduckgo"), nil
	}

	// Prefer the first transport error (Instant), fall back to HTML error.
	// Live residual: connectex / dial failures were bare TOOL_EXECUTION with
	// generic next_action, so models blind-retried the same query.
	primary := instantErr
	if primary == nil {
		primary = htmlErr
	}
	return w.failureSearchResult(query, primary, instantErr, htmlErr), nil
}

// failureSearchResult stamps structured recovery for transport/network failures
// so chat-log / Diagnose / disposition replay do not treat them as opaque
// TOOL_EXECUTION.
func (w *WebSearchTool) failureSearchResult(query string, primary, instantErr, htmlErr error) *toolkit.ToolResult {
	if primary == nil {
		primary = errors.New("web_search failed")
	}
	err := fmt.Errorf("搜索失败: %w", primary)
	code := classifyWebSearchFailureCode(primary)
	if code == "" {
		code = classifyWebSearchFailureCode(instantErr)
	}
	if code == "" {
		code = classifyWebSearchFailureCode(htmlErr)
	}
	extra := map[string]interface{}{
		"query": query,
		"attempted_args": map[string]interface{}{
			"query": query,
		},
	}
	if instantErr != nil {
		extra["instant_error"] = instantErr.Error()
	}
	if htmlErr != nil {
		extra["html_error"] = htmlErr.Error()
	}
	next := ""
	if code != "" {
		// Prefer code-specific next_action; leave empty so Diagnose fills network guidance.
		next = webSearchFailureNextAction(code, query)
		extra["failure_class"] = webSearchFailureClass(code)
	}
	return toolResultFailureWithCode(err, code, next, extra)
}

func classifyWebSearchFailureCode(err error) string {
	if err == nil {
		return ""
	}
	// Scan the full error string first so HTTP 429/5xx still win when the
	// client wraps a transport/status error in *url.Error / *net.OpError.
	if code := classifyWebSearchFailureMessage(err.Error()); code != "" {
		// Prefer explicit upstream/rate-limit signals over generic network class.
		switch runtimeerrors.ErrorCode(code) {
		case runtimeerrors.ErrAPIRateLimit, runtimeerrors.ErrAPIServerError:
			return code
		}
	}

	// Prefer typed network errors over string heuristics.
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return string(runtimeerrors.ErrNetworkTimeout)
		}
		// url.Error implements net.Error; only treat as unavailable when it is
		// not an HTTP status-bearing upstream failure (checked above).
		if code := classifyWebSearchFailureMessage(err.Error()); code != "" {
			return code
		}
		return string(runtimeerrors.ErrNetworkUnavailable)
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return string(runtimeerrors.ErrNetworkUnavailable)
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return string(runtimeerrors.ErrNetworkUnavailable)
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if urlErr.Timeout() {
			return string(runtimeerrors.ErrNetworkTimeout)
		}
		// Unwrap further when possible; otherwise treat dial/transport failures as network.
		if urlErr.Err != nil {
			if nested := classifyWebSearchFailureCode(urlErr.Err); nested != "" {
				return nested
			}
		}
		if code := classifyWebSearchFailureMessage(urlErr.Error()); code != "" {
			return code
		}
		return string(runtimeerrors.ErrNetworkUnavailable)
	}

	return classifyWebSearchFailureMessage(err.Error())
}

func classifyWebSearchFailureMessage(message string) string {
	msg := strings.ToLower(strings.TrimSpace(message))
	if msg == "" {
		return ""
	}
	switch {
	case strings.Contains(msg, "http 429") || strings.Contains(msg, "status 429") ||
		strings.Contains(msg, "rate limit"):
		return string(runtimeerrors.ErrAPIRateLimit)
	case strings.Contains(msg, "http 502") || strings.Contains(msg, "http 503") ||
		strings.Contains(msg, "http 504") || strings.Contains(msg, "status 502") ||
		strings.Contains(msg, "status 503") || strings.Contains(msg, "status 504") ||
		strings.Contains(msg, "bad gateway") || strings.Contains(msg, "service unavailable"):
		return string(runtimeerrors.ErrAPIServerError)
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded") ||
		strings.Contains(msg, "context deadline"):
		return string(runtimeerrors.ErrNetworkTimeout)
	case strings.Contains(msg, "connection refused") || strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "connectex") || strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "network is unreachable") || strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "tls:") || strings.Contains(msg, "x509:") ||
		strings.Contains(msg, "dial tcp") || strings.Contains(msg, "eof") ||
		strings.Contains(msg, "wsarecv") || strings.Contains(msg, "forcibly closed"):
		return string(runtimeerrors.ErrNetworkUnavailable)
	default:
		return ""
	}
}

func webSearchFailureClass(code string) string {
	switch runtimeerrors.ErrorCode(strings.TrimSpace(code)) {
	case runtimeerrors.ErrNetworkTimeout, runtimeerrors.ErrNetworkUnavailable:
		return "network"
	case runtimeerrors.ErrAPIRateLimit:
		return "rate_limit"
	case runtimeerrors.ErrAPIServerError:
		return "upstream"
	default:
		return "execution"
	}
}

func webSearchFailureNextAction(code, query string) string {
	q := strings.TrimSpace(query)
	switch runtimeerrors.ErrorCode(strings.TrimSpace(code)) {
	case runtimeerrors.ErrNetworkTimeout, runtimeerrors.ErrNetworkUnavailable:
		if q != "" {
			return fmt.Sprintf(
				"NETWORK_UNAVAILABLE: search transport failed for query %q. Retry with bounded backoff, simplify/split the query, or continue without web evidence if offline. Do not spam identical web_search retries.",
				q,
			)
		}
		return "NETWORK_UNAVAILABLE: search transport failed. Retry with bounded backoff, simplify/split the query, or continue without web evidence if offline. Do not spam identical web_search retries."
	case runtimeerrors.ErrAPIRateLimit:
		return "Rate limited by the search provider. Wait with backoff, reduce count/frequency, or continue with local tools. Do not immediately replay the same query."
	case runtimeerrors.ErrAPIServerError:
		return "Search upstream returned a server error. Retry with bounded backoff or continue without web evidence. Do not spam identical queries."
	default:
		return ""
	}
}

// successSearchResult builds a successful search ToolResult, stamping empty
// disposition only when the provider returned a true zero-hit payload.
func (w *WebSearchTool) successSearchResult(query string, results []DuckDuckGoResult, source string) *toolkit.ToolResult {
	if results == nil {
		results = []DuckDuckGoResult{}
	}
	metadata := map[string]interface{}{
		"query":          query,
		"count":          len(results),
		"match_count":    len(results),
		"returned_count": len(results),
		"result_count":   len(results),
		"source":         source,
	}
	content := w.formatResults(results)
	if len(results) == 0 {
		content = "未找到匹配的内容"
		toolresult.MarkEmptySuccess(metadata)
	}
	return &toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content:    content,
		Metadata:   metadata,
	}
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
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("instant answer API 错误 (状态码 %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
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
		return []DuckDuckGoResult{}, nil
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
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTML 搜索错误 (状态码 %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
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
		return []DuckDuckGoResult{}, nil
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
