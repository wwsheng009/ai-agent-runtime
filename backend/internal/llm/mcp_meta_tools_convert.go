package llm

import "github.com/ai-gateway/ai-agent-runtime/internal/llm/adapter"

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
		name, _ := tool["name"].(string)
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		combined = append(combined, tool)
	}

	for _, tool := range tools {
		addTool(tool)
	}
	if includeMeta {
		for _, tool := range adapter.BuildMCPMetaTools() {
			addTool(tool)
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
		result = append(result, map[string]interface{}{
			"name":        tool["name"],
			"description": tool["description"],
			"parameters":  tool["parameters"],
		})
	}
	return result
}
