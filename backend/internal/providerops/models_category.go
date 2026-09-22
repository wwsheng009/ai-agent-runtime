package providerops

import (
	"encoding/json"
	"strings"
)

// ---------------------------------------------------------------------------
// /models 响应解析类别（category）。
//
// 不同厂商/网关的模型清单形状差异很大：OpenAI 兼容网关是扁平键
// （id / input_modalities / context_length ...），OpenRouter 及同族聚合网关把
// 元数据嵌在 architecture / reasoning / top_provider 等子对象里。类别解析器把
// 「形状差异」收敛到一处：每个类别声明自己的探测规则与逐条解析函数，通用链路
// （收集 → 去重 → 能力投影）保持不变。
//
// 新增同族网关时只需追加一个 modelListCategorySpec：
//   - matchesProvider: provider 名称 / base_url 命中（零成本，优先）
//   - matchesEntry:    载荷形状命中（覆盖别名域名、自建镜像站）
//   - parseEntry:      逐条解析为 ModelInfo
// ---------------------------------------------------------------------------

// ModelListCategory 是 /models 响应的解析类别标签。
type ModelListCategory string

const (
	// ModelListCategoryGeneric 是扁平键形状：OpenAI 兼容网关、Codex、vLLM
	// 以及多数自建站点的默认形状。
	ModelListCategoryGeneric ModelListCategory = "generic"
	// ModelListCategoryOpenRouter 是 OpenRouter 及其同族聚合网关的嵌套形状
	// （architecture / reasoning / top_provider / supported_parameters）。
	ModelListCategoryOpenRouter ModelListCategory = "openrouter"
)

// modelListCategoryShapeProbeLimit 是形状探测时最多检查的条目数。
const modelListCategoryShapeProbeLimit = 5

// modelListCategorySpec 描述一个解析类别的探测与解析规则。
type modelListCategorySpec struct {
	// category 是类别标签。
	category ModelListCategory
	// matchesProvider 按 provider 名称 / base_url 判断类别；命中后不再看载荷。
	matchesProvider func(providerName, baseURL string) bool
	// matchesEntry 按单条载荷形状判断类别（别名域名 / 镜像站回退）。
	matchesEntry func(item map[string]interface{}) bool
	// parseEntry 把一条载荷解析为 ModelInfo；返回零值（ID 为空）表示该条无效。
	parseEntry func(item map[string]interface{}, loginProtocol string) ModelInfo
}

// modelListCategorySpecs 是类别注册表；顺序即探测优先级。
var modelListCategorySpecs = []modelListCategorySpec{
	{
		category: ModelListCategoryOpenRouter,
		matchesProvider: func(providerName, baseURL string) bool {
			if strings.Contains(strings.ToLower(strings.TrimSpace(providerName)), "openrouter") {
				return true
			}
			return strings.Contains(strings.ToLower(strings.TrimSpace(baseURL)), "openrouter.ai")
		},
		matchesEntry: openRouterEntryMatches,
		parseEntry:   openRouterModelInfoFromMap,
	},
}

// modelListCategorySpecFor 返回类别的解析规则；未注册类别返回 false，调用方
// 回退到通用扁平解析。
func modelListCategorySpecFor(category ModelListCategory) (modelListCategorySpec, bool) {
	for _, spec := range modelListCategorySpecs {
		if spec.category == category {
			return spec, true
		}
	}
	return modelListCategorySpec{}, false
}

// DetectModelListCategory 推断 /models 响应的解析类别：先按 provider 名称 /
// base_url，再按载荷形状（只看前若干条，避免为探测做全量结构分析）。
// raw 为空或无法解码时返回通用类别。
func DetectModelListCategory(providerName, baseURL string, raw []byte) ModelListCategory {
	for _, spec := range modelListCategorySpecs {
		if spec.matchesProvider != nil && spec.matchesProvider(providerName, baseURL) {
			return spec.category
		}
	}
	if len(raw) == 0 {
		return ModelListCategoryGeneric
	}
	var decoded interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return ModelListCategoryGeneric
	}
	entries := collectProviderModelEntries(decoded)
	for index, entry := range entries {
		if index >= modelListCategoryShapeProbeLimit {
			break
		}
		item, ok := entry.(map[string]interface{})
		if !ok {
			continue
		}
		for _, spec := range modelListCategorySpecs {
			if spec.matchesEntry != nil && spec.matchesEntry(item) {
				return spec.category
			}
		}
	}
	return ModelListCategoryGeneric
}

// ModelListCategoryName 返回类别标签的字符串形式（空值归一化为 generic），
// 供日志 / JSON 输出使用。
func ModelListCategoryName(category ModelListCategory) string {
	if strings.TrimSpace(string(category)) == "" {
		return string(ModelListCategoryGeneric)
	}
	return string(category)
}
