package providerops

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
	httpclient "github.com/wwsheng009/ai-agent-runtime/internal/pkg/httpclient"
)

type ModelInfo struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
	// InputModalities 是端点声明的输入模态（text/image/audio/video/file）。
	// 类别解析器负责把嵌套形状（如 openrouter 的 architecture.input_modalities）
	// 归一到这里。
	InputModalities []string `json:"input_modalities,omitempty"`
	// ReasoningModel 是端点显式声明的 reasoning/thinking 模型标记；此前只能由
	// reasoning_efforts 非空隐式推断，导致「声明了推理能力但没有档位列表」的
	// 模型被当成普通模型。
	ReasoningModel         bool     `json:"reasoning_model,omitempty"`
	ReasoningEfforts       []string `json:"reasoning_efforts,omitempty"`
	DefaultReasoningEffort string   `json:"default_reasoning_effort,omitempty"`
	MaxContextTokens       int      `json:"max_context_tokens,omitempty"`
	// MaxTokens 是端点声明的最大输出 token 数（openrouter: top_provider
	// .max_completion_tokens）。
	MaxTokens int `json:"max_tokens,omitempty"`
	// SupportsTools 是端点声明支持函数调用（openrouter: supported_parameters
	// 含 tools）；端点未声明该清单时为 false（未知，不代表不支持）。
	SupportsTools       bool                   `json:"supports_tools,omitempty"`
	SupportsRemoteCodex bool                   `json:"supports_remote_codex,omitempty"`
	Raw                 map[string]interface{} `json:"-"`
}

type FetchModelsRequest struct {
	Config        *config.Config
	ProviderName  string
	Provider      config.Provider
	LoginProtocol string
	ModelsPath    string
	Timeout       time.Duration
}

type FetchModelsResult struct {
	Endpoint   string      `json:"endpoint"`
	StatusCode int         `json:"status_code"`
	Models     []ModelInfo `json:"models"`
	// Category 是本次响应命中的解析类别（generic/openrouter/...），便于调用方
	// 在 UI 或日志里说明「用哪种形状解析的」。
	Category   ModelListCategory `json:"category,omitempty"`
	VerifiedAt string            `json:"verified_at"`
}

// FetchModels 等价于 commands.validateProviderModels；ctx 用于请求取消，
// 传 nil 时等价 context.Background()。
func FetchModels(ctx context.Context, req FetchModelsRequest) (*FetchModelsResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return fetchModels(ctx, req)
}

