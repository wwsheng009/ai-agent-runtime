package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/imagegen"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolnames"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

// OpenAIImageGenerateTool performs an OpenAI-compatible image generations request and
// saves the resulting images into the active session artifact directory.
type OpenAIImageGenerateTool struct {
	*toolkit.BaseTool
	runtimeConfig          *runtimecfg.RuntimeConfig
	providerConfigResolver func() *agentconfig.Config
	clientFactory          func(provider agentconfig.Provider, timeout time.Duration, proxy *agentconfig.ProxyConfig) imagegen.Generator
}

// NewOpenAIImageGenerateTool creates a new tool instance using the provided runtime
// configuration for default values.
func NewOpenAIImageGenerateTool(runtimeConfig *runtimecfg.RuntimeConfig) *OpenAIImageGenerateTool {
	resolved := runtimeConfig
	if resolved == nil {
		resolved = runtimecfg.DefaultRuntimeConfig()
	}
	tool := &OpenAIImageGenerateTool{
		runtimeConfig:          resolved,
		providerConfigResolver: agentconfig.GetGlobalConfig,
		clientFactory: func(provider agentconfig.Provider, timeout time.Duration, proxy *agentconfig.ProxyConfig) imagegen.Generator {
			return imagegen.NewClient(provider, timeout, proxy)
		},
	}
	tool.BaseTool = toolkit.NewBaseTool(
		toolnames.OpenAIImageGenerateToolName,
		"通过 OpenAI 兼容的 /v1/images/generations 端点生成图片，并将结果保存到当前会话的 generated-images 目录。",
		"1.0.0",
		tool.parameters(),
		true,
	)
	return tool
}

func (t *OpenAIImageGenerateTool) parameters() map[string]interface{} {
	defaults := runtimecfg.DefaultRuntimeConfig()
	if t != nil && t.runtimeConfig != nil {
		defaults = t.runtimeConfig
	}
	generationCfg := defaults.Images.Generations
	maxN := generationCfg.MaxN
	if maxN < 1 {
		maxN = runtimecfg.DefaultRuntimeConfig().Images.Generations.MaxN
	}
	if maxN > 10 {
		maxN = 10
	}
	if maxN < 1 {
		maxN = 1
	}

	properties := map[string]interface{}{
		"prompt": map[string]interface{}{
			"type":        "string",
			"description": "要生成的图片提示词。",
		},
		"model": map[string]interface{}{
			"type":        "string",
			"description": "图像模型名称；默认值来自 runtime.images.generations.default_model。",
			"default":     generationCfg.DefaultModel,
		},
		"n": map[string]interface{}{
			"type":        "integer",
			"description": "单次请求生成的图片数量。",
			"default":     1,
			"minimum":     1,
			"maximum":     maxN,
		},
		"size": map[string]interface{}{
			"type":        "string",
			"description": "图片尺寸，默认值来自 runtime.images.generations.default_size。",
			"default":     generationCfg.DefaultSize,
		},
		"quality": map[string]interface{}{
			"type":        "string",
			"description": "图片质量，默认值来自 runtime.images.generations.default_quality。",
			"enum":        []string{"low", "medium", "high", "auto"},
			"default":     generationCfg.DefaultQuality,
		},
		"background": map[string]interface{}{
			"type":        "string",
			"description": "背景模式。",
			"enum":        []string{"transparent", "opaque", "auto"},
		},
		"output_format": map[string]interface{}{
			"type":        "string",
			"description": "输出格式，默认值来自 runtime.images.generations.default_output_format。",
			"enum":        []string{"png", "jpeg", "webp"},
			"default":     generationCfg.DefaultOutputFormat,
		},
		"output_compression": map[string]interface{}{
			"type":        "integer",
			"description": "输出压缩级别（0-100）。仅对部分格式生效。",
			"minimum":     0,
			"maximum":     100,
		},
	}

	return map[string]interface{}{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           properties,
		"required":             []string{"prompt"},
	}
}

// Execute performs the image generation request, persists all returned images,
// and returns a structured result compatible with existing artifact flows.
func (t *OpenAIImageGenerateTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req, explicitModel := t.buildRequest(params)
	if req == nil {
		return failureToolResult(fmt.Errorf("image generation request could not be constructed")), nil
	}

	providerCfg := t.resolveProviderConfig()
	if providerCfg == nil {
		return failureToolResult(fmt.Errorf("image generations provider configuration is unavailable")), nil
	}

	selection, err := t.selectProvider(providerCfg, req.Model, explicitModel)
	if err != nil {
		return failureToolResult(err), nil
	}
	req.Model = selection.Model
	t.applyRuntimeDefaults(req)

	if maxN := t.resolveMaxN(); maxN > 0 && req.N > maxN {
		req.N = maxN
	}

	imagegen.NormalizeGenerateRequest(req)
	if err := imagegen.Validate(req); err != nil {
		return failureToolResult(err), nil
	}

	timeout := t.resolveRequestTimeout()
	client := t.newClient(selection.Provider, timeout)
	resp, err := client.Generate(ctx, req)
	if err != nil {
		return failureToolResult(err), nil
	}

	outputDir := toolctx.GeneratedImageOutputDir(ctx)
	if strings.TrimSpace(outputDir) == "" {
		return failureToolResult(fmt.Errorf("generated image output dir is unavailable")), nil
	}

	images := make([]imagegen.SavedImage, 0, len(resp.Data))
	for index, item := range resp.Data {
		saved, saveErr := imagegen.SaveBase64Image(outputDir, fmt.Sprintf("image_%d", index+1), item.B64JSON, req.OutputFormat)
		if saveErr != nil {
			return failureToolResult(saveErr), nil
		}
		saved.Status = "completed"
		saved.RevisedPrompt = strings.TrimSpace(item.RevisedPrompt)
		images = append(images, saved)
	}

	return &toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindStructured,
		Content:    llm.GeneratedImageSummary(images),
		Metadata:   t.buildMetadata(selection, req, outputDir, images),
	}, nil
}

