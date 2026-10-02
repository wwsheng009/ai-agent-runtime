package providerops

import (
	"strings"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/providercompat"
	"github.com/wwsheng009/ai-agent-runtime/internal/modelcard"
)

// ---------------------------------------------------------------------------
// 模型能力匹配：/models 端点元数据 → model card → 协议兼容默认值的优先级
// 合并链。login 与 fetch-models（runtime 编辑器）共用同一实现。
// ---------------------------------------------------------------------------

// ChatWebConfigModel 是单个模型的配置视图：模型名 + 其 model_capabilities
// 条目的**完整字段投影**（前端「模型编辑器」面板可直接回显并整体写回）。
//
// 字段与 config.ModelCapabilitySpec 一一对应：视图是 spec 的单向投影，写回
// 走 POST /web/api/config/providers 的 model_capabilities 字段，不再是只读
// 子集。新增 capability 字段时必须同步补充这里的投影与写入结构，否则 Web
// 端编辑该字段会被静默丢弃。
type ChatWebConfigModel struct {
	Name                   string                        `json:"name"`
	ReasoningModel         bool                          `json:"reasoning_model"`
	ReasoningEfforts       []string                      `json:"reasoning_efforts,omitempty"`
	ReasoningEffortBudgets map[string]int                `json:"reasoning_effort_budgets,omitempty"`
	DefaultReasoningEffort string                        `json:"default_reasoning_effort,omitempty"`
	CompactReasoningEffort string                        `json:"compact_reasoning_effort,omitempty"`
	MaxContextTokens       int                           `json:"max_context_tokens,omitempty"`
	MaxTokens              int                           `json:"max_tokens,omitempty"`
	AutoCompactRatio       float64                       `json:"auto_compact_ratio,omitempty"`
	AutoCompactTokenLimit  int                           `json:"auto_compact_token_limit,omitempty"`
	AutoCompactMode        string                        `json:"auto_compact_mode,omitempty"`
	SupportsRemoteCompact  bool                          `json:"supports_remote_compact,omitempty"`
	ReplayReasoningContent *bool                         `json:"replay_reasoning_content,omitempty"`
	InputModalities        []string                      `json:"input_modalities,omitempty"`
	NativeTools            config.NativeToolCapabilities `json:"native_tools"`
}

// ModelCapabilityView 把 model_capabilities 条目投影为前端字段视图。GET
// /web/api/config 快照与 fetch-models 的 model_metadata 必须是同一次投影的
// 两个入口，否则「模型编辑器」回显的字段集合会与写回集合不一致。
func ModelCapabilityView(name string, spec config.ModelCapabilitySpec) ChatWebConfigModel {
	view := ChatWebConfigModel{
		Name:                   strings.TrimSpace(name),
		ReasoningModel:         spec.ReasoningModel,
		ReasoningEfforts:       append([]string(nil), spec.ReasoningEfforts...),
		DefaultReasoningEffort: strings.TrimSpace(spec.DefaultReasoningEffort),
		CompactReasoningEffort: strings.TrimSpace(spec.CompactReasoningEffort),
		MaxContextTokens:       spec.MaxContextTokens,
		MaxTokens:              spec.MaxTokens,
		AutoCompactRatio:       spec.AutoCompactRatio,
		AutoCompactTokenLimit:  spec.AutoCompactTokenLimit,
		AutoCompactMode:        strings.TrimSpace(spec.AutoCompactMode),
		SupportsRemoteCompact:  spec.SupportsRemoteCompact,
		ReplayReasoningContent: spec.ReplayReasoningContent,
		InputModalities:        append([]string(nil), spec.InputModalities...),
		NativeTools:            spec.NativeTools,
	}
	if len(spec.ReasoningEffortBudgets) > 0 {
		budgets := make(map[string]int, len(spec.ReasoningEffortBudgets))
		for effort, budget := range spec.ReasoningEffortBudgets {
			if effort = strings.TrimSpace(effort); effort == "" || budget <= 0 {
				continue
			}
			budgets[effort] = budget
		}
		if len(budgets) > 0 {
			view.ReasoningEffortBudgets = budgets
		}
	}
	return view
}

