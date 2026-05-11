package llm

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/imagegen"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolnames"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const (
	MetadataKeyGeneratedImageOutputDir     = "generated_image_output_dir"
	MetadataKeyGeneratedImages             = "generated_images"
	MetadataKeyCodexImageGenerationOptions = "codex_image_generation_options"
	codexImageGenerationToolType           = toolnames.CodexNativeImageGenerationToolType
	codexImageGenerationCallType           = toolnames.CodexNativeImageGenerationCallType
	defaultGeneratedImageFormat            = "png"
)

// GeneratedImage stores local metadata for one saved image_generation result.
type GeneratedImage = imagegen.SavedImage

// CodexImageGenerationOptions carries request-side options for the Codex
// Responses image_generation native tool.
type CodexImageGenerationOptions struct {
	Size              string
	Quality           string
	Background        string
	OutputFormat      string
	OutputCompression *int
}

// BuildToolDefinitionsForRequest converts local tool definitions into protocol
// request payloads and appends Codex native tools when the selected model
// explicitly supports them.
func BuildToolDefinitionsForRequest(
	tools []types.ToolDefinition,
	protocol string,
	model string,
	modelCapabilities map[string]agentconfig.ModelCapabilitySpec,
	includeMeta bool,
) interface{} {
	return BuildToolDefinitionsForRequestWithImageOptions(tools, protocol, model, modelCapabilities, includeMeta, nil)
}

// BuildToolDefinitionsForRequestWithImageOptions is BuildToolDefinitionsForRequest
// plus optional request-side fields for Codex native image_generation.
func BuildToolDefinitionsForRequestWithImageOptions(
	tools []types.ToolDefinition,
	protocol string,
	model string,
	modelCapabilities map[string]agentconfig.ModelCapabilitySpec,
	includeMeta bool,
	imageOptions *CodexImageGenerationOptions,
) interface{} {
	nativeImageGenerationEnabled := CodexImageGenerationEnabled(protocol, model, modelCapabilities)
	tools = filterOpenAIImageGenerateToolForCodexNative(tools, nativeImageGenerationEnabled)
	if len(tools) == 0 && !nativeImageGenerationEnabled && !includeMeta {
		return nil
	}

	normalized := make([]map[string]interface{}, 0, len(tools)+1)
	for _, tool := range tools {
		definition := map[string]interface{}{
			"name":        tool.Name,
			"description": tool.Description,
			"parameters":  cloneDeepMapStringAny(tool.Parameters),
		}
		if len(tool.Metadata) > 0 {
			definition["metadata"] = cloneDeepMapStringAny(tool.Metadata)
		}
		normalized = append(normalized, definition)
	}
	if nativeImageGenerationEnabled {
		normalized = append(normalized, buildCodexNativeImageGenerationTool(imageOptions))
	}

	return buildToolDefinitionsForProtocol(normalized, protocol, includeMeta)
}

func buildCodexNativeImageGenerationTool(options *CodexImageGenerationOptions) map[string]interface{} {
	tool := map[string]interface{}{
		"type": codexImageGenerationToolType,
	}
	outputFormat := defaultGeneratedImageFormat
	if options != nil && strings.TrimSpace(options.OutputFormat) != "" {
		outputFormat = strings.TrimSpace(options.OutputFormat)
	}
	if outputFormat != "" {
		tool["output_format"] = outputFormat
	}
	if options == nil {
		return tool
	}
	if value := strings.TrimSpace(options.Size); value != "" {
		tool["size"] = value
	}
	if value := strings.TrimSpace(options.Quality); value != "" {
		tool["quality"] = value
	}
	if value := strings.TrimSpace(options.Background); value != "" {
		tool["background"] = value
	}
	if options.OutputCompression != nil {
		tool["output_compression"] = *options.OutputCompression
	}
	return tool
}

func filterOpenAIImageGenerateToolForCodexNative(tools []types.ToolDefinition, nativeImageGenerationEnabled bool) []types.ToolDefinition {
	if !nativeImageGenerationEnabled || len(tools) == 0 {
		return tools
	}
	filtered := make([]types.ToolDefinition, 0, len(tools))
	for _, tool := range tools {
		if toolnames.IsOpenAIImageGenerateToolName(tool.Name) {
			continue
		}
		filtered = append(filtered, tool)
	}
	return filtered
}

// CodexImageGenerationEnabled reports whether the configured provider/model pair
// may expose the Responses image_generation native tool.
func CodexImageGenerationEnabled(
	protocol string,
	model string,
	modelCapabilities map[string]agentconfig.ModelCapabilitySpec,
) bool {
	if !strings.EqualFold(strings.TrimSpace(protocol), "codex") {
		return false
	}
	capability, ok := ResolveModelCapabilitySpec(model, modelCapabilities)
	return ok && agentconfig.ModelCapabilityHasTextImageNativeGeneration(capability)
}

