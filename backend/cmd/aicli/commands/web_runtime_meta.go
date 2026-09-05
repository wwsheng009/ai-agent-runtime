package commands

import (
	"net/http"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// ChatWebAPIRuntimePath 是 provider/model/reasoning_effort 运行时元数据端点。
// 供 micro web client 的配置选择器拉取：当前生效值 + 可用 provider 及模型列表。
// 切换动作本身不走此端点——前端把 `/model ...` 命令注入 /web/api/input，
// 由主循环统一执行（与 TTY 行为一致）。
const ChatWebAPIRuntimePath = "/web/api/runtime"

// chatWebRuntimeMeta 是 GET /web/api/runtime 的响应体。
type chatWebRuntimeMeta struct {
	Current   chatWebRuntimeCurrent    `json:"current"`
	Providers []chatWebRuntimeProvider `json:"providers"`
}

// chatWebRuntimeCurrent 是当前会话生效的 provider/model/reasoning 配置。
// ReasoningOptions/ReasoningDefault 描述当前 model 的可选 effort 列表
// （来自 ModelCapabilities 或 openai 协议 fallback），供底部状态栏动态
// 渲染 reasoning 下拉；为空表示该模型未声明可用值，前端回退通用列表。
type chatWebRuntimeCurrent struct {
	Provider           string   `json:"provider"`
	Protocol           string   `json:"protocol"`
	Model              string   `json:"model"`
	ReasoningEffort    string   `json:"reasoning_effort"`
	ReasoningOptions   []string `json:"reasoning_options,omitempty"`
	ReasoningDefault   string   `json:"reasoning_default,omitempty"`
	ReasoningSupported bool     `json:"reasoning_supported"`
	BaseURL            string   `json:"base_url"`
}

// chatWebRuntimeModelDetail 是单个模型的 reasoning 能力视图，供前端在
// provider/model 本地切换时即时刷新 reasoning 下拉，无需等待后端生效。
type chatWebRuntimeModelDetail struct {
	Name                   string   `json:"name"`
	ReasoningModel         bool     `json:"reasoning_model,omitempty"`
	ReasoningEfforts       []string `json:"reasoning_efforts,omitempty"`
	DefaultReasoningEffort string   `json:"default_reasoning_effort,omitempty"`
}

// chatWebRuntimeProvider 是单个可用 provider 及其可选模型。
type chatWebRuntimeProvider struct {
	Name         string                      `json:"name"`
	Protocol     string                      `json:"protocol"`
	DefaultModel string                      `json:"default_model"`
	Models       []string                    `json:"models,omitempty"`
	ModelDetails []chatWebRuntimeModelDetail `json:"model_details,omitempty"`
}

// HandleChatWebAPIRuntime 返回当前会话的运行时 provider/model 元数据。
func HandleChatWebAPIRuntime(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeWebAPIJSON(w, http.StatusMethodNotAllowed, map[string]string{
			"status": "rejected",
			"reason": "method not allowed",
		})
		return
	}

	meta := chatWebRuntimeMeta{
		Current:   chatWebRuntimeCurrent{},
		Providers: []chatWebRuntimeProvider{},
	}

	session := chatWebSession()
	if session == nil {
		writeWebAPIJSON(w, http.StatusOK, meta)
		return
	}

	currentModel := strings.TrimSpace(effectiveRuntimeModel(session))
	// 当前 model 的 reasoning 可选项：与 /reasoning_effort status 同源
	// （reasoningEffortCatalogForModel），保证底部栏与 TTY 看到同一列表。
	currentOptions, currentDefault, _, currentSupported := chatWebReasoningForModel(
		strings.TrimSpace(session.ProviderName), session.Provider, currentModel)

	meta.Current = chatWebRuntimeCurrent{
		Provider:           strings.TrimSpace(session.ProviderName),
		Protocol:           strings.TrimSpace(session.Provider.GetProtocol()),
		Model:              currentModel,
		ReasoningEffort:    strings.TrimSpace(session.ReasoningEffort),
		ReasoningOptions:   currentOptions,
		ReasoningDefault:   currentDefault,
		ReasoningSupported: currentSupported,
		BaseURL:            strings.TrimSpace(session.BaseURL),
	}

	// 与 /model 命令一致：使用前重新读取配置文件，让 /login 等方式新增的
	// provider 立即进入选择器。读取失败时保留内存配置（不影响当前会话）。
	_ = reloadChatConfigForModelCommand(session)

	if session.Config != nil {
		for name, provider := range session.Config.Providers.Items {
			if !provider.Enabled {
				continue
			}
			models := collectRuntimeProviderModels(provider)
			entry := chatWebRuntimeProvider{
				Name:         name,
				Protocol:     strings.TrimSpace(provider.Protocol),
				DefaultModel: strings.TrimSpace(provider.DefaultModel),
				Models:       models,
				ModelDetails: collectRuntimeModelDetails(name, provider, models),
			}
			meta.Providers = append(meta.Providers, entry)
		}
		sort.Slice(meta.Providers, func(i, j int) bool {
			return meta.Providers[i].Name < meta.Providers[j].Name
		})
	}

	writeWebAPIJSON(w, http.StatusOK, meta)
}

