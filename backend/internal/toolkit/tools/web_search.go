package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
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
	// providers 按优先级排列的搜索引擎后端链，由 WEB_SEARCH_PROVIDERS 配置。
	providers []webSearchProvider
}

// webSearchProvider 一个搜索引擎后端。source 用于结果 metadata 标记
// （duckduckgo / duckduckgo_html / bing）。
type webSearchProvider struct {
	name   string
	search func(ctx context.Context, query string, count int) (source string, results []DuckDuckGoResult, err error)
}

const (
	// webSearchProvidersEnv 逗号分隔、按优先级排列的搜索引擎列表，取值
	// duckduckgo 或 bing。默认 "duckduckgo,bing"：DDG 网络不可达时快速
	// 失败并自动回退 Bing（内网/防火墙场景），DDG 可达环境行为不变。
	webSearchProvidersEnv      = "WEB_SEARCH_PROVIDERS"
	defaultWebSearchProviders  = "duckduckgo,bing"
	bingSearchURLCN            = "https://cn.bing.com/search?q=%s&count=%d"
	bingSearchURLWWW           = "https://www.bing.com/search?q=%s&count=%d"
	webSearchConnectTimeout    = 5 * time.Second
	webSearchTLSHandshakeTo    = 5 * time.Second
	webSearchResponseHeaderTo  = 8 * time.Second
	webSearchTotalTimeout      = 30 * time.Second
)

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

	tool := &WebSearchTool{
		BaseTool: toolkit.NewBaseTool(
			"web_search",
			"使用网络搜索引擎（默认 DuckDuckGo，可配置 Bing）搜索网络信息，返回相关网页标题、链接和摘要。若有多个不同搜索意图，请拆分为多个更小的 web_search 调用，每次只聚焦一个搜索目标。",
			"1.1.0",
			parameters,
			true,
		),
		httpClient: newSearchHTTPClient(),
	}
	tool.providers = tool.buildProviders()
	return tool
}

// newSearchHTTPClient 构造带快速失败语义的 HTTP 客户端：连接建立、
// TLS 握手、响应头等待都设短的超时，网络不可达时在数秒内报错并回退
// 下一个 provider，而不是被整体 Timeout 拖到 30s 才失败。
func newSearchHTTPClient() *http.Client {
	return &http.Client{
		Timeout: webSearchTotalTimeout,
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: webSearchConnectTimeout, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   webSearchTLSHandshakeTo,
			ResponseHeaderTimeout: webSearchResponseHeaderTo,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
		},
	}
}

// buildProviders 按环境变量构建 provider 链；配置缺失或全部非法时回退默认链。
func (w *WebSearchTool) buildProviders() []webSearchProvider {
	names := parseWebSearchProviders(os.Getenv(webSearchProvidersEnv))
	if len(names) == 0 {
		names = parseWebSearchProviders(defaultWebSearchProviders)
	}
	out := make([]webSearchProvider, 0, len(names))
	for _, name := range names {
		switch name {
		case "duckduckgo":
			out = append(out, webSearchProvider{name: name, search: w.searchDuckDuckGoProvider})
		case "bing":
			out = append(out, webSearchProvider{name: name, search: w.searchBingProvider})
		}
	}
	return out
}