// ProcessCodexAssistantImageGeneration saves image_generation results to disk,
// strips the raw base64 payload from replay metadata, and annotates the
// assistant message with generated image metadata.
func ProcessCodexAssistantImageGeneration(msg map[string]interface{}, outputDir string) ([]GeneratedImage, error) {
	return ProcessCodexAssistantImageGenerationWithOptions(msg, outputDir, nil)
}

// ProcessCodexAssistantImageGenerationWithOptions is ProcessCodexAssistantImageGeneration
// with a caller-provided output format hint for native image_generation.
func ProcessCodexAssistantImageGenerationWithOptions(msg map[string]interface{}, outputDir string, options *CodexImageGenerationOptions) ([]GeneratedImage, error) {
	items := extractCodexOutputItems(msg)
	if len(items) == 0 || strings.TrimSpace(outputDir) == "" {
		return nil, nil
	}

	sanitized := make([]map[string]interface{}, 0, len(items))
	generated := make([]GeneratedImage, 0, 1)
	var errs []string

	for _, item := range items {
		cloned := cloneDeepMapStringAny(item)
		if !strings.EqualFold(strings.TrimSpace(stringValue(cloned["type"])), codexImageGenerationCallType) {
			sanitized = append(sanitized, cloned)
			continue
		}

		payload := strings.TrimSpace(stringValue(cloned["result"]))
		if payload == "" {
			if canonical := canonicalizeCodexOutputItem(cloned); canonical != nil {
				sanitized = append(sanitized, canonical)
			} else {
				sanitized = append(sanitized, cloned)
			}
			continue
		}

		image, err := saveGeneratedImage(outputDir, cloned, options)
		if err != nil {
			errs = append(errs, err.Error())
			if canonical := canonicalizeCodexOutputItem(cloned); canonical != nil {
				sanitized = append(sanitized, canonical)
			} else {
				sanitized = append(sanitized, cloned)
			}
			continue
		}

		delete(cloned, "result")
		cloned["status"] = "completed"
		if canonical := canonicalizeCodexOutputItem(cloned); canonical != nil {
			cloned = canonical
		}
		generated = append(generated, image)
		sanitized = append(sanitized, cloned)
	}

	if len(generated) > 0 {
		msg[codexResponseOutputItemsMessageKey] = sanitized
		if details := decodeMapAny(msg[assistantReasoningDetailsKey]); details != nil {
			metadata := decodeMapAny(details["metadata"])
			if metadata == nil {
				metadata = map[string]interface{}{}
			}
			metadata[reasoningMetadataCodexOutputItemsKey] = sanitized
			details["metadata"] = metadata
			msg[assistantReasoningDetailsKey] = details
		}
		attachGeneratedImagesMetadata(msg, generated)
		if strings.TrimSpace(stringValue(msg["content"])) == "" {
			msg["content"] = GeneratedImageSummary(generated)
		}
	}

	if len(errs) == 0 {
		return generated, nil
	}
	return generated, errors.New(strings.Join(errs, "; "))
}

// GeneratedImageSummary renders a minimal user-facing summary for saved images.
func GeneratedImageSummary(images []GeneratedImage) string {
	if len(images) == 0 {
		return ""
	}
	if len(images) == 1 {
		return fmt.Sprintf("Generated image saved to %s", markdownLinkForLocalPath(images[0].SavedPath))
	}
	return fmt.Sprintf("Generated %d images. First saved to %s", len(images), markdownLinkForLocalPath(images[0].SavedPath))
}

func markdownLinkForLocalPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	fileURL := fileURLForLocalPath(trimmed)
	if fileURL == "" {
		return trimmed
	}
	return fmt.Sprintf("[%s](%s)", trimmed, fileURL)
}

func fileURLForLocalPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	resolved := trimmed
	if !filepath.IsAbs(resolved) {
		if abs, err := filepath.Abs(resolved); err == nil {
			resolved = abs
		}
	}
	slashed := filepath.ToSlash(resolved)
	if vol := filepath.VolumeName(resolved); vol != "" && !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	return (&url.URL{Scheme: "file", Path: slashed}).String()
}

func extractCodexOutputItems(msg map[string]interface{}) []map[string]interface{} {
	if len(msg) == 0 {
		return nil
	}
	if items := decodeSliceOfMaps(msg[codexResponseOutputItemsMessageKey]); len(items) > 0 {
		return items
	}
	if details := decodeMapAny(msg[assistantReasoningDetailsKey]); details != nil {
		if metadata := decodeMapAny(details["metadata"]); metadata != nil {
			if items := decodeSliceOfMaps(metadata[reasoningMetadataCodexOutputItemsKey]); len(items) > 0 {
				return items
			}
		}
	}
	return nil
}

