package llm

import (
	"sort"
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
	seen := make(map[string]struct{}, len(tools)+4)
	addTool := func(tool map[string]interface{}) {
		if len(tool) == 0 {
			return
		}
		key := protocolToolDedupKey(tool)
		if key == "" {
			return
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
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

	sort.SliceStable(combined, func(i, j int) bool {
		left, _ := combined[i]["name"].(string)
		right, _ := combined[j]["name"].(string)
		left = strings.TrimSpace(left)
		right = strings.TrimSpace(right)
		if left == right {
			return false
		}
		if left == "" {
			return false
		}
		if right == "" {
			return true
		}
		return left < right
	})

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
	return cloned
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
		result = append(result, map[string]interface{}{
			"name":        tool["name"],
			"description": tool["description"],
			"parameters":  tool["parameters"],
		})
	}
	return result
}

func protocolToolDedupKey(tool map[string]interface{}) string {
	if len(tool) == 0 {
		return ""
	}
	if name, _ := tool["name"].(string); strings.TrimSpace(name) != "" {
		return "name:" + strings.TrimSpace(name)
	}
	if toolType, _ := tool["type"].(string); strings.TrimSpace(toolType) != "" {
		return "type:" + strings.TrimSpace(toolType)
	}
	return ""
}
