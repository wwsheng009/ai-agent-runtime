package commands

import (
	"strings"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/modelcard"
)

// providerFetchModelGroup 是 fetch-models 响应中的一个协议分组：按 model
// card 推荐（与 aicli login 的自动分类同源）把 /models 返回的模型清单按
// provider template / 协议归类后的结果。Primary=true 的组与当前 provider
// 协议一致，其模型才应合并进该 provider 的 supported_models；其余组说明
// 上游网关还托管了其他协议的模型（如 new-api 类聚合网关同时列出
// gpt-*/claude-*/gemini-*），应由「自动导入」生成对应协议的独立 provider。
type providerFetchModelGroup struct {
	Protocol         string   `json:"protocol"`
	LoginProtocol    string   `json:"login_protocol,omitempty"`
	ProviderTemplate string   `json:"provider_template,omitempty"`
	HasTemplate      bool     `json:"has_template"`
	Primary          bool     `json:"primary"`
	Models           []string `json:"models"`
	// OtherModels 是该组中不属于主组的模型 ID。一个模型可能同时命中多张
	// 多协议卡（如 grok-4.5 同时支持 openai/anthropic/codex），此时它会
	// 同时出现在主组和其他组里；这部分模型对当前 provider 仍然可用，不应
	// 计入“被过滤”。各组 OtherModels 之和 == 响应的 other_models_count，
	// 保证前端“过滤 N 个（protocol×count …）”提示口径自洽。
	OtherModels []string `json:"other_models,omitempty"`
}

// providerFetchModelsClassification 是一次 fetch-models 的分类结果。
// nil 表示分类不可用（model card catalog 缺失 / 分组为空等），调用方应
// 退化为旧行为（返回原始全量模型 ID 列表），保证「获取模型列表」按钮
// 在极端环境下依然可用。
type providerFetchModelsClassification struct {
	LoginProtocol   string
	RuntimeProtocol string
	Groups          []providerFetchModelGroup
	PrimaryModels   []providerModelInfo
	// VerifiedModels 是主组中有非 fallback model card 数据支持的模型；
	// AssumedModels 是仅因“未匹配任何卡片、跟随当前协议 fallback”而归入
	// 主组的模型。/v1/models 是 OpenAI 风格端点，列出的模型几乎必然支持
	// chat/completions，因此 openai 协议下 assumed 模型照旧合并（旧行为）；
	// 但 codex/anthropic 等协议把它们当作可用属于无证据假定——实测
	// opencode.ai 网关对 minimax/kimi 走 /v1/responses 会明确返回
	// “Model X is not supported for format openai”。调用方可据此不把
	// assumed 模型合并进 supported_models，并单独提示用户。
	VerifiedModels []providerModelInfo
	AssumedModels  []providerModelInfo
	TotalModels    int
}

// classifyProviderFetchedModels 复用 aicli login 的模型分类链路
// （normalizeProviderLoginModelsForProtocol →
// groupProviderLoginModelsByProviderTemplate →
// ensureExplicitProviderLoginModelGroup →
// selectPrimaryProviderLoginModelGroup），把 /models 返回的混合模型清单
// 按协议 / provider template 分组，并选出与当前 provider 协议一致的主组。
// groupingLoginProtocol 必须是具体协议（login 语义中的“已校验协议”），
// requestedLoginProtocol 可为 auto；两者都不可用时应由调用方先归一化。
func classifyProviderFetchedModels(
	providerName string,
	provider config.Provider,
	requestedLoginProtocol, groupingLoginProtocol string,
	models []providerModelInfo,
	cfg *config.Config,
) *providerFetchModelsClassification {
	catalog, _, err := loadProviderLoginModelCardCatalog(providerLoginRequest{}, cfg)
	if err != nil || catalog == nil {
		return nil
	}
	normalized := normalizeProviderLoginModelsForProtocol(models, groupingLoginProtocol)
	if len(normalized) == 0 {
		return nil
	}
	groups := groupProviderLoginModelsByProviderTemplate(providerName, groupingLoginProtocol, requestedLoginProtocol, provider, normalized, catalog, provider.AuthMode)
	groups = ensureExplicitProviderLoginModelGroup(requestedLoginProtocol, provider, normalized, groups, catalog, provider.AuthMode)
	if len(groups) == 0 {
		return nil
	}
	primaryIndex := selectPrimaryProviderLoginModelGroup(groups, requestedLoginProtocol, provider, catalog)
	if primaryIndex < 0 {
		return nil
	}
	primary := groups[primaryIndex]
	verified, assumed := splitProviderFetchPrimaryModels(catalog, providerName, groupingLoginProtocol, provider, primary)
	out := &providerFetchModelsClassification{
		LoginProtocol:   primary.LoginProtocol,
		RuntimeProtocol: primary.RuntimeProtocol,
		PrimaryModels:   primary.Models,
		VerifiedModels:  verified,
		AssumedModels:   assumed,
		TotalModels:     len(normalized),
	}
	primaryIDs := make(map[string]struct{}, len(primary.Models))
	for _, id := range providerModelIDs(primary.Models) {
		primaryIDs[id] = struct{}{}
	}
	for i, group := range groups {
		entry := providerFetchModelGroup{
			Protocol:         group.RuntimeProtocol,
			LoginProtocol:    group.LoginProtocol,
			ProviderTemplate: group.ProviderTemplate.ID,
			HasTemplate:      group.HasTemplate,
			Primary:          i == primaryIndex,
			Models:           providerModelIDs(group.Models),
		}
		if i != primaryIndex {
			entry.OtherModels = providerFetchGroupModelsOutsidePrimary(group.Models, primaryIDs)
		}
		out.Groups = append(out.Groups, entry)
	}
	return out
}

