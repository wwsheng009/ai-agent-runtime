package commands

import (
	"regexp"
	"strings"

	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolnames"
)

const imagegenSkillFunctionName = "skill__imagegen"

var imageGenerationIntentPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(generate|create|draw|make|render|design)\b.{0,40}\b(image|picture|photo|illustration|poster|avatar|wallpaper|portrait|cover)\b`),
	regexp.MustCompile(`(?i)\b(image|picture|photo|illustration|poster|avatar|wallpaper|portrait|cover)\b.{0,20}\b(generate|create|draw|make|render|design)\b`),
	regexp.MustCompile(`(?:生成|创建|画|绘制|做|出).{0,20}(?:图片|图像|插画|海报|头像|壁纸|封面|照片)`),
	regexp.MustCompile(`(?:来|给我).{0,8}(?:一张|一个).{0,16}(?:图片|图像|插画|海报|头像|壁纸|照片)`),
}

func filterImageGenerationToolExposure(
	session *ChatSession,
	prompt string,
	selection *aicliFunctionSelection,
	details *skillExposureDetails,
) *aicliFunctionSelection {
	if selection == nil {
		return selection
	}
	if sessionHasCodexNativeImageGeneration(session) {
		return removeFunctionsFromSelection(
			selection,
			toolnames.OpenAIImageGenerateToolName,
			toolnames.LegacyImageGenerateToolName,
			imagegenSkillFunctionName,
		)
	}
	if !selectionContainsAnyFunction(selection, toolnames.OpenAIImageGenerateToolName, toolnames.LegacyImageGenerateToolName) {
		return selection
	}
	if shouldExposeOpenAIImageGenerateTool(session, prompt, selection, details) {
		return selection
	}
	return removeFunctionsFromSelection(selection, toolnames.OpenAIImageGenerateToolName, toolnames.LegacyImageGenerateToolName)
}

func shouldExposeOpenAIImageGenerateTool(
	session *ChatSession,
	prompt string,
	selection *aicliFunctionSelection,
	details *skillExposureDetails,
) bool {
	if selectionContainsAnyFunction(selection, imagegenSkillFunctionName) {
		return false
	}

	normalizedPrompt := normalizeImageGenerationExposurePrompt(prompt, details)
	if toolnames.IsOpenAIImageGenerateToolName(normalizedPrompt) || strings.Contains(normalizedPrompt, toolnames.OpenAIImageGenerateToolName) {
		return true
	}
	if promptLooksLikeImageGenerationIntent(normalizedPrompt) {
		return true
	}
	if hasPreviouslyCalledImageGenerationFunction(session) {
		return true
	}
	return false
}

func normalizeImageGenerationExposurePrompt(prompt string, details *skillExposureDetails) string {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" && details != nil {
		trimmed = strings.TrimSpace(details.RoutingPrompt)
	}
	return strings.ToLower(trimmed)
}

func promptLooksLikeImageGenerationIntent(prompt string) bool {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		return false
	}
	for _, pattern := range imageGenerationIntentPatterns {
		if pattern.MatchString(trimmed) {
			return true
		}
	}
	return false
}

func hasPreviouslyCalledImageGenerationFunction(session *ChatSession) bool {
	if session == nil || len(session.Messages) == 0 {
		return false
	}
	for _, message := range session.Messages {
		for _, call := range message.ToolCalls {
			if toolnames.IsOpenAIImageGenerateToolName(call.Name) {
				return true
			}
		}
	}
	return false
}

func sessionHasCodexNativeImageGeneration(session *ChatSession) bool {
	if session == nil {
		return false
	}
	return runtimellm.CodexImageGenerationEnabled(
		session.Provider.GetProtocol(),
		strings.TrimSpace(session.Model),
		session.Provider.ModelCapabilities,
	)
}

func selectionContainsAnyFunction(selection *aicliFunctionSelection, names ...string) bool {
	if selection == nil || len(names) == 0 {
		return false
	}
	for _, name := range names {
		if selectionContainsFunction(selection, name) {
			return true
		}
	}
	return false
}

func selectionContainsFunction(selection *aicliFunctionSelection, name string) bool {
	name = strings.TrimSpace(name)
	if selection == nil || name == "" {
		return false
	}
	for _, item := range selection.FinalFunctionNames {
		if strings.EqualFold(strings.TrimSpace(item), name) {
			return true
		}
	}
	for _, item := range selection.BuiltinFunctions {
		if strings.EqualFold(strings.TrimSpace(item), name) {
			return true
		}
	}
	for _, item := range selection.SkillFunctions {
		if strings.EqualFold(strings.TrimSpace(item), name) {
			return true
		}
	}
	for _, schema := range selection.Schemas {
		schemaName, _ := schema["name"].(string)
		if strings.EqualFold(strings.TrimSpace(schemaName), name) {
			return true
		}
	}
	return false
}

func removeFunctionsFromSelection(selection *aicliFunctionSelection, names ...string) *aicliFunctionSelection {
	if selection == nil || len(names) == 0 {
		return selection
	}
	blocked := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = strings.ToLower(strings.TrimSpace(name))
		if name != "" {
			blocked[name] = struct{}{}
		}
	}
	filterNames := func(items []string) []string {
		if len(items) == 0 {
			return nil
		}
		filtered := make([]string, 0, len(items))
		for _, item := range items {
			key := strings.ToLower(strings.TrimSpace(item))
			if _, ok := blocked[key]; ok {
				continue
			}
			filtered = append(filtered, item)
		}
		return filtered
	}
	filterSchemas := func(items []map[string]interface{}) []map[string]interface{} {
		if len(items) == 0 {
			return nil
		}
		filtered := make([]map[string]interface{}, 0, len(items))
		for _, schema := range items {
			schemaName, _ := schema["name"].(string)
			key := strings.ToLower(strings.TrimSpace(schemaName))
			if _, ok := blocked[key]; ok {
				continue
			}
			filtered = append(filtered, cloneFunctionSchema(schema))
		}
		return filtered
	}

	selection.BuiltinFunctions = filterNames(selection.BuiltinFunctions)
	selection.SkillFunctions = filterNames(selection.SkillFunctions)
	selection.FinalFunctionNames = filterNames(selection.FinalFunctionNames)
	selection.Schemas = filterSchemas(selection.Schemas)
	return selection
}
