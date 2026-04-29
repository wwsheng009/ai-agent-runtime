package tools

import (
	"context"
	"encoding/base64"
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
