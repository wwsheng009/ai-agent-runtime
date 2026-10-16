package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
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
	// bingLocale Bing 端点的 setlang/mkt 参数（默认英文 en / en-US）。
	bingLocale bingSearchLocale
	// native 是原生搜索（P2-5b/c）解析后的路由配置；mode=off 时链不含 native。
	native webSearchNativeConfig
	// configuredProviders 是 runtime.yaml webSearch.providers 的链序回退
	// （WEB_SEARCH_PROVIDERS 环境变量优先）。
	configuredProviders string
	// providerConfig 查询全局 agent 配置（原生 target 解析用；测试可替换）。
	providerConfig func() *agentconfig.Config
}

// webSearchProvider 一个搜索引擎后端。source 用于结果 metadata 标记
// （duckduckgo / duckduckgo_html / bing / bing_cn）。
type webSearchProvider struct {
	name   string
	search func(ctx context.Context, query string, count int) (source string, results []DuckDuckGoResult, err error)
}

// webSearchAttempt is one provider's result after the client-side domain filter
// is applied. rawCount/filteredOut preserve the pre-filter accounting. The
// chain keeps these attempts so an empty or low-quality result can trigger the
// next provider while still preserving the best result for the final response.
type webSearchAttempt struct {
	name        string
	source      string
	results     []DuckDuckGoResult
	rawCount    int
	quality     webSearchQuality
	filteredOut int
	finalHost   string
}

const (
	// webSearchProvidersEnv 逗号分隔、按优先级排列的搜索引擎列表，取值
	// bing（Bing 英文版 www.bing.com）、duckduckgo、bing_cn（Bing 中文站
	// cn.bing.com）。默认 "bing,duckduckgo,bing_cn"：Bing 英文版优先，
	// DuckDuckGo 二级备份，cn.bing.com 最后兜底；链序与取舍均可配置。
	webSearchProvidersEnv     = "WEB_SEARCH_PROVIDERS"
	defaultWebSearchProviders = "bing,duckduckgo,bing_cn"
	// webSearchBingSetLangEnv / webSearchBingMarketEnv Bing 的语言与市场
	// 参数：setlang 控制界面与内容语言，mkt 控制结果市场（语言+地区）。
	// 默认英文（en / en-US）；空值或非法值回退默认，避免按出口 IP 地区
	// 返回中文结果。
	webSearchBingSetLangEnv = "WEB_SEARCH_BING_SETLANG"
	webSearchBingMarketEnv  = "WEB_SEARCH_BING_MKT"
	defaultBingSetLang      = "en"
	defaultBingMarket       = "en-US"
	// bingSearchURLWWW 为 Bing 英文版默认端点，bingSearchURLCN 为中文站
	// 端点（默认链中仅作最后兜底）。语言/市场参数由 searchBing 用
	// url.Values 安全拼装，不在端点常量中硬编码。
	// 链序取值额外支持 native（P2-5 原生搜索；mode=off 时自动剔除）。
	bingSearchURLWWW          = "https://www.bing.com/search"
	bingSearchURLCN           = "https://cn.bing.com/search"
	webSearchConnectTimeout   = 5 * time.Second
	webSearchTLSHandshakeTo   = 5 * time.Second
	webSearchResponseHeaderTo = 8 * time.Second
	webSearchTotalTimeout     = 30 * time.Second
)

// NewWebSearchTool 创建网络搜索工具（仅环境变量/内置默认；等价于
// NewWebSearchToolWithConfig(nil)）。
func NewWebSearchTool() *WebSearchTool {
	return NewWebSearchToolWithConfig(nil)
}