// chatWebConfigModel 是包内既有点名的内部别名（fetch-models 元数据投影）。
type chatWebConfigModel = ChatWebConfigModel

// ModelCapabilityIsEmpty 判断 spec 是否不携带任何有效配置。
//
// 必须覆盖 ModelCapabilitySpec 的每个字段：漏判会让「只声明了该字段」的
// spec 在 fetch-models 匹配与 Web 端整体写回后被当作空条目丢弃
// （replay_reasoning_content 曾长期漏判，导致 Web 端无法保存该契约）。
func ModelCapabilityIsEmpty(spec config.ModelCapabilitySpec) bool {
	return len(spec.InputModalities) == 0 &&
		!spec.NativeTools.ImageGeneration &&
		!spec.NativeTools.ImagesGenerationsAPI &&
		!spec.ReasoningModel &&
		len(spec.ReasoningEfforts) == 0 &&
		len(spec.ReasoningEffortBudgets) == 0 &&
		strings.TrimSpace(spec.DefaultReasoningEffort) == "" &&
		spec.MaxContextTokens == 0 &&
		spec.MaxTokens == 0 &&
		spec.AutoCompactRatio == 0 &&
		spec.AutoCompactTokenLimit == 0 &&
		strings.TrimSpace(spec.AutoCompactMode) == "" &&
		!spec.SupportsRemoteCompact &&
		spec.ReplayReasoningContent == nil &&
		strings.TrimSpace(spec.CompactReasoningEffort) == ""
}

// providerLoginModelCapabilityIsEmpty 是包内既有点名的兼容包装。
func providerLoginModelCapabilityIsEmpty(spec config.ModelCapabilitySpec) bool {
	return ModelCapabilityIsEmpty(spec)
}

func providerLoginModelCapabilitySpec(model ModelInfo) config.ModelCapabilitySpec {
	spec := config.ModelCapabilitySpec{}
	if len(model.InputModalities) > 0 {
		spec.InputModalities = dedupeProviderStringOptions(model.InputModalities)
	}
	if len(model.ReasoningEfforts) > 0 {
		spec.ReasoningEfforts = dedupeProviderStringOptions(model.ReasoningEfforts)
		spec.ReasoningModel = true
	}
	// 端点显式声明的 reasoning 标记：即使没有档位列表（例如只声明
	// default_enabled / mandatory），也要按推理模型处理。
	if model.ReasoningModel {
		spec.ReasoningModel = true
	}
	if effort := strings.TrimSpace(model.DefaultReasoningEffort); effort != "" {
		spec.DefaultReasoningEffort = effort
	}
	if model.MaxContextTokens > 0 {
		spec.MaxContextTokens = model.MaxContextTokens
	}
	if model.MaxTokens > 0 {
		spec.MaxTokens = model.MaxTokens
	}
	if model.SupportsRemoteCodex {
		spec.SupportsRemoteCompact = true
	}
	return spec
}

func defaultProviderLoginReasoningEfforts(providerName, loginProtocol string, provider config.Provider, models []ModelInfo) []string {
	return providercompat.DefaultLoginReasoningEfforts(providercompat.Context{
		ProviderName: providerName,
		Protocol:     RuntimeProtocolForLoginProtocol(loginProtocol),
		BaseURL:      provider.BaseURL,
		Model:        providerLoginCompatModelHint(providerName, loginProtocol, provider, models),
	})
}

func providerLoginModelUsesDefaultReasoningEfforts(modelID, providerName, loginProtocol string, provider config.Provider) bool {
	return providercompat.LoginModelUsesDefaultReasoningEfforts(providercompat.Context{
		ProviderName: providerName,
		Protocol:     RuntimeProtocolForLoginProtocol(loginProtocol),
		BaseURL:      provider.BaseURL,
		Model:        modelID,
	}, modelID)
}

func providerLoginUsesWildcardReasoningEfforts(loginProtocol string, provider config.Provider) bool {
	return providercompat.LoginUsesWildcardReasoningEfforts(providercompat.Context{
		Protocol: RuntimeProtocolForLoginProtocol(loginProtocol),
		BaseURL:  provider.BaseURL,
	})
}

