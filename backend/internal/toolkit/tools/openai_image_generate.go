package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/imagegen"
	runtimehttpclient "github.com/wwsheng009/ai-agent-runtime/internal/pkg/httpclient"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolnames"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// OpenAIImageGenerateTool performs an OpenAI-compatible image generations request and
// saves the resulting images into the active session artifact directory.
type OpenAIImageGenerateTool struct {
	*toolkit.BaseTool
	runtimeConfig          *runtimecfg.RuntimeConfig
	providerConfigResolver func() *agentconfig.Config
	clientFactory          func(provider agentconfig.Provider, timeout time.Duration, proxy *agentconfig.ProxyConfig) imagegen.Generator
}

type openAIImageGenerateDebug struct {
	enabled bool
	writer  io.Writer
}

type imageGeneratePath string

const (
	imageGeneratePathAuto        imageGeneratePath = "auto"
	imageGeneratePathAPI         imageGeneratePath = "images_generations_api"
	imageGeneratePathCodexNative imageGeneratePath = "codex_native"
)

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
		"provider": map[string]interface{}{
			"type":        "string",
			"description": "指定使用的图像生成 provider 名称（如 OPENAI_IMAGE、SENSENOVA_IMAGE）。不指定时自动选择。",
		},
		"path": map[string]interface{}{
			"type":        "string",
			"description": "图片生成路径：auto 优先使用 /v1/images/generations，必要时回退 Codex 原生；images_generations_api 强制 Path A；codex_native 强制 Path B。",
			"enum":        []string{"auto", "images_generations_api", "api", "codex_native", "native"},
			"default":     "auto",
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
// When multiple providers are available and the primary one fails, Execute
// automatically falls through to the next candidate (failover).
func (t *OpenAIImageGenerateTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	debug := newOpenAIImageGenerateDebug(params)
	req, explicitModel, explicitProvider := t.buildRequest(params)
	if req == nil {
		debug.printf("request_build_failed")
		return failureToolResult(fmt.Errorf("image generation request could not be constructed")), nil
	}
	debug.printf(
		"request prompt_chars=%d explicit_provider=%t provider=%q explicit_model=%t model=%q",
		len([]rune(strings.TrimSpace(req.Prompt))),
		explicitProvider,
		strings.TrimSpace(req.Provider),
		explicitModel,
		strings.TrimSpace(req.Model),
	)

	providerCfg := t.resolveProviderConfig()
	if providerCfg == nil {
		debug.printf("provider_config unavailable")
		return failureToolResult(fmt.Errorf("image generations provider configuration is unavailable")), nil
	}
	debug.printf("provider_config providers=%d", len(providerCfg.Providers.Items))

	path, pathErr := imageGeneratePathParam(params)
	if pathErr != nil {
		debug.printf("path_invalid error=%q", pathErr.Error())
		return failureToolResult(pathErr), nil
	}
	timeout := t.resolveRequestTimeout()
	outputDir := toolctx.GeneratedImageOutputDir(ctx)
	if strings.TrimSpace(outputDir) == "" {
		debug.printf("output_dir unavailable")
		return failureToolResult(fmt.Errorf("generated image output dir is unavailable")), nil
	}
	debug.printf("path=%q output_dir=%q request_timeout=%s max_n=%d", path, outputDir, timeout, t.resolveMaxN())
	if path == imageGeneratePathCodexNative {
		return t.executeCodexNative(ctx, providerCfg, req, explicitModel, explicitProvider, outputDir, timeout, debug), nil
	}

	debug.printf(
		"select requested_provider=%q requested_model=%q default_model=%q explicit_provider=%t explicit_model=%t",
		strings.TrimSpace(req.Provider),
		strings.TrimSpace(req.Model),
		t.resolveDefaultModel(),
		explicitProvider,
		explicitModel,
	)
	candidates, err := t.selectProviders(providerCfg, req.Model, explicitModel, req.Provider, explicitProvider)
	if err != nil {
		if path == imageGeneratePathAuto {
			debug.printf("select_failed error=%q fallback_path=%q", err.Error(), imageGeneratePathCodexNative)
			return t.executeCodexNative(ctx, providerCfg, req, explicitModel, explicitProvider, outputDir, timeout, debug), nil
		}
		debug.printf("select_failed error=%q", err.Error())
		return failureToolResult(err), nil
	}
	debug.printf("candidates count=%d list=%s", len(candidates), formatImageGenerateCandidates(candidates))

	// Failover: try each candidate in order. Return on the first success.
	// If all candidates fail, return the last error.
	var lastErr error
	for i, selection := range candidates {
		attemptReq := *req
		attemptReq.Model = selection.Model
		t.applyRuntimeDefaults(&attemptReq)
		if maxN := t.resolveMaxN(); maxN > 0 && attemptReq.N > maxN {
			debug.printf("attempt=%d clamp_n requested=%d max=%d", i+1, attemptReq.N, maxN)
			attemptReq.N = maxN
		}

		imagegen.NormalizeGenerateRequest(&attemptReq)
		debug.printf(
			"attempt=%d/%d provider=%q model=%q api_url=%q timeout=%s proxy=%t",
			i+1,
			len(candidates),
			selection.ProviderName,
			attemptReq.Model,
			imageGenerateAPIURL(selection.Provider),
			timeout,
			selection.Provider.Proxy != nil,
		)
		debug.printf(
			"attempt=%d request n=%d size=%q quality=%q background=%q output_format=%q output_compression=%s",
			i+1,
			attemptReq.N,
			attemptReq.Size,
			attemptReq.Quality,
			attemptReq.Background,
			attemptReq.OutputFormat,
			formatOptionalInt(attemptReq.OutputCompression),
		)
		if vErr := imagegen.Validate(&attemptReq); vErr != nil {
			lastErr = vErr
			debug.printf("attempt=%d validation_failed error=%q", i+1, vErr.Error())
			continue
		}

		client := t.newClient(selection.Provider, timeout)
		debug.printf("attempt=%d api_call provider=%q model=%q", i+1, selection.ProviderName, attemptReq.Model)
		resp, genErr := client.Generate(ctx, &attemptReq)
		if genErr != nil {
			lastErr = fmt.Errorf("provider %s (model %s): %w", selection.ProviderName, selection.Model, genErr)
			debug.printf("attempt=%d api_error error=%q", i+1, lastErr.Error())
			// Skip failover if context is cancelled or deadline exceeded.
			if ctx.Err() != nil {
				debug.printf("attempt=%d context_done error=%q", i+1, ctx.Err().Error())
				return failureToolResult(lastErr), nil
			}
			continue
		}
		debug.printf("attempt=%d api_response created=%d items=%d", i+1, resp.Created, len(resp.Data))

		images := make([]imagegen.SavedImage, 0, len(resp.Data))
		for index, item := range resp.Data {
			var saved imagegen.SavedImage
			var saveErr error
			if item.HasB64JSON() {
				debug.printf("attempt=%d save index=%d source=b64_json output_format=%q", i+1, index+1, attemptReq.OutputFormat)
				saved, saveErr = imagegen.SaveBase64Image(outputDir, fmt.Sprintf("image_%d", index+1), item.B64JSON, attemptReq.OutputFormat)
			} else if item.HasURL() {
				debug.printf("attempt=%d save index=%d source=url output_format=%q", i+1, index+1, attemptReq.OutputFormat)
				downloadClient := runtimehttpclient.NewProviderHTTPClient(timeout, selection.Provider.Proxy, false)
				saved, saveErr = imagegen.SaveURLImage(ctx, outputDir, fmt.Sprintf("image_%d", index+1), item.URL, attemptReq.OutputFormat, downloadClient)
			} else {
				debug.printf("attempt=%d save_failed index=%d error=%q", i+1, index+1, "image response item has neither b64_json nor url")
				return failureToolResult(fmt.Errorf("image response item %d has neither b64_json nor url", index)), nil
			}
			if saveErr != nil {
				debug.printf("attempt=%d save_failed index=%d error=%q", i+1, index+1, saveErr.Error())
				return failureToolResult(saveErr), nil
			}
			saved.Status = "completed"
			saved.RevisedPrompt = strings.TrimSpace(item.RevisedPrompt)
			images = append(images, saved)
			debug.printf(
				"attempt=%d saved index=%d path=%q bytes=%d mime=%q sha256=%s",
				i+1,
				index+1,
				saved.SavedPath,
				saved.ByteCount,
				saved.MimeType,
				saved.SHA256,
			)
		}

		metadata := t.buildMetadata(selection, &attemptReq, outputDir, images)
		if len(candidates) > 1 {
			metadata["failover_attempt"] = i + 1
			metadata["failover_total"] = len(candidates)
		}
		debug.printf("success provider=%q model=%q generated_count=%d output_dir=%q", selection.ProviderName, attemptReq.Model, len(images), outputDir)

		return &toolkit.ToolResult{
			Success:    true,
			OutputKind: toolresult.KindStructured,
			Content:    llm.GeneratedImageSummary(images),
			Metadata:   metadata,
		}, nil
	}

	if lastErr != nil {
		debug.printf("failed last_error=%q", lastErr.Error())
	} else {
		debug.printf("failed")
	}
	return failureToolResult(lastErr), nil
}

func (t *OpenAIImageGenerateTool) executeCodexNative(
	ctx context.Context,
	providerCfg *agentconfig.Config,
	req *imagegen.GenerateRequest,
	explicitModel bool,
	explicitProvider bool,
	outputDir string,
	timeout time.Duration,
	debug openAIImageGenerateDebug,
) *toolkit.ToolResult {
	candidates, err := t.selectCodexNativeProviders(providerCfg, req.Model, explicitModel, req.Provider, explicitProvider)
	if err != nil {
		debug.printf("native_select_failed error=%q", err.Error())
		return failureToolResult(err)
	}
	debug.printf("native_candidates count=%d list=%s", len(candidates), formatCodexNativeImageCandidates(candidates))

	var lastErr error
	for i, selection := range candidates {
		attemptReq := *req
		attemptReq.Model = selection.Model
		imagegen.NormalizeGenerateRequest(&attemptReq)
		if maxN := t.resolveMaxN(); maxN > 0 && attemptReq.N > maxN {
			debug.printf("native_attempt=%d clamp_n requested=%d max=%d", i+1, attemptReq.N, maxN)
			attemptReq.N = maxN
		}
		if err := validateCodexNativeImageRequest(&attemptReq); err != nil {
			lastErr = err
			debug.printf("native_attempt=%d validation_failed error=%q", i+1, err.Error())
			continue
		}

		provider, err := t.newCodexNativeProvider(selection.Provider, timeout)
		if err != nil {
			lastErr = fmt.Errorf("provider %s (model %s): %w", selection.ProviderName, selection.Model, err)
			debug.printf("native_attempt=%d provider_build_error error=%q", i+1, lastErr.Error())
			continue
		}

		debug.printf(
			"native_attempt=%d/%d provider=%q model=%q timeout=%s",
			i+1,
			len(candidates),
			selection.ProviderName,
			attemptReq.Model,
			timeout,
		)
		resp, callErr := provider.Call(ctx, &llm.LLMRequest{
			Model:    attemptReq.Model,
			Messages: []runtimetypes.Message{{Role: "user", Content: codexNativeImagePrompt(&attemptReq)}},
			Stream:   true,
			Metadata: codexNativeImageMetadata(outputDir, &attemptReq),
		})
		if callErr != nil {
			lastErr = fmt.Errorf("provider %s (model %s): %w", selection.ProviderName, selection.Model, callErr)
			debug.printf("native_attempt=%d api_error error=%q", i+1, lastErr.Error())
			if ctx.Err() != nil {
				debug.printf("native_attempt=%d context_done error=%q", i+1, ctx.Err().Error())
				return failureToolResult(lastErr)
			}
			continue
		}

		metadata := t.buildCodexNativeMetadata(selection, &attemptReq, outputDir, resp)
		generatedCount := metadataInt(metadata, "generated_count")
		debug.printf("native_success provider=%q model=%q generated_count=%d output_dir=%q", selection.ProviderName, attemptReq.Model, generatedCount, outputDir)
		if generatedCount < 1 {
			lastErr = fmt.Errorf("codex native image_generation did not return generated images")
			debug.printf("native_attempt=%d no_images error=%q", i+1, lastErr.Error())
			continue
		}
		if len(candidates) > 1 {
			metadata["failover_attempt"] = i + 1
			metadata["failover_total"] = len(candidates)
		}

		content := ""
		if resp != nil {
			content = strings.TrimSpace(resp.Content)
		}
		if content == "" {
			content = firstGeneratedImageSummaryFromMetadata(metadata)
		}
		return &toolkit.ToolResult{
			Success:    true,
			OutputKind: toolresult.KindStructured,
			Content:    content,
			Metadata:   metadata,
		}
	}

	if lastErr != nil {
		debug.printf("native_failed last_error=%q", lastErr.Error())
	} else {
		debug.printf("native_failed")
	}
	return failureToolResult(lastErr)
}

func (t *OpenAIImageGenerateTool) CanDirectCall() bool { return true }

func (t *OpenAIImageGenerateTool) DefinitionMetadata() map[string]interface{} {
	return map[string]interface{}{
		"tool_class":     "image_generation",
		"upstream_path":  "/v1/images/generations",
		"provider_scope": "images_generations_api",
		"supported_paths": []string{
			string(imageGeneratePathAPI),
			string(imageGeneratePathCodexNative),
		},
		runtimetypes.ToolMetadataSupportsParallelKey: false,
	}
}

func (t *OpenAIImageGenerateTool) buildRequest(params map[string]interface{}) (*imagegen.GenerateRequest, bool, bool) {
	req := &imagegen.GenerateRequest{}
	if params == nil {
		return req, false, false
	}

	var explicitModel bool
	var explicitProvider bool
	if value, ok := stringParam(params, "prompt"); ok {
		req.Prompt = value
	}
	if value, ok := stringParam(params, "model"); ok {
		req.Model = value
		explicitModel = true
	}
	if value, ok := stringParam(params, "provider"); ok {
		req.Provider = value
		explicitProvider = true
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
	return req, explicitModel, explicitProvider
}

func (t *OpenAIImageGenerateTool) resolveProviderConfig() *agentconfig.Config {
	if t != nil && t.providerConfigResolver != nil {
		if cfg := t.providerConfigResolver(); cfg != nil {
			return cfg
		}
	}
	return agentconfig.GetGlobalConfig()
}

func (t *OpenAIImageGenerateTool) selectProviders(cfg *agentconfig.Config, requestedModel string, explicitModel bool, providerName string, explicitProvider bool) ([]*agentconfig.ImagesGenerationsSelection, error) {
	// Build ordered hint list for fallback selection.
	var hints []agentconfig.ImagesGenerationsHint

	if explicitProvider {
		// User explicitly specified a provider → constrain to that provider.
		hints = append(hints, agentconfig.ImagesGenerationsHint{
			ProviderName: strings.TrimSpace(providerName),
			Model:        strings.TrimSpace(requestedModel),
		})
	} else if explicitModel {
		// User explicitly specified a model → find all providers for it.
		hints = append(hints, agentconfig.ImagesGenerationsHint{Model: strings.TrimSpace(requestedModel)})
	} else {
		// No explicit selection → try default model first, then any available.
		defaultModel := strings.TrimSpace(t.resolveDefaultModel())
		if defaultModel != "" {
			hints = append(hints, agentconfig.ImagesGenerationsHint{Model: defaultModel})
		}
		hints = append(hints, agentconfig.ImagesGenerationsHint{})
	}

	// Try each hint, collect deduplicated results for failover.
	seen := make(map[string]bool)
	var results []*agentconfig.ImagesGenerationsSelection
	for _, h := range hints {
		candidates, err := agentconfig.SelectAllImagesGenerationsProviders(cfg, h)
		if err != nil {
			continue
		}
		for _, c := range candidates {
			key := c.ProviderName + "/" + c.Model
			if !seen[key] {
				seen[key] = true
				results = append(results, c)
			}
		}
	}

	if len(results) == 0 {
		return nil, agentconfig.ErrNoImagesGenerationsProvider
	}
	return results, nil
}

func (t *OpenAIImageGenerateTool) selectCodexNativeProviders(cfg *agentconfig.Config, requestedModel string, explicitModel bool, providerName string, explicitProvider bool) ([]*agentconfig.CodexNativeImageGenerationSelection, error) {
	var hints []agentconfig.CodexNativeImageGenerationHint

	if explicitProvider {
		hints = append(hints, agentconfig.CodexNativeImageGenerationHint{
			ProviderName: strings.TrimSpace(providerName),
			Model:        strings.TrimSpace(requestedModel),
		})
	} else if explicitModel {
		hints = append(hints, agentconfig.CodexNativeImageGenerationHint{Model: strings.TrimSpace(requestedModel)})
	} else {
		defaultModel := strings.TrimSpace(t.resolveDefaultModel())
		if defaultModel != "" {
			hints = append(hints, agentconfig.CodexNativeImageGenerationHint{Model: defaultModel})
		}
		hints = append(hints, agentconfig.CodexNativeImageGenerationHint{})
	}

	seen := make(map[string]bool)
	var results []*agentconfig.CodexNativeImageGenerationSelection
	for _, h := range hints {
		candidates, err := agentconfig.SelectAllCodexNativeImageGenerationProviders(cfg, h)
		if err != nil {
			continue
		}
		for _, c := range candidates {
			key := c.ProviderName + "/" + c.Model
			if !seen[key] {
				seen[key] = true
				results = append(results, c)
			}
		}
	}
	if len(results) == 0 {
		return nil, agentconfig.ErrNoCodexNativeImageGenerationProvider
	}
	return results, nil
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
	// Size, quality, and output_format defaults are GPT-Image-specific.
	// For non-GPT-Image models, leave them empty so the upstream API
	// applies its own defaults rather than receiving an incompatible value.
	if imagegen.IsGPTImageModel(req.Model) {
		if strings.TrimSpace(req.Size) == "" {
			req.Size = t.resolveDefaultSize()
		}
		if strings.TrimSpace(req.Quality) == "" {
			req.Quality = t.resolveDefaultQuality()
		}
		if strings.TrimSpace(req.OutputFormat) == "" {
			req.OutputFormat = t.resolveDefaultOutputFormat()
		}
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

func (t *OpenAIImageGenerateTool) newCodexNativeProvider(provider agentconfig.Provider, timeout time.Duration) (llm.Provider, error) {
	providerTimeout := provider.Timeout
	if timeout > 0 {
		providerTimeout = timeout
	}
	return llm.NewProvider(&llm.ProviderConfig{
		Type:                    provider.GetProtocol(),
		APIKey:                  provider.GetAPIKey(),
		BaseURL:                 provider.BaseURL,
		APIPath:                 provider.APIPath,
		Timeout:                 providerTimeout,
		MaxRetries:              3,
		DefaultModel:            strings.TrimSpace(provider.DefaultModel),
		SupportedModels:         append([]string(nil), provider.SupportedModels...),
		ModelMappings:           cloneStringStringMap(provider.ModelMappings),
		ModelCapabilities:       cloneModelCapabilityMap(provider.ModelCapabilities),
		Headers:                 cloneStringStringMap(provider.Headers),
		HeaderMappings:          cloneStringStringMap(provider.HeaderMappings),
		HeaderMappingRules:      cloneLLMHeaderMappingRules(provider.HeaderMappingRules),
		SupportsMaxOutputTokens: provider.SupportsMaxOutputTokens,
		Proxy:                   provider.Proxy.Clone(),
		RequestsPerMinute:       provider.RequestsPerMinute,
	})
}

func (t *OpenAIImageGenerateTool) buildMetadata(selection *agentconfig.ImagesGenerationsSelection, req *imagegen.GenerateRequest, outputDir string, images []imagegen.SavedImage) map[string]interface{} {
	metadata := map[string]interface{}{
		"image_generation_path":        string(imageGeneratePathAPI),
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

func (t *OpenAIImageGenerateTool) buildCodexNativeMetadata(selection *agentconfig.CodexNativeImageGenerationSelection, req *imagegen.GenerateRequest, outputDir string, resp *llm.LLMResponse) map[string]interface{} {
	metadata := map[string]interface{}{
		"image_generation_path":        string(imageGeneratePathCodexNative),
		"native_tool":                  toolnames.CodexNativeImageGenerationToolType,
		"model":                        strings.TrimSpace(req.Model),
		"output_dir":                   strings.TrimSpace(outputDir),
		"requested_n":                  req.N,
		llm.MetadataKeyGeneratedImages: []map[string]interface{}{},
	}
	if selection != nil {
		metadata["provider"] = strings.TrimSpace(selection.ProviderName)
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
	if resp != nil && len(resp.Metadata) > 0 {
		for key, value := range resp.Metadata {
			metadata[key] = value
		}
	}
	generated := generatedImagesMetadata(metadata[llm.MetadataKeyGeneratedImages])
	metadata[llm.MetadataKeyGeneratedImages] = generated
	metadata["generated_count"] = len(generated)
	if len(generated) > 0 {
		if savedPath := strings.TrimSpace(fmt.Sprint(generated[0]["saved_path"])); savedPath != "" && savedPath != "<nil>" {
			metadata["saved_path"] = savedPath
		}
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

func generatedImagesMetadata(raw interface{}) []map[string]interface{} {
	switch typed := raw.(type) {
	case []map[string]interface{}:
		return typed
	case []interface{}:
		out := make([]map[string]interface{}, 0, len(typed))
		for _, item := range typed {
			if entry, ok := item.(map[string]interface{}); ok {
				out = append(out, entry)
			}
		}
		return out
	default:
		return []map[string]interface{}{}
	}
}

func metadataInt(metadata map[string]interface{}, key string) int {
	if len(metadata) == 0 {
		return 0
	}
	return intFromAny(metadata[key])
}

func firstGeneratedImageSummaryFromMetadata(metadata map[string]interface{}) string {
	images := generatedImagesMetadata(metadata[llm.MetadataKeyGeneratedImages])
	if len(images) == 0 {
		return ""
	}
	savedPath := strings.TrimSpace(fmt.Sprint(images[0]["saved_path"]))
	if savedPath == "" || savedPath == "<nil>" {
		return ""
	}
	if len(images) == 1 {
		return fmt.Sprintf("Generated image saved to %s", savedPath)
	}
	return fmt.Sprintf("Generated %d images. First saved to %s", len(images), savedPath)
}

func cloneStringStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func cloneModelCapabilityMap(input map[string]agentconfig.ModelCapabilitySpec) map[string]agentconfig.ModelCapabilitySpec {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]agentconfig.ModelCapabilitySpec, len(input))
	for key, value := range input {
		cloned := value
		if len(value.InputModalities) > 0 {
			cloned.InputModalities = append([]string(nil), value.InputModalities...)
		}
		if len(value.ReasoningEfforts) > 0 {
			cloned.ReasoningEfforts = append([]string(nil), value.ReasoningEfforts...)
		}
		if len(value.ReasoningEffortBudgets) > 0 {
			cloned.ReasoningEffortBudgets = make(map[string]int, len(value.ReasoningEffortBudgets))
			for effort, budget := range value.ReasoningEffortBudgets {
				cloned.ReasoningEffortBudgets[effort] = budget
			}
		}
		output[key] = cloned
	}
	return output
}

func cloneLLMHeaderMappingRules(input []agentconfig.HeaderMappingRule) []llm.HeaderMappingRule {
	if len(input) == 0 {
		return nil
	}
	output := make([]llm.HeaderMappingRule, len(input))
	for i, rule := range input {
		output[i] = llm.HeaderMappingRule{
			Name:         rule.Name,
			Enabled:      rule.Enabled,
			Header:       rule.Header,
			TargetHeader: rule.TargetHeader,
			MatchType:    rule.MatchType,
			Match:        rule.Match,
			Value:        rule.Value,
		}
	}
	return output
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

func intFromAny(value interface{}) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int8:
		return int(typed)
	case int16:
		return int(typed)
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case float32:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		number, err := typed.Int64()
		if err == nil {
			return int(number)
		}
		f, err := typed.Float64()
		if err != nil {
			return 0
		}
		return int(f)
	case string:
		number, err := strconv.Atoi(strings.TrimSpace(typed))
		if err == nil {
			return number
		}
	}
	return 0
}

func newOpenAIImageGenerateDebug(params map[string]interface{}) openAIImageGenerateDebug {
	if !boolParam(params, "debug") {
		return openAIImageGenerateDebug{}
	}
	writer := io.Writer(os.Stderr)
	if raw, ok := params["_debug_writer"]; ok {
		if typed, ok := raw.(io.Writer); ok && typed != nil {
			writer = typed
		}
	}
	return openAIImageGenerateDebug{enabled: true, writer: writer}
}

func imageGeneratePathParam(params map[string]interface{}) (imageGeneratePath, error) {
	value, ok := stringParam(params, "path")
	if !ok {
		return imageGeneratePathAuto, nil
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "auto":
		return imageGeneratePathAuto, nil
	case "api", "images_generations_api", "images-generations-api", "path_a":
		return imageGeneratePathAPI, nil
	case "native", "codex_native", "codex-native", "path_b":
		return imageGeneratePathCodexNative, nil
	default:
		return imageGeneratePathAuto, fmt.Errorf("path must be one of auto, images_generations_api/api, or codex_native/native")
	}
}

func validateCodexNativeImageRequest(req *imagegen.GenerateRequest) error {
	if req == nil {
		return fmt.Errorf("generate request is nil")
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	req.Model = strings.TrimSpace(req.Model)
	req.Size = strings.TrimSpace(req.Size)
	req.Quality = strings.ToLower(strings.TrimSpace(req.Quality))
	req.Background = strings.ToLower(strings.TrimSpace(req.Background))
	req.OutputFormat = strings.ToLower(strings.TrimSpace(req.OutputFormat))
	if req.OutputFormat == "jpg" {
		req.OutputFormat = "jpeg"
	}
	if req.Prompt == "" {
		return fmt.Errorf("prompt cannot be empty")
	}
	if req.Model == "" {
		return fmt.Errorf("model cannot be empty")
	}
	if req.N < 1 || req.N > 10 {
		return fmt.Errorf("n must be between 1 and 10")
	}
	if req.Quality != "" {
		if _, ok := imagegen.AllowedQualities[req.Quality]; !ok {
			return fmt.Errorf("quality must be one of low, medium, high, or auto")
		}
	}
	if _, ok := imagegen.AllowedBackgrounds[req.Background]; !ok {
		return fmt.Errorf("background must be one of transparent, opaque, or auto")
	}
	if req.OutputFormat != "" {
		if _, ok := imagegen.AllowedOutputFormats[req.OutputFormat]; !ok {
			return fmt.Errorf("output_format must be png, jpeg, or webp")
		}
	}
	if req.OutputCompression != nil && (*req.OutputCompression < 0 || *req.OutputCompression > 100) {
		return fmt.Errorf("output_compression must be between 0 and 100")
	}
	return nil
}

func codexNativeImagePrompt(req *imagegen.GenerateRequest) string {
	if req == nil {
		return ""
	}
	prompt := strings.TrimSpace(req.Prompt)
	if req.N <= 1 {
		return prompt
	}
	return fmt.Sprintf("Generate exactly %d images for this request.\n\n%s", req.N, prompt)
}

func codexNativeImageMetadata(outputDir string, req *imagegen.GenerateRequest) map[string]interface{} {
	metadata := map[string]interface{}{
		llm.MetadataKeyGeneratedImageOutputDir: outputDir,
		"tool_choice": map[string]interface{}{
			"type": toolnames.CodexNativeImageGenerationToolType,
		},
	}
	if options := codexNativeImageOptions(req); options != nil {
		metadata[llm.MetadataKeyCodexImageGenerationOptions] = options
	}
	return metadata
}

func codexNativeImageOptions(req *imagegen.GenerateRequest) *llm.CodexImageGenerationOptions {
	if req == nil {
		return nil
	}
	options := &llm.CodexImageGenerationOptions{
		Size:         strings.TrimSpace(req.Size),
		Quality:      strings.TrimSpace(req.Quality),
		Background:   strings.TrimSpace(req.Background),
		OutputFormat: strings.TrimSpace(req.OutputFormat),
	}
	if req.OutputCompression != nil {
		value := *req.OutputCompression
		options.OutputCompression = &value
	}
	if options.Size == "" && options.Quality == "" && options.Background == "" && options.OutputFormat == "" && options.OutputCompression == nil {
		return nil
	}
	return options
}

func (d openAIImageGenerateDebug) printf(format string, args ...interface{}) {
	if !d.enabled {
		return
	}
	writer := d.writer
	if writer == nil {
		writer = os.Stderr
	}
	fmt.Fprintf(writer, "[image-debug/tool] "+format+"\n", args...)
}

func boolParam(params map[string]interface{}, key string) bool {
	if params == nil {
		return false
	}
	value, ok := params[key]
	if !ok || value == nil {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return parseDebugBool(typed)
	case fmt.Stringer:
		return parseDebugBool(typed.String())
	default:
		return parseDebugBool(fmt.Sprint(value))
	}
}

func parseDebugBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "t", "true", "y", "yes", "on":
		return true
	default:
		return false
	}
}

func formatImageGenerateCandidates(candidates []*agentconfig.ImagesGenerationsSelection) string {
	if len(candidates) == 0 {
		return "<none>"
	}
	parts := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate == nil {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s/%s", strings.TrimSpace(candidate.ProviderName), strings.TrimSpace(candidate.Model)))
	}
	if len(parts) == 0 {
		return "<none>"
	}
	return strings.Join(parts, ",")
}

func formatCodexNativeImageCandidates(candidates []*agentconfig.CodexNativeImageGenerationSelection) string {
	if len(candidates) == 0 {
		return "<none>"
	}
	parts := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate == nil {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s/%s", strings.TrimSpace(candidate.ProviderName), strings.TrimSpace(candidate.Model)))
	}
	if len(parts) == 0 {
		return "<none>"
	}
	return strings.Join(parts, ",")
}

func imageGenerateAPIURL(provider agentconfig.Provider) string {
	apiPath := strings.TrimSpace(provider.ForwardURL)
	if apiPath == "" {
		apiPath = strings.TrimSpace(provider.APIPath)
	}
	if apiPath == "" {
		apiPath = "/v1/images/generations"
	}
	return agentconfig.JoinBaseURLAndPath(strings.TrimRight(strings.TrimSpace(provider.BaseURL), "/"), apiPath)
}

func formatOptionalInt(value *int) string {
	if value == nil {
		return "<nil>"
	}
	return strconv.Itoa(*value)
}