// NewWebSearchToolWithConfig 按宿主运行时配置构造工具：webSearch.providers
// 提供链序回退，webSearch.native 控制原生搜索成员（P2-5b/c）。cfg 为 nil 或
// 分节缺省时行为与历史一致。
func NewWebSearchToolWithConfig(cfg *runtimecfg.RuntimeConfig) *WebSearchTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "搜索关键词或问题。若包含多个不同搜索意图，请拆分为多个更小的 query 调用，每次只聚焦一个搜索目标；需要限定时间时配合 time_range 使用。",
			},
			"count": map[string]interface{}{
				"type":        "integer",
				"description": "返回结果数量（默认 5，最大 10）",
				"default":     5,
			},
			"time_range": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"day", "week", "month", "year"},
				"description": "可选：只返回指定时间范围内的结果（day/week/month/year）。provider 不支持时会忽略该参数并在 metadata 中标记。",
			},
			"language": map[string]interface{}{
				"type":        "string",
				"description": "可选：结果语言/市场提示（如 auto、en、zh-CN）。auto 会根据查询中的 CJK 字符选择中文或默认英文市场。",
			},
			"allowed_domains": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "只保留这些域名（含子域）的结果，例如 [\"example.com\"]；与 blocked_domains 互斥。可传数组或逗号分隔字符串。",
			},
			"blocked_domains": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "排除这些域名（含子域）的结果，例如 [\"pinterest.com\"]；与 allowed_domains 互斥。可传数组或逗号分隔字符串。",
			},
		},
		"required": []string{"query"},
	}

	tool := &WebSearchTool{
		BaseTool: toolkit.NewBaseTool(
			"web_search",
			"使用网络搜索引擎（默认 Bing 英文版 www.bing.com，DuckDuckGo 二级备份，cn.bing.com 最后兜底）搜索网络信息，返回相关网页标题、链接和摘要。空结果或低相关结果会自动尝试后续引擎；可选 time_range=day/week/month/year 限定时间范围，language=auto/en/zh-CN 提示结果语言。若有多个不同搜索意图，请拆分为多个更小的 web_search 调用，每次只聚焦一个搜索目标。支持客户端域名过滤 allowed_domains/blocked_domains（互斥）。",
			"1.2.0",
			parameters,
			true,
		),
		httpClient:          newSearchHTTPClient(),
		bingLocale:          resolveBingSearchLocale(),
		native:              resolveWebSearchNativeConfig(cfg),
		configuredProviders: webSearchProvidersFromConfig(cfg),
		providerConfig:      agentconfig.GetGlobalConfig,
	}
	tool.providers = tool.buildProviders()
	return tool
}

// webSearchOptions carries optional, provider-neutral request hints parsed
// from tool arguments. They travel through context so the provider function
// signature stays compatible with existing tests and registrations.
type webSearchOptions struct {
	timeRange string
	language  string
}

type webSearchContextKey struct{}

// webSearchRunDiagnostics carries per-Execute observations from provider
// functions back to the chain without changing the provider function
// signature. Execute creates one pointer per call and resets finalHost before
// each provider attempt.
type webSearchRunDiagnostics struct {
	finalHost string
}

type webSearchDiagnosticsKey struct{}

func withWebSearchOptions(ctx context.Context, options webSearchOptions) context.Context {
	return context.WithValue(ctx, webSearchContextKey{}, options)
}

func webSearchOptionsFromContext(ctx context.Context) webSearchOptions {
	options, _ := ctx.Value(webSearchContextKey{}).(webSearchOptions)
	return options
}

func withWebSearchDiagnostics(ctx context.Context, diagnostics *webSearchRunDiagnostics) context.Context {
	return context.WithValue(ctx, webSearchDiagnosticsKey{}, diagnostics)
}

func webSearchDiagnosticsFromContext(ctx context.Context) *webSearchRunDiagnostics {
	diagnostics, _ := ctx.Value(webSearchDiagnosticsKey{}).(*webSearchRunDiagnostics)
	return diagnostics
}

func recordWebSearchFinalHost(ctx context.Context, resp *http.Response) {
	if resp == nil || resp.Request == nil || resp.Request.URL == nil {
		return
	}
	if diagnostics := webSearchDiagnosticsFromContext(ctx); diagnostics != nil {
		diagnostics.finalHost = resp.Request.URL.Hostname()
	}
}