func providerLoginCompatModelHint(providerName, loginProtocol string, provider config.Provider, models []ModelInfo) string {
	modelIDs := make([]string, 0, len(models))
	for _, model := range models {
		if modelID := strings.TrimSpace(model.ID); modelID != "" {
			modelIDs = append(modelIDs, modelID)
		}
	}
	return providercompat.LoginModelHint(providercompat.Context{
		ProviderName: providerName,
		Protocol:     RuntimeProtocolForLoginProtocol(loginProtocol),
		BaseURL:      provider.BaseURL,
	}, modelIDs)
}

// BuildProviderLoginModelCapabilitiesForLogin 是 fetch-models / login 共用的
// 能力匹配入口：已有配置为基底，依次合并端点元数据、model card、协议兼容
// 默认值，并返回匹配报告（applied/skipped）。
func BuildProviderLoginModelCapabilitiesForLogin(
	providerName, loginProtocol string,
	provider config.Provider,
	models []ModelInfo,
	catalog *modelcard.Catalog,
) (map[string]config.ModelCapabilitySpec, []ModelCardAppliedInfo, []ModelCardSkippedInfo) {
	merged := cloneProviderLoginModelCapabilities(provider.ModelCapabilities)
	if merged == nil {
		merged = make(map[string]config.ModelCapabilitySpec)
	}
	defaultEfforts := defaultProviderLoginReasoningEfforts(providerName, loginProtocol, provider, models)
	ctx := modelcard.Context{
		ProviderName:    providerName,
		LoginProtocol:   loginProtocol,
		RuntimeProtocol: RuntimeProtocolForLoginProtocol(loginProtocol),
		BaseURL:         provider.BaseURL,
	}
	if catalog != nil {
		if template, ok := ResolveProviderTemplate(catalog, RuntimeProtocolForLoginProtocol(loginProtocol), provider); ok {
			ctx.ProviderTemplate = strings.TrimSpace(template.ID)
		}
	}
	applied := make([]ModelCardAppliedInfo, 0)
	skipped := make([]ModelCardSkippedInfo, 0)

	for _, model := range models {
		modelID := strings.TrimSpace(model.ID)
		if modelID == "" {
			continue
		}
		existing := merged[modelID]
		remote := providerLoginModelCapabilitySpec(model)
		compat := providerLoginCompatModelCapabilitySpec(modelID, providerName, loginProtocol, provider, defaultEfforts)
		cardCapability := config.ModelCapabilitySpec{}
		cardApplied := []modelcard.AppliedCard(nil)
		if catalog != nil {
			cardCapability, cardApplied = catalog.Resolve(ctx, modelID)
			if len(cardApplied) == 0 {
				skipped = append(skipped, ModelCardSkippedInfo{Model: modelID, Reason: "no_matching_card"})
			}
			for _, item := range cardApplied {
				applied = append(applied, ModelCardAppliedInfo{
					Model:  modelID,
					CardID: item.CardID,
					Fields: append([]string(nil), item.Fields...),
				})
			}
		}
		spec := modelcard.MergeCapability(existing, remote, cardCapability, compat)
		if !ModelCapabilityIsEmpty(spec) {
			merged[modelID] = spec
		}
	}

	if providerLoginUsesWildcardReasoningEfforts(loginProtocol, provider) && len(defaultEfforts) > 0 {
		compat := config.ModelCapabilitySpec{
			ReasoningModel:   true,
			ReasoningEfforts: append([]string(nil), defaultEfforts...),
		}
		spec := modelcard.MergeCapability(merged["*"], config.ModelCapabilitySpec{}, config.ModelCapabilitySpec{}, compat)
		if !ModelCapabilityIsEmpty(spec) {
			merged["*"] = spec
		}
	}
	if len(merged) == 0 {
		return nil, applied, skipped
	}
	return merged, applied, skipped
}

