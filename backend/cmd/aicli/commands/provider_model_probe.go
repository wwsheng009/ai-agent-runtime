package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/buildinfo"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
	"github.com/wwsheng009/ai-agent-runtime/internal/modelcard"
	httpclient "github.com/wwsheng009/ai-agent-runtime/internal/pkg/httpclient"
	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// 模型协议探测：对 assumed（无 model card 数据、仅靠协议 fallback 假定）的
// 模型，用各协议的最小补全请求实测网关是否支持，把“无证据假定”变成
// “有证据结论”。判定采用错误分类学：
//   - HTTP 200 + 可识别的补全结构            → supported（强正向证据）
//   - 网关明确拒绝该模型/格式（401/404/400 +  → unsupported（强负向证据）
//     "not supported for format" 等措辞）
//   - 鉴权 / 配额 / 区域 / 限流 / 参数错误 /  → unknown（不能下负向结论，
//     网络错误 / 无法识别的 200 响应             避免把区域限制误判成不支持）
// ---------------------------------------------------------------------------

// providerModelProbeVerdict 是单个模型 × 协议探测的结论。
type providerModelProbeVerdict string

const (
	probeVerdictSupported   providerModelProbeVerdict = "supported"   // 实测支持
	probeVerdictUnsupported providerModelProbeVerdict = "unsupported" // 网关明确拒绝
	probeVerdictUnknown     providerModelProbeVerdict = "unknown"     // 无法定性（鉴权/限流/区域等）
)