// parseWebSearchProviders 解析 WEB_SEARCH_PROVIDERS：逗号分隔、去重、
// 小写化、忽略未知值。空输入返回空列表（由 buildProviders 回退默认）。
func parseWebSearchProviders(value string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, part := range strings.Split(value, ",") {
		name := strings.ToLower(strings.TrimSpace(part))
		if name != "duckduckgo" && name != "bing" {
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
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

	// 按配置顺序尝试各搜索引擎：首个成功（含零结果）即返回；
	// 前一个 provider 网络/传输失败时快速失败并回退到下一个。
	var failures []providerFailure
	for _, p := range w.providers {
		source, results, err := p.search(ctx, query, count)
		if err == nil {
			return w.successSearchResult(query, results, source), nil
		}
		failures = append(failures, providerFailure{name: p.name, err: err})
	}
	return w.failureSearchResult(query, failures), nil
}

// providerFailure 记录单个搜索引擎后端的失败，供诊断与模型处置。
type providerFailure struct {
	name string
	err  error
}

// failureSearchResult stamps structured recovery for transport/network failures
// so chat-log / Diagnose / disposition replay do not treat them as opaque
// TOOL_EXECUTION. primary 取链上第一个失败的 provider 错误，与旧行为一致。
func (w *WebSearchTool) failureSearchResult(query string, failures []providerFailure) *toolkit.ToolResult {
	var primary error
	if len(failures) > 0 {
		primary = failures[0].err
	}
	if primary == nil {
		primary = errors.New("web_search failed")
	}
	err := fmt.Errorf("搜索失败: %w", primary)

	// 错误分类按 provider 链顺序取第一个可识别的信号，避免后链的
	// 上游错误掩盖前链（如 DDG 网络不可达 + Bing 429）的网络根因。
	code := ""
	for _, f := range failures {
		if code = classifyWebSearchFailureCode(f.err); code != "" {
			break
		}
	}

	extra := map[string]interface{}{
		"query": query,
		"attempted_args": map[string]interface{}{
			"query": query,
		},
	}
	names := make([]string, 0, len(failures))
	providerErrors := make(map[string]string, len(failures))
	for _, f := range failures {
		names = append(names, f.name)
		providerErrors[f.name] = f.err.Error()
	}
	extra["providers_attempted"] = names
	extra["provider_errors"] = providerErrors

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
		strings.Contains(msg, "状态码 429") || strings.Contains(msg, "rate limit"):
		return string(runtimeerrors.ErrAPIRateLimit)
	case strings.Contains(msg, "http 502") || strings.Contains(msg, "http 503") ||
		strings.Contains(msg, "http 504") || strings.Contains(msg, "status 502") ||
		strings.Contains(msg, "status 503") || strings.Contains(msg, "status 504") ||
		strings.Contains(msg, "状态码 502") || strings.Contains(msg, "状态码 503") ||
		strings.Contains(msg, "状态码 504") ||
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

// searchDuckDuckGoProvider 保持原有 instant → HTML 双端点语义：
// instant 成功（含零结果，避免模型对空结果盲目重试）即视为该 provider
// 成功；传输失败时回退 HTML 端点；两者都失败时合并两个错误供分类诊断。
func (w *WebSearchTool) searchDuckDuckGoProvider(ctx context.Context, query string, count int) (string, []DuckDuckGoResult, error) {
	results, instantErr := w.searchDuckDuckGo(ctx, query, count)
	if instantErr == nil {
		return "duckduckgo", results, nil
	}
	htmlResults, htmlErr := w.searchDuckDuckGoHTML(ctx, query, count)
	if htmlErr == nil {
		return "duckduckgo_html", htmlResults, nil
	}
	return "", nil, errors.Join(instantErr, htmlErr)
}

// searchBingProvider 使用 Bing 搜索：CN 端点失败时回退 www 端点
// （二者在国内外网络的可达性不同，双端点覆盖内网/防火墙场景）。
func (w *WebSearchTool) searchBingProvider(ctx context.Context, query string, count int) (string, []DuckDuckGoResult, error) {
	results, err := w.searchBing(ctx, query, count, bingSearchURLCN)
	if err == nil {
		return "bing", results, nil
	}
	wwwResults, wwwErr := w.searchBing(ctx, query, count, bingSearchURLWWW)
	if wwwErr == nil {
		return "bing", wwwResults, nil
	}
	return "", nil, errors.Join(err, wwwErr)
}

// searchBing 请求单个 Bing 端点并解析 HTML 结果。
func (w *WebSearchTool) searchBing(ctx context.Context, query string, count int, endpoint string) ([]DuckDuckGoResult, error) {
	searchURL := fmt.Sprintf(endpoint, url.QueryEscape(query), count)
	if err := w.checkURL(searchURL); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "GET", searchURL, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")

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
		return nil, fmt.Errorf("Bing 搜索错误 (状态码 %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return parseBingResults(string(body), count), nil
}

// parseBingResults 解析 Bing SERP：每个 <li class="b_algo"> 为一个结果，
// 标题取 <h2> 内首个链接文本，摘要取块内首个 <p>（缺失时用标题兜底）。
func parseBingResults(htmlBody string, count int) []DuckDuckGoResult {
	results := make([]DuckDuckGoResult, 0)
	const marker = `class="b_algo"`
	pos := 0
	for len(results) < count {
		algoStart := strings.Index(htmlBody[pos:], marker)
		if algoStart == -1 {
			break
		}
		algoStart += pos
		algoEnd := strings.Index(htmlBody[algoStart+len(marker):], marker)
		if algoEnd == -1 {
			algoEnd = len(htmlBody)
		} else {
			algoEnd += algoStart + len(marker)
		}
		block := htmlBody[algoStart:algoEnd]
		if link, ok := extractBingLink(block); ok {
			results = append(results, link)
		}
		pos = algoEnd
	}
	return results
}

// extractBingLink 从单个 b_algo 块提取标题、链接和摘要。
func extractBingLink(block string) (DuckDuckGoResult, bool) {
	h2Start := strings.Index(block, "<h2")
	if h2Start == -1 {
		return DuckDuckGoResult{}, false
	}
	hrefStart := strings.Index(block[h2Start:], `href="`)
	if hrefStart == -1 {
		return DuckDuckGoResult{}, false
	}
	hrefStart += h2Start + len(`href="`)
	hrefEnd := strings.Index(block[hrefStart:], `"`)
	if hrefEnd == -1 {
		return DuckDuckGoResult{}, false
	}
	href := html.UnescapeString(block[hrefStart : hrefStart+hrefEnd])

	// href 之后可能还有尾随属性（如 h="ID=SERP,..."），
	// 标题从 a 标签的关闭尖括号之后开始。
	relEnd := strings.Index(block[hrefStart+hrefEnd:], ">")
	if relEnd == -1 {
		return DuckDuckGoResult{}, false
	}
	titleStart := hrefStart + hrefEnd + relEnd + 1
	titleEnd := strings.Index(block[titleStart:], "</a>")
	if titleEnd == -1 {
		return DuckDuckGoResult{}, false
	}
	title := html.UnescapeString(strings.TrimSpace(stripTags(block[titleStart : titleStart+titleEnd])))

	// 摘要：块内第一个 <p>...</p>，Bing 常见于 <div class="b_caption"> 内。
	snippet := ""
	if pStart := strings.Index(block, "<p"); pStart != -1 {
		contentStart := strings.Index(block[pStart:], ">")
		if contentStart != -1 {
			contentStart += pStart + 1
			pEnd := strings.Index(block[contentStart:], "</p>")
			if pEnd != -1 {
				snippet = html.UnescapeString(strings.TrimSpace(stripTags(block[contentStart : contentStart+pEnd])))
			}
		}
	}
	if snippet == "" {
		snippet = title
	}

	return DuckDuckGoResult{
		Title:   title,
		URL:     decodeBingRedirect(href),
		Snippet: snippet,
	}, true
}

// decodeBingRedirect 解码 Bing /ck/ 跳转链接：真实目标 URL 在 u 参数中，
// 格式为 "a1a" + base64url(目标URL)（也可能无前缀）。只有解码结果是
// http(s) URL 才替换，否则返回原始链接，避免把垃圾字节当目标。
func decodeBingRedirect(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if !strings.Contains(parsed.Path, "/ck/") {
		return raw
	}
	target := parsed.Query().Get("u")
	if target == "" {
		return raw
	}
	encoded := strings.TrimPrefix(target, "a1a")
	for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding} {
		decoded, err := enc.DecodeString(encoded)
		if err == nil && (strings.HasPrefix(string(decoded), "http://") || strings.HasPrefix(string(decoded), "https://")) {
			return string(decoded)
		}
	}
	return raw
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