func (t *OpenAIImageGenerateTool) CanDirectCall() bool { return true }

func (t *OpenAIImageGenerateTool) DefinitionMetadata() map[string]interface{} {
	return map[string]interface{}{
		"tool_class":     "image_generation",
		"upstream_path":  "/v1/images/generations",
		"provider_scope": "images_generations_api",
	}
}

func (t *OpenAIImageGenerateTool) buildRequest(params map[string]interface{}) (*imagegen.GenerateRequest, bool) {
	req := &imagegen.GenerateRequest{}
	if params == nil {
		return req, false
	}

	var explicitModel bool
	if value, ok := stringParam(params, "prompt"); ok {
		req.Prompt = value
	}
	if value, ok := stringParam(params, "model"); ok {
		req.Model = value
		explicitModel = true
	}
	if value, ok := intParam(params, "n"); ok {
		req.N = value
	}
	if value, ok := stringParam(params, "size"); ok {
		req.Size = value
	}
	if value, ok := stringParam(params, "quality"); ok {
		req.Quality = value
	}
	if value, ok := stringParam(params, "background"); ok {
		req.Background = value
	}
	if value, ok := stringParam(params, "output_format"); ok {
		req.OutputFormat = value
	}
	if value, ok := intParam(params, "output_compression"); ok {
		v := value
		req.OutputCompression = &v
	}
	if value, ok := stringParam(params, "moderation"); ok {
		req.Moderation = value
	}
	return req, explicitModel
}

func (t *OpenAIImageGenerateTool) resolveProviderConfig() *agentconfig.Config {
	if t != nil && t.providerConfigResolver != nil {
		if cfg := t.providerConfigResolver(); cfg != nil {
			return cfg
		}
	}
	return agentconfig.GetGlobalConfig()
}

func (t *OpenAIImageGenerateTool) selectProvider(cfg *agentconfig.Config, requestedModel string, explicitModel bool) (*agentconfig.ImagesGenerationsSelection, error) {
	requestedModel = strings.TrimSpace(requestedModel)
	if explicitModel {
		return agentconfig.SelectImagesGenerationsProvider(cfg, agentconfig.ImagesGenerationsHint{Model: requestedModel})
	}

	defaultModel := strings.TrimSpace(t.resolveDefaultModel())
	if defaultModel != "" {
		if selection, err := agentconfig.SelectImagesGenerationsProvider(cfg, agentconfig.ImagesGenerationsHint{Model: defaultModel}); err == nil {
			return selection, nil
		}
	}
	return agentconfig.SelectImagesGenerationsProvider(cfg, agentconfig.ImagesGenerationsHint{})
}

func (t *OpenAIImageGenerateTool) resolveDefaultModel() string {
	if t == nil || t.runtimeConfig == nil {
		return runtimecfg.DefaultRuntimeConfig().Images.Generations.DefaultModel
	}
	return strings.TrimSpace(t.runtimeConfig.Images.Generations.DefaultModel)
}

func (t *OpenAIImageGenerateTool) resolveDefaultSize() string {
	if t == nil || t.runtimeConfig == nil {
		return runtimecfg.DefaultRuntimeConfig().Images.Generations.DefaultSize
	}
	return strings.TrimSpace(t.runtimeConfig.Images.Generations.DefaultSize)
}

func (t *OpenAIImageGenerateTool) resolveDefaultQuality() string {
	if t == nil || t.runtimeConfig == nil {
		return runtimecfg.DefaultRuntimeConfig().Images.Generations.DefaultQuality
	}
	return strings.TrimSpace(t.runtimeConfig.Images.Generations.DefaultQuality)
}

func (t *OpenAIImageGenerateTool) resolveDefaultOutputFormat() string {
	if t == nil || t.runtimeConfig == nil {
		return runtimecfg.DefaultRuntimeConfig().Images.Generations.DefaultOutputFormat
	}
	return strings.TrimSpace(t.runtimeConfig.Images.Generations.DefaultOutputFormat)
}

func (t *OpenAIImageGenerateTool) resolveRequestTimeout() time.Duration {
	if t == nil || t.runtimeConfig == nil {
		return runtimecfg.DefaultRuntimeConfig().Images.Generations.RequestTimeout
	}
	if t.runtimeConfig.Images.Generations.RequestTimeout > 0 {
		return t.runtimeConfig.Images.Generations.RequestTimeout
	}
	return runtimecfg.DefaultRuntimeConfig().Images.Generations.RequestTimeout
}