// providerModelSingleProbe 是一次模型 × 协议探测的结果。
type providerModelSingleProbe struct {
	Protocol   string `json:"protocol"`
	Verdict    string `json:"verdict"`
	HTTPStatus int    `json:"http_status,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

// providerModelProbeResult 是单个模型的跨协议探测结果。
type providerModelProbeResult struct {
	Model  string                     `json:"model"`
	Probes []providerModelSingleProbe `json:"probes"`
}

// 措辞分类表（全部小写匹配）。顺序即优先级：参数错误 > 鉴权/配额/区域 >
// 明确不支持；前两类命中时即使带“not supported”字样也保守判 unknown，
// 避免 “max_tokens is not supported for this model” 这类参数错误被误判
// 成“模型不支持该协议”。
var providerProbeParamErrorSignals = []string{
	"max_tokens", "max_output_tokens", "temperature", "top_p", "top-k",
	"parameter", "unknown field", "extra_forbidden", "invalid request",
	"invalid_request_error", "unexpected", "missing required", "required field",
}

var providerProbeInconclusiveSignals = []string{
	"invalid api key", "invalid_api_key", "unauthorized", "authentication",
	"permission", "forbidden", "quota", "billing", "insufficient",
	"region", "not available in your", "resource_exhausted", "rate limit",
	"too many requests", "temporarily", "overloaded", "capacity",
	"service unavailable", "maintenance",
}

var providerProbeUnsupportedSignals = []string{
	"not supported", "unsupported", "does not support", "doesn't support",
	"isn't supported", "no such model", "unknown model", "model not found",
	"model_not_found", "invalid model", "does not exist", "not a valid model",
	"not available for", "wrong model", "no endpoints found",
}

// classifyProviderModelProbeMessage 按错误措辞给结论。无法识别的措辞一律
// unknown——负向结论只认明确拒绝该模型/格式的网关措辞。
func classifyProviderModelProbeMessage(message string) providerModelProbeVerdict {
	lower := strings.ToLower(strings.TrimSpace(message))
	if lower == "" {
		return probeVerdictUnknown
	}
	for _, signal := range providerProbeParamErrorSignals {
		if strings.Contains(lower, signal) {
			return probeVerdictUnknown
		}
	}
	for _, signal := range providerProbeInconclusiveSignals {
		if strings.Contains(lower, signal) {
			return probeVerdictUnknown
		}
	}
	for _, signal := range providerProbeUnsupportedSignals {
		if strings.Contains(lower, signal) {
			return probeVerdictUnsupported
		}
	}
	return probeVerdictUnknown
}

// extractProviderProbeErrorMessage 从常见网关错误结构里提取错误消息：
// OpenAI {"error":{"message":...}}、Anthropic {"type":"error","error":{...}}、
// Gemini {"error":{"message":...}}、FastAPI {"detail":...}、以及部分网关的
// 顶层 {"message":...} / {"error":"..."}。
func extractProviderProbeErrorMessage(body []byte) (string, bool) {
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", false
	}
	if raw, ok := payload["error"]; ok {
		switch typed := raw.(type) {
		case string:
			if strings.TrimSpace(typed) != "" {
				return strings.TrimSpace(typed), true
			}
		case map[string]interface{}:
			for _, key := range []string{"message", "msg", "description"} {
				if text, ok := typed[key].(string); ok && strings.TrimSpace(text) != "" {
					return strings.TrimSpace(text), true
				}
			}
		}
	}
	for _, key := range []string{"message", "msg", "detail"} {
		if text, ok := payload[key].(string); ok && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text), true
		}
	}
	return "", false
}

// probeResponseLooksSuccessful 判断 2xx 响应体是否为可识别的补全成功结构
// （各协议 adapter 的 ExtractResponse 目标形状）。
func probeResponseLooksSuccessful(protocol string, payload map[string]interface{}) bool {
	switch protocol {
	case "openai", "openai_image":
		choices, ok := payload["choices"].([]interface{})
		return ok && len(choices) > 0
	case "anthropic":
		if _, ok := payload["content"].([]interface{}); ok {
			return true
		}
		id, _ := payload["id"].(string)
		msgType, _ := payload["type"].(string)
		return strings.TrimSpace(id) != "" && strings.EqualFold(msgType, "message")
	case "codex":
		if _, ok := payload["output"].([]interface{}); ok {
			return true
		}
		id, _ := payload["id"].(string)
		return strings.TrimSpace(id) != ""
	case "gemini":
		_, ok := payload["candidates"].([]interface{})
		return ok
	}
	return false
}

// classifyProviderModelProbeResponse 按 HTTP 状态码 + 响应体给探测结论。
// detail 是给人看的判定依据（错误消息或响应体预览）。
func classifyProviderModelProbeResponse(statusCode int, body []byte, protocol string) (providerModelProbeVerdict, string) {
	message, hasMessage := extractProviderProbeErrorMessage(body)
	detail := message
	if detail == "" {
		detail = responsePreview(body, 300)
	}
	switch {
	case statusCode >= 200 && statusCode < 300:
		// 200 + 内嵌 error：new-api 类网关先收单、上游失败后再在 200 里
		// 返回错误。只有明确拒绝该模型/格式的措辞才判 unsupported。
		if hasMessage && strings.TrimSpace(message) != "" {
			if classifyProviderModelProbeMessage(message) == probeVerdictUnsupported {
				return probeVerdictUnsupported, detail
			}
			return probeVerdictUnknown, detail
		}
		var payload map[string]interface{}
		if err := json.Unmarshal(body, &payload); err != nil {
			return probeVerdictUnknown, "HTTP 200 但响应不是 JSON: " + detail
		}
		if probeResponseLooksSuccessful(protocol, payload) {
			return probeVerdictSupported, detail
		}
		return probeVerdictUnknown, "HTTP 200 但响应结构无法识别: " + detail
	case statusCode == http.StatusNotFound:
		// 404 只有在错误消息明确指向模型时才是负向证据；纯路由 404
		// （HTML / 空体）说明路径不对，不能给所有模型下“不支持”结论。
		if hasMessage && classifyProviderModelProbeMessage(message) == probeVerdictUnsupported {
			return probeVerdictUnsupported, detail
		}
		return probeVerdictUnknown, detail
	case statusCode == http.StatusBadRequest, statusCode == http.StatusConflict, statusCode == http.StatusUnprocessableEntity:
		if hasMessage && classifyProviderModelProbeMessage(message) == probeVerdictUnsupported {
			return probeVerdictUnsupported, detail
		}
		return probeVerdictUnknown, detail
	case statusCode == http.StatusUnauthorized, statusCode == http.StatusForbidden:
		// 401/403 默认是鉴权/权限问题（unknown）；但 opencode.ai 这类网关
		// 会用 401 携带 “Model X is not supported for format openai”——
		// 这是明确的格式拒绝，必须判 unsupported。
		if hasMessage && classifyProviderModelProbeMessage(message) == probeVerdictUnsupported {
			return probeVerdictUnsupported, detail
		}
		return probeVerdictUnknown, detail
	case statusCode == http.StatusTooManyRequests:
		return probeVerdictUnknown, detail
	case statusCode >= 500:
		return probeVerdictUnknown, detail
	}
	return probeVerdictUnknown, detail
}

// ---------------------------------------------------------------------------
// 探测请求构造：复用各协议 adapter 的 BuildRequest / BuildHeaders / GetAPIPath，
// 保证探测请求与运行时真实请求同源（同样的消息格式、鉴权头、API 路径）。
// ---------------------------------------------------------------------------

// providerProbeMaxTokensPerProtocol 是各协议探测请求的最小输出预算。
// codex（/v1/responses）走推理模型时需要 reasoning 预算，max_output_tokens=1
// 会因预算耗尽产生误导性错误，用 16；其余协议 1 token 足够。
func providerProbeMaxTokensPerProtocol(runtimeProtocol string) int {
	if strings.EqualFold(runtimeProtocol, "codex") {
		return 16
	}
	return 1
}

// buildProviderModelProbeCall 构造一次模型 × 协议探测请求的 endpoint / body /
// headers。provider 自身协议沿用其自定义 APIPath（与运行时一致）；探测其他
// 协议时使用该协议 adapter 的默认路径（codex→/v1/responses、
// anthropic→/v1/messages、openai→/v1/chat/completions、gemini→
// /v1beta/models/{model}:generateContent）。
func buildProviderModelProbeCall(provider config.Provider, probeProtocol, modelID string) (endpoint string, payload map[string]interface{}, headers map[string]string, err error) {
	runtimeProtocol := runtimeProtocolForLoginProtocol(probeProtocol)
	switch strings.ToLower(strings.TrimSpace(runtimeProtocol)) {
	case "openai", "anthropic", "gemini", "codex":
	default:
		// GetAdapterOrDefault 对未知协议兜底返回 OpenAIAdapter；这里显式
		// 拒绝，避免未知协议被静默按 openai 探测得出误导性结论。
		return "", nil, nil, fmt.Errorf("unsupported probe protocol: %s", probeProtocol)
	}
	llmAdapter := adapter.GetAdapterOrDefault(runtimeProtocol)
	payload = llmAdapter.BuildRequest(adapter.RequestConfig{
		Model:       modelID,
		Messages:    []map[string]interface{}{{"role": "user", "content": "hi"}},
		Stream:      false,
		MaxTokens:   providerProbeMaxTokensPerProtocol(runtimeProtocol),
		Temperature: 0,
	})
	apiPath := llmAdapter.GetAPIPath()
	if strings.EqualFold(runtimeProtocol, "gemini") {
		modelPart := strings.TrimPrefix(strings.TrimSpace(modelID), "models/")
		apiPath = "/v1beta/models/" + modelPart + ":generateContent"
	} else if strings.EqualFold(strings.TrimSpace(provider.Protocol), runtimeProtocol) && strings.TrimSpace(provider.APIPath) != "" {
		apiPath = strings.TrimSpace(provider.APIPath)
	}
	baseURL := strings.TrimSpace(provider.BaseURL)
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}
	endpoint = config.JoinBaseURLAndPath(baseURL, apiPath)
	headers = llmAdapter.BuildHeaders(adapter.AdapterConfig{
		Type:    runtimeProtocol,
		APIKey:  providerModelsAPIKey(provider),
		Headers: providerProbeHeaders(provider, modelID),
	})
	return endpoint, payload, headers, nil
}

// providerProbeHeaders 解析 provider 头里的 {session_id} 等模板占位符。
// 生产聊天路径在 effectiveChatProviderHeaders 统一展开模板；探测请求没有
// 真实会话，用合成会话 ID（网关仅用于路由/亲和，不做鉴权），client /
// project 上下文与聊天路径同源，{model} 解析为被探测模型。不展开的话，
// opencode.ai 这类强制会话头的网关会收到字面 "{session_id}"，所有探测
// 都被 400 拒绝，探测对这类 provider 完全失效。
func providerProbeHeaders(provider config.Provider, modelID string) map[string]string {
	ctx := config.HeaderTemplateContext{
		SessionID: providerProbeSessionID(),
		Model:     modelID,
		Client:    buildinfo.Originator(),
		ProjectID: headerTemplateProjectID(),
	}
	return config.ResolveHeaderTemplates(provider.Headers, ctx)
}

// providerProbeSessionID 生成探测请求专用的合成会话 ID：每次探测运行
// 唯一，让网关把整轮探测视为一个独立会话，不污染真实会话的亲和路由。
func providerProbeSessionID() string {
	return fmt.Sprintf("aicli-probe-%d", time.Now().UnixNano())
}

// probeProviderModelOnce 发送一次探测请求并给出结论。网络错误 / 超时一律
// unknown——传输层失败不能证明模型不支持该协议。
func probeProviderModelOnce(client *http.Client, provider config.Provider, probeProtocol, modelID string, timeout time.Duration) providerModelSingleProbe {
	probe := providerModelSingleProbe{Protocol: runtimeProtocolForLoginProtocol(probeProtocol)}
	endpoint, payload, headers, err := buildProviderModelProbeCall(provider, probe.Protocol, modelID)
	if err != nil {
		probe.Verdict = string(probeVerdictUnknown)
		probe.Detail = err.Error()
		return probe
	}
	rawBody, err := json.Marshal(payload)
	if err != nil {
		probe.Verdict = string(probeVerdictUnknown)
		probe.Detail = "marshal probe request: " + err.Error()
		return probe
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(rawBody))
	if err != nil {
		probe.Verdict = string(probeVerdictUnknown)
		probe.Detail = "create probe request: " + err.Error()
		return probe
	}
	for key, value := range headers {
		if strings.TrimSpace(value) == "" {
			continue
		}
		httpReq.Header.Set(key, value)
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		probe.Verdict = string(probeVerdictUnknown)
		probe.Detail = "请求失败（无法定性）: " + err.Error()
		return probe
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		probe.Verdict = string(probeVerdictUnknown)
		probe.Detail = "读取探测响应失败: " + err.Error()
		return probe
	}
	probe.HTTPStatus = resp.StatusCode
	verdict, detail := classifyProviderModelProbeResponse(resp.StatusCode, respBody, probe.Protocol)
	probe.Verdict = string(verdict)
	probe.Detail = detail
	return probe
}

// providerModelProbeRequest 是一次批量探测的输入。
type providerModelProbeRequest struct {
	Config        *config.Config
	Provider      config.Provider
	Models        []string
	Protocols     []string
	Timeout       time.Duration
	MaxConcurrent int
}

// runProviderModelProbes 并发执行模型 × 协议探测矩阵。结果按输入顺序排列
// （按索引写回，无需锁）；默认并发 6、单请求超时 15s，防止探测拖垮上游。
func runProviderModelProbes(req providerModelProbeRequest) []providerModelProbeResult {
	provider := req.Provider
	if req.Config != nil {
		provider.Headers = config.EffectiveProviderHeaders(req.Config.Providers.Headers, provider.Headers)
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	workers := req.MaxConcurrent
	if workers <= 0 {
		workers = 6
	}
	if workers > 12 {
		workers = 12
	}
	client := http.DefaultClient
	if req.Config != nil {
		client = httpclient.GetHTTPClientWithProvider(req.Config, &provider)
	}
	cloned := *client
	cloned.Timeout = 0 // 超时由每请求 context 控制，不占用全局 client 超时
	client = &cloned

	models := make([]string, 0, len(req.Models))
	for _, model := range req.Models {
		model = strings.TrimSpace(model)
		if model != "" {
			models = append(models, model)
		}
	}
	protocols := make([]string, 0, len(req.Protocols))
	for _, protocol := range req.Protocols {
		protocol = runtimeProtocolForLoginProtocol(protocol)
		if protocol == "" {
			continue
		}
		duplicated := false
		for _, existing := range protocols {
			if strings.EqualFold(existing, protocol) {
				duplicated = true
				break
			}
		}
		if !duplicated {
			protocols = append(protocols, protocol)
		}
	}

	results := make([]providerModelProbeResult, len(models))
	type probeTask struct {
		modelIndex int
		probeIndex int
		model      string
		protocol   string
	}
	tasks := make([]probeTask, 0, len(models)*len(protocols))
	for i, model := range models {
		results[i] = providerModelProbeResult{
			Model:  model,
			Probes: make([]providerModelSingleProbe, len(protocols)),
		}
		for j, protocol := range protocols {
			results[i].Probes[j].Protocol = protocol
			tasks = append(tasks, probeTask{modelIndex: i, probeIndex: j, model: model, protocol: protocol})
		}
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for _, task := range tasks {
		wg.Add(1)
		go func(task probeTask) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[task.modelIndex].Probes[task.probeIndex] = probeProviderModelOnce(client, provider, task.protocol, task.model, timeout)
		}(task)
	}
	wg.Wait()
	return results
}

// ---------------------------------------------------------------------------
// 探测结果写回：把 supported 结论沉淀为用户级 model_cards.yaml 里的探测
// 卡片，让后续 fetch-models / login 的分类链路把它们从 assumed 升级成
// verified（providerFetchModelHasCardBacking 认非 fallback 卡命中）。
// 卡片用 base_url_contains 限定网关范围，避免污染其他 provider。
// ---------------------------------------------------------------------------

// providerProbeCardPriority 是探测卡片的优先级：高于内置卡（多数为 10-120
// 之外的默认场景），低于用户手工卡（通常更高）；同名冲突以手工卡为准。
const providerProbeCardPriority = 50

// providerProbeBaseURLHost 提取 base_url 的 host（小写），用于把探测卡片
// 限定在该网关范围内。
func providerProbeBaseURLHost(baseURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(parsed.Host))
}

// providerProbeSlug 把任意字符串转成卡片 ID 片段（小写字母数字 + 连字符）。
func providerProbeSlug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			builder.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && builder.Len() > 0 {
				builder.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(builder.String(), "-")
}

// providerProbeCardID 生成探测卡片 ID：probe.<provider|host>.<model>.<protocol>。
func providerProbeCardID(providerName, host, modelID, protocol string) string {
	scope := providerProbeSlug(providerName)
	if scope == "" {
		scope = providerProbeSlug(host)
	}
	if scope == "" {
		scope = "unknown"
	}
	return "probe." + scope + "." + providerProbeSlug(modelID) + "." + providerProbeSlug(protocol)
}

// providerProbeCardExistsFor 判断用户卡片目录里是否已有覆盖该模型 × template
// 的非 fallback 卡片（精确 model_ids 命中即可，探测卡只处理精确场景）。
func providerProbeCardExistsFor(catalog *modelcard.Catalog, modelID, templateID string) bool {
	if catalog == nil {
		return false
	}
	for _, card := range catalog.Cards {
		if card.Fallback {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(card.ProviderTemplate), strings.TrimSpace(templateID)) {
			continue
		}
		for _, id := range card.Match.ModelIDs {
			if strings.EqualFold(strings.TrimSpace(id), strings.TrimSpace(modelID)) {
				return true
			}
		}
	}
	return false
}

// appendProviderModelProbeCards 把探测结论写回用户级 model_cards.yaml：
//   - 只写 supported 结论（unknown/unsupported 不落卡——负向与不定性结论
//     都可能是暂时性的，如区域/配额）；
//   - 已有同 ID 卡片或同 template × model 的非 fallback 卡片时跳过（幂等，
//     手工卡优先）；
//   - 已有文件先备份为 <path>.bak 再整体重写（yaml 重写会丢注释，备份兜底）；
//   - 现有文件解析失败时拒绝写入并保留原文件，绝不覆盖用户手工内容。
func appendProviderModelProbeCards(
	cfg *config.Config,
	providerName string,
	provider config.Provider,
	results []providerModelProbeResult,
) (written []string, cardsPath string, err error) {
	userPath := "~/.aicli/model_cards.yaml"
	if cfg != nil && cfg.AICLI != nil && cfg.AICLI.ModelCards != nil && strings.TrimSpace(cfg.AICLI.ModelCards.UserPath) != "" {
		userPath = cfg.AICLI.ModelCards.UserPath
	}
	resolved := resolveProviderLoginModelCardPath(userPath)
	catalog := &modelcard.Catalog{Version: 1}
	existingData := ([]byte)(nil)
	fileExisted := false
	if data, readErr := os.ReadFile(resolved); readErr == nil && len(strings.TrimSpace(string(data))) > 0 {
		fileExisted = true
		existingData = data
		parsed := &modelcard.Catalog{}
		if unmarshalErr := yaml.Unmarshal(data, parsed); unmarshalErr != nil {
			return nil, resolved, fmt.Errorf("现有用户卡片文件 %s 无法解析（已保留原文件未改动）: %w", resolved, unmarshalErr)
		}
		catalog = parsed
		if catalog.Version == 0 {
			catalog.Version = 1
		}
	}

	// 模板 ID 用内置目录解析（codex→codex.responses 等），不依赖用户文件。
	builtin, _, loadErr := modelcard.LoadSources([]modelcard.Source{modelcard.BuiltinSource()}, false)
	if loadErr != nil || builtin == nil {
		return nil, resolved, fmt.Errorf("加载内置卡片目录失败: %w", loadErr)
	}
	host := providerProbeBaseURLHost(provider.BaseURL)
	existingIDs := make(map[string]struct{}, len(catalog.Cards))
	for _, card := range catalog.Cards {
		existingIDs[strings.ToLower(strings.TrimSpace(card.ID))] = struct{}{}
	}
	for _, result := range results {
		for _, probe := range result.Probes {
			if probe.Verdict != string(probeVerdictSupported) {
				continue
			}
			template, ok := builtin.ProviderTemplateForProtocol(probe.Protocol)
			if !ok || strings.TrimSpace(template.ID) == "" {
				continue
			}
			cardID := providerProbeCardID(providerName, host, result.Model, probe.Protocol)
			if _, exists := existingIDs[strings.ToLower(cardID)]; exists {
				continue
			}
			if providerProbeCardExistsFor(catalog, result.Model, template.ID) {
				continue
			}
			card := modelcard.Card{
				ID:               cardID,
				Title:            fmt.Sprintf("Probe %s %s", result.Model, probe.Protocol),
				Priority:         providerProbeCardPriority,
				ProviderTemplate: template.ID,
				Match: modelcard.MatchSpec{
					ModelIDs:        []string{result.Model},
					BaseURLContains: hostWithFallback(host),
				},
				Capability: config.ModelCapabilitySpec{InputModalities: []string{"text"}},
			}
			catalog.Cards = append(catalog.Cards, card)
			existingIDs[strings.ToLower(cardID)] = struct{}{}
			written = append(written, cardID)
		}
	}
	if len(written) == 0 {
		return nil, resolved, nil
	}
	if fileExisted && len(existingData) > 0 {
		// 注释等非结构内容在重写时会丢失，备份兜底（尽力而为）。
		_ = os.WriteFile(resolved+".bak", existingData, 0o600)
	}
	encoded, marshalErr := yaml.Marshal(catalog)
	if marshalErr != nil {
		return written, resolved, fmt.Errorf("序列化探测卡片失败: %w", marshalErr)
	}
	if writeErr := os.WriteFile(resolved, encoded, 0o600); writeErr != nil {
		return written, resolved, fmt.Errorf("写入用户卡片文件 %s 失败: %w", resolved, writeErr)
	}
	return written, resolved, nil
}

func hostWithFallback(host string) []string {
	if strings.TrimSpace(host) == "" {
		return nil
	}
	return []string{strings.TrimSpace(host)}
}