// splitProviderFetchPrimaryModels 把主组模型拆成“有卡数据支持”（verified）
// 与“仅靠当前协议 fallback 假定”（assumed）两部分。判定与
// groupProviderLoginModelsByProviderTemplate 使用完全相同的上下文（同一
// catalog、同一 currentTemplate fallback 语义）：模型对主组 template 有
// 非 fallback 卡片命中才算 verified；否则即 assumed。
func splitProviderFetchPrimaryModels(
	catalog *modelcard.Catalog,
	providerName, loginProtocol string,
	provider config.Provider,
	group providerLoginModelGroup,
) (verified, assumed []providerModelInfo) {
	runtimeProtocol := runtimeProtocolForLoginProtocol(loginProtocol)
	currentTemplate, _ := resolveProviderLoginProviderTemplate(catalog, runtimeProtocol, provider)
	ctx := modelcard.Context{
		ProviderName:     providerName,
		LoginProtocol:    loginProtocol,
		RuntimeProtocol:  runtimeProtocol,
		BaseURL:          provider.BaseURL,
		ProviderTemplate: strings.TrimSpace(currentTemplate.ID),
	}
	for _, model := range group.Models {
		if providerFetchModelHasCardBacking(catalog, ctx, model.ID, group) {
			verified = append(verified, model)
		} else {
			assumed = append(assumed, model)
		}
	}
	return verified, assumed
}

// providerFetchModelHasCardBacking 判定模型在主组 template 上是否有非
// fallback 卡片命中。组本身无 template（协议兜底组）时一律视为 assumed。
func providerFetchModelHasCardBacking(catalog *modelcard.Catalog, ctx modelcard.Context, modelID string, group providerLoginModelGroup) bool {
	if catalog == nil || !group.HasTemplate {
		return false
	}
	for _, recommendation := range catalog.RecommendedProviderTemplates(ctx, modelID) {
		if !sameProviderLoginTemplateID(recommendation.Template, group.ProviderTemplate) {
			continue
		}
		for _, applied := range recommendation.Applied {
			if !applied.Fallback {
				return true
			}
		}
	}
	return false
}

// providerFetchGroupModelsOutsidePrimary 返回组内不属于主组的模型 ID，
// 用于跨组去重后的“被过滤”计数（一个模型命中多组时只计入不含它的组）。
func providerFetchGroupModelsOutsidePrimary(models []providerModelInfo, primaryIDs map[string]struct{}) []string {
	out := make([]string, 0, len(models))
	for _, id := range providerModelIDs(models) {
		if _, ok := primaryIDs[id]; !ok {
			out = append(out, id)
		}
	}
	return out
}

// resolveProviderFetchModelsLoginProtocol 归一化 fetch-models 请求中的协议：
// 表单协议为空 / auto 时回退到 provider 已保存协议；仍不可用则按 openai
// 处理（与 validateProviderModels 对空协议的 models path 解析默认一致）。
func resolveProviderFetchModelsLoginProtocol(requested string, provider config.Provider) string {
	requested = strings.TrimSpace(requested)
	if requested != "" && !isAutoLoginProtocol(requested) {
		return normalizeLoginProtocol(requested, provider.AuthMode)
	}
	if protocol := loginProtocolFromProvider(provider, provider.AuthMode); protocol != "" && !isAutoLoginProtocol(protocol) {
		return normalizeLoginProtocol(protocol, provider.AuthMode)
	}
	return "openai"
}