func fetchModels(ctx context.Context, req FetchModelsRequest) (*FetchModelsResult, error) {
	provider := req.Provider
	if req.Config != nil {
		provider.Headers = config.EffectiveProviderHeaders(req.Config.Providers.Headers, provider.Headers)
	}
	loginProtocol := NormalizeLoginProtocol(req.LoginProtocol, req.Provider.AuthMode)
	modelsPath := resolveProviderModelsPath(loginProtocol, provider, req.ModelsPath)
	endpoint, err := buildProviderModelsURL(provider, modelsPath)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(provider.GetAPIKey()) == "" && !strings.EqualFold(provider.AuthMode, "oauth") {
		return nil, fmt.Errorf("api key is required for provider models validation")
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create models request: %w", err)
	}
	headers := buildProviderModelsHeaders(provider, loginProtocol)
	for key, value := range headers {
		if strings.TrimSpace(value) == "" {
			continue
		}
		httpReq.Header.Set(key, value)
	}
	httpReq.Header.Set("Accept", "application/json")

	client := http.DefaultClient
	if req.Config != nil {
		client = httpclient.GetHTTPClientWithProvider(req.Config, &provider)
	}
	if req.Timeout > 0 {
		cloned := *client
		cloned.Timeout = req.Timeout
		client = &cloned
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("models request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, fmt.Errorf("read models response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("models endpoint %s returned HTTP %d: %s", endpoint, resp.StatusCode, responsePreview(body, 600))
	}
	// 类别先按 provider 名称 / base_url 判定（openrouter.ai 等），未命中再看
	// 载荷形状：镜像站、别名域名同样能走到对应解析器。
	category := DetectModelListCategory(req.ProviderName, provider.BaseURL, body)
	models, err := parseProviderModelsResponseForCategory(body, loginProtocol, category)
	if err != nil {
		return nil, fmt.Errorf("parse models response from %s: %w", endpoint, err)
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("models endpoint %s returned an empty model list", endpoint)
	}
	return &FetchModelsResult{
		Endpoint:   endpoint,
		StatusCode: resp.StatusCode,
		Models:     models,
		Category:   category,
		VerifiedAt: time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// ---------------------------------------------------------------------------
// 导出面：commands 侧 provider_models.go 的薄转发层目标。fetch/parse/normalize
// 是 CLI、web client 与 runtime server（/api/runtime/providers/fetch-models、
// auto-import、probe）共用的单一实现来源。
// ---------------------------------------------------------------------------

// BuildProviderModelsHeaders 构造 /models 请求头（鉴权 + {session_id} 模板替换）。
func BuildProviderModelsHeaders(provider config.Provider, loginProtocol string) map[string]string {
	return buildProviderModelsHeaders(provider, loginProtocol)
}

// ResolveProviderModelsPath 解析 provider 配置 / 覆盖值为实际 models path。
func ResolveProviderModelsPath(loginProtocol string, provider config.Provider, override string) string {
	return resolveProviderModelsPath(loginProtocol, provider, override)
}

// DefaultModelsPath 按协议 + base_url 推断默认 models path。
func DefaultModelsPath(loginProtocol, baseURL string) string {
	return defaultModelsPath(loginProtocol, baseURL)
}

// BuildProviderModelsURL 拼接最终 /models 请求 URL。
func BuildProviderModelsURL(provider config.Provider, modelsPath string) (string, error) {
	return buildProviderModelsURL(provider, modelsPath)
}

// ParseProviderModelsResponse 解析 /models 响应体为模型清单（兼容多种网关形状）。
func ParseProviderModelsResponse(raw []byte, loginProtocol string) ([]ModelInfo, error) {
	return parseProviderModelsResponse(raw, loginProtocol)
}

// ParseProviderModelsResponseForCategory 按显式类别解析 /models 响应体；类别为
// 空或未注册时按通用扁平形状解析。
func ParseProviderModelsResponseForCategory(raw []byte, loginProtocol string, category ModelListCategory) ([]ModelInfo, error) {
	return parseProviderModelsResponseForCategory(raw, loginProtocol, category)
}

// DedupeProviderModels 按模型 ID 去重并保持输入顺序。
func DedupeProviderModels(models []ModelInfo) []ModelInfo { return dedupeProviderModels(models) }

// NormalizeProviderModelID 规范化单个模型 ID（协议特定处理）。
func NormalizeProviderModelID(id, loginProtocol string) string {
	return normalizeProviderModelID(id, loginProtocol)
}

// DedupeProviderStringOptions 字符串选项去重（去空白、保持顺序）。
func DedupeProviderStringOptions(values []string) []string {
	return dedupeProviderStringOptions(values)
}

// ResponsePreview 截断响应体用于错误消息展示。
func ResponsePreview(raw []byte, limit int) string { return responsePreview(raw, limit) }

// ModelsEndpointAllowsAnonymous 探测 models 端点是否校验 API key：用不带任何
// 鉴权头的匿名请求重放同一端点，若也返回 2xx 且能解析出非空模型列表，则说明
// 该端点公开（如 opencode.ai 网关，匿名也能拉取完整列表）。此时“获取模型列表
// 成功”不能证明 key 有效，调用方应在 UI 上提示用户以聊天/补全请求验证 key。
// 探测失败（网络错误、非 2xx、解析不出模型）一律视为鉴权端点，返回 false。
// client 由调用方提供（应复用 provider 的代理/超时配置），nil 时用短超时的
// 裸默认 client。
func ModelsEndpointAllowsAnonymous(client *http.Client, endpoint, loginProtocol string) bool {
	httpReq, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return false
	}
	httpReq.Header.Set("Accept", "application/json")
	if client == nil {
		client = &http.Client{Timeout: 6 * time.Second}
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return false
	}
	models, err := parseProviderModelsResponse(body, loginProtocol)
	if err != nil || len(models) == 0 {
		return false
	}
	return true
}

func buildProviderModelsHeaders(provider config.Provider, loginProtocol string) map[string]string {
	runtimeProtocol := RuntimeProtocolForLoginProtocol(loginProtocol)
	provider.Protocol = runtimeProtocol
	llmAdapter := adapter.GetAdapterOrDefault(runtimeProtocol)
	return llmAdapter.BuildHeaders(adapter.AdapterConfig{
		Type:    runtimeProtocol,
		APIKey:  APIKey(provider),
		Headers: provider.Headers,
	})
}

// APIKey 返回 models 校验请求使用的 API key。
// 内联配置的 APIKey 字段优先：login 交互中输入的新 key 写入该字段，而
// api_key_ref 仍指向 auth store 中的旧凭据，若按常规优先读 ref 会用旧 key
// 校验新输入（httptest 之外的实测表现为 HTTP 401 Invalid access key）。
// 非 login 路径（运行时/doctor 等）APIKey 字段为空，行为与 GetAPIKey 一致。
func APIKey(provider config.Provider) string {
	if strings.TrimSpace(provider.APIKey) != "" {
		return strings.TrimSpace(provider.APIKey)
	}
	return provider.GetAPIKey()
}

func resolveProviderModelsPath(loginProtocol string, provider config.Provider, override string) string {
	if trimmed := strings.TrimSpace(override); trimmed != "" {
		return trimmed
	}
	if trimmed := strings.TrimSpace(provider.ModelsPath); trimmed != "" {
		return trimmed
	}
	return defaultModelsPath(loginProtocol, provider.BaseURL)
}

func defaultModelsPath(loginProtocol, baseURL string) string {
	switch NormalizeLoginProtocol(loginProtocol, "") {
	case LoginProtocolOpenAIImage:
		return "/v1/models"
	case "gemini":
		return "/v1beta/models"
	case "codex-apikey", "codex-oauth":
		lowerBase := strings.ToLower(strings.TrimSpace(baseURL))
		if strings.Contains(lowerBase, "chatgpt.com") {
			if strings.Contains(lowerBase, "/backend-api/codex") {
				return "/models"
			}
			return "/backend-api/codex/models"
		}
		return "/v1/models"
	default:
		return "/v1/models"
	}
}

func buildProviderModelsURL(provider config.Provider, modelsPath string) (string, error) {
	modelsPath = strings.TrimSpace(modelsPath)
	if modelsPath == "" {
		return "", fmt.Errorf("models path is required")
	}
	if parsed, err := url.Parse(modelsPath); err == nil && parsed.IsAbs() {
		return modelsPath, nil
	}
	baseURL := strings.TrimRight(strings.TrimSpace(provider.BaseURL), "/")
	if baseURL == "" {
		return "", fmt.Errorf("base url is required")
	}
	if !strings.HasPrefix(modelsPath, "/") {
		modelsPath = "/" + modelsPath
	}
	return config.JoinBaseURLAndPath(baseURL, modelsPath), nil
}

func parseProviderModelsResponse(raw []byte, loginProtocol string) ([]ModelInfo, error) {
	return parseProviderModelsResponseForCategory(raw, loginProtocol, DetectModelListCategory("", "", raw))
}

func parseProviderModelsResponseForCategory(raw []byte, loginProtocol string, category ModelListCategory) ([]ModelInfo, error) {
	var decoded interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	models := collectProviderModels(decoded, NormalizeLoginProtocol(loginProtocol, ""), category)
	return dedupeProviderModels(models), nil
}

func collectProviderModels(value interface{}, loginProtocol string, category ModelListCategory) []ModelInfo {
	entries := collectProviderModelEntries(value)
	if len(entries) == 0 {
		return nil
	}
	return collectProviderModelsFromList(entries, loginProtocol, category)
}

// collectProviderModelEntries 从任意 /models 载荷里取出模型条目列表
// （data / models / items，兼容 result / response 包裹）。形状探测与解析共用，
// 避免两处各写一遍列表定位逻辑。
func collectProviderModelEntries(value interface{}) []interface{} {
	switch typed := value.(type) {
	case []interface{}:
		return typed
	case map[string]interface{}:
		for _, key := range []string{"data", "models", "items"} {
			if list, ok := typed[key].([]interface{}); ok {
				return list
			}
		}
		for _, key := range []string{"result", "response"} {
			if nested, ok := typed[key]; ok {
				if entries := collectProviderModelEntries(nested); len(entries) > 0 {
					return entries
				}
			}
		}
	}
	return nil
}

func collectProviderModelsFromList(items []interface{}, loginProtocol string, category ModelListCategory) []ModelInfo {
	models := make([]ModelInfo, 0, len(items))
	for _, item := range items {
		switch typed := item.(type) {
		case string:
			if id := normalizeProviderModelID(typed, loginProtocol); id != "" {
				models = append(models, ModelInfo{ID: id, DisplayName: id})
			}
		case map[string]interface{}:
			if model := providerModelInfoFromMap(typed, loginProtocol, category); model.ID != "" {
				models = append(models, model)
			}
		}
	}
	return models
}

// providerModelInfoFromMap 按类别解析单条模型载荷；类别未注册或该条解析不出 ID
// 时回退到通用扁平解析。
func providerModelInfoFromMap(item map[string]interface{}, loginProtocol string, category ModelListCategory) ModelInfo {
	if spec, ok := modelListCategorySpecFor(category); ok && spec.parseEntry != nil {
		if model := spec.parseEntry(item, loginProtocol); model.ID != "" {
			return model
		}
	}
	return genericProviderModelInfoFromMap(item, loginProtocol)
}

// genericProviderModelInfoFromMap 解析扁平键形状（OpenAI 兼容网关 / Codex /
// vLLM 等），是历史实现，也是类别解析器的兜底。
func genericProviderModelInfoFromMap(item map[string]interface{}, loginProtocol string) ModelInfo {
	id := firstStringField(item, "id", "slug", "name", "model")
	id = normalizeProviderModelID(id, loginProtocol)
	if id == "" {
		return ModelInfo{}
	}
	displayName := firstStringField(item, "display_name", "displayName", "title", "name")
	if displayName == "" {
		displayName = id
	}
	return ModelInfo{
		ID:                  id,
		DisplayName:         displayName,
		InputModalities:     firstStringSliceField(item, "input_modalities", "inputModalities", "modalities"),
		ReasoningEfforts:    providerModelReasoningEfforts(item),
		MaxContextTokens:    firstIntField(item, "max_context_tokens", "maxContextTokens", "context_window", "contextWindow", "context_length"),
		SupportsTools:       stringSliceFieldContains(item, "tools", "supported_parameters", "supportedParameters"),
		SupportsRemoteCodex: firstBoolField(item, "supports_remote_codex", "supportsRemoteCodex"),
		Raw:                 item,
	}
}

func providerModelReasoningEfforts(item map[string]interface{}) []string {
	values := make([]string, 0, 8)
	values = append(values, allStringSliceFields(item, "reasoning_efforts", "reasoningEfforts", "supported_reasoning_efforts")...)
	values = append(values, allStringSliceFields(item, "thinking_efforts", "thinkingEfforts", "supported_thinking_efforts")...)
	return dedupeProviderStringOptions(values)
}

func normalizeProviderModelID(id, loginProtocol string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	if NormalizeLoginProtocol(loginProtocol, "") == "gemini" {
		id = strings.TrimPrefix(id, "models/")
	}
	return id
}

func dedupeProviderModels(models []ModelInfo) []ModelInfo {
	seen := make(map[string]struct{}, len(models))
	out := make([]ModelInfo, 0, len(models))
	for _, model := range models {
		model.ID = strings.TrimSpace(model.ID)
		if model.ID == "" {
			continue
		}
		key := strings.ToLower(model.ID)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, model)
	}
	return out
}

func ModelIDs(models []ModelInfo) []string {
	ids := make([]string, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		id := strings.TrimSpace(model.ID)
		if id == "" {
			continue
		}
		key := strings.ToLower(id)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

func firstStringField(item map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := item[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstStringSliceField(item map[string]interface{}, keys ...string) []string {
	for _, key := range keys {
		if values := stringSliceField(item, key); len(values) > 0 {
			return values
		}
	}
	return nil
}

func allStringSliceFields(item map[string]interface{}, keys ...string) []string {
	values := make([]string, 0)
	for _, key := range keys {
		values = append(values, stringSliceField(item, key)...)
	}
	return values
}

func stringSliceField(item map[string]interface{}, key string) []string {
	values, ok := item[key]
	if !ok {
		return nil
	}
	switch typed := values.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []interface{}:
		out := make([]string, 0, len(typed))
		for _, value := range typed {
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
				out = append(out, strings.TrimSpace(text))
			}
		}
		return out
	default:
		return nil
	}
}

// stringSliceFieldContains 判断字符串列表字段是否包含目标值（大小写不敏感，
// 命中任一 key 即返回 true）。用于 supported_parameters 之类的端点能力清单。
func stringSliceFieldContains(item map[string]interface{}, target string, keys ...string) bool {
	target = strings.ToLower(strings.TrimSpace(target))
	if target == "" {
		return false
	}
	for _, key := range keys {
		for _, value := range stringSliceField(item, key) {
			if strings.EqualFold(strings.TrimSpace(value), target) {
				return true
			}
		}
	}
	return false
}

func dedupeProviderStringOptions(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}

func firstIntField(item map[string]interface{}, keys ...string) int {
	for _, key := range keys {
		value, ok := item[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case int:
			return typed
		case float64:
			return int(typed)
		case json.Number:
			n, _ := typed.Int64()
			return int(n)
		}
	}
	return 0
}

func firstBoolField(item map[string]interface{}, keys ...string) bool {
	for _, key := range keys {
		if value, ok := item[key].(bool); ok {
			return value
		}
	}
	return false
}

func responsePreview(raw []byte, limit int) string {
	text := strings.TrimSpace(string(raw))
	if limit > 0 && len(text) > limit {
		return text[:limit] + "..."
	}
	return text
}