// buildProviderLoginModelCapabilitiesForLogin 是包内既有点名的兼容包装。
func buildProviderLoginModelCapabilitiesForLogin(
	providerName, loginProtocol string,
	provider config.Provider,
	models []ModelInfo,
	catalog *modelcard.Catalog,
) (map[string]config.ModelCapabilitySpec, []ModelCardAppliedInfo, []ModelCardSkippedInfo) {
	return BuildProviderLoginModelCapabilitiesForLogin(providerName, loginProtocol, provider, models, catalog)
}

func providerLoginCompatModelCapabilitySpec(modelID, providerName, loginProtocol string, provider config.Provider, defaultEfforts []string) config.ModelCapabilitySpec {
	if len(defaultEfforts) == 0 {
		return config.ModelCapabilitySpec{}
	}
	if !providerLoginModelUsesDefaultReasoningEfforts(modelID, providerName, loginProtocol, provider) {
		return config.ModelCapabilitySpec{}
	}
	return config.ModelCapabilitySpec{
		ReasoningModel:   true,
		ReasoningEfforts: append([]string(nil), defaultEfforts...),
	}
}

// MergeProviderLoginModelCapabilities 把端点发现的 capabilities 合并进已有
// 配置（login 语义：已有配置为基底，不被端点数据清空）。
func MergeProviderLoginModelCapabilities(existing, discovered map[string]config.ModelCapabilitySpec) map[string]config.ModelCapabilitySpec {
	if len(discovered) == 0 {
		return cloneProviderLoginModelCapabilities(existing)
	}
	merged := cloneProviderLoginModelCapabilities(existing)
	if merged == nil {
		merged = make(map[string]config.ModelCapabilitySpec, len(discovered))
	}
	for model, spec := range discovered {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		merged[model] = mergeProviderLoginModelCapabilitySpec(merged[model], spec)
	}
	return merged
}

func mergeProviderLoginModelCapabilities(existing, discovered map[string]config.ModelCapabilitySpec) map[string]config.ModelCapabilitySpec {
	return MergeProviderLoginModelCapabilities(existing, discovered)
}

func mergeProviderLoginModelCapabilitySpec(base, update config.ModelCapabilitySpec) config.ModelCapabilitySpec {
	if len(update.InputModalities) > 0 {
		base.InputModalities = append([]string(nil), update.InputModalities...)
	}
	if update.NativeTools.ImageGeneration {
		base.NativeTools.ImageGeneration = true
	}
	if update.NativeTools.ImagesGenerationsAPI {
		base.NativeTools.ImagesGenerationsAPI = true
	}
	if update.ReasoningModel {
		base.ReasoningModel = true
	}
	if len(update.ReasoningEfforts) > 0 {
		base.ReasoningEfforts = append([]string(nil), update.ReasoningEfforts...)
	}
	if effort := strings.TrimSpace(update.DefaultReasoningEffort); effort != "" {
		base.DefaultReasoningEffort = effort
	}
	if update.MaxContextTokens > 0 {
		base.MaxContextTokens = update.MaxContextTokens
	}
	if update.MaxTokens > 0 {
		base.MaxTokens = update.MaxTokens
	}
	if update.SupportsRemoteCompact {
		base.SupportsRemoteCompact = true
	}
	return base
}

func cloneProviderLoginModelCapabilities(input map[string]config.ModelCapabilitySpec) map[string]config.ModelCapabilitySpec {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]config.ModelCapabilitySpec, len(input))
	for key, value := range input {
		if strings.TrimSpace(key) == "" {
			continue
		}
		if len(value.InputModalities) > 0 {
			value.InputModalities = append([]string(nil), value.InputModalities...)
		}
		if len(value.ReasoningEfforts) > 0 {
			value.ReasoningEfforts = append([]string(nil), value.ReasoningEfforts...)
		}
		if len(value.ReasoningEffortBudgets) > 0 {
			budgets := make(map[string]int, len(value.ReasoningEffortBudgets))
			for budgetKey, budgetValue := range value.ReasoningEffortBudgets {
				budgets[budgetKey] = budgetValue
			}
			value.ReasoningEffortBudgets = budgets
		}
		output[strings.TrimSpace(key)] = value
	}
	return output
}
