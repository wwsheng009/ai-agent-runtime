package providerops

import (
	"strings"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// ---------------------------------------------------------------------------
// 契约入口：runtime server（backend/internal/api/skills）调用的冻结 API。
// 全部为转发/投影，逻辑本体在 classify.go / capabilities.go / catalog.go。
// ---------------------------------------------------------------------------

// ClassifyRequest 是一次 fetch-models 分类请求。
type ClassifyRequest struct {
	ProviderName           string
	RequestedLoginProtocol string // 可为 auto
	GroupingLoginProtocol  string // 必须是具体协议
	Provider               config.Provider
	Models                 []ModelInfo
	Config                 *config.Config
}

// Classify 等价于 commands.classifyProviderFetchedModels，nil 表示分类不可用。
func Classify(req ClassifyRequest) *Classification {
	return classifyFetchedModels(req.ProviderName, req.Provider, req.RequestedLoginProtocol, req.GroupingLoginProtocol, req.Models, req.Config)
}

// ModelMetadata 是单个模型的元数据匹配结果（前端 reasoning 编辑器可直接
// 回显的字段视图，比 ModelCapabilitySpec 少掉非 reasoning 字段）。
type ModelMetadata struct {
	ID                     string   `json:"id"`
	Name                   string   `json:"name"`
	ReasoningModel         bool     `json:"reasoning_model"`
	ReasoningEfforts       []string `json:"reasoning_efforts,omitempty"`
	DefaultReasoningEffort string   `json:"default_reasoning_effort,omitempty"`
	CompactReasoningEffort string   `json:"compact_reasoning_effort,omitempty"`
	MaxContextTokens       int      `json:"max_context_tokens,omitempty"`
	MaxTokens              int      `json:"max_tokens,omitempty"`
}

// MetadataRequest 是一次元数据匹配请求（覆盖重匹配语义）。
type MetadataRequest struct {
	ProviderName  string
	LoginProtocol string
	Provider      config.Provider
	Models        []ModelInfo
	Config        *config.Config
}

// BuildModelCapabilities 返回 provider 编辑器可直接写入 config 的
// model_capabilities（完整 spec）。MatchMetadata 与它是同一次匹配的两个
// 投影：元数据匹配为空的模型不出现在 MatchMetadata 返回值中。
func BuildModelCapabilities(req MetadataRequest) map[string]config.ModelCapabilitySpec {
	if len(req.Models) == 0 {
		return nil
	}
	catalog, _, err := LoadModelCardCatalog(req.Config)
	if err != nil {
		// 卡片目录不可用不应阻断匹配：端点元数据与协议兼容默认值仍可用。
		catalog = nil
	}
	// 覆盖语义：不把已保存的 capabilities 作为合并基底。
	base := req.Provider
	base.ModelCapabilities = nil
	capabilities, _, _ := BuildProviderLoginModelCapabilitiesForLogin(req.ProviderName, req.LoginProtocol, base, req.Models, catalog)
	if len(capabilities) == 0 {
		return nil
	}
	return capabilities
}

// MatchMetadata 把完整能力 spec 投影为前端字段视图；无元数据的模型不出现
// 在返回值中，由调用方决定是否清空旧配置。
func MatchMetadata(req MetadataRequest) map[string]ModelMetadata {
	capabilities := BuildModelCapabilities(req)
	out := make(map[string]ModelMetadata)
	if len(capabilities) == 0 {
		return out
	}
	for _, model := range req.Models {
		modelID := strings.TrimSpace(model.ID)
		if modelID == "" {
			continue
		}
		spec, ok := capabilities[modelID]
		if !ok || ModelCapabilityIsEmpty(spec) {
			continue
		}
		out[modelID] = ModelMetadata{
			ID:                     modelID,
			Name:                   modelID,
			ReasoningModel:         spec.ReasoningModel,
			ReasoningEfforts:       append([]string(nil), spec.ReasoningEfforts...),
			DefaultReasoningEffort: strings.TrimSpace(spec.DefaultReasoningEffort),
			CompactReasoningEffort: strings.TrimSpace(spec.CompactReasoningEffort),
			MaxContextTokens:       spec.MaxContextTokens,
			MaxTokens:              spec.MaxTokens,
		}
	}
	return out
}
