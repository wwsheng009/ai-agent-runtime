package toolnames

import "strings"

const (
	OpenAIImageGenerateToolName        = "openai_image_generate"
	LegacyImageGenerateToolName        = "image_generate"
	CodexNativeImageGenerationToolType = "image_generation"
	CodexNativeImageGenerationCallType = "image_generation_call"
)

func IsOpenAIImageGenerateToolName(name string) bool {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return false
	}
	return strings.EqualFold(trimmed, OpenAIImageGenerateToolName) || strings.EqualFold(trimmed, LegacyImageGenerateToolName)
}

func CanonicalOpenAIImageGenerateToolName(name string) string {
	if IsOpenAIImageGenerateToolName(name) {
		return OpenAIImageGenerateToolName
	}
	return strings.TrimSpace(name)
}
