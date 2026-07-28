package llm

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
)

func buildMetaToolsForProtocol(protocol string) interface{} {
	meta := adapter.BuildMCPMetaTools()
	switch protocol {
	case "codex":
		return convertNamedToolsToCodex(meta)
	case "anthropic":
		return convertNamedToolsToAnthropic(meta)
	case "gemini":
		return convertNamedToolsToGemini(meta)
	default:
		return convertNamedToolsToOpenAI(meta)
	}
}

func buildToolDefinitionsForProtocol(tools []map[string]interface{}, protocol string, includeMeta bool) interface{} {
	if len(tools) == 0 && !includeMeta {
		return nil
	}

	combined := make([]map[string]interface{}, 0, len(tools)+4)
	addTool := func(tool map[string]interface{}) {
		if len(tool) == 0 {
			return
		}
		combined = append(combined, tool)
	}

	for _, tool := range tools {
		addTool(prepareToolDefinitionForProtocol(tool, protocol))
	}
	if includeMeta {
		for _, tool := range adapter.BuildMCPMetaTools() {
			addTool(prepareToolDefinitionForProtocol(tool, protocol))
		}
	}

	switch protocol {
	case "codex":
		return convertNamedToolsToCodex(combined)
	case "anthropic":
		return convertNamedToolsToAnthropic(combined)
	case "gemini":
		return convertNamedToolsToGemini(combined)
	default:
		return convertNamedToolsToOpenAI(combined)
	}
}

func prepareToolDefinitionForProtocol(tool map[string]interface{}, protocol string) map[string]interface{} {
	if len(tool) == 0 {
		return nil
	}
	normalizedProtocol := strings.ToLower(strings.TrimSpace(protocol))
	if normalizedProtocol == "codex" {
		if custom := buildCodexFreeformToolDefinition(tool); custom != nil {
			return custom
		}
	}

	cloned := make(map[string]interface{}, len(tool))
	for key, value := range tool {
		if key == "metadata" {
			continue
		}
		cloned[key] = value
	}
	cloned["parameters"] = normalizeRawToolParametersForRequest(cloned["parameters"])
	return cloned
}

func normalizeRawToolParametersForRequest(raw interface{}) map[string]interface{} {
	params, _ := raw.(map[string]interface{})
	return cloneToolParametersForRequest(params)
}

func buildCodexFreeformToolDefinition(tool map[string]interface{}) map[string]interface{} {
	metadata, _ := tool["metadata"].(map[string]interface{})
	if len(metadata) == 0 {
		return nil
	}
	freeform, _ := metadata["freeform"].(map[string]interface{})
	if len(freeform) == 0 {
		return nil
	}

	name, _ := tool["name"].(string)
	description, _ := tool["description"].(string)
	if strings.TrimSpace(name) == "" || strings.TrimSpace(description) == "" {
		return nil
	}

	format := cloneDeepMapStringAny(freeform)
	if len(format) == 0 {
		return nil
	}
	return map[string]interface{}{
		"type":        "custom",
		"name":        strings.TrimSpace(name),
		"description": strings.TrimSpace(description),
		"format":      format,
	}
}

func convertNamedToolsToOpenAI(meta []map[string]interface{}) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(meta))
	for _, tool := range meta {
		result = append(result, map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":        tool["name"],
				"description": tool["description"],
				"parameters":  tool["parameters"],
			},
		})
	}
	return result
}

func convertNamedToolsToAnthropic(meta []map[string]interface{}) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(meta))
	for _, tool := range meta {
		result = append(result, map[string]interface{}{
			"name":         tool["name"],
			"description":  tool["description"],
			"input_schema": tool["parameters"],
		})
	}
	return result
}

func convertNamedToolsToGemini(meta []map[string]interface{}) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(meta))
	for _, tool := range meta {
		result = append(result, map[string]interface{}{
			"name":        tool["name"],
			"description": tool["description"],
			"parameters":  tool["parameters"],
		})
	}
	return result
}

func convertNamedToolsToCodex(meta []map[string]interface{}) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(meta))
	for _, tool := range meta {
		if toolType, _ := tool["type"].(string); strings.TrimSpace(toolType) != "" && !strings.EqualFold(strings.TrimSpace(toolType), "function") {
			cloned := make(map[string]interface{}, len(tool))
			for key, value := range tool {
				cloned[key] = value
			}
			result = append(result, cloned)
			continue
		}
		converted := map[string]interface{}{
			"name":        tool["name"],
			"description": tool["description"],
			"parameters":  tool["parameters"],
		}
		if strict, ok := tool["strict"].(bool); ok {
			converted["strict"] = strict
		} else {
			converted["strict"] = false
		}
		result = append(result, converted)
	}
	return result
}