func (t *OpenAIImageGenerateTool) resolveMaxN() int {
	if t == nil || t.runtimeConfig == nil {
		return runtimecfg.DefaultRuntimeConfig().Images.Generations.MaxN
	}
	if t.runtimeConfig.Images.Generations.MaxN > 0 {
		return t.runtimeConfig.Images.Generations.MaxN
	}
	return runtimecfg.DefaultRuntimeConfig().Images.Generations.MaxN
}

func (t *OpenAIImageGenerateTool) applyRuntimeDefaults(req *imagegen.GenerateRequest) {
	if req == nil {
		return
	}
	if strings.TrimSpace(req.Model) == "" {
		req.Model = t.resolveDefaultModel()
	}
	if strings.TrimSpace(req.Size) == "" {
		req.Size = t.resolveDefaultSize()
	}
	if strings.TrimSpace(req.Quality) == "" {
		req.Quality = t.resolveDefaultQuality()
	}
	if strings.TrimSpace(req.OutputFormat) == "" {
		req.OutputFormat = t.resolveDefaultOutputFormat()
	}
	if req.N <= 0 {
		req.N = 1
	}
}

func (t *OpenAIImageGenerateTool) newClient(provider agentconfig.Provider, timeout time.Duration) imagegen.Generator {
	if t != nil && t.clientFactory != nil {
		return t.clientFactory(provider, timeout, provider.Proxy)
	}
	return imagegen.NewClient(provider, timeout, provider.Proxy)
}

func (t *OpenAIImageGenerateTool) buildMetadata(selection *agentconfig.ImagesGenerationsSelection, req *imagegen.GenerateRequest, outputDir string, images []imagegen.SavedImage) map[string]interface{} {
	metadata := map[string]interface{}{
		"model":                        strings.TrimSpace(req.Model),
		"output_dir":                   strings.TrimSpace(outputDir),
		"requested_n":                  req.N,
		"generated_count":              len(images),
		llm.MetadataKeyGeneratedImages: toGeneratedImageMetadata(images),
	}
	if selection != nil {
		metadata["provider"] = strings.TrimSpace(selection.ProviderName)
	}
	if len(images) > 0 {
		metadata["saved_path"] = images[0].SavedPath
	}
	if req.OutputFormat != "" {
		metadata["output_format"] = req.OutputFormat
	}
	if req.Size != "" {
		metadata["size"] = req.Size
	}
	if req.Quality != "" {
		metadata["quality"] = req.Quality
	}
	if req.Background != "" {
		metadata["background"] = req.Background
	}
	if req.Moderation != "" {
		metadata["moderation"] = req.Moderation
	}
	return metadata
}

func toGeneratedImageMetadata(images []imagegen.SavedImage) []map[string]interface{} {
	if len(images) == 0 {
		return []map[string]interface{}{}
	}
	out := make([]map[string]interface{}, 0, len(images))
	for _, image := range images {
		out = append(out, map[string]interface{}{
			"id":             image.ID,
			"status":         image.Status,
			"revised_prompt": image.RevisedPrompt,
			"mime_type":      image.MimeType,
			"saved_path":     image.SavedPath,
			"sha256":         image.SHA256,
			"byte_count":     image.ByteCount,
		})
	}
	return out
}

func failureToolResult(err error) *toolkit.ToolResult {
	if err == nil {
		err = fmt.Errorf("image generation failed")
	}
	return &toolkit.ToolResult{
		Success:    false,
		OutputKind: toolresult.KindText,
		Content:    err.Error(),
		Error:      err,
	}
}

func stringParam(params map[string]interface{}, key string) (string, bool) {
	if params == nil {
		return "", false
	}
	value, ok := params[key]
	if !ok {
		return "", false
	}
	switch typed := value.(type) {
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" || strings.EqualFold(trimmed, "null") {
			return "", false
		}
		return trimmed, true
	case fmt.Stringer:
		trimmed := strings.TrimSpace(typed.String())
		if trimmed == "" || strings.EqualFold(trimmed, "null") {
			return "", false
		}
		return trimmed, true
	default:
		trimmed := strings.TrimSpace(fmt.Sprint(value))
		if trimmed == "" || trimmed == "<nil>" || strings.EqualFold(trimmed, "null") {
			return "", false
		}
		return trimmed, true
	}
}

func intParam(params map[string]interface{}, key string) (int, bool) {
	if params == nil {
		return 0, false
	}
	value, ok := params[key]
	if !ok || value == nil {
		return 0, false
	}
	switch typed := value.(type) {
	case int:
		return typed, true
	case int8:
		return int(typed), true
	case int16:
		return int(typed), true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float32:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		number, err := typed.Int64()
		if err == nil {
			return int(number), true
		}
		f, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		return int(f), true
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return 0, false
		}
		number, err := strconv.Atoi(trimmed)
		if err != nil {
			return 0, false
		}
		return number, true
	default:
		return 0, false
	}
}
