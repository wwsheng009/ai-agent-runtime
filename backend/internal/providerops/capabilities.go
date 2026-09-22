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

// ChatWebConfigModel 是单个模型的配置视图：模型名 + 其 capability 中与
// reasoning 相关的字段（前端 reasoning 编辑器可直接回显）。
type ChatWebConfigModel struct {
	Name                   string   `json:"name"`
	ReasoningModel         bool     `json:"reasoning_model"`
	ReasoningEfforts       []string `json:"reasoning_efforts,omitempty"`
	DefaultReasoningEffort string   `json:"default_reasoning_effort,omitempty"`
	CompactReasoningEffort string   `json:"compact_reasoning_effort,omitempty"`
	MaxContextTokens       int      `json:"max_context_tokens,omitempty"`
	MaxTokens              int      `json:"max_tokens,omitempty"`
}

// chatWebConfigModel 是包内既有点名的内部别名（fetch-models 元数据投影）。
type chatWebConfigModel = ChatWebConfigModel

// ModelCapabilityIsEmpty 判断 spec 是否不携带任何有效配置。
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