func attachGeneratedImagesMetadata(msg map[string]interface{}, images []GeneratedImage) {
	if len(images) == 0 {
		return
	}
	metadata := decodeMapAny(msg["metadata"])
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	payload := make([]map[string]interface{}, 0, len(images))
	for _, image := range images {
		entry := map[string]interface{}{
			"id":             image.ID,
			"status":         image.Status,
			"revised_prompt": image.RevisedPrompt,
			"mime_type":      image.MimeType,
			"saved_path":     image.SavedPath,
			"sha256":         image.SHA256,
			"byte_count":     image.ByteCount,
		}
		payload = append(payload, entry)
	}
	metadata[MetadataKeyGeneratedImages] = payload
	msg["metadata"] = metadata
}

func saveGeneratedImage(outputDir string, item map[string]interface{}, options *CodexImageGenerationOptions) (GeneratedImage, error) {
	id := strings.TrimSpace(stringValue(item["id"]))
	if id == "" {
		id = "generated_image"
	}
	payload := strings.TrimSpace(stringValue(item["result"]))
	if payload == "" {
		return GeneratedImage{}, fmt.Errorf("image_generation %s returned empty payload", id)
	}
	format := codexImageGenerationOutputFormat(item, options)
	saved, err := imagegen.SaveBase64Image(outputDir, id, payload, format)
	if err != nil {
		return GeneratedImage{}, fmt.Errorf("image_generation %s: %w", id, err)
	}
	saved.Status = strings.TrimSpace(stringValue(item["status"]))
	saved.RevisedPrompt = strings.TrimSpace(stringValue(item["revised_prompt"]))
	return saved, nil
}

func codexImageGenerationOutputFormat(item map[string]interface{}, options *CodexImageGenerationOptions) string {
	if options != nil {
		if value := strings.TrimSpace(options.OutputFormat); value != "" {
			return value
		}
	}
	if value := strings.TrimSpace(stringValue(item["output_format"])); value != "" {
		return value
	}
	return defaultGeneratedImageFormat
}

// CodexImageGenerationOptionsFromMetadata extracts native image_generation
// options from an LLM request metadata map.
func CodexImageGenerationOptionsFromMetadata(metadata map[string]interface{}) *CodexImageGenerationOptions {
	if len(metadata) == 0 {
		return nil
	}
	raw, ok := metadata[MetadataKeyCodexImageGenerationOptions]
	if !ok || raw == nil {
		return nil
	}
	switch typed := raw.(type) {
	case CodexImageGenerationOptions:
		return cloneCodexImageGenerationOptions(&typed)
	case *CodexImageGenerationOptions:
		return cloneCodexImageGenerationOptions(typed)
	case map[string]interface{}:
		return codexImageGenerationOptionsFromMap(typed)
	default:
		if decoded := decodeMapAny(raw); decoded != nil {
			return codexImageGenerationOptionsFromMap(decoded)
		}
	}
	return nil
}

func codexImageGenerationOptionsFromMap(values map[string]interface{}) *CodexImageGenerationOptions {
	if len(values) == 0 {
		return nil
	}
	options := &CodexImageGenerationOptions{
		Size:         strings.TrimSpace(stringValue(values["size"])),
		Quality:      strings.TrimSpace(stringValue(values["quality"])),
		Background:   strings.TrimSpace(stringValue(values["background"])),
		OutputFormat: strings.TrimSpace(stringValue(values["output_format"])),
	}
	if raw, ok := values["output_compression"]; ok && raw != nil {
		value := intValue(raw)
		options.OutputCompression = &value
	}
	if options.Size == "" && options.Quality == "" && options.Background == "" && options.OutputFormat == "" && options.OutputCompression == nil {
		return nil
	}
	return options
}

func cloneCodexImageGenerationOptions(options *CodexImageGenerationOptions) *CodexImageGenerationOptions {
	if options == nil {
		return nil
	}
	cloned := *options
	if options.OutputCompression != nil {
		value := *options.OutputCompression
		cloned.OutputCompression = &value
	}
	return &cloned
}

func cloneDeepMapStringAny(input map[string]interface{}) map[string]interface{} {
	if input == nil {
		return nil
	}
	cloned := make(map[string]interface{}, len(input))
	for key, value := range input {
		cloned[key] = cloneDeepInterfaceValue(value)
	}
	return cloned
}

func cloneDeepInterfaceValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return cloneDeepMapStringAny(typed)
	case []interface{}:
		if typed == nil {
			return nil
		}
		cloned := make([]interface{}, len(typed))
		for index, item := range typed {
			cloned[index] = cloneDeepInterfaceValue(item)
		}
		return cloned
	case []map[string]interface{}:
		if typed == nil {
			return nil
		}
		cloned := make([]map[string]interface{}, len(typed))
		for index, item := range typed {
			cloned[index] = cloneDeepMapStringAny(item)
		}
		return cloned
	case []string:
		if typed == nil {
			return nil
		}
		cloned := make([]string, len(typed))
		copy(cloned, typed)
		return cloned
	default:
		return typed
	}
}

func stringValue(value interface{}) string {
	if value == nil {
		return ""
	}
	if typed, ok := value.(string); ok {
		return typed
	}
	return fmt.Sprint(value)
}
