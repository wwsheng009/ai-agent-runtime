package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/imagegen"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolnames"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

type mockImageGenerator struct {
	calls    int32
	lastReq  *imagegen.GenerateRequest
	response *imagegen.GenerateResponse
	err      error
}

func (m *mockImageGenerator) Generate(ctx context.Context, req *imagegen.GenerateRequest) (*imagegen.GenerateResponse, error) {
	atomic.AddInt32(&m.calls, 1)
	if req != nil {
		copied := *req
		if req.OutputCompression != nil {
			value := *req.OutputCompression
			copied.OutputCompression = &value
		}
		m.lastReq = &copied
	}
	if m.err != nil {
		return nil, m.err
	}
	if m.response != nil {
		return m.response, nil
	}
	return &imagegen.GenerateResponse{
		Created: int(time.Now().Unix()),
		Data: []imagegen.GenerateResponseItem{
			{B64JSON: base64.StdEncoding.EncodeToString([]byte("image-bytes-1")), RevisedPrompt: "revised one"},
		},
	}, nil
}

func TestOpenAIImageGenerateTool_CodexNativePathSavesImageAndForcesToolChoice(t *testing.T) {
	var capturedBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/v1/responses", r.URL.Path)
		require.Equal(t, "Bearer codex-key", r.Header.Get("Authorization"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&capturedBody))
		w.Header().Set("Content-Type", "text/event-stream")
		result := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVQIHWP4////fwAJ+wP9KobjigAAAABJRU5ErkJggg=="
		fmt.Fprintf(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_image\",\"model\":\"gpt-5.4\"}}\n\n")
		fmt.Fprintf(w, "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"image_generation_call\",\"id\":\"img:1\",\"status\":\"in_progress\",\"revised_prompt\":\"tiny robot\"}}\n\n")
		fmt.Fprintf(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"image_generation_call\",\"id\":\"img:1\",\"status\":\"completed\",\"revised_prompt\":\"tiny robot\",\"output_format\":\"png\",\"result\":%q}}\n\n", result)
		fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_image\",\"status\":\"completed\",\"stop_reason\":\"end_turn\",\"usage\":{\"input_tokens\":10,\"output_tokens\":5,\"total_tokens\":15}}}\n\n")
	}))
	defer server.Close()

	outputDir := t.TempDir()
	tool := NewOpenAIImageGenerateTool(&runtimecfg.RuntimeConfig{
		Images: runtimecfg.ImagesConfig{
			Generations: runtimecfg.ImagesGenerationsConfig{
				DefaultModel:        "gpt-image-2",
				DefaultOutputFormat: "png",
				RequestTimeout:      time.Second,
				MaxN:                4,
			},
		},
	})
	tool.providerConfigResolver = func() *agentconfig.Config {
		return &agentconfig.Config{
			Providers: agentconfig.ProvidersConfig{
				Items: map[string]agentconfig.Provider{
					"CODEX_NATIVE": {
						Enabled:         true,
						Protocol:        "codex",
						BaseURL:         server.URL,
						APIKey:          "codex-key",
						DefaultModel:    "gpt-5.4",
						SupportedModels: []string{"gpt-5.4"},
						ModelCapabilities: map[string]agentconfig.ModelCapabilitySpec{
							"gpt-5.4": {
								InputModalities: []string{"text", "image"},
								NativeTools: agentconfig.NativeToolCapabilities{
									ImageGeneration: true,
								},
							},
						},
					},
				},
			},
		}
	}
	tool.clientFactory = func(provider agentconfig.Provider, timeout time.Duration, proxy *agentconfig.ProxyConfig) imagegen.Generator {
		t.Fatalf("Path B should not use /v1/images/generations client")
		return nil
	}

	ctx := toolctx.WithGeneratedImageOutputDir(context.Background(), outputDir)
	result, err := tool.Execute(ctx, map[string]interface{}{
		"prompt":        "draw a tiny robot",
		"provider":      "CODEX_NATIVE",
		"model":         "gpt-5.4",
		"path":          "codex_native",
		"size":          "1024x1024",
		"quality":       "high",
		"output_format": "png",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Success)
	require.Equal(t, string(imageGeneratePathCodexNative), result.Metadata["image_generation_path"])
	require.Equal(t, "CODEX_NATIVE", result.Metadata["provider"])
	require.Equal(t, "gpt-5.4", result.Metadata["model"])
	require.Equal(t, 1, result.Metadata["generated_count"])
	require.Contains(t, result.Content, "Generated image saved to")

	generated := generatedImagesMetadata(result.Metadata[llm.MetadataKeyGeneratedImages])
	require.Len(t, generated, 1)
	require.FileExists(t, generated[0]["saved_path"].(string))

	require.Equal(t, true, capturedBody["stream"])
	choice, ok := capturedBody["tool_choice"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "image_generation", choice["type"])
	require.Equal(t, false, capturedBody["parallel_tool_calls"])
	tools, ok := capturedBody["tools"].([]interface{})
	require.True(t, ok)
	var sawNative bool
	for _, raw := range tools {
		toolMap, ok := raw.(map[string]interface{})
		require.True(t, ok)
		if toolMap["type"] == "image_generation" {
			sawNative = true
			require.Equal(t, "png", toolMap["output_format"])
			require.Equal(t, "1024x1024", toolMap["size"])
			require.Equal(t, "high", toolMap["quality"])
		}
	}
	require.True(t, sawNative)
}

