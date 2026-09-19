package commands

import (
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/providerops"
)

// ---------------------------------------------------------------------------
// fetch-models 分类 / 元数据（commands 薄转发层）。
//
// 实现本体已迁移至 internal/providerops（classify.go + catalog.go +
// capabilities.go），与 runtime server 的 /api/runtime/providers/fetch-models
// 及 auto-import 共用同一份逻辑；本文件只保留既有调用点
// （web_config_handlers）与单元测试使用的类型别名和转发函数。
// ---------------------------------------------------------------------------

// providerFetchModelGroup 是 fetch-models 响应中的一个协议分组（JSON 形状与
// 迁移前完全一致：RuntimeProtocol 序列化为 "protocol"）。
type providerFetchModelGroup = providerops.ModelGroup

// providerFetchModelsClassification 是一次 fetch-models 的分类结果；nil 表示
// 分类不可用，调用方应退化为旧行为（返回原始全量模型 ID 列表）。
type providerFetchModelsClassification = providerops.Classification

// chatWebConfigModel 是单个模型的配置视图（web JSON 形状不变）。
type chatWebConfigModel = providerops.ChatWebConfigModel

// classifyProviderFetchedModels 复用 aicli login 的模型分类链路，把 /models
// 返回的混合模型清单按协议 / provider template 分组，并选出与当前 provider
// 协议一致的主组。
func classifyProviderFetchedModels(
	providerName string,
	provider config.Provider,
	requestedLoginProtocol, groupingLoginProtocol string,
	models []providerModelInfo,
	cfg *config.Config,
) *providerFetchModelsClassification {
	return providerops.ClassifyFetchedModels(providerName, provider, requestedLoginProtocol, groupingLoginProtocol, models, cfg)
}

// resolveProviderFetchModelsLoginProtocol 归一化 fetch-models 请求中的协议：
// 表单协议为空 / auto 时回退到 provider 已保存协议；仍不可则按 openai 处理。
func resolveProviderFetchModelsLoginProtocol(requested string, provider config.Provider) string {
	return providerops.ResolveFetchModelsLoginProtocol(requested, provider)
}

// buildProviderFetchModelMetadata 为本次拉取到的模型重新匹配 reasoning 相关
// 元数据（覆盖语义，已保存配置不参与合并）。
func buildProviderFetchModelMetadata(
	providerName, loginProtocol string,
	provider config.Provider,
	models []providerModelInfo,
	cfg *config.Config,
) map[string]chatWebConfigModel {
	return providerops.MatchFetchedModelMetadata(providerName, loginProtocol, provider, models, cfg)
}
