package providerops

import "strings"

// ---------------------------------------------------------------------------
// openrouter 类别解析器。
//
// OpenRouter /v1/models 的条目形状（2026-09 实测，19 个顶层键）：
//
//	{
//	  "id": "z-ai/glm-5.3-flash", "name": "...", "created": 1750000000,
//	  "context_length": 1310720, "canonical_slug": "...", "hugging_face_id": "...",
//	  "architecture": {"modality": "text+image->text",
//	                   "input_modalities": ["text","image","video"],
//	                   "output_modalities": ["text"], "tokenizer": "...", ...},
//	  "pricing": {"prompt": "...", "completion": "...", ...},
//	  "top_provider": {"context_length": 1048576, "max_completion_tokens": 131072,
//	                   "is_moderated": false},
//	  "supported_parameters": ["tools","reasoning","include_reasoning", ...],
//	  "reasoning": {"mandatory": false, "default_enabled": true,
//	                "supported_efforts": ["max","high","low"], "default_effort": "high"},
//	  "knowledge_cutoff": "...", "expiration_date": null, ...
//	}
//
// 通用（generic）解析器只查顶层扁平键，因此上述嵌套字段此前全部丢失，只有
// context_length 存活。本解析器把这些嵌套字段映射到 ModelInfo，再由
// capabilities.go 投影进 model_capabilities。
// ---------------------------------------------------------------------------

// openRouterEntryMatches 判断单条载荷是否属于 OpenRouter 形状。命中任意一个
// 特征键即可：这三个键是 OpenRouter 独有/少见的嵌套结构，扁平网关不会出现。
func openRouterEntryMatches(item map[string]interface{}) bool {
	for _, key := range []string{"architecture", "top_provider", "supported_parameters"} {
		if _, ok := item[key]; ok {
			return true
		}
	}
	return false
}

// openRouterModelInfoFromMap 把一条 OpenRouter 模型载荷解析为 ModelInfo。
// 每条嵌套字段都保留扁平回退，便于同族网关只保留部分字段时仍可解析。
func openRouterModelInfoFromMap(item map[string]interface{}, loginProtocol string) ModelInfo {
	id := firstStringField(item, "id", "canonical_slug", "slug", "model", "name")
	id = normalizeProviderModelID(id, loginProtocol)
	if id == "" {
		return ModelInfo{}
	}
	displayName := firstStringField(item, "name", "display_name", "displayName", "title")
	if displayName == "" {
		displayName = id
	}

	architecture := nestedMapField(item, "architecture")
	reasoning := nestedMapField(item, "reasoning")
	topProvider := nestedMapField(item, "top_provider", "topProvider")
	supportedParameters := dedupeProviderStringOptions(
		firstStringSliceField(item, "supported_parameters", "supportedParameters"),
	)

	return ModelInfo{
		ID:                     id,
		DisplayName:            displayName,
		InputModalities:        openRouterInputModalities(item, architecture),
		ReasoningEfforts:       dedupeProviderStringOptions(firstStringSliceField(reasoning, "supported_efforts", "supportedEfforts")),
		DefaultReasoningEffort: firstStringField(reasoning, "default_effort", "defaultEffort"),
		ReasoningModel:         openRouterReasoningModel(reasoning, supportedParameters),
		MaxContextTokens:       openRouterMaxContextTokens(item, topProvider),
		MaxTokens:              firstIntField(topProvider, "max_completion_tokens", "maxCompletionTokens"),
		SupportsTools:          stringSliceContains(supportedParameters, "tools"),
		Raw:                    item,
	}
}

// stringSliceContains 判断字符串列表是否包含目标值（大小写不敏感）。
func stringSliceContains(values []string, target string) bool {
	target = strings.ToLower(strings.TrimSpace(target))
	if target == "" {
		return false
	}
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), target) {
			return true
		}
	}
	return false
}

// openRouterInputModalities 解析输入模态：architecture.input_modalities 优先，
// 其次由 architecture.modality（"text+image->text"）左半部分推导，最后回退到
// 扁平键（同族网关可能只保留扁平字段）。
func openRouterInputModalities(item, architecture map[string]interface{}) []string {
	if values := firstStringSliceField(architecture, "input_modalities", "inputModalities"); len(values) > 0 {
		return dedupeProviderStringOptions(values)
	}
	if modality := firstStringField(architecture, "modality"); modality != "" {
		if arrow := strings.Index(modality, "->"); arrow >= 0 {
			modality = modality[:arrow]
		}
		return dedupeProviderStringOptions(strings.Split(modality, "+"))
	}
	return dedupeProviderStringOptions(firstStringSliceField(item, "input_modalities", "inputModalities", "modalities"))
}

// openRouterReasoningModel 判断该模型是否属于 reasoning/thinking 模型：
// reasoning 对象出现即表示端点声明了推理能力（mandatory / default_enabled /
// supported_efforts 任意一项）；此外 supported_parameters 里的推理参数同样
// 视为证据。
func openRouterReasoningModel(reasoning map[string]interface{}, supportedParameters []string) bool {
	if len(reasoning) > 0 {
		return true
	}
	for _, parameter := range supportedParameters {
		switch strings.ToLower(strings.TrimSpace(parameter)) {
		case "reasoning", "include_reasoning", "reasoning_effort", "reasoning_effort_max", "thinking":
			return true
		}
	}
	return false
}

// openRouterMaxContextTokens 解析有效上下文长度。OpenRouter 同时给出模型声明值
// （context_length）与 top provider 的实际上限（top_provider.context_length），
// 两者都存在时取较小值：请求会路由到 top provider，按较小值做压缩预算才不会
// 触发上游 400。
func openRouterMaxContextTokens(item, topProvider map[string]interface{}) int {
	modelContext := firstIntField(item, "context_length", "contextLength", "max_context_tokens", "maxContextTokens", "context_window", "contextWindow")
	providerContext := firstIntField(topProvider, "context_length", "contextLength")
	switch {
	case modelContext > 0 && providerContext > 0:
		return min(modelContext, providerContext)
	case modelContext > 0:
		return modelContext
	default:
		return providerContext
	}
}

// nestedMapField 返回首个存在的嵌套对象；不存在或类型不符时返回 nil
// （读 nil map 安全，等价于「没有该字段」）。
func nestedMapField(item map[string]interface{}, keys ...string) map[string]interface{} {
	for _, key := range keys {
		if nested, ok := item[key].(map[string]interface{}); ok {
			return nested
		}
	}
	return nil
}