func TestOpenAIImageGenerateTool_ExecuteUsesRuntimeDefaultsAndPersistsImages(t *testing.T) {
	outputDir := t.TempDir()
	runtimeConfig := &runtimecfg.RuntimeConfig{
		Images: runtimecfg.ImagesConfig{
			Generations: runtimecfg.ImagesGenerationsConfig{
				DefaultModel:        "gpt-image-2",
				DefaultSize:         "1536x1024",
				DefaultQuality:      "high",
				DefaultOutputFormat: "png",
				RequestTimeout:      2 * time.Second,
				MaxN:                2,
			},
		},
	}

	mock := &mockImageGenerator{
		response: &imagegen.GenerateResponse{
			Created: 123,
			Data: []imagegen.GenerateResponseItem{
				{B64JSON: base64.StdEncoding.EncodeToString([]byte("image-bytes-1")), RevisedPrompt: "revised one"},
				{B64JSON: base64.StdEncoding.EncodeToString([]byte("image-bytes-2")), RevisedPrompt: "revised two"},
			},
		},
	}

	var capturedProvider agentconfig.Provider
	var capturedTimeout time.Duration
	var capturedProxy *agentconfig.ProxyConfig

	tool := NewOpenAIImageGenerateTool(runtimeConfig)
	tool.providerConfigResolver = func() *agentconfig.Config {
		return &agentconfig.Config{
			Providers: agentconfig.ProvidersConfig{
				Items: map[string]agentconfig.Provider{
					"openai_image": {
						Enabled:      true,
						Type:         "openai",
						BaseURL:      "https://api.openai.com",
						APIKey:       "test-key",
						DefaultModel: "gpt-image-2",
						SupportedModels: []string{
							"gpt-image-2",
						},
						ModelCapabilities: map[string]agentconfig.ModelCapabilitySpec{
							"gpt-image-2": {
								NativeTools: agentconfig.NativeToolCapabilities{ImagesGenerationsAPI: true},
							},
						},
					},
				},
			},
		}
	}
	tool.clientFactory = func(provider agentconfig.Provider, timeout time.Duration, proxy *agentconfig.ProxyConfig) imagegen.Generator {
		capturedProvider = provider
		capturedTimeout = timeout
		capturedProxy = proxy
		return mock
	}

	require.Equal(t, toolnames.OpenAIImageGenerateToolName, tool.Name())

	ctx := toolctx.WithGeneratedImageOutputDir(context.Background(), outputDir)
	result, err := tool.Execute(ctx, map[string]interface{}{
		"prompt": "  draw a robot  ",
		"n":      3,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Success)
	require.Equal(t, toolresult.KindStructured, result.NormalizedOutputKind())
	require.Equal(t, 1, int(atomic.LoadInt32(&mock.calls)))
	require.Equal(t, "gpt-image-2", mock.lastReq.Model)
	require.Equal(t, "draw a robot", mock.lastReq.Prompt)
	require.Equal(t, 2, mock.lastReq.N)
	require.Equal(t, "1536x1024", mock.lastReq.Size)
	require.Equal(t, "high", mock.lastReq.Quality)
	require.Equal(t, "png", mock.lastReq.OutputFormat)
	require.Equal(t, "gpt-image-2", capturedProvider.DefaultModel)
	require.Equal(t, "https://api.openai.com", capturedProvider.BaseURL)
	require.Equal(t, 2*time.Second, capturedTimeout)
	require.Nil(t, capturedProxy)

	require.Equal(t, "gpt-image-2", result.Metadata["model"])
	require.Equal(t, outputDir, result.Metadata["output_dir"])
	require.Equal(t, 2, result.Metadata["requested_n"])
	require.Equal(t, 2, result.Metadata["generated_count"])
	require.Equal(t, "openai_image", result.Metadata["provider"])
	require.Equal(t, "png", result.Metadata["output_format"])
	require.Equal(t, "1536x1024", result.Metadata["size"])
	require.Equal(t, "high", result.Metadata["quality"])
	require.Contains(t, result.Content, "Generated 2 images")

	generatedRaw, ok := result.Metadata[llm.MetadataKeyGeneratedImages]
	require.True(t, ok)
	generated, ok := generatedRaw.([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, generated, 2)
	require.Equal(t, "image_1", generated[0]["id"])
	require.Equal(t, "completed", generated[0]["status"])
	require.Equal(t, "revised one", generated[0]["revised_prompt"])
	require.Equal(t, "image/png", generated[0]["mime_type"])
	require.NotEmpty(t, generated[0]["saved_path"])
	require.NotEmpty(t, generated[0]["sha256"])
	require.Equal(t, 13, generated[0]["byte_count"])
	require.Equal(t, generated[0]["saved_path"], result.Metadata["saved_path"])

	for _, entry := range generated {
		savedPath, _ := entry["saved_path"].(string)
		require.NotEmpty(t, savedPath)
		require.FileExists(t, savedPath)
	}
}

func TestOpenAIImageGenerateTool_ExecuteHonorsExplicitModelSelection(t *testing.T) {
	outputDir := t.TempDir()
	runtimeConfig := &runtimecfg.RuntimeConfig{
		Images: runtimecfg.ImagesConfig{
			Generations: runtimecfg.ImagesGenerationsConfig{
				DefaultModel:        "gpt-image-2",
				DefaultSize:         "1024x1024",
				DefaultQuality:      "medium",
				DefaultOutputFormat: "png",
				RequestTimeout:      time.Second,
				MaxN:                4,
			},
		},
	}

	mock := &mockImageGenerator{
		response: &imagegen.GenerateResponse{
			Created: 123,
			Data: []imagegen.GenerateResponseItem{
				{B64JSON: base64.StdEncoding.EncodeToString([]byte("image-bytes")), RevisedPrompt: "revised"},
			},
		},
	}

	tool := NewOpenAIImageGenerateTool(runtimeConfig)
	tool.providerConfigResolver = func() *agentconfig.Config {
		return &agentconfig.Config{
			Providers: agentconfig.ProvidersConfig{
				Items: map[string]agentconfig.Provider{
					"openai_image": {
						Enabled:      true,
						Type:         "openai",
						BaseURL:      "https://api.openai.com",
						APIKey:       "test-key",
						DefaultModel: "gpt-image-2",
						SupportedModels: []string{
							"gpt-image-1.5",
							"gpt-image-2",
						},
						ModelCapabilities: map[string]agentconfig.ModelCapabilitySpec{
							"gpt-image-1.5": {
								NativeTools: agentconfig.NativeToolCapabilities{ImagesGenerationsAPI: true},
							},
							"gpt-image-2": {
								NativeTools: agentconfig.NativeToolCapabilities{ImagesGenerationsAPI: true},
							},
						},
					},
				},
			},
		}
	}
	tool.clientFactory = func(provider agentconfig.Provider, timeout time.Duration, proxy *agentconfig.ProxyConfig) imagegen.Generator {
		return mock
	}

	ctx := toolctx.WithGeneratedImageOutputDir(context.Background(), outputDir)
	result, err := tool.Execute(ctx, map[string]interface{}{
		"prompt": "draw a robot",
		"model":  "gpt-image-1.5",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Success)
	require.Equal(t, "gpt-image-1.5", mock.lastReq.Model)
	require.Equal(t, "gpt-image-1.5", result.Metadata["model"])
	generatedRaw, ok := result.Metadata[llm.MetadataKeyGeneratedImages]
	require.True(t, ok)
	generated, ok := generatedRaw.([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, generated, 1)
	require.Equal(t, "image_1", generated[0]["id"])
	require.Contains(t, result.Content, "Generated image saved to")
}

func TestOpenAIImageGenerateTool_ExecuteIgnoresNullModelString(t *testing.T) {
	outputDir := t.TempDir()
	runtimeConfig := &runtimecfg.RuntimeConfig{
		Images: runtimecfg.ImagesConfig{
			Generations: runtimecfg.ImagesGenerationsConfig{
				DefaultModel:        "gpt-image-2",
				DefaultSize:         "1024x1024",
				DefaultQuality:      "medium",
				DefaultOutputFormat: "png",
				RequestTimeout:      time.Second,
				MaxN:                4,
			},
		},
	}

	mock := &mockImageGenerator{
		response: &imagegen.GenerateResponse{
			Created: 123,
			Data: []imagegen.GenerateResponseItem{
				{B64JSON: base64.StdEncoding.EncodeToString([]byte("image-bytes")), RevisedPrompt: "revised"},
			},
		},
	}

	tool := NewOpenAIImageGenerateTool(runtimeConfig)
	tool.providerConfigResolver = func() *agentconfig.Config {
		return &agentconfig.Config{
			Providers: agentconfig.ProvidersConfig{
				Items: map[string]agentconfig.Provider{
					"openai_image": {
						Enabled:      true,
						Type:         "openai",
						BaseURL:      "https://api.openai.com",
						APIKey:       "test-key",
						DefaultModel: "gpt-image-2",
						SupportedModels: []string{
							"gpt-image-2",
						},
						ModelCapabilities: map[string]agentconfig.ModelCapabilitySpec{
							"gpt-image-2": {
								NativeTools: agentconfig.NativeToolCapabilities{ImagesGenerationsAPI: true},
							},
						},
					},
				},
			},
		}
	}
	tool.clientFactory = func(provider agentconfig.Provider, timeout time.Duration, proxy *agentconfig.ProxyConfig) imagegen.Generator {
		return mock
	}

	ctx := toolctx.WithGeneratedImageOutputDir(context.Background(), outputDir)
	result, err := tool.Execute(ctx, map[string]interface{}{
		"prompt": "draw a robot",
		"model":  "null",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Success)
	require.Equal(t, "gpt-image-2", mock.lastReq.Model)
	require.Equal(t, "gpt-image-2", result.Metadata["model"])
	generatedRaw, ok := result.Metadata[llm.MetadataKeyGeneratedImages]
	require.True(t, ok)
	generated, ok := generatedRaw.([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, generated, 1)
	require.Equal(t, "image_1", generated[0]["id"])
	require.Contains(t, result.Content, "Generated image saved to")
}

func TestOpenAIImageGenerateTool_FailoverToSecondProvider(t *testing.T) {
	outputDir := t.TempDir()
	runtimeConfig := &runtimecfg.RuntimeConfig{
		Images: runtimecfg.ImagesConfig{
			Generations: runtimecfg.ImagesGenerationsConfig{
				DefaultModel:        "gpt-image-2",
				DefaultSize:         "1024x1024",
				DefaultQuality:      "medium",
				DefaultOutputFormat: "png",
				RequestTimeout:      time.Second,
				MaxN:                4,
			},
		},
	}

	// First provider fails, second succeeds.
	failMock := &mockImageGenerator{
		err: fmt.Errorf("provider unavailable"),
	}
	successMock := &mockImageGenerator{
		response: &imagegen.GenerateResponse{
			Created: 456,
			Data: []imagegen.GenerateResponseItem{
				{B64JSON: base64.StdEncoding.EncodeToString([]byte("failover-image")), RevisedPrompt: "failover"},
			},
		},
	}

	var callCount int32
	tool := NewOpenAIImageGenerateTool(runtimeConfig)
	tool.providerConfigResolver = func() *agentconfig.Config {
		return &agentconfig.Config{
			Providers: agentconfig.ProvidersConfig{
				Items: map[string]agentconfig.Provider{
					"OPENAI_IMAGE": {
						Enabled:      true,
						Type:         "openai",
						BaseURL:      "https://api.openai.com",
						APIKey:       "openai-key",
						DefaultModel: "gpt-image-2",
						SupportedModels: []string{
							"gpt-image-2",
						},
						ModelCapabilities: map[string]agentconfig.ModelCapabilitySpec{
							"gpt-image-2": {
								NativeTools: agentconfig.NativeToolCapabilities{ImagesGenerationsAPI: true},
							},
						},
					},
					"SENSENOVA_IMAGE": {
						Enabled:      true,
						Type:         "openai",
						BaseURL:      "https://token.sensenova.cn",
						APIKey:       "sensenova-key",
						DefaultModel: "sensenova-u1-fast",
						SupportedModels: []string{
							"sensenova-u1-fast",
						},
						ModelCapabilities: map[string]agentconfig.ModelCapabilitySpec{
							"sensenova-u1-fast": {
								NativeTools: agentconfig.NativeToolCapabilities{ImagesGenerationsAPI: true},
							},
						},
					},
				},
			},
		}
	}
	tool.clientFactory = func(provider agentconfig.Provider, timeout time.Duration, proxy *agentconfig.ProxyConfig) imagegen.Generator {
		idx := atomic.AddInt32(&callCount, 1)
		if idx == 1 {
			return failMock
		}
		return successMock
	}

	ctx := toolctx.WithGeneratedImageOutputDir(context.Background(), outputDir)
	result, err := tool.Execute(ctx, map[string]interface{}{
		"prompt": "draw a sunset",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Success)
	// The second provider's model should be used.
	require.Equal(t, "sensenova-u1-fast", successMock.lastReq.Model)
	require.Empty(t, successMock.lastReq.Size)
	require.Empty(t, successMock.lastReq.Quality)
	require.Empty(t, successMock.lastReq.OutputFormat)
	require.Equal(t, "sensenova-u1-fast", result.Metadata["model"])
	require.Equal(t, "SENSENOVA_IMAGE", result.Metadata["provider"])
	// Failover metadata should be present.
	require.Equal(t, 2, result.Metadata["failover_attempt"])
	require.Equal(t, 2, result.Metadata["failover_total"])
	require.Contains(t, result.Content, "Generated image saved to")
}

func TestOpenAIImageGenerateTool_ExplicitProviderSelection(t *testing.T) {
	outputDir := t.TempDir()
	runtimeConfig := &runtimecfg.RuntimeConfig{
		Images: runtimecfg.ImagesConfig{
			Generations: runtimecfg.ImagesGenerationsConfig{
				DefaultModel:        "gpt-image-2",
				DefaultSize:         "1024x1024",
				DefaultQuality:      "medium",
				DefaultOutputFormat: "png",
				RequestTimeout:      time.Second,
				MaxN:                4,
			},
		},
	}

	mock := &mockImageGenerator{
		response: &imagegen.GenerateResponse{
			Created: 789,
			Data: []imagegen.GenerateResponseItem{
				{B64JSON: base64.StdEncoding.EncodeToString([]byte("sensenova-image")), RevisedPrompt: "sensenova"},
			},
		},
	}

	var capturedProvider agentconfig.Provider
	tool := NewOpenAIImageGenerateTool(runtimeConfig)
	tool.providerConfigResolver = func() *agentconfig.Config {
		return &agentconfig.Config{
			Providers: agentconfig.ProvidersConfig{
				Items: map[string]agentconfig.Provider{
					"OPENAI_IMAGE": {
						Enabled:         true,
						BaseURL:         "https://api.openai.com",
						APIKey:          "openai-key",
						DefaultModel:    "gpt-image-2",
						SupportedModels: []string{"gpt-image-2"},
						ModelCapabilities: map[string]agentconfig.ModelCapabilitySpec{
							"gpt-image-2": {
								NativeTools: agentconfig.NativeToolCapabilities{ImagesGenerationsAPI: true},
							},
						},
					},
					"SENSENOVA_IMAGE": {
						Enabled:         true,
						BaseURL:         "https://token.sensenova.cn",
						APIKey:          "sensenova-key",
						DefaultModel:    "sensenova-u1-fast",
						SupportedModels: []string{"sensenova-u1-fast"},
						ModelCapabilities: map[string]agentconfig.ModelCapabilitySpec{
							"sensenova-u1-fast": {
								NativeTools: agentconfig.NativeToolCapabilities{ImagesGenerationsAPI: true},
							},
						},
					},
				},
			},
		}
	}
	tool.clientFactory = func(provider agentconfig.Provider, timeout time.Duration, proxy *agentconfig.ProxyConfig) imagegen.Generator {
		capturedProvider = provider
		return mock
	}

	ctx := toolctx.WithGeneratedImageOutputDir(context.Background(), outputDir)
	result, err := tool.Execute(ctx, map[string]interface{}{
		"prompt":   "draw a robot",
		"provider": "SENSENOVA_IMAGE",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Success)
	require.Equal(t, "sensenova-u1-fast", mock.lastReq.Model)
	require.Empty(t, mock.lastReq.Size)
	require.Empty(t, mock.lastReq.Quality)
	require.Empty(t, mock.lastReq.OutputFormat)
	require.Equal(t, "sensenova-u1-fast", result.Metadata["model"])
	require.Equal(t, "SENSENOVA_IMAGE", result.Metadata["provider"])
	require.Equal(t, "https://token.sensenova.cn", capturedProvider.BaseURL)
}

func TestOpenAIImageGenerateTool_DebugOutputIncludesSelectionAPICallAndSave(t *testing.T) {
	outputDir := t.TempDir()
	runtimeConfig := &runtimecfg.RuntimeConfig{
		Images: runtimecfg.ImagesConfig{
			Generations: runtimecfg.ImagesGenerationsConfig{
				DefaultModel:        "gpt-image-2",
				DefaultSize:         "1024x1024",
				DefaultQuality:      "medium",
				DefaultOutputFormat: "png",
				RequestTimeout:      time.Second,
				MaxN:                4,
			},
		},
	}
	mock := &mockImageGenerator{
		response: &imagegen.GenerateResponse{
			Created: 123,
			Data: []imagegen.GenerateResponseItem{
				{B64JSON: base64.StdEncoding.EncodeToString([]byte("image-bytes")), RevisedPrompt: "revised"},
			},
		},
	}

	tool := NewOpenAIImageGenerateTool(runtimeConfig)
	tool.providerConfigResolver = func() *agentconfig.Config {
		return &agentconfig.Config{
			Providers: agentconfig.ProvidersConfig{
				Items: map[string]agentconfig.Provider{
					"OPENAI_IMAGE": {
						Enabled:         true,
						BaseURL:         "https://api.openai.example",
						APIKey:          "test-key",
						ForwardURL:      "/v1/images/generations",
						DefaultModel:    "gpt-image-2",
						SupportedModels: []string{"gpt-image-2"},
						ModelCapabilities: map[string]agentconfig.ModelCapabilitySpec{
							"gpt-image-2": {
								NativeTools: agentconfig.NativeToolCapabilities{ImagesGenerationsAPI: true},
							},
						},
					},
				},
			},
		}
	}
	tool.clientFactory = func(provider agentconfig.Provider, timeout time.Duration, proxy *agentconfig.ProxyConfig) imagegen.Generator {
		return mock
	}

	var debug bytes.Buffer
	ctx := toolctx.WithGeneratedImageOutputDir(context.Background(), outputDir)
	result, err := tool.Execute(ctx, map[string]interface{}{
		"prompt":        "raw prompt should not be logged",
		"debug":         true,
		"_debug_writer": &debug,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Success)

	output := debug.String()
	for _, expected := range []string{
		"[image-debug/tool] request",
		"select requested_provider=",
		"candidates count=1 list=OPENAI_IMAGE/gpt-image-2",
		"attempt=1/1 provider=\"OPENAI_IMAGE\" model=\"gpt-image-2\" api_url=\"https://api.openai.example/v1/images/generations\"",
		"attempt=1 api_call provider=\"OPENAI_IMAGE\" model=\"gpt-image-2\"",
		"attempt=1 api_response created=123 items=1",
		"attempt=1 save index=1 source=b64_json",
		"attempt=1 saved index=1 path=",
		"success provider=\"OPENAI_IMAGE\" model=\"gpt-image-2\" generated_count=1",
	} {
		require.Contains(t, output, expected)
	}
	require.NotContains(t, output, "raw prompt should not be logged")
	require.False(t, strings.Contains(strings.ToLower(output), "test-key"), "debug output leaked API key:\n%s", output)
}