func parseWebSearchOptions(params map[string]interface{}) (webSearchOptions, error) {
	var options webSearchOptions
	if raw, ok := params["time_range"]; ok && raw != nil {
		value, ok := raw.(string)
		if !ok {
			return options, fmt.Errorf("time_range 参数必须是字符串")
		}
		value = strings.ToLower(strings.TrimSpace(value))
		switch value {
		case "":
		case "day", "week", "month", "year":
			options.timeRange = value
		default:
			return options, fmt.Errorf("time_range 仅支持 day/week/month/year")
		}
	}
	if raw, ok := params["language"]; ok && raw != nil {
		value, ok := raw.(string)
		if !ok {
			return options, fmt.Errorf("language 参数必须是字符串")
		}
		value = strings.TrimSpace(value)
		if strings.EqualFold(value, "auto") {
			options.language = "auto"
		} else if value != "" {
			if !bingLocaleValuePattern.MatchString(value) {
				return options, fmt.Errorf("language 格式无效")
			}
			options.language = value
		}
	}
	return options, nil
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

// buildProviders 构建 provider 链：链序来自 WEB_SEARCH_PROVIDERS（优先）、
// runtime.yaml webSearch.providers，最后回退默认链。native 成员仅在
// webSearch.native.mode 非 off 时参与：prefer 要求本地可用（能力位/凭据/
// 端点均可解析），force 始终保留（执行期报结构化错误）；显式 mode 但链序未
// 列出 native 时置于链首，列入时按位置参与。
func (w *WebSearchTool) buildProviders() []webSearchProvider {
	names := w.resolveChainNames()
	if w.native.enabled() && !containsProviderName(names, webSearchNativeName) {
		names = append([]string{webSearchNativeName}, names...)
	}
	out := make([]webSearchProvider, 0, len(names))
	for _, name := range names {
		switch name {
		case webSearchNativeName:
			if !w.native.enabled() {
				continue
			}
			if w.native.mode != webSearchNativeModeForce && !w.nativeAvailable() {
				continue
			}
			out = append(out, webSearchProvider{name: name, search: w.searchNativeProvider})
		case "duckduckgo":
			out = append(out, webSearchProvider{name: name, search: w.searchDuckDuckGoProvider})
		case "bing":
			out = append(out, webSearchProvider{name: name, search: w.searchBingProvider})
		case "bing_cn":
			out = append(out, webSearchProvider{name: name, search: w.searchBingCNProvider})
		}
	}
	return out
}

// resolveChainNames 解析链序：环境变量 > runtime 配置 > 内置默认；空/全非法
// 输入回退默认链。
func (w *WebSearchTool) resolveChainNames() []string {
	if names := parseWebSearchProviders(os.Getenv(webSearchProvidersEnv)); len(names) > 0 {
		return names
	}
	if names := parseWebSearchProviders(w.configuredProviders); len(names) > 0 {
		return names
	}
	return parseWebSearchProviders(defaultWebSearchProviders)
}

// containsProviderName reports whether the resolved chain already lists name.
func containsProviderName(names []string, name string) bool {
	for _, candidate := range names {
		if candidate == name {
			return true
		}
	}
	return false
}

// parseWebSearchProviders 解析 WEB_SEARCH_PROVIDERS：逗号分隔、去重、
// 小写化、忽略未知值。空输入返回空列表（由 buildProviders 回退默认）。
func parseWebSearchProviders(value string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, part := range strings.Split(value, ",") {
		name := strings.ToLower(strings.TrimSpace(part))
		if name != "duckduckgo" && name != "bing" && name != "bing_cn" && name != webSearchNativeName {
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

// bingSearchLocale Bing 请求的语言/市场参数：setlang 控制界面与内容语言，
// mkt 控制结果市场（语言+地区）。
type bingSearchLocale struct {
	setLang string
	mkt     string
}

// bingLocaleValuePattern 语言/市场取值形态：主语言 2-3 个字母，可选 1-2 段
// 字母数字子标签（en、en-US、zh-hans）。
var bingLocaleValuePattern = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)

// resolveBingSearchLocale 从环境变量解析 Bing 语言/市场，空值或非法值回退
// 英文默认（en / en-US）；下划线分隔归一为连字符。
func resolveBingSearchLocale() bingSearchLocale {
	return bingSearchLocale{
		setLang: normalizeBingLocaleValue(os.Getenv(webSearchBingSetLangEnv), defaultBingSetLang),
		mkt:     normalizeBingLocaleValue(os.Getenv(webSearchBingMarketEnv), defaultBingMarket),
	}
}

// normalizeBingLocaleValue 校验单个语言/市场值；空或非法回退默认值。
func normalizeBingLocaleValue(raw, fallback string) string {
	value := strings.ReplaceAll(strings.TrimSpace(raw), "_", "-")
	if value == "" || !bingLocaleValuePattern.MatchString(value) {
		return fallback
	}
	return value
}

// acceptLanguageForMarket 从 mkt 派生 Accept-Language 头，保持与 URL 参数
// 一致：en-US → "en-US,en;q=0.9"，zh-CN → "zh-CN,zh;q=0.9"。
func acceptLanguageForMarket(mkt string) string {
	mkt = strings.TrimSpace(mkt)
	if mkt == "" {
		mkt = defaultBingMarket
	}
	if idx := strings.IndexByte(mkt, '-'); idx > 0 {
		return fmt.Sprintf("%s,%s;q=0.9", mkt, mkt[:idx])
	}
	return mkt
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

	// 客户端域名过滤：provider 侧没有同时适配 Bing/DDG 的域名过滤能力，
	// 因此在解析出的结果 URL 上执行（互斥校验失败即拒绝，避免半配置歧义）。
	filter, filterErr := parseWebSearchDomainFilter(params)
	if filterErr != nil {
		return toolResultFailureWithCode(
			fmt.Errorf("web_search 域名过滤参数无效: %w", filterErr),
			string(runtimeerrors.ErrToolInvalidArgs),
			"allowed_domains 与 blocked_domains 互斥，且各自至少需要一个有效域名（可传数组或逗号分隔字符串）。请只保留一个过滤器后重试。",
			map[string]interface{}{
				"query":          query,
				"failure_class":  "invalid_domain_filter",
				"attempted_args": map[string]interface{}{"query": query},
			},
		), nil
	}
	options, optionsErr := parseWebSearchOptions(params)
	if optionsErr != nil {
		return toolResultFailureWithCode(
			fmt.Errorf("web_search 可选参数无效: %w", optionsErr),
			string(runtimeerrors.ErrToolInvalidArgs),
			"请只使用 time_range=day/week/month/year 和 language=auto/en/zh-CN 等受支持的值后重试。",
			map[string]interface{}{
				"query":          query,
				"failure_class":  "invalid_search_option",
				"attempted_args": map[string]interface{}{"query": query},
			},
		), nil
	}
	ctx = withWebSearchOptions(ctx, options)
	diagnostics := &webSearchRunDiagnostics{}
	ctx = withWebSearchDiagnostics(ctx, diagnostics)

	// 按配置顺序尝试各搜索引擎。空结果、过滤后空结果和低相关结果都
	// 不能直接结束链路：它们是“换一个引擎再试”的信号。只有达到相关性
	// 阈值的结果才立即返回；否则保留最佳尝试，链路结束后再决定返回低
	// 质量成功、空成功还是结构化失败。
	var failures []providerFailure
	var attempts []webSearchAttempt
	bestIndex := -1
	for _, p := range w.providers {
		diagnostics.finalHost = ""
		source, results, err := p.search(ctx, query, count)
		if err != nil {
			failures = append(failures, providerFailure{name: p.name, err: err})
			continue
		}
		kept, dropped := filter.apply(results)
		attempt := webSearchAttempt{
			name:        p.name,
			source:      source,
			results:     kept,
			rawCount:    len(results),
			quality:     assessWebSearchQuality(query, kept),
			filteredOut: dropped,
			finalHost:   diagnostics.finalHost,
		}
		attempts = append(attempts, attempt)
		currentIndex := len(attempts) - 1
		if bestIndex < 0 || attempts[currentIndex].quality.Score > attempts[bestIndex].quality.Score {
			bestIndex = currentIndex
		}
		if attempts[currentIndex].quality.Tier == "ok" && len(attempts[currentIndex].results) > 0 {
			return w.successSearchAttempt(query, attempts[currentIndex], filter, attempts, failures, options), nil
		}
	}

	if bestIndex >= 0 {
		return w.successSearchAttempt(query, attempts[bestIndex], filter, attempts, failures, options), nil
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
	// Native-routing configuration failures carry an explicit typed marker so
	// they never get misread as transport errors by the heuristics below.
	var unavailable *searchProviderUnavailableError
	if errors.As(err, &unavailable) {
		return string(runtimeerrors.ErrSearchProviderUnavailable)
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
		strings.Contains(msg, "bad gateway") || strings.Contains(msg, "service unavailable") ||
		strings.Contains(msg, "captcha") || strings.Contains(msg, "unusual traffic") ||
		strings.Contains(msg, "verify you are human") || strings.Contains(msg, "are you a robot") ||
		strings.Contains(msg, "反爬") || strings.Contains(msg, "验证页") ||
		strings.Contains(msg, "异常流量"):
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
	case runtimeerrors.ErrSearchProviderUnavailable:
		return "Native web search is configured with mode=force but unavailable. Fix webSearch.native (provider/model capability, credential, endpoint) or switch mode to prefer/off; do not retry the same query."
	default:
		return ""
	}
}

// successSearchResultWithCounts builds a successful search ToolResult from an
// already-filtered result set. providerCount and dropped preserve the raw
// provider payload size and filter accounting for metadata.
func (w *WebSearchTool) successSearchResultWithCounts(query string, results []DuckDuckGoResult, source string, filter webSearchDomainFilter, providerCount, dropped int) *toolkit.ToolResult {
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
	if filter.active() {
		metadata["provider_count"] = providerCount
		metadata["filtered_out"] = dropped
		for key, value := range filter.metadata() {
			metadata[key] = value
		}
	}
	content := w.formatResults(results)
	if len(results) == 0 && dropped > 0 {
		// 搜索本身成功，但过滤器排除了全部结果：这是「过滤组合需要放宽」的
		// 信号，不是查询无结果；给出过滤器专属 next_action。
		content = fmt.Sprintf(
			"未找到匹配的内容（搜索返回 %d 条结果，全部被域名过滤器排除）",
			dropped,
		)
		toolresult.MarkEmptySuccess(metadata)
		metadata[toolresult.MetadataNextActionKey] = fmt.Sprintf(
			"域名过滤排除了全部 %d 条结果：放宽或移除 allowed_domains/blocked_domains，或改写查询；不要原样重试同一过滤组合。",
			dropped,
		)
	} else if len(results) == 0 {
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

// successSearchAttempt stamps chain diagnostics onto the selected provider
// result. Low-quality results are still successful (the search itself ran),
// but the metadata and next_action tell the model to refine or split the query
// instead of treating the output as a confident hit.
func (w *WebSearchTool) successSearchAttempt(query string, attempt webSearchAttempt, filter webSearchDomainFilter, attempts []webSearchAttempt, failures []providerFailure, options webSearchOptions) *toolkit.ToolResult {
	result := w.successSearchResultWithCounts(query, attempt.results, attempt.source, filter, attempt.rawCount, attempt.filteredOut)
	quality := attempt.quality
	if result.Metadata == nil {
		result.Metadata = map[string]interface{}{}
	}
	result.Metadata["quality"] = quality.Tier
	result.Metadata["quality_score"] = quality.Score
	if len(quality.Terms) > 0 {
		result.Metadata["quality_terms"] = quality.Terms
	}
	names := make([]string, 0, len(attempts))
	for _, item := range attempts {
		names = append(names, item.name)
	}
	result.Metadata["providers_attempted"] = names
	if attempt.finalHost != "" {
		result.Metadata["final_host"] = attempt.finalHost
	}
	if len(failures) > 0 {
		providerErrors := make(map[string]string, len(failures))
		for _, failure := range failures {
			providerErrors[failure.name] = failure.err.Error()
		}
		result.Metadata["provider_errors"] = providerErrors
	}
	if options.timeRange != "" {
		result.Metadata["time_range"] = options.timeRange
		result.Metadata["time_range_supported"] = attempt.source == "bing" ||
			attempt.source == "bing_cn" ||
			attempt.source == "duckduckgo_html"
	}
	if options.language != "" {
		result.Metadata["language"] = options.language
		result.Metadata["language_supported"] = attempt.source == "bing" || attempt.source == "bing_cn"
	}
	if quality.Tier != "ok" && len(attempt.results) > 0 {
		if _, exists := result.Metadata[toolresult.MetadataNextActionKey]; !exists {
			result.Metadata[toolresult.MetadataNextActionKey] = fmt.Sprintf(
				"LOW_SEARCH_QUALITY: returned results matched only %.0f%% of the query terms. Refine the query, split it into focused searches, or fetch a likely source directly; do not treat these results as a confident hit.",
				quality.Score*100,
			)
		}
	}
	return result
}

// searchDuckDuckGoProvider 保持 instant → HTML 双端点语义，但零结果不再
// 直接结束链路：Instant Answer 命中时立即返回，零结果时继续尝试 HTML；
// HTML 失败则保留 instant 的零结果语义，让外层 provider 链继续兜底。
func (w *WebSearchTool) searchDuckDuckGoProvider(ctx context.Context, query string, count int) (string, []DuckDuckGoResult, error) {
	results, instantErr := w.searchDuckDuckGo(ctx, query, count)
	if instantErr == nil && len(results) > 0 {
		return "duckduckgo", results, nil
	}
	htmlResults, htmlErr := w.searchDuckDuckGoHTML(ctx, query, count)
	if htmlErr == nil {
		return "duckduckgo_html", htmlResults, nil
	}
	if instantErr == nil {
		return "duckduckgo", results, nil
	}
	return "", nil, errors.Join(instantErr, htmlErr)
}

// searchBingProvider 使用 Bing 英文版（www.bing.com）搜索：语言/市场由
// WEB_SEARCH_BING_SETLANG / WEB_SEARCH_BING_MKT 配置，默认 en / en-US。
// 是否回退其他引擎由 provider 链顺序（WEB_SEARCH_PROVIDERS）决定。
func (w *WebSearchTool) searchBingProvider(ctx context.Context, query string, count int) (string, []DuckDuckGoResult, error) {
	results, err := w.searchBing(ctx, query, count, bingSearchURLWWW)
	if err != nil {
		return "", nil, err
	}
	return "bing", results, nil
}

// searchBingCNProvider 使用 Bing 中文站（cn.bing.com）搜索：默认链中仅作为
// 最后兜底（内网/防火墙场景可达），与英文站共用语言/市场配置。
func (w *WebSearchTool) searchBingCNProvider(ctx context.Context, query string, count int) (string, []DuckDuckGoResult, error) {
	results, err := w.searchBing(ctx, query, count, bingSearchURLCN)
	if err != nil {
		return "", nil, err
	}
	return "bing_cn", results, nil
}

// buildBingSearchURL 用 url.Values 安全拼装 Bing 搜索 URL：查询词与可配置的
// 语言/市场值统一编码，避免注入额外查询参数。
func buildBingSearchURL(endpoint, query string, count int, locale bingSearchLocale) string {
	params := url.Values{}
	params.Set("q", query)
	params.Set("count", strconv.Itoa(count))
	params.Set("setlang", locale.setLang)
	params.Set("mkt", locale.mkt)
	return endpoint + "?" + params.Encode()
}

func buildBingSearchURLWithOptions(endpoint, query string, count int, locale bingSearchLocale, options webSearchOptions) string {
	params := url.Values{}
	params.Set("q", query)
	params.Set("count", strconv.Itoa(count))
	params.Set("setlang", locale.setLang)
	params.Set("mkt", locale.mkt)
	if options.timeRange != "" {
		if filter := bingTimeRangeFilter(options.timeRange); filter != "" {
			params.Set("filters", filter)
		}
	}
	return endpoint + "?" + params.Encode()
}

func bingTimeRangeFilter(timeRange string) string {
	switch timeRange {
	case "day":
		return `ex1:"ez1"`
	case "week":
		return `ex1:"ez2"`
	case "month":
		return `ex1:"ez3"`
	case "year":
		now := time.Now().UTC()
		start := now.AddDate(-1, 0, 0)
		return fmt.Sprintf(`ex1:"ez5_%d_%d"`, start.Unix()/86400, now.Unix()/86400)
	default:
		return ""
	}
}

func resolveWebSearchBingLocale(base bingSearchLocale, query string, options webSearchOptions) bingSearchLocale {
	if options.language != "" && !strings.EqualFold(options.language, "auto") {
		value := normalizeBingLocaleValue(options.language, base.setLang)
		base.setLang = value
		if idx := strings.IndexByte(value, '-'); idx > 0 {
			base.mkt = value
		} else {
			base.mkt = value + "-US"
		}
		return base
	}
	if (options.language == "" || strings.EqualFold(options.language, "auto")) && containsCJK(query) {
		base.setLang = "zh-hans"
		base.mkt = "zh-CN"
	}
	return base
}

// searchBing 请求单个 Bing 端点并解析 HTML 结果。
func (w *WebSearchTool) searchBing(ctx context.Context, query string, count int, endpoint string) ([]DuckDuckGoResult, error) {
	options := webSearchOptionsFromContext(ctx)
	locale := resolveWebSearchBingLocale(w.bingLocale, query, options)
	searchURL := buildBingSearchURLWithOptions(endpoint, query, count, locale, options)
	if err := w.checkURL(searchURL); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "GET", searchURL, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Accept-Language", acceptLanguageForMarket(locale.mkt))

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	recordWebSearchFinalHost(ctx, resp)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Bing 搜索错误 (状态码 %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	htmlBody := string(body)
	results := parseBingResults(htmlBody, count)
	if len(results) == 0 && shouldTryBingRSS(htmlBody) {
		if rssResults, rssErr := w.searchBingRSS(ctx, query, count, endpoint, locale, options); rssErr == nil && len(rssResults) > 0 {
			results = rssResults
		}
	}
	if len(results) == 0 && looksLikeBingBlockedPage(htmlBody) {
		return nil, fmt.Errorf("Bing 搜索返回验证/反爬页面，无法解析结果")
	}
	return results, nil
}

func shouldTryBingRSS(body string) bool {
	return strings.Contains(body, `id="b_results"`) || looksLikeBingBlockedPage(body)
}

func (w *WebSearchTool) searchBingRSS(ctx context.Context, query string, count int, endpoint string, locale bingSearchLocale, options webSearchOptions) ([]DuckDuckGoResult, error) {
	searchURL := buildBingSearchURLWithOptions(endpoint, query, count, locale, options) + "&format=rss"
	if err := w.checkURL(searchURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", searchURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Accept-Language", acceptLanguageForMarket(locale.mkt))
	resp, err := w.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	recordWebSearchFinalHost(ctx, resp)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Bing RSS 搜索错误 (状态码 %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return parseBingRSSResults(string(body), count)
}

type bingRSSFeed struct {
	Channel struct {
		Items []bingRSSItem `xml:"item"`
	} `xml:"channel"`
}

type bingRSSItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	Description string `xml:"description"`
}

func parseBingRSSResults(xmlBody string, count int) ([]DuckDuckGoResult, error) {
	var feed bingRSSFeed
	if err := xml.Unmarshal([]byte(xmlBody), &feed); err != nil {
		return nil, err
	}
	results := make([]DuckDuckGoResult, 0, len(feed.Channel.Items))
	for _, item := range feed.Channel.Items {
		if len(results) >= count {
			break
		}
		link := strings.TrimSpace(item.Link)
		title := strings.TrimSpace(item.Title)
		if link == "" || title == "" {
			continue
		}
		snippet := html.UnescapeString(stripTags(item.Description))
		if snippet == "" {
			snippet = title
		}
		results = append(results, DuckDuckGoResult{Title: title, URL: link, Snippet: snippet})
	}
	return results, nil
}

func looksLikeBingBlockedPage(body string) bool {
	lower := strings.ToLower(body)
	for _, marker := range []string{
		"captcha",
		"unusual traffic",
		"verify you are human",
		"are you a robot",
		"solve the puzzle",
		"验证",
		"异常流量",
		"安全验证",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
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
	recordWebSearchFinalHost(ctx, resp)

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
	params := url.Values{}
	params.Set("q", query)
	if df := duckDuckGoTimeRangeParam(webSearchOptionsFromContext(ctx).timeRange); df != "" {
		params.Set("df", df)
	}
	searchURL := "https://html.duckduckgo.com/html/?" + params.Encode()
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
	recordWebSearchFinalHost(ctx, resp)

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

func duckDuckGoTimeRangeParam(timeRange string) string {
	switch timeRange {
	case "day":
		return "d"
	case "week":
		return "w"
	case "month":
		return "m"
	case "year":
		return "y"
	default:
		return ""
	}
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