// chatWebReasoningForModel 返回指定 provider/model 的 reasoning 可选项。
// 与 chat_reasoning.go 的 catalog 逻辑同源：优先显式 ModelCapabilities，
// 其次 openai 协议 fallback。options 已按 minimal/low/medium/high/max/xhigh
// 排序；supported 表示是否声明了可用值。
func chatWebReasoningForModel(providerName string, provider agentconfig.Provider, model string) (options []string, defaultEffort string, reasoningModel bool, supported bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, "", false, false
	}
	if capSpec, ok := reasoningEffortCapabilityForModel(provider, model); ok {
		opts := normalizeReasoningEffortOptions(capSpec.ReasoningEfforts)
		if len(opts) > 0 {
			return append([]string(nil), opts...),
				strings.TrimSpace(capSpec.DefaultReasoningEffort),
				capSpec.ReasoningModel, true
		}
		// 声明了 capability 但无 efforts：仍回传 reasoning_model/default，
		// 让前端知道该模型支持 reasoning 但用通用列表。
		if strings.TrimSpace(capSpec.DefaultReasoningEffort) != "" || capSpec.ReasoningModel {
			return nil, strings.TrimSpace(capSpec.DefaultReasoningEffort), capSpec.ReasoningModel, true
		}
	}
	if capSpec, ok := fallbackReasoningEffortCapabilityForProvider(providerName, provider, model); ok {
		opts := normalizeReasoningEffortOptions(capSpec.ReasoningEfforts)
		if len(opts) > 0 {
			return append([]string(nil), opts...),
				strings.TrimSpace(capSpec.DefaultReasoningEffort),
				capSpec.ReasoningModel, true
		}
	}
	return nil, "", false, false
}

// collectRuntimeModelDetails 为 models 列表中每个模型生成 reasoning 能力视图。
func collectRuntimeModelDetails(providerName string, provider agentconfig.Provider, models []string) []chatWebRuntimeModelDetail {
	if len(models) == 0 {
		return nil
	}
	details := make([]chatWebRuntimeModelDetail, 0, len(models))
	for _, m := range models {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		options, defaultEffort, reasoningModel, _ := chatWebReasoningForModel(providerName, provider, m)
		details = append(details, chatWebRuntimeModelDetail{
			Name:                   m,
			ReasoningModel:         reasoningModel,
			ReasoningEfforts:       options,
			DefaultReasoningEffort: defaultEffort,
		})
	}
	return details
}

// collectRuntimeProviderModels 返回 provider 的可选模型（default +
// supported + ModelCapabilities 显式声明的模型，去重并按名称排序）。
// ModelCapabilities 常含 supported 之外的模型（如 auto-import 写入的
// capability 但未同步到 supported），通配键（*, mimo-*）除外。
// 为空时前端允许自由输入模型名。
func collectRuntimeProviderModels(provider agentconfig.Provider) []string {
	seen := make(map[string]struct{})
	models := make([]string, 0, len(provider.SupportedModels)+1)
	add := func(m string) {
		m = strings.TrimSpace(m)
		if m == "" {
			return
		}
		if _, dup := seen[m]; dup {
			return
		}
		seen[m] = struct{}{}
		models = append(models, m)
	}
	add(provider.DefaultModel)
	for _, m := range provider.SupportedModels {
		add(m)
	}
	for m := range provider.ModelCapabilities {
		m = strings.TrimSpace(m)
		if m == "" || strings.Contains(m, "*") {
			continue
		}
		add(m)
	}
	sort.Strings(models)
	return models
}
